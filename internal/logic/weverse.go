package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"time"
)

func formatWeverseEvent(e weverse.Event) string {
	label := "动态"
	if e.Kind == "comment" {
		label = "回复"
	}
	if e.Kind == "live" {
		label = "直播"
	}
	if e.Kind == "live_replay" {
		label = "直播回放"
	}
	if e.Kind == "live_end" {
		label = "直播结束"
	}
	lines := []string{fmt.Sprintf("【%s|Weverse%s】", e.Author, label)}
	if e.Kind == "live" {
		lines = append(lines, "已开播")
	}
	if e.Kind == "live_replay" {
		lines = append(lines, "直播回放已生成")
	}
	if e.Kind == "live_end" {
		lines = append(lines, "直播已结束")
		if e.LiveDuration > 0 {
			lines = append(lines, "直播时长："+formatDouyinDuration(time.Duration(e.LiveDuration)*time.Second))
		}
	}

	author := strings.TrimSpace(e.Author)
	if author == "" {
		author = "成员"
	}
	fan := strings.TrimSpace(e.ParentAuthor)
	if fan == "" {
		fan = "原帖"
	}
	say := func(name, body string) {
		if body != "" {
			lines = append(lines, name+"："+body)
		}
	}
	if e.Translation != "" || e.ParentTranslation != "" {
		lines = append(lines, "", "中文")
		say(author, e.Translation)
		if e.Translation == "" && e.Body != "" {
			say(author, "（译文暂不可用，请看下方原文）")
		}
		say(fan, e.ParentTranslation)
		if e.ParentTranslation == "" && e.ParentBody != "" {
			say(fan, "（译文暂不可用，请看下方原文）")
		}
		lines = append(lines, "", "────────", "原文")
	}
	say(author, e.Body)
	say(fan, e.ParentBody)
	if e.Translation == "" && e.ParentTranslation == "" && e.TranslationError != "" {
		lines = append(lines, "（翻译暂不可用，已保留原文）")
	}
	lines = append(lines, "", "────────")
	lines = append(lines, e.URL)
	if e.Time > 0 {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		if loc == nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		timeText := time.UnixMilli(e.Time).In(loc).Format("2006-01-02 15:04:05")
		if e.Kind == "live" {
			timeText = "开播时间：" + timeText
		}
		if e.Kind == "live_replay" {
			timeText = "检测到回放：" + timeText
		}
		if e.Kind == "live_end" {
			if e.LiveEndedAt > 0 {
				timeText = "结束时间：" + timeText
			} else {
				timeText = "检测到结束：" + timeText
			}
		}
		lines = append(lines, timeText)
	}
	return strings.Join(lines, "\n")
}
func (b *Bot) runWeverseLoop(ctx context.Context) {
	dir := weverse.Dir(b.cfg.ConfigPath())
	var state weverse.Runtime
	if e := weverse.Read(dir, "state.json", &state); e != nil {
		log.Printf("[Weverse] 无法读取去重状态: %v", e)
		return
	}
	var status weverse.Status
	_ = weverse.Read(dir, "status.json", &status)
	for {
		cfg, e := weverse.LoadSettings(dir)
		interval := 60 * time.Second
		if cfg.PollSeconds >= 30 {
			interval = time.Duration(cfg.PollSeconds) * time.Second
		}
		if e == nil && cfg.Enabled && len(cfg.Subscriptions) > 0 {
			c := weverse.NewClient(dir, cfg.ProxyURL)
			cycle, cancel := context.WithTimeout(ctx, 3*time.Minute)
			events, err := c.Events(cycle)
			if err == nil {
				var ends []weverse.Event
				ends, err = c.LiveEndEvents(cycle, &state, cfg, events, time.Now())
				if err == nil {
					events = append(events, ends...)
				}
			}
			status.LastCheck = time.Now().Format(time.RFC3339)
			if err != nil {
				status.Error = err.Error()
				log.Printf("[Weverse] 检查失败: %v", err)
			} else {
				status.Error = ""
				status.LastSuccess = status.LastCheck
				status.Events = len(events)
				enabled := map[string]bool{}
				for _, s := range cfg.Subscriptions {
					if s.Enabled {
						enabled[s.ID] = true
					}
				}
				translated := map[string]weverse.Event{}
				for _, s := range cfg.Subscriptions {
					if !s.Enabled {
						delete(state.Subscriptions, s.ID)
						continue
					}
					enabled[s.ID] = true
					pending := weverse.Pending(&state, s, events, time.Now())
					// Save baseline before any sends. Failed state writes must not cause a
					// restart storm of duplicate notifications.
					if err = weverse.Write(dir, "state.json", state); err != nil {
						status.Error = "无法保存去重状态"
						break
					}
					for _, event := range pending {
						if cycle.Err() != nil {
							break
						}
						if s.Translate {
							if cached, ok := translated[event.ID]; ok {
								event = cached
							} else {
								cachedTitle := event.Translation
								c.TranslateEvent(cycle, &event)
								if (event.Kind == "live_end" || event.Kind == "live_replay") && event.Translation == "" && cachedTitle != "" {
									event.Translation = cachedTitle
									event.TranslationError = ""
								}
								translated[event.ID] = event
								if event.Kind == "live" {
									key := fmt.Sprintf("%d/%s", event.CommunityID, event.PostID)
									if tracked := state.Lives[key]; tracked != nil {
										tracked.Event.Translation = event.Translation
									}
								}
							}
						}
						b.napcat.SendGroupMessage(s.GroupID, weverseMessageSegments(s, event))
						weverse.MarkDelivered(&state, s, event)
						if err = weverse.Write(dir, "state.json", state); err != nil {
							status.Error = "消息已入队，但去重状态保存失败"
							break
						}
					}
					if err != nil {
						break
					}
				}
				for id := range state.Subscriptions {
					if !enabled[id] {
						delete(state.Subscriptions, id)
					}
				}
				_ = weverse.Write(dir, "state.json", state)
			}
			cancel()
			c.HTTP.CloseIdleConnections()
			_ = weverse.Write(dir, "status.json", status)
		} else if e != nil {
			log.Printf("[Weverse] 读取配置失败: %v", e)
		} else { // A paused platform starts from a fresh baseline when resumed.
			state = weverse.Runtime{}
			_ = weverse.Write(dir, "state.json", state)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func weverseMessageSegments(s weverse.Subscription, e weverse.Event) []interface{} {
	segments := []interface{}{}
	if s.MentionsAll(e.MemberID) {
		segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	segments = append(segments, napcat.TextSegment(formatWeverseEvent(e)))
	for _, image := range e.Images {
		segments = append(segments, napcat.ImageSegment(image))
	}
	return segments
}
