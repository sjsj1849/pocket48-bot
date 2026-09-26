package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/melon"
	"pocket48-bot/internal/napcat"
	"strings"
	"time"
)

func melonMessage(sub melon.Subscription, event melon.Event) []interface{} {
	name := strings.TrimSpace(sub.ArtistName)
	if name == "" {
		name = "Melon 艺人"
	}
	parts := []interface{}{}
	if sub.MentionsAll(event.Author) {
		parts = append(parts, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	parts = append(parts, napcat.TextSegment(fmt.Sprintf("【%s|Melon】\n%s\n%s", name, event.Title, event.Body)))
	for _, image := range event.Images {
		if image != "" {
			parts = append(parts, napcat.TextSegment("\n"), napcat.ImageSegment(image))
		}
	}
	footer := event.URL
	if event.Time > 0 {
		footer += "\n\n" + time.UnixMilli(event.Time).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	}
	if footer != "" {
		parts = append(parts, napcat.TextSegment("\n\n"+footer))
	}
	return parts
}

func (b *Bot) runMelonLoop(ctx context.Context) {
	dir := melon.Dir(b.cfg.ConfigPath())
	var state melon.Runtime
	if err := melon.Read(dir, "state.json", &state); err != nil {
		log.Print("[Melon] 无法读取去重状态，停止采集以避免重复推送")
		return
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]melon.Cursor{}
	}
	status := melon.Status{StartedAt: time.Now().Format(time.RFC3339), Targets: map[string]string{}}
	failCount := 0
	for {
		if ctx.Err() != nil {
			return
		}
		settings, err := melon.LoadSettings(dir)
		interval := 120 * time.Second
		if settings.PollSeconds >= 60 {
			interval = time.Duration(settings.PollSeconds) * time.Second
		}
		if err != nil {
			status.Error = "无法读取 Melon 配置"
			status.LastCheck = time.Now().Format(time.RFC3339)
			_ = melon.Write(dir, "status.json", status)
		} else if settings.Enabled {
			client := melon.Client{ProxyURL: settings.ProxyURL}
			cache := map[string][]melon.Event{}
			failures := map[string]error{}
			status.Error, status.Events, status.Forwarded = "", 0, 0
			status.Targets = map[string]string{}
			for _, sub := range settings.Subscriptions {
				if !sub.Enabled || ctx.Err() != nil {
					continue
				}
				events, found := cache[sub.ArtistID]
				fetchErr := failures[sub.ArtistID]
				if !found && fetchErr == nil {
					events, fetchErr = client.Timeline(ctx, sub.ArtistID)
					if fetchErr == nil {
						cache[sub.ArtistID] = events
						status.Events += len(events)
					} else {
						failures[sub.ArtistID] = fetchErr
					}
				}
				if fetchErr != nil {
					status.Targets[sub.ID] = fetchErr.Error()
					status.Error = sub.ArtistName + "：" + fetchErr.Error()
					continue
				}
				cursor := state.Subscriptions[sub.ID]
				pending := melon.Pending(cursor, sub, events)
				state.Subscriptions[sub.ID] = melon.Advance(cursor, sub, events)
				if err := melon.Write(dir, "state.json", state); err != nil {
					state.Subscriptions[sub.ID] = cursor
					status.Targets[sub.ID], status.Error = "无法保存去重状态", "无法保存 Melon 去重状态"
					continue
				}
				for _, event := range pending {
					message := melonMessage(sub, event)
					if strings.EqualFold(b.cfg.MediaDelivery, "local") {
						b.localizeMessageGroups([][]interface{}{message})
					}
					b.napcat.SendGroupMessage(sub.GroupID, message)
					status.Forwarded++
					log.Printf("[Melon] 新内容已入推送队列: artist=%s event=%s group=%d", sub.ArtistID, event.ID, sub.GroupID)
				}
				status.Targets[sub.ID] = "正常"
			}
			status.LastCheck = time.Now().Format(time.RFC3339)
			if status.Error == "" {
				status.LastSuccess = status.LastCheck
			}
			_ = melon.Write(dir, "status.json", status)
			if status.Error == "" {
				log.Printf("[Melon] 扫描完成: %d 条内容，新内容 %d 条已入队", status.Events, status.Forwarded)
			} else {
				log.Printf("[Melon] 扫描异常: %s", status.Error)
			}
		}
		if status.Error != "" && settings.Enabled {
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
