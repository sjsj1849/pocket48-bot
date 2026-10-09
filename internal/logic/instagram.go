package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// h2hSuffixRe 匹配账号名尾部的 H2H 后缀。
//
// ★ 2026-10-09（用户要求）：Instagram 账号的**全名**本身就是
//
//	"Hearts2Hearts H2H"（实测 author.name），不只是 caption 里带 #H2H。
//	名称规范要求全平台统一大写 Hearts2Hearts，所以顶栏也要清掉。
var h2hSuffixRe = regexp.MustCompile(`(?i)[\s\-_|/·]*\bH2H\b\s*$`)

// instagramCleanAuthorName 规范化账号显示名。
//
// 规则（2026-10-09 用户口径）：
//   - 剥掉尾部的 H2H（可能重复出现，逐次剥净）；
//   - 去首尾空白；
//   - 结果为空时依次回退 username / 订阅用户名，绝不返回空串
//     （顶栏空着比写个兜底名更糟）。
func instagramCleanAuthorName(name, fallback string) string {
	cleaned := strings.TrimSpace(name)
	for {
		next := strings.TrimSpace(h2hSuffixRe.ReplaceAllString(cleaned, ""))
		if next == cleaned {
			break
		}
		cleaned = next
	}
	if cleaned == "" {
		cleaned = strings.TrimSpace(fallback)
	}
	if cleaned == "" {
		cleaned = "Instagram"
	}
	return cleaned
}

// instagramCleanCaption 清洗 caption 文本（2026-09 用户要求）。
//
// ★ 名称规范（2026-10-09 用户要求）：全平台统一大写 Hearts2Hearts，
//
//	所以 caption 里艺人自己写的 #H2H 要去掉，只保留 #Hearts2Hearts。
//	理由：#H2H 是同一团的另一个缩写 tag，同时出现会让群里看起来像两个团。
//	韩文 #하츠투하츠 属意译（"心连心"），保留 —— 各平台原文本来就不一致，
//	用户只点名了 H2H。
func instagramCleanCaption(body string) string {
	if body == "" {
		return ""
	}
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for i, line := range lines {
		cleaned := strings.TrimRight(hashtagH2H.ReplaceAllString(line, ""), " \t")
		// ★ 只有「这一行原本非空、清洗后变空」才丢掉它 ——
		//   那说明整行就是 #H2H。**原本就空的行必须保留**：
		//   caption 靠空行分隔正文与标签，误删会让整段挤成一行。
		if cleaned == "" && strings.TrimSpace(line) != "" {
			continue
		}
		_ = i
		kept = append(kept, cleaned)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// hashtagH2H 匹配 #H2H（含前后空白边界与结尾换行）。
var hashtagH2H = regexp.MustCompile(`(?i)(^|\s)#H2H\b`)

// instagramKindLabel 把Instagram 的内容类型映射成标签，
// 用于飞书卡片底栏的「Instagram · Reels · 时间」。
//
// ★ 2026-10-09（用户要求）：来源与内容类型不再写进正文，
//
//	改到底栏，样式对齐 Melon 的「Melon 官方文章」。
func instagramKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "story":
		return "Story"
	case "reel", "reels":
		return "Reels"
	default:
		return "帖子"
	}
}

// instagramMessageGroups 组装一条内容要发出去的消息段。
//
// skipVideo 为 true 时只发文字与封面，不发视频本体 —— 用于该视频
// 已被其它平台发过的情况（视频本体只发第一次，见跨平台去重）。
func instagramMessageGroups(sub instagram.Subscription, e instagram.Event, skipVideo bool) [][]interface{} {
	name := instagramCleanAuthorName(e.Author.Name, firstNonEmpty(e.Author.Username, sub.Username))
	lines := []string{fmt.Sprintf("【%s|Instagram】", name)}
	// ★ 2026-10-09（用户要求）：正文只放 caption。
	// 内容类型（帖子 / Reels / Story）与视频占位说明**都不写进正文** ——
	// 类型改到飞书卡片底栏「Instagram · Reels · 时间」里，
	// 视频则作为独立消息本体发送，正文不再重复说明。
	if body := instagramCleanCaption(e.Body); body != "" {
		lines = append(lines, body)
	}
	media := e.Media
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
	// ★ 来源标注交给飞书卡片的 footer（source + sourceLabel），
	//   见下方document 分支。QQ 侧没有 footer 概念，保持原样。
	if footer != "" {
		card = append(card, napcat.TextSegment("\n\n"+footer))
	}
	groups := [][]interface{}{card}
	if skipVideo {
		return groups
	}
	seen = map[string]bool{}
	for _, m := range media {
		if url := instagramVideoURL(m); url != "" && !seen[url] {
			groups = append(groups, []interface{}{napcat.VideoSegment(url, m.Cover)})
			seen[url] = true
		}
	}
	return groups
}

// instagramAuthorKey 产出 Instagram 侧的作者标识，供去重二级判定（团名+时长）使用。
//
// 用 @用户名 而不是显示名：显示名会带空格与 emoji，且能被用户随时改，
// 拿它当键会让同一作者在两次抓取之间对不上。
func instagramAuthorKey(e instagram.Event) string {
	name := strings.TrimSpace(e.Author.Username)
	if name == "" {
		name = strings.TrimSpace(e.Author.Name)
	}
	return strings.ToLower(name)
}

// instagramVideoSeconds 返回媒体视频时长（秒），0 表示未知。
func instagramVideoSeconds(m instagram.Media) int {
	if m.DurationMS <= 0 {
		return 0
	}
	// 毫秒转秒向上取整，避免 9.6 秒被截成 9 秒而与别的平台差 1 秒对不上。
	return int((m.DurationMS + 999) / 1000)
}

// instagramDedupeTitle 产出参与跨平台去重的标题。
//
// Instagram 的正文常年为空，多数 Reels 只有一句 caption 甚至什么都没有，
// 此时拿 id 当键：宁可漏判（多发一次），也不能拿空标题去撞别人的内容
// ——空标题会让任何一条都被判成同名。
func instagramDedupeTitle(e instagram.Event) string {
	if body := strings.TrimSpace(e.Body); body != "" {
		return body
	}
	return "instagram:" + e.ID
}

// instagramHasVideo 判断这条内容里是否真的有可发的视频本体。
func instagramHasVideo(e instagram.Event) bool {
	for _, m := range e.Media {
		if m.Kind == "video" || m.Kind == "gif" || m.Kind == "animated" {
			if instagramVideoURL(m) != "" {
				return true
			}
		}
	}
	return false
}

// instagramShouldSkipVideo 判断这条视频本体是否已被别的平台发过。
//
// 口径完全对齐 bilibiliShouldSkipVideo：publishedAt 传 **seenAt（推送时刻）**
// 而不是作品发布时间，否则「对方发布时间比我早」会把候选直接排除掉。
func instagramShouldSkipVideo(ix *dedupe.Index, e instagram.Event, seenAt int64) bool {
	if ix == nil || !instagramHasVideo(e) {
		return false
	}
	dedupe.SetActiveSource("instagram")
	if seenAt <= 0 {
		seenAt = time.Now().UnixMilli()
	}
	// 时长取视频本体那一档，多视频时取最长的一条做代表。
	seconds := instagramLongestVideoSeconds(e)
	firstSeen, ok := ix.MatchWithAuthor(instagramDedupeTitle(e), seconds, instagramAuthorKey(e), seenAt)
	if !ok {
		return false
	}
	return otherSentFirst(firstSeen, seenAt)
}

// instagramLongestVideoSeconds 返回最长视频的时长（秒），0 表示没有视频或时长未知。
func instagramLongestVideoSeconds(e instagram.Event) int {
	seconds := 0
	for _, m := range e.Media {
		if m.Kind == "video" || m.Kind == "gif" || m.Kind == "animated" {
			if s := instagramVideoSeconds(m); s > seconds {
				seconds = s
			}
		}
	}
	return seconds
}

// instagramSend 把内容投递到订阅配置的所有目标。
//
// 走统一出口而不是 b.napcat.SendGroupMessage：只有这样飞书群才会收到，
// 多目标配置也才生效。TargetIDs 为空时回退到老的单群 GroupID，
// 保证既有只填了 groupId 的订阅不会静默失联。
func (b *Bot) instagramSend(sub instagram.Subscription, groups [][]interface{}, event instagram.Event) {
	// 目标解析：显式列表优先，退回单个 GroupID。
	var targets []string
	if len(sub.TargetIDs) > 0 {
		targets = sub.TargetIDs
	} else if sub.GroupID != 0 {
		targets = []string{strconv.FormatInt(sub.GroupID, 10)}
	} else {
		log.Printf("[Instagram] 订阅 %s 没有任何投递目标，已跳过", sub.Username)
		return
	}

	// groups[0] 是文字 + 封面（含 @全体、链接、时间戳），走 Document ——
	// 这样飞书能渲染出footer里的「Instagram · Reels · 时间」。
	doc := b.instagramDocument(sub, event)
	b.sendToTargetIDs(targets, doc)

	// ★ groups[1:] 是视频本体（独立消息），必须照旧发出去。
	//   我第一版改造时写了 `_ = groups`，把视频整段丢掉了 ——
	//   表现为「Reels 只收到封面、没有视频」。别再犯。
	//
	// 视频**不走 Document**：Document.Media 只当图片处理（见
	// instagramDocument 里的 map[Kind:"image"]），视频要作为原生视频段
	// 直链交给 QQ/飞书，才能被当成可播放的视频。
	for _, group := range groups[1:] {
		if !instagramGroupHasVideo(group) {
			continue
		}
		b.sendToTargetIDs(targets, group)
	}
}

// instagramGroupHasVideo 判断一个消息组里是否含视频段。
func instagramGroupHasVideo(group []interface{}) bool {
	for _, item := range group {
		if seg, ok := item.(napcat.MessageSegment); ok && seg.Type == "video" {
			return true
		}
	}
	return false
}

// instagramDocumentText 只取 caption 清洗后的正文，供测试断言「正文里不该有什么」。
func instagramDocumentText(e instagram.Event) string {
	return instagramCleanCaption(e.Body)
}

// instagramDocument 产出统一的消息体（message.Document）。
//
// ★ 2026-10-09（用户要求）样式对齐 Melon：
//   - 来源与内容类型放卡片底栏 footer（「Instagram· Reels · 时间」），
//     不再写进正文；
//   - 正文只留 caption；
//   - 视频占位说明（「视频单独发送」等）全部去掉 —— 视频作为独立消息本体发出。
//
// Document 对所有渠道通用：QQ 走 ToSegments 的线性回退，飞书渲染成带footer 的卡片。
func (b *Bot) instagramDocument(sub instagram.Subscription, e instagram.Event) message.Document {
	name := instagramCleanAuthorName(e.Author.Name, firstNonEmpty(e.Author.Username, sub.Username))
	doc := message.Document{
		Source: "Instagram",
		// 底栏：「Instagram · Reels · 2026-10-09 19:20:17」
		Label:      "Instagram " + instagramKindLabel(e.Kind),
		Author:     name,
		Body:       instagramCleanCaption(e.Body),
		Kind:       e.Kind,
		MentionAll: sub.AtAll,
	}
	if e.Time > 0 {
		doc.CreatedAt = time.UnixMilli(e.Time).In(time.FixedZone("CST", 8*3600))
	}
	if e.URL != "" {
		doc.Link = e.URL
	}
	for _, m := range e.Media {
		url := m.URL
		if m.Kind != "photo" && m.Kind != "image" {
			url = m.Cover
		}
		if url == "" {
			continue
		}
		doc.Media = append(doc.Media, message.Media{Kind: "image", Source: url})
	}
	return doc
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
		// ★ 默认 60 秒（2026-10-09 用户要求）。LoadSettings 在配置缺失时也返回
		//   60，这里只是兜底；面板可改范围见 instagram.PollSecondsBounds。
		interval := 60 * time.Second
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
					// 订阅里已经存了 userId。usernameinfo 是这套里唯一会稳定吃
					// 429 的接口，而它返回的 id 与 sub.UserID 本来就是同一个，
					// 所以每轮再查一次只会把账号推进退避里。只有首次订阅、
					// 还没有 userId 时才真的需要解析用户名。
					if sub.UserID != "" {
						user = instagram.User{ID: sub.UserID, Username: sub.Username}
					} else {
						user, e = client.Lookup(ctx, sub.Username)
					}
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
								// 本轮扫描时刻：去重登记与判重都用它，
								// 不用 event.Time（作品发布时间）—— 见 bilibili.go 同处注释。
								scanSeenAt := time.Now().UnixMilli()
								titleIndex := crossTitleIndex(b.cfg.ConfigPath())
								for _, event := range pending {
									if ctx.Err() != nil {
										return
									}
									// 视频本体只发第一次：命中跨平台去重时，
									// 文字封面照发，只跳过 Reels 视频本体。
									skipVideo := instagramShouldSkipVideo(titleIndex, event, scanSeenAt)
									groups := instagramMessageGroups(sub, event, skipVideo)
									if strings.EqualFold(b.cfg.MediaDelivery, "local") {
										b.localizeMessageGroups(groups)
									}
									b.instagramSend(sub, groups, event)
									status.Forwarded++
									if instagramHasVideo(event) {
										titleIndex.RecordWithAuthor(
											instagramDedupeTitle(event), scanSeenAt, "instagram",
											instagramLongestVideoSeconds(event), instagramAuthorKey(event))
									}
									if skipVideo {
										log.Printf("[Instagram] 视频本体已被其它平台发过，本次只发文字封面: @%s id=%s",
											sub.Username, event.ID)
									}
									log.Printf("[Instagram] 新帖子已入推送队列: @%s id=%s group=%s targets=%d",
										sub.Username, event.ID, strconv.FormatInt(sub.GroupID, 10), len(sub.TargetIDs))
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
