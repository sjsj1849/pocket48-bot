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

func melonMusicWaveMessages(sub melon.Subscription, events []melon.Event) [][]interface{} {
	if len(events) == 0 {
		return nil
	}
	const (
		maxRunes  = 600
		maxEvents = 8
	)
	header := fmt.Sprintf("【%s|Melon Music Wave】\n", strings.TrimSpace(sub.ArtistName))
	if strings.TrimSpace(sub.ArtistName) == "" {
		header = "【Melon Music Wave】\n"
	}
	messages := [][]interface{}{}
	current := []interface{}{}
	var text strings.Builder
	groupRunes := 0
	groupEvents := 0
	textHasLines := false
	groupHasContent := false
	mentionAll := false
	lastTime, link := int64(0), ""
	start := func() {
		current = []interface{}{}
		lastTime, link = 0, ""
		text.Reset()
		text.WriteString(header)
		groupRunes = len([]rune(header))
		groupEvents = 0
		textHasLines = false
		groupHasContent = false
		mentionAll = false
	}
	flush := func() {
		if !groupHasContent {
			return
		}
		footer := link
		if lastTime > 0 {
			footer += "\n\n" + time.UnixMilli(lastTime).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02 15:04:05")
		}
		if footer != "" {
			text.WriteString("\n\n" + footer)
		}
		if text.Len() > 0 {
			current = append(current, napcat.TextSegment(text.String()))
		}
		if mentionAll {
			current = append([]interface{}{napcat.AtSegment("all"), napcat.TextSegment("\n")}, current...)
		}
		messages = append(messages, current)
	}
	start()
	for _, event := range events {
		line := strings.TrimSpace(event.Author) + "：" + strings.TrimSpace(event.Body)
		if strings.TrimSpace(event.Author) == "" {
			line = strings.TrimSpace(event.Body)
		}
		if (groupEvents >= maxEvents || groupRunes+len([]rune(line))+1 > maxRunes) && groupHasContent {
			flush()
			start()
		}
		if textHasLines {
			text.WriteByte('\n')
		}
		text.WriteString(line)
		groupRunes += len([]rune(line)) + 1
		groupEvents++
		textHasLines = true
		groupHasContent = true
		mentionAll = mentionAll || sub.MentionsAll(event.Author)
		for _, image := range event.Images {
			if image != "" {
				current = append(current, napcat.TextSegment(text.String()+"\n"), napcat.ImageSegment(image))
				text.Reset()
				textHasLines = false
			}
		}
		if event.Time > lastTime {
			lastTime = event.Time
		}
		if event.URL != "" {
			link = event.URL
		}
	}
	flush()
	return messages
}

func (b *Bot) runMelonMusicWaveLoop(ctx context.Context) {
	dir := melon.Dir(b.cfg.ConfigPath())
	var state melon.Runtime
	if err := melon.Read(dir, "music-wave-state.json", &state); err != nil {
		log.Print("[Melon Music Wave] 无法读取去重状态，停止采集以避免重复推送")
		return
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]melon.Cursor{}
	}
	status := melon.Status{StartedAt: time.Now().Format(time.RFC3339), Targets: map[string]string{}}
	for {
		if ctx.Err() != nil {
			return
		}
		settings, err := melon.LoadSettings(dir)
		interval := 10 * time.Second
		if settings.MusicWavePollSeconds >= 5 && settings.MusicWavePollSeconds <= 60 {
			interval = time.Duration(settings.MusicWavePollSeconds) * time.Second
		}
		if err != nil {
			status.Error = "无法读取 Melon 配置"
		} else if settings.Enabled {
			client := melon.Client{ProxyURL: settings.ProxyURL}
			cache := map[string][]melon.Event{}
			failures := map[string]error{}
			status.Error, status.Events, status.Forwarded = "", 0, 0
			status.Targets = map[string]string{}
			for _, sub := range settings.Subscriptions {
				if !sub.Enabled || !sub.MusicWave || ctx.Err() != nil {
					continue
				}
				events, found := cache[sub.ArtistID]
				fetchErr := failures[sub.ArtistID]
				if !found && fetchErr == nil {
					events, fetchErr = client.MusicWave(ctx, sub.ArtistID)
					if fetchErr == nil {
						cache[sub.ArtistID] = events
						status.Events += len(events)
					} else {
						failures[sub.ArtistID] = fetchErr
					}
				}
				if fetchErr != nil {
					status.Targets[sub.ID], status.Error = fetchErr.Error(), sub.ArtistName+"："+fetchErr.Error()
					continue
				}
				cursor := state.Subscriptions[sub.ID]
				pending := melon.Pending(cursor, sub, events)
				state.Subscriptions[sub.ID] = melon.Advance(cursor, sub, events)
				if err := melon.Write(dir, "music-wave-state.json", state); err != nil {
					state.Subscriptions[sub.ID] = cursor
					status.Targets[sub.ID], status.Error = "无法保存去重状态", "无法保存 Music Wave 去重状态"
					continue
				}
				for _, message := range melonMusicWaveMessages(sub, pending) {
					if strings.EqualFold(b.cfg.MediaDelivery, "local") {
						b.localizeMessageGroups([][]interface{}{message})
					}
					b.napcat.SendGroupMessage(sub.GroupID, message)
					status.Forwarded++
				}
				if len(pending) > 0 {
					log.Printf("[Melon Music Wave] %d 条成员消息已按时间线入队: artist=%s group=%d", len(pending), sub.ArtistID, sub.GroupID)
				}
				status.Targets[sub.ID] = "正常"
			}
			status.LastCheck = time.Now().Format(time.RFC3339)
			if status.Error == "" {
				status.LastSuccess = status.LastCheck
			}
			_ = melon.Write(dir, "music-wave-status.json", status)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
