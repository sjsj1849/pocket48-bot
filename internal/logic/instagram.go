package logic

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/napcat"
	"strconv"
	"strings"
	"time"
)

func instagramVideoURL(media instagram.Media) string {
	best := instagram.Variant{}
	for _, v := range media.Variants {
		if v.URL != "" && v.Bitrate <= 2500000 && v.Bitrate > best.Bitrate {
			best = v
		}
	}
	if best.URL != "" {
		return best.URL
	}
	for _, v := range media.Variants {
		if v.URL != "" && (best.URL == "" || v.Bitrate < best.Bitrate) {
			best = v
		}
	}
	return best.URL
}
func instagramMessageGroups(sub instagram.Subscription, e instagram.Event) [][]interface{} {
	name := e.Author.Name
	if name == "" {
		name = e.Author.Username
	}
	lines := []string{fmt.Sprintf("【%s|Instagram】", name)}
	if e.Kind == "story" {
		lines = append(lines, "Story")
	}
	if e.Kind == "reel" {
		lines = append(lines, "Reels")
	}
	if e.Body != "" {
		lines = append(lines, e.Body)
	}
	media := e.Media
	for _, m := range media {
		if m.Kind == "video" || m.Kind == "gif" || m.Kind == "animated" {
			if instagramVideoURL(m) != "" {
				lines = append(lines, "[视频]（视频单独发送）")
			} else {
				lines = append(lines, "[视频]（请打开原帖观看）")
			}
		}
	}
	card := []interface{}{}
	if sub.AtAll {
		card = append(card, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	card = append(card, napcat.TextSegment(strings.Join(lines, "\n")))
	seen := map[string]bool{}
	for _, m := range media {
		url := m.URL
		if m.Kind != "photo" && m.Kind != "image" {
			url = m.Cover
		}
		if url != "" && !seen[url] {
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
	seen = map[string]bool{}
	for _, m := range media {
		if url := instagramVideoURL(m); url != "" && !seen[url] {
			groups = append(groups, []interface{}{napcat.VideoSegment(url, m.Cover)})
			seen[url] = true
		}
	}
	return groups
}

func (b *Bot) runInstagramLoop(ctx context.Context) {
	dir := instagram.Dir(b.cfg.ConfigPath())
	var state instagram.Runtime
	if err := instagram.Read(dir, "state.json", &state); err != nil {
		log.Print("[Instagram] 无法读取去重状态，停止采集以避免重复推送")
		return
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]instagram.Cursor{}
	}
	failCount := 0
	status := instagram.Status{StartedAt: time.Now().Format(time.RFC3339), Targets: map[string]string{}}
	for {
		if ctx.Err() != nil {
			return
		}
		cfg, err := instagram.LoadSettings(dir)
		interval := 300 * time.Second
		if cfg.PollSeconds >= 60 {
			interval = time.Duration(cfg.PollSeconds) * time.Second
		}
		if err != nil {
			status.Error = "无法读取 Instagram 配置"
			status.LastCheck = time.Now().Format(time.RFC3339)
			_ = instagram.Write(dir, "status.json", status)
		}
		if err == nil {
			var probe struct {
				Pending     bool   `json:"pending"`
				Username    string `json:"username"`
				UserID      string `json:"userId"`
				NextRetryAt string `json:"nextRetryAt"`
			}
			if instagram.Read(dir, "probe-state.json", &probe) == nil && probe.Pending {
				next, _ := time.Parse(time.RFC3339, probe.NextRetryAt)
				if !next.After(time.Now()) {
					_, _ = (instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}).Call(ctx, map[string]any{"operation": "feed_probe", "username": probe.Username, "userId": probe.UserID, "limit": 2})
				}
			}
			if _, statErr := os.Stat(filepath.Join(dir, "browser-candidate.json")); statErr == nil {
				_, _ = (instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}).Call(ctx, map[string]any{"operation": "session_browser_apply"})
			}
		}
		if err == nil && cfg.Enabled {
			client := instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
			results := map[string][]instagram.Event{}
			users := map[string]instagram.User{}
			failures := map[string]error{}
			status.Error = ""
			status.ErrorCode = ""
			status.NextRetryAt = ""
			status.Events = 0
			status.Forwarded = 0
			status.Targets = map[string]string{}
			for _, sub := range cfg.Subscriptions {
				if !sub.Enabled || ctx.Err() != nil {
					continue
				}
				key := fmt.Sprintf("%s:%t:%t:%t:%v", strings.ToLower(sub.Username), sub.Posts, sub.Reels, sub.Stories, instagram.ScanSince(state.Subscriptions[sub.ID]))
				user, known := users[key]
				events := results[key]
				e := failures[key]
				if !known && e == nil {
					user, e = client.Lookup(ctx, sub.Username)
					if e == nil && sub.UserID != "" && user.ID != sub.UserID {
						e = fmt.Errorf("账号编号发生变化，请重新保存订阅")
					}
					if e == nil {
						limit := 100
						if !state.Subscriptions[sub.ID].Ready {
							limit = 10
						}
						events, e = client.Collect(ctx, sub, limit, instagram.ScanSince(state.Subscriptions[sub.ID]))
					}
					if ce, ok := e.(*instagram.Error); ok && ce.Code == "scan_incomplete" {
						events, e = client.Collect(ctx, sub, 500, instagram.ScanSince(state.Subscriptions[sub.ID]))
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

					if e == nil {
						pending := instagram.Pending(cursor, sub, events)
						next, advanceErr := instagram.Advance(cursor, sub, events, time.Now())
						e = advanceErr
						if e == nil {
							state.Subscriptions[sub.ID] = next
							e = instagram.Write(dir, "state.json", state)
							if e != nil {
								state.Subscriptions[sub.ID] = cursor
							}
							if e == nil {
								for _, event := range pending {
									if ctx.Err() != nil {
										return
									}
									for _, group := range instagramMessageGroups(sub, event) {
										if strings.EqualFold(b.cfg.MediaDelivery, "local") {
											for i, item := range group {
												if segment, ok := item.(napcat.MessageSegment); ok && (segment.Type == "image" || segment.Type == "video") {
													segment.Data["file"] = b.mediaPathForMessage(nil, segment.Data["file"])
													group[i] = segment
												}
											}
										}
										b.napcat.SendGroupMessage(sub.GroupID, group)
									}
									status.Forwarded++
									log.Printf("[Instagram] 新帖子已入推送队列: @%s id=%s group=%s", sub.Username, event.ID, strconv.FormatInt(sub.GroupID, 10))
								}
							}
						}
					}
				}
				if e != nil {
					status.Targets[sub.ID] = e.Error()
					status.Error = "@" + sub.Username + "：" + e.Error()
					if ce, ok := e.(*instagram.Error); ok {
						status.ErrorCode = ce.Code
						status.NextRetryAt = ce.NextRetryAt
					}
				} else {
					status.Targets[sub.ID] = "正常"
				}
			}
			status.LastCheck = time.Now().Format(time.RFC3339)
			if status.Error == "" {
				status.LastSuccess = status.LastCheck
			}
			if instagram.Write(dir, "status.json", status) != nil {
				log.Print("[Instagram] 无法保存监控状态")
			} else if status.Error != "" {
				log.Printf("[Instagram] 扫描异常: %s", status.Error)
			} else {
				log.Printf("[Instagram] 扫描完成: %d 条帖子，新内容 %d 条已入队", status.Events, status.Forwarded)
			}
		}
		if status.Error != "" && cfg.Enabled {
			failCount++
			if failCount > 4 {
				failCount = 4
			}
			interval *= time.Duration(1 << failCount)
			if interval > 30*time.Minute {
				interval = 30 * time.Minute
			}
		} else {
			failCount = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
