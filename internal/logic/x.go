package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/xmonitor"
	"strconv"
	"strings"
	"time"
)

func xVideoURL(media xmonitor.Media) string {
	best := xmonitor.Variant{}
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
func xMessageGroups(sub xmonitor.Subscription, e xmonitor.Event) [][]interface{} {
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
				lines = append(lines, "[视频]（视频单独发送）")
			} else {
				lines = append(lines, "[视频]（请打开原帖观看）")
			}
		}
	}
	card := []interface{}{}
	if sub.AtAll {
		card = append(card, napcat.AtSegment("all"))
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
		if url := xVideoURL(m); url != "" && !seen[url] {
			groups = append(groups, []interface{}{napcat.VideoSegment(url, m.Cover)})
			seen[url] = true
		}
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
	for {
		if ctx.Err() != nil {
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
					user, e = client.Lookup(ctx, sub.Username)
					if e == nil && sub.UserID != "" && user.ID != sub.UserID {
						e = fmt.Errorf("账号编号发生变化，请重新保存订阅")
					}
					if e == nil {
						events, e = client.Timeline(ctx, user.ID, 100)
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
						events, e = client.Timeline(ctx, user.ID, 500)
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
							e = xmonitor.Write(dir, "state.json", state)
							if e != nil {
								state.Subscriptions[sub.ID] = cursor
							}
							if e == nil {
								for _, event := range pending {
									if ctx.Err() != nil {
										return
									}
									for _, group := range xMessageGroups(sub, event) {
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
			status.LastCheck = time.Now().Format(time.RFC3339)
			if status.Error == "" {
				status.LastSuccess = status.LastCheck
			}
			if xmonitor.Write(dir, "status.json", status) != nil {
				log.Print("[X] 无法保存监控状态")
			} else if status.Error != "" {
				log.Printf("[X] 扫描异常: %s", status.Error)
			} else {
				log.Printf("[X] 扫描完成: %d 条帖子，新内容 %d 条已入队", status.Events, status.Forwarded)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
