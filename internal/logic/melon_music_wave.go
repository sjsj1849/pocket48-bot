package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/hearts2hearts"
	"pocket48-bot/internal/melon"
	"pocket48-bot/internal/message"
	"strings"
	"time"
)

func melonMusicWaveMessages(sub melon.Subscription, events []melon.Event) []interface{} {
	if len(events) == 0 {
		return nil
	}
	name := strings.TrimSpace(sub.ArtistName)
	if name == "" {
		name = "Melon Music Wave"
	}
	const maxRunes = 1800
	maxEvents := melon.MusicWaveBatchSize
	if maxEvents <= 0 {
		maxEvents = len(events)
	}
	var docs []interface{}
	var current *message.Document
	runes := 0
	eventCount := 0
	mentionAll := false
	var lastTime int64
	var link string

	flush := func() {
		if current == nil {
			return
		}
		current.Author = musicWaveAuthorLabel(current.Turns, name)
		if link != "" {
			current.Link = link
		}
		if lastTime > 0 {
			current.CreatedAt = time.UnixMilli(lastTime).In(time.FixedZone("CST", 8*3600))
		}
		current.MentionAll = mentionAll
		docs = append(docs, current)
		current = nil
		runes = 0
		eventCount = 0
		mentionAll = false
		lastTime = 0
		link = ""
	}

	for _, event := range events {
		author := strings.TrimSpace(event.Author)
		body := strings.TrimSpace(event.Body)
		translation := strings.TrimSpace(event.Translation)
		if translation == body {
			translation = ""
		}
		addRunes := len([]rune(author)) + len([]rune(body)) + len([]rune(translation)) + 4
		if current != nil && (eventCount >= maxEvents || runes+addRunes > maxRunes) {
			flush()
		}
		if current == nil {
			current = &message.Document{Source: "Melon Music Wave", Author: name}
		}
		current.Turns = append(current.Turns, message.Turn{Author: author, Text: body, Translation: translation})
		runes += addRunes
		eventCount++
		mentionAll = mentionAll || sub.MentionsAll(event.Author)
		for _, image := range event.Images {
			if image != "" {
				current.Media = append(current.Media, message.Media{Kind: "image", Source: image})
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
	return docs
}

// musicWaveAuthorLabel names the card after the members who actually spoke in
// this batch. One member gets their own name; two to five are joined with " & ".
// It falls back to the artist name when nobody spoke or more than five did.
func musicWaveAuthorLabel(turns []message.Turn, fallback string) string {
	seen := map[string]bool{}
	var authors []string
	for _, turn := range turns {
		author := strings.TrimSpace(turn.Author)
		if author != "" && !seen[author] {
			seen[author] = true
			authors = append(authors, author)
		}
	}
	switch {
	case len(authors) == 0, len(authors) > 5:
		return fallback
	case len(authors) == 1:
		return authors[0]
	default:
		return strings.Join(authors, " & ")
	}
}

func translateMelonEvents(ctx context.Context, configPath string, settings melon.Settings, events []melon.Event) ([]melon.Event, error) {
	glossary, err := hearts2hearts.Load(configPath)
	if err != nil {
		return events, fmt.Errorf("无法读取 Hearts2Hearts 术语表")
	}
	lines := make([]melon.TranslationLine, len(events))
	for i := range events {
		lines[i] = melon.TranslationLine{Speaker: events[i].Author, Text: events[i].Body}
	}
	translations, err := melon.TranslateConversation(ctx, settings.MusicWaveTranslationProvider, settings.MusicWaveTranslationAPIKey, lines, glossary)
	if err != nil {
		return events, err
	}
	result := append([]melon.Event(nil), events...)
	for i := range result {
		result[i].Translation = translations[i]
	}
	return result, nil
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
	if state.MusicWaveBatches == nil {
		state.MusicWaveBatches = map[string]melon.MusicWaveBatch{}
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
			activeSubscriptions := make(map[string]bool, len(settings.Subscriptions))
			client := melon.Client{ProxyURL: settings.ProxyURL}
			cache := map[string][]melon.Event{}
			failures := map[string]error{}
			status.Error, status.Events, status.Forwarded = "", 0, 0
			status.Targets = map[string]string{}
			for _, sub := range settings.Subscriptions {
				if !sub.Enabled || !sub.MusicWave || ctx.Err() != nil {
					continue
				}
				activeSubscriptions[sub.ID] = true
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
				previousBatch := state.MusicWaveBatches[sub.ID]
				state.MusicWaveBatches[sub.ID] = melon.AppendMusicWaveBatch(previousBatch, pending, time.Now())
				state.Subscriptions[sub.ID] = melon.Advance(cursor, sub, events)
				if err := melon.Write(dir, "music-wave-state.json", state); err != nil {
					state.Subscriptions[sub.ID] = cursor
					state.MusicWaveBatches[sub.ID] = previousBatch
					status.Targets[sub.ID], status.Error = "无法保存去重状态", "无法保存 Music Wave 去重状态"
					continue
				}
				if len(pending) > 0 {
					log.Printf("[Melon Music Wave] %d 条成员消息已加入合并队列: artist=%s group=%d queued=%d", len(pending), sub.ArtistID, sub.GroupID, len(state.MusicWaveBatches[sub.ID].Events))
				}
				saveFailed := false
				for melon.MusicWaveBatchReady(state.MusicWaveBatches[sub.ID], time.Now()) {
					batch, remaining := melon.TakeMusicWaveBatch(state.MusicWaveBatches[sub.ID])
					if settings.MusicWaveTranslate {
						translated, translateErr := translateMelonEvents(ctx, b.cfg.ConfigPath(), settings, batch)
						if translateErr != nil {
							log.Printf("[Melon Music Wave] AI 翻译失败，保留原文发送: %v", translateErr)
						} else {
							batch = translated
						}
					}
					for _, message := range melonMusicWaveMessages(sub, batch) {
						b.sendToTargetIDs(sub.TargetIDs, message)
					}
					status.Forwarded += len(batch)
					state.MusicWaveBatches[sub.ID] = remaining
					if err := melon.Write(dir, "music-wave-state.json", state); err != nil {
						status.Targets[sub.ID], status.Error = "无法保存合并队列", "无法保存 Music Wave 合并队列"
						saveFailed = true
						break
					}
					log.Printf("[Melon Music Wave] %d 条成员消息已合并并入队: artist=%s group=%d", len(batch), sub.ArtistID, sub.GroupID)
				}
				if !saveFailed {
					status.Targets[sub.ID] = "正常"
				}
			}
			for id := range state.MusicWaveBatches {
				if !activeSubscriptions[id] {
					delete(state.MusicWaveBatches, id)
				}
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
