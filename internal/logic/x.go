package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/xmonitor"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	xRegularTimelineLimit = 20
	xDeepTimelineLimit    = 500
	xUserRefreshInterval  = 6 * time.Hour
	// 用户缓存上限，防止 state.json 无限增长。
	xUserCacheLimit = 64
)

// xUserCacheKeys 仅用于日志，按字典序输出便于排查。
func xUserCacheKeys(cache map[string]xUserCacheEntry) []string {
	keys := make([]string, 0, len(cache))
	for k := range cache {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// xSyncUserCache 把内存缓存回写到 Runtime，供下一次落盘。
func xSyncUserCache(state *xmonitor.Runtime, cache map[string]xUserCacheEntry) {
	persisted := make(map[string]xmonitor.UserCacheEntry, len(cache))
	for k, v := range cache {
		persisted[k] = xmonitor.UserCacheEntry{User: v.user, ExpiresAt: v.expiresAt}
	}
	state.UserCache = xmonitor.PruneUserCache(persisted, xUserCacheLimit)
}

type xUserCacheEntry struct {
	user      xmonitor.User
	expiresAt time.Time
}

func xRetryDelay(code string, consecutiveFailures int, regular time.Duration) time.Duration {
	switch code {
	case "account_unavailable", "timeout", "collection_failed", "timeline_unavailable":
		switch consecutiveFailures {
		case 1:
			return 5 * time.Minute
		case 2:
			return 15 * time.Minute
		default:
			return 30 * time.Minute
		}
	default:
		return regular
	}
}

// configPath 用于跨平台去重索引（videoAlreadySent 需要它），故由调用方传入。
func xMessageGroups(configPath string, sub xmonitor.Subscription, e xmonitor.Event) [][]interface{} {
	name := e.Author.Name
	if name == "" {
		name = e.Author.Username
	}
	lines := []string{fmt.Sprintf("【%s|X】", name)}
	if e.Kind == "reply" {
		lines = append(lines, "回复")
	}
	if e.Kind == "repost" {
		lines = append(lines, "转发")
	}
	if e.Body != "" {
		lines = append(lines, e.Body)
	}
	nested := e.Quoted
	if e.Reposted != nil {
		nested = e.Reposted
	}
	if nested != nil {
		lines = append(lines, "", nested.Author.Name+"："+nested.Body)
	}
	media := append([]xmonitor.Media{}, e.Media...)
	if nested != nil {
		media = append(media, nested.Media...)
	}
	for _, m := range media {
		if m.Kind == "video" || m.Kind == "gif" || m.Kind == "animated" {
			if xVideoURL(m) != "" {
				lines = append(lines, "[视频]")
			} else {
				lines = append(lines, "[视频]（请打开原帖观看）")
			}
		}
	}
	card := []interface{}{}
	if sub.AtAll {
		// @全体成员 独立成行，避免与正文首行粘连
		card = append(card, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	card = append(card, napcat.TextSegment(strings.Join(lines, "\n")))
	// ★ 只发图片/照片类媒体的图，**视频的封面不在这里发**
	//   （2026-10-05 用户要求：文本消息里不要带视频占位）。
	//
	// 原来任何非photo/image 的媒体都取 m.Cover 当占位图先发一遍，
	// 于是「文本+封面」→「再发视频」，视频的封面就出现了两次。
	// 视频封面已由后面的 VideoSegment(url, m.Cover) 携带，不需要占位。
	seen := map[string]bool{}
	for _, m := range media {
		if m.Kind != "photo" && m.Kind != "image" {
			continue
		}
		if url := strings.TrimSpace(m.URL); url != "" && !seen[url] {
			card = append(card, napcat.TextSegment("\n"), napcat.ImageSegment(url))
			seen[url] = true
		}
	}
	footer := e.URL
	if e.Time > 0 {
		loc := time.FixedZone("CST", 8*3600)
		footer += "\n\n" + time.UnixMilli(e.Time).In(loc).Format("2006-01-02 15:04:05")
	}
	if footer != "" {
		card = append(card, napcat.TextSegment("\n\n"+footer))
	}
	groups := [][]interface{}{card}

	// ★ 跨平台去重：只跳视频本体，文字与封面照发（2026-10-05 用户口径）。
	//
	// X 的时长来自推文 API 的 duration_ms（xmonitor.Media.DurationMS），
	// **不需要下载**就能判定，所以命中时可以连下载都不发生。
	skipVideo := xVideoAlreadySent(configPath, e)

	seen = map[string]bool{}
	for _, m := range media {
		url := xVideoURL(m)
		if url == "" || seen[url] {
			continue
		}
		seen[url] = true
		if skipVideo {
			log.Printf("[X] 跳过视频（%d 秒内有平台已发过同一条）: id=%s",
				dedupe.MatchWindowMillis/60000, e.ID)
			continue
		}
		groups = append(groups, []interface{}{napcat.VideoSegment(url, m.Cover)})
	}
	return groups
}

func (b *Bot) runXLoop(ctx context.Context) {
	dir := xmonitor.Dir(b.cfg.ConfigPath())
	var state xmonitor.Runtime
	if err := xmonitor.Read(dir, "state.json", &state); err != nil {
		log.Print("[X] 无法读取去重状态，停止采集以避免重复推送")
		return
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]xmonitor.Cursor{}
	}
	status := xmonitor.Status{StartedAt: time.Now().Format(time.RFC3339), Targets: map[string]string{}}
	userCache := map[string]xUserCacheEntry{}
	// 复用上次进程写入的解析结果，避免重启后集中重复查询 UserByScreenName
	// 而把唯一账号触发 twscrape 自锁限流。
	for key, entry := range xmonitor.PruneUserCache(state.UserCache, xUserCacheLimit) {
		userCache[key] = xUserCacheEntry{user: entry.User, expiresAt: entry.ExpiresAt}
	}
	if len(userCache) > 0 {
		log.Printf("[X] 已从 state.json 复用用户缓存 %d 条: %s", len(userCache), strings.Join(xUserCacheKeys(userCache), ","))
	}
	consecutiveFailures := 0
	for {
		if ctx.Err() != nil {
			// 正常退出：把最新缓存写回 state.json，下次启动可直接复用。
			xSyncUserCache(&state, userCache)
			if xmonitor.Write(dir, "state.json", state) == nil {
				log.Printf("[X] 用户缓存已落盘 %d 条", len(state.UserCache))
			}
			return
		}
		cfg, err := xmonitor.LoadSettings(dir)
		interval := 120 * time.Second
		if cfg.PollSeconds >= 30 {
			interval = time.Duration(cfg.PollSeconds) * time.Second
		}
		if err != nil {
			status.Error = "无法读取 X 配置"
			status.LastCheck = time.Now().Format(time.RFC3339)
			_ = xmonitor.Write(dir, "status.json", status)
		}
		if err == nil && cfg.Enabled {
			client := xmonitor.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
			results := map[string][]xmonitor.Event{}
			users := map[string]xmonitor.User{}
			failures := map[string]error{}
			status.Error = ""
			status.ErrorCode = ""
			status.Events = 0
			status.Forwarded = 0
			status.Targets = map[string]string{}
			for _, sub := range cfg.Subscriptions {
				if !sub.Enabled || ctx.Err() != nil {
					continue
				}
				key := strings.ToLower(sub.Username)
				user, known := users[key]
				events := results[key]
				e := failures[key]
				if !known && e == nil {
					now := time.Now()
					cached, cachedOK := userCache[key]
					if cachedOK && now.Before(cached.expiresAt) && (sub.UserID == "" || cached.user.ID == sub.UserID) {
						user = cached.user
					} else {
						user, e = client.Lookup(ctx, sub.Username)
						if e == nil {
							userCache[key] = xUserCacheEntry{user: user, expiresAt: now.Add(xUserRefreshInterval)}
						} else if cachedOK && cached.user.ID != "" && (sub.UserID == "" || cached.user.ID == sub.UserID) {
							// Profile refresh is optional. Keep using the last verified,
							// immutable user ID when only the lookup endpoint is unavailable.
							user = cached.user
							userCache[key] = xUserCacheEntry{user: user, expiresAt: now.Add(30 * time.Minute)}
							e = nil
						}
					}
					if e == nil && sub.UserID != "" && user.ID != sub.UserID {
						e = fmt.Errorf("账号编号发生变化，请重新保存订阅")
					}
					if e == nil {
						events, e = client.Timeline(ctx, user.ID, xRegularTimelineLimit)
					}
					if e == nil {
						users[key] = user
						results[key] = events
						status.Events += len(events)
					} else {
						failures[key] = e
					}
				}
				if e == nil {
					sub.UserID = user.ID
					sub.Username = user.Username
					cursor := state.Subscriptions[sub.ID]
					initialized := cursor.Ready && cursor.UserID == sub.UserID && strings.EqualFold(cursor.Username, sub.Username)
					if initialized && !xmonitor.Overlap(cursor, events, user.PinnedIDs) {
						events, e = client.Timeline(ctx, user.ID, xDeepTimelineLimit)
						if e == nil && !xmonitor.Overlap(cursor, events, user.PinnedIDs) {
							e = fmt.Errorf("时间线与上次扫描没有重叠，已保留进度；请检查账号或重新添加订阅建立基线")
						}
					}
					if e == nil {
						pending := xmonitor.Pending(cursor, sub, events)
						next, advanceErr := xmonitor.Advance(cursor, sub, events)
						e = advanceErr
						if e == nil {
							state.Subscriptions[sub.ID] = next
							xSyncUserCache(&state, userCache)
							e = xmonitor.Write(dir, "state.json", state)
							if e != nil {
								state.Subscriptions[sub.ID] = cursor
							}
							if e == nil {
								for _, event := range pending {
									if ctx.Err() != nil {
										return
									}
									groups := xMessageGroups(b.cfg.ConfigPath(), sub, event)
									if strings.EqualFold(b.cfg.MediaDelivery, "local") {
										b.localizeMessageGroups(groups)
									}
									for _, group := range groups {
										b.sendToTargetIDs(sub.TargetIDs, group)
									}
									status.Forwarded++
									log.Printf("[X] 新帖子已入推送队列: @%s id=%s group=%s", sub.Username, event.ID, strconv.FormatInt(sub.GroupID, 10))
								}
							}
						}
					}
				}
				if e != nil {
					status.Targets[sub.ID] = e.Error()
					status.Error = "@" + sub.Username + "：" + e.Error()
					if ce, ok := e.(*xmonitor.Error); ok {
						status.ErrorCode = ce.Code
					}
				} else {
					status.Targets[sub.ID] = "正常"
				}
			}
			now := time.Now()
			status.LastCheck = now.Format(time.RFC3339)
			wait := interval
			if status.Error == "" {
				status.LastSuccess = status.LastCheck
				status.NextRetryAt = ""
				status.ConsecutiveFailures = 0
				consecutiveFailures = 0
			} else {
				consecutiveFailures++
				wait = xRetryDelay(status.ErrorCode, consecutiveFailures, interval)
				status.ConsecutiveFailures = consecutiveFailures
				status.NextRetryAt = now.Add(wait).Format(time.RFC3339)
			}
			if xmonitor.Write(dir, "status.json", status) != nil {
				log.Print("[X] 无法保存监控状态")
			} else if status.Error != "" {
				log.Printf("[X] 扫描异常: %s；将在 %s 后重试", status.Error, wait)
			} else {
				log.Printf("[X] 扫描完成: %d 条帖子，新内容 %d 条已入队", status.Events, status.Forwarded)
			}
			interval = wait
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
