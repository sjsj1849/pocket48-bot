package logic

import (
	"context"
	"log"
	"pocket48-bot/internal/melon"
	"pocket48-bot/internal/message"
	"strings"
	"time"
)

func melonMessage(sub melon.Subscription, event melon.Event) interface{} {
	name := strings.TrimSpace(sub.ArtistName)
	if name == "" {
		name = "Melon 艺人"
	}
	body := strings.TrimSpace(event.Body)
	translation := strings.TrimSpace(event.Translation)
	if translation == body {
		translation = ""
	}

	// event.Title is a source tag (e.g. "Melon 官方文章" / "新专辑"), not the
	// post's own title. It goes into the footer; Body carries the real content.
	label := strings.TrimSpace(event.Title)

	// ★ 单人内容（artist_note）顶栏用**真人名**，不是组合名（2026-10-05 修）。
	//
	// 现象：新专辑发布时 Melon 会给每位成员各发一条 Artist Note，
	// 连着来一批（实测 17:01:16 一次入队 9 条 = 1 专辑 + 8 条单人）。
	// 原来顶栏一律写订阅的艺人名 Hearts2Hearts，
	// 于是「谁发的」完全看不出来 —— 用户原话「顶栏不要写 hearts to hearts」。
	//
	// event.Author 就是 note.ArtistName（真实成员名），原先只用来算
	// MentionAll，白白浪费了。
	author := name
	if who := strings.TrimSpace(event.Author); who != "" {
		author = who
	}

	// ★ 底栏来源：kind 决定板块名（Artist Note / 新专辑 / Music Wave…），
	//   但要带上平台 —— 用户要求底栏写「Melon Artist Note」而不是光秃秃的
	//   「artist note」。formatMelonLabel 统一处理。
	doc := &message.Document{
		Source:      "Melon",
		Label:       formatMelonLabel(event, label),
		Author:      author,
		Body:        body,
		Translation: translation,
		Link:        event.URL,
		MentionAll:  sub.MentionsAll(event.Author),
	}
	if event.Time > 0 {
		doc.CreatedAt = time.UnixMilli(event.Time).In(time.FixedZone("CST", 8*3600))
	}
	// ★ 每人一条 Artist Note 都带两张图（成员照 + 专辑封面），
	//   连着 8 条就是 16 张图刷屏 —— 用户问「那张单人照是什么，为什么每次都有」。
	//   它们是 parseAlbumArtistNotes 里拼的 [ArtistImage, albumImage]。
	//   专辑封面在 album 那条里已经有了，所以这里只保留**第一张**
	//   （成员照），封面不重复发。
	for i, image := range event.Images {
		if image == "" {
			continue
		}
		if i > 0 {
			break
		}
		doc.Media = append(doc.Media, message.Media{Kind: "image", Source: image})
	}
	return doc
}

// formatMelonLabel 产出底栏来源标签。
//
// ★ 2026-10-05 用户指出底栏最左边出现「artist note」很难懂，
//
//	要求标明平台。统一加「Melon」前缀。
//	artist_note -> "Melon Artist Note"（其余 kind 沿用 event.Title）。
func formatMelonLabel(event melon.Event, label string) string {
	if event.Kind == "artist_note" {
		// event.Title 形如 "Artist Note · 成员名"，成员名已在顶栏，
		// 底栏只需要板块名，别重复。
		return "Melon Artist Note"
	}
	if label == "" {
		return "Melon"
	}
	if strings.HasPrefix(label, "Melon") {
		return label
	}
	return "Melon " + label
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
				if settings.MusicWaveTranslate && len(pending) > 0 {
					translated, translateErr := translateMelonEvents(ctx, b.cfg.ConfigPath(), settings, pending)
					if translateErr != nil {
						log.Printf("[Melon] 中文翻译失败，保留原文发送: %v", translateErr)
					} else {
						pending = translated
					}
				}
				state.Subscriptions[sub.ID] = melon.Advance(cursor, sub, events)
				if err := melon.Write(dir, "state.json", state); err != nil {
					state.Subscriptions[sub.ID] = cursor
					status.Targets[sub.ID], status.Error = "无法保存去重状态", "无法保存 Melon 去重状态"
					continue
				}
				for _, event := range pending {
					message := melonMessage(sub, event)
					b.sendToTargetIDs(sub.TargetIDs, message)
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
