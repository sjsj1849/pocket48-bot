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
	lines := []string{fmt.Sprintf("【%s|Weverse%s】", e.Author, label)}
	if e.Kind == "live" {
		lines = append(lines, "已开播")
	}
	if e.ParentBody != "" {
		lines = append(lines, "原文上下文：", e.ParentBody)
		if e.ParentTranslation != "" {
			lines = append(lines, "中文（机器翻译）：", e.ParentTranslation)
		}
		lines = append(lines, "回复：")
	}
	if e.Body != "" {
		lines = append(lines, e.Body)
	}
	if e.Translation != "" {
		lines = append(lines, "中文（机器翻译）：", e.Translation)
	} else if e.TranslationError != "" {
		lines = append(lines, "（翻译暂不可用，已保留原文）")
	}
	lines = append(lines, e.URL)
	if e.Time > 0 {
		loc, _ := time.LoadLocation("Asia/Shanghai")
		if loc == nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		lines = append(lines, time.UnixMilli(e.Time).In(loc).Format("2006-01-02 15:04:05"))
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
								c.TranslateEvent(cycle, &event)
								translated[event.ID] = event
							}
						}
						segments := []interface{}{}
						if s.AtAll {
							segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
						}
						segments = append(segments, napcat.TextSegment(formatWeverseEvent(event)))
						for _, image := range event.Images {
							segments = append(segments, napcat.ImageSegment(image))
						}
						b.napcat.SendGroupMessage(s.GroupID, segments)
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
