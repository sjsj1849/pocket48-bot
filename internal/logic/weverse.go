package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"time"
)

func weverseEventBody(e weverse.Event) string {
	headerAuthor := e.Author
	if e.Kind == "comment" && e.PostContext != nil && e.PostContext.AuthorIsArtist &&
		strings.TrimSpace(e.MemberID) != "" && strings.TrimSpace(e.PostContext.MemberID) != "" &&
		e.MemberID != e.PostContext.MemberID {
		if postAuthor := strings.TrimSpace(e.PostContext.Author); postAuthor != "" {
			headerAuthor += "（" + postAuthor + "）"
		}
	}
	lines := []string{fmt.Sprintf("【%s|Weverse】", headerAuthor)}
	if e.Kind == "live" {
		lines = append(lines, "已开播")
	}
	if e.Kind == "live_replay" {
		lines = append(lines, "直播回放已生成")
	}
	if e.Kind == "live_end" {
		lines = append(lines, "直播已结束")
	}
	if e.Kind == "live_chat" {
		host := strings.TrimSpace(e.LiveHostAuthor)
		if host == "" {
			host = "其他成员"
		}
		lines = append(lines, "来自 "+host+" 的直播")
	}
	if e.Kind == "moment" && e.MembershipOnly {
		lines = append(lines, "发布了会员专属 Moment（当前账号无权查看内容）")
	}
	if e.PasswordProtected {
		contentType := "帖子"
		if e.Kind == "moment" {
			contentType = " Moment"
		}
		lines = append(lines, "发布了密码保护"+contentType+"（尚未配置密码）")
	}

	if (e.Kind == "live_end" || e.Kind == "live_replay") && e.LiveDuration > 0 {
		lines = append(lines, "直播时长："+formatDouyinDuration(time.Duration(e.LiveDuration)*time.Second))
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
		say(fan, e.ParentTranslation)
		if e.ParentTranslation == "" && e.ParentBody != "" {
			say(fan, "（译文暂不可用，原文见下）")
		}
		say(author, e.Translation)
		if e.Translation == "" && e.Body != "" {
			say(author, "（译文暂不可用，原文见下）")
		}
		lines = append(lines, "")
		say(fan, e.ParentBody)
		say(author, e.Body)
	} else {
		say(fan, e.ParentBody)
		say(author, e.Body)
	}
	if e.Translation == "" && e.ParentTranslation == "" && e.TranslationError != "" {
		lines = append(lines, "（翻译暂不可用，已保留原文）")
	}
	for _, video := range e.Videos {
		if video.URL == "" {
			lines = append(lines, "[视频]（播放文件暂不可用，请打开原帖观看）")
		} else {
			lines = append(lines, "[视频]（视频单独发送）")
		}
	}
	return strings.Join(lines, "\n")
}

func weverseEventFooter(e weverse.Event) string {
	parts := []string{}
	if e.URL != "" {
		parts = append(parts, e.URL)
	}
	if e.Time > 0 {
		loc, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		parts = append(parts, time.UnixMilli(e.Time).In(loc).Format("2006-01-02 15:04:05"))
	}
	return strings.Join(parts, "\n\n")
}

func formatWeverseEvent(e weverse.Event) string {
	body, footer := weverseEventBody(e), weverseEventFooter(e)
	if footer != "" {
		return body + "\n\n" + footer
	}
	return body
}
func (b *Bot) runWeverseLoop(ctx context.Context) {
	dir := weverse.Dir(b.cfg.ConfigPath())
	history, historyErr := weverse.OpenHistory(dir)
	if historyErr != nil {
		log.Printf("[Weverse] 无法打开原文历史记录: %v", historyErr)
		return
	}
	defer history.Close()
	go b.runWeverseAISummaryLoop(ctx, dir)
	go b.runWeverseReportLoop(ctx, dir)
	go b.runWeversePasswordSyncLoop(ctx, dir)
	// 复用同一个 Client：成员名单缓存挂在 Client 实例上，若每轮 NewClient，
	// 缓存永远命不中（等于没缓存）。配置里的代理变化时才重建。
	var client *weverse.Client
	var clientProxy string
	// 用本地历史库预热成员名单缓存：启动后第一轮即可省掉成员列表请求。
	if cfg, e := weverse.LoadSettings(dir); e == nil {
		client = weverse.NewClient(dir, cfg.ProxyURL)
		clientProxy = cfg.ProxyURL
		for _, sub := range cfg.Subscriptions {
			if sub.Enabled && sub.CommunityID > 0 {
				client.PrimeMembersFromHistory(history, sub.CommunityID)
			}
		}
	}
	var state weverse.Runtime
	if e := weverse.Read(dir, "state.json", &state); e != nil {
		log.Printf("[Weverse] 无法读取去重状态: %v", e)
		return
	}
	var status weverse.Status
	_ = weverse.Read(dir, "status.json", &status)
	// 失败退避：连续失败时逐步拉长等待，避免在 Weverse 侧异常时形成紧密重试。
	failures := 0
	for {
		cfg, e := weverse.LoadSettings(dir)
		// 成功时用配置的间隔（下限 1s）——Weverse 实际未对本 bot 限流，
		// 间隔只作为"下一轮何时开始"的最小等待，不必像以前那样默认 60s。
		// 真正的保护是失败退避，见循环末尾。
		interval := time.Duration(weverse.MinPollSeconds) * time.Second
		if cfg.PollSeconds >= weverse.MinPollSeconds {
			interval = time.Duration(cfg.PollSeconds) * time.Second
		}
		if e == nil && cfg.Enabled && len(cfg.Subscriptions) > 0 {
			if client == nil || clientProxy != cfg.ProxyURL {
				if client != nil {
					client.HTTP.CloseIdleConnections()
				}
				client = weverse.NewClient(dir, cfg.ProxyURL)
				clientProxy = cfg.ProxyURL
			}
			c := client
			cycle, cancel := context.WithTimeout(ctx, 3*time.Minute)
			events, err := c.Events(cycle)
			if err == nil {
				var ends []weverse.Event
				ends, err = c.LiveEndEvents(cycle, &state, cfg, events, time.Now())
				if err == nil {
					events = append(events, ends...)
				}
			}
			if err == nil {
				err = history.Record(events, "")
				for cid, members := range c.Artists {
					if err == nil {
						err = history.SaveMembers(cid, members)
					}
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
						c.ResolveEventVideos(cycle, &event)
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
						deliveryEvent := event
						if !s.Translate && deliveryEvent.Kind == "live_chat" {
							deliveryEvent.Translation = ""
						}
						// A member replying to another comment becomes a structured
						// reply so Feishu can thread it under the parent message.
						// QQ keeps the legacy flat text so its format stays unchanged.
						if doc := weverseReplyDocument(s, deliveryEvent); doc != nil {
							b.sendWeverseReply(s, deliveryEvent, doc)
						} else if doc := weversePostDocument(s, deliveryEvent); doc != nil {
							// 帖子也走Document：它要作为后续顶层回复的挂载目标，
							// 必须带 SourceID 才能被 ReplyMap 记录。
							b.sendWeversePost(s, deliveryEvent, doc)
						} else {
							messages := weverseMessageGroups(s, deliveryEvent)
							if strings.EqualFold(b.cfg.MediaDelivery, "local") {
								b.localizeMessageGroups(messages)
							}
							for _, msg := range messages {
								b.sendToTargetIDs(s.TargetIDs, msg)
							}
						}
						if err := history.Record([]weverse.Event{event}, s.ID); err != nil {
							log.Printf("[Weverse] 无法保存已转发原文: %v", err)
						}
						if err := weverse.CollectAI(dir, s, event, time.Now()); err != nil {
							log.Printf("[Weverse AI] 无法保存聊天记录: %v", err)
						}
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
			_ = weverse.Write(dir, "status.json", status)
		} else if e != nil {
			log.Printf("[Weverse] 读取配置失败: %v", e)
		} else { // A paused platform starts from a fresh baseline when resumed.
			state = weverse.Runtime{}
			_ = weverse.Write(dir, "state.json", state)
		}
		// 成功则立刻恢复最小间隔；失败按 5/10/20 秒退避，避免异常时紧密重试。
		if e == nil {
			failures = 0
		} else {
			failures++
			backoff := time.Duration(5*(1<<min(failures-1, 2))) * time.Second
			if backoff > interval {
				interval = backoff
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// runWeversePasswordSyncLoop 从密码来源站（h2h-link.com）同步私密帖密码。
//
// ★★★ 调度策略：按需触发（2026-10-08 用户定稿）
//
//	用户原话：
//	「如果检测到有需要解锁的私密帖，且还没有解锁，接下来这几天一天打一次；
//	  如果没有需要解锁的私密帖，压根就不需要打。」
//
//	理由（用户原话）：「我也不知道那个网站上到底什么时候更新。」
//	固定 7 天一轮的问题是：不管有没有待解锁帖都在打，浪费；而真的有新私密帖时
//	又要干等最多 7 天。改成按需之后：
//
//	  有待解锁帖 → 每 20 小时打一次（留 4 小时余量，不会掐在同一时间点）
//	  无待解锁帖 → 每 24 小时只**查一次本地待解锁列表**（不联网），
//	             一旦出现新的待解锁帖，下一轮立刻转入高频模式
//
//	「有没有待解锁帖」完全靠本地判断（PendingLockedPosts 读 history.db），
//	不产生任何外部请求。
const (
	// weversePasswordSyncActiveInterval 有待解锁帖时的同步间隔（20 小时）。
	weversePasswordSyncActiveInterval = 20 * time.Hour

	// weversePasswordSyncIdleCheckInterval 无待解锁帖时的检查间隔（24 小时）。
	// 这一轮**不联网**，只读本地库判断有没有新帖需要解锁。
	weversePasswordSyncIdleCheckInterval = 24 * time.Hour

	// weversePasswordSyncRetryInterval 同步失败后的重试间隔（2 小时）。
	weversePasswordSyncRetryInterval = 2 * time.Hour
)

// weversePendingLockedCount 返回「已采集到但还没有密码」的私密帖数量。
// 只读本地 history.db，不发任何网络请求。
func weversePendingLockedCount(dir string) int {
	settings, err := weverse.LoadReportSettings(dir)
	if err != nil || settings.CommunityID <= 0 {
		return 0
	}
	known, err := weverse.LoadPostPasswords(dir)
	if err != nil {
		return 0
	}
	history, err := weverse.OpenHistory(dir)
	if err != nil {
		return 0
	}
	defer history.Close()
	pending, err := history.PendingLockedPosts(settings.CommunityID, known)
	if err != nil {
		return 0
	}
	return len(pending)
}

func (b *Bot) runWeversePasswordSyncLoop(ctx context.Context, dir string) {
	var previous weverse.PasswordSyncStatus
	_ = weverse.Read(dir, "password-sync-status.json", &previous)

	for {
		// ── 决定这一轮要不要真的联网同步 ──
		//
		// 判定完全基于本地待解锁列表。只有「有帖等着解锁」才值得去打来源站 ——
		// 没有待解锁帖时，那个站上有没有更新对我们毫无意义。
		pending := weversePendingLockedCount(dir)
		if pending == 0 {
			// 空闲模式：睡一整天，期间只做本地检查（本次循环开头那次）
			log.Printf("[Weverse Password] 当前没有待解锁的私密帖，暂停同步（下次本地检查 %s）",
				weversePasswordSyncIdleCheckInterval)
			if !sleepCtx(ctx, weversePasswordSyncIdleCheckInterval) {
				return
			}
			continue
		}

		// 高频模式：先按上次成功时间算要不要等；有待解锁帖时等 20 小时。
		if last, err := time.Parse(time.RFC3339, previous.LastSuccess); err == nil {
			wait := time.Until(last.Add(weversePasswordSyncActiveInterval))
			if wait > 0 {
				if !sleepCtx(ctx, wait) {
					return
				}
			}
		}
		// 睡醒后重新确认（期间可能已被手动解锁过）
		if weversePendingLockedCount(dir) == 0 {
			continue
		}

		settings, err := weverse.LoadSettings(dir)
		var communityID int64
		slug := ""
		if err == nil && settings.Enabled {
			for _, subscription := range settings.Subscriptions {
				if subscription.Enabled && subscription.Slug == "hearts2hearts" {
					communityID = subscription.CommunityID
					slug = subscription.Slug
					break
				}
			}
		}
		if err != nil || communityID == 0 {
			if !sleepCtx(ctx, 6*time.Hour) {
				return
			}
			continue
		}

		history, openErr := weverse.OpenHistory(dir)
		if openErr != nil {
			log.Printf("[Weverse Password] 无法打开历史记录: %v", openErr)
			if !sleepCtx(ctx, 6*time.Hour) {
				return
			}
			continue
		}
		client := weverse.NewClient(dir, settings.ProxyURL)
		cycle, cancel := context.WithTimeout(ctx, 15*time.Minute)
		status, syncErr := client.SyncH2HPasswords(cycle, history, communityID, slug)
		cancel()
		client.HTTP.CloseIdleConnections()
		history.Close()
		if syncErr != nil {
			status.Error = syncErr.Error()
			log.Printf("[Weverse Password] 同步失败: %v", syncErr)
		} else {
			log.Printf("[Weverse Password] 同步完成: 来源=%d 候选=%d 新增=%d 已有=%d 未匹配=%d",
				status.Entries, status.Candidates, status.Imported, status.AlreadyKnown, status.Unmatched)
		}
		if err := weverse.Write(dir, "password-sync-status.json", status); err != nil {
			log.Printf("[Weverse Password] 无法保存同步状态: %v", err)
		}
		previous = status

		// ★ 下次等待多久，取决于「还有没有待解锁帖」，而不是固定 7 天。
		next := weversePasswordSyncActiveInterval
		if syncErr != nil {
			next = weversePasswordSyncRetryInterval
		} else if weversePendingLockedCount(dir) == 0 {
			// 这次同步把待解锁的都解掉了 ⇒ 转空闲，按 24 小时本地检查
			next = weversePasswordSyncIdleCheckInterval
			log.Printf("[Weverse Password] 待解锁帖已清空，转入空闲模式")
		}
		if !sleepCtx(ctx, next) {
			return
		}
	}
}

// sleepCtx 等 d，返回 false 表示 ctx 已取消（调用方应退出）。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func weverseMessageSegments(s weverse.Subscription, e weverse.Event) []interface{} {
	segments := []interface{}{}
	if s.MentionsAll(e.MemberID) {
		segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	body := weverseEventBody(e)
	if len(e.Images) > 0 || len(e.Videos) > 0 {
		body += "\n\n"
	}
	segments = append(segments, napcat.TextSegment(body))
	for _, image := range e.Images {
		segments = append(segments, napcat.ImageSegment(image))
	}
	for _, video := range e.Videos {
		if video.CoverURL != "" {
			segments = append(segments, napcat.ImageSegment(video.CoverURL))
		}
	}
	if footer := weverseEventFooter(e); footer != "" {
		segments = append(segments, napcat.TextSegment("\n\n"+footer))
	}
	return segments
}

// QQ requires video to be sent alone. The card keeps its cover and placeholder.
func weverseMessageGroups(s weverse.Subscription, e weverse.Event) [][]interface{} {
	groups := [][]interface{}{weverseMessageSegments(s, e)}
	for _, video := range e.Videos {
		if video.URL != "" {
			groups = append(groups, []interface{}{napcat.VideoSegment(video.URL, video.CoverURL)})
		}
	}
	return groups
}

// sendWeverseReply routes a member comment-reply by platform. Feishu receives the
// structured Document (native threading + 译文/原文 grey blocks); QQ keeps the
// legacy flat text produced by weverseMessageGroups so its rendering is unchanged.
func (b *Bot) sendWeverseReply(s weverse.Subscription, e weverse.Event, doc *message.Document) {
	if b == nil || b.outbound == nil || doc == nil {
		return
	}
	qqGroups := weverseMessageGroups(s, e)
	if strings.EqualFold(b.cfg.MediaDelivery, "local") {
		b.localizeMessageGroups(qqGroups)
	}
	for _, targetID := range s.TargetIDs {
		target := b.cfg.ResolveTarget(targetID)
		if target.ID == "" {
			continue
		}
		if target.Platform == "feishu" {
			b.sendTarget(target, doc)
		} else {
			for _, msg := range qqGroups {
				b.sendTarget(target, msg)
			}
		}
	}
}

// isWeversePostAuthoredByMember 判断这条评论所依附的主帖是否由成员本人发布。
// 成员（含队友）主帖下的顶层回复挂到该帖下，读起来就是"在帖子下面回了一条"。
func isWeversePostAuthoredByMember(e weverse.Event) bool {
	if e.PostContext == nil {
		return false
	}
	if !e.PostContext.AuthorIsArtist {
		return false
	}
	if postMember := strings.TrimSpace(e.PostContext.MemberID); postMember == "" {
		return false
	}
	return true
}

// weversePostDocument 把成员/队友的主帖渲染成带 SourceID 的 Document。
// 帖子走 Document 的目的是让后续的顶层回复能原生挂载到它下面；QQ 侧仍走
// 原有文本路径，格式不受影响。
func weversePostDocument(s weverse.Subscription, e weverse.Event) *message.Document {
	if e.Kind != "post" || strings.TrimSpace(e.PostID) == "" {
		return nil
	}
	author := strings.TrimSpace(e.Author)
	if author == "" {
		author = strings.TrimSpace(e.MemberID)
	}
	if author == "" {
		return nil
	}
	body := strings.TrimSpace(e.Body)
	translation := strings.TrimSpace(e.Translation)
	if translation == body {
		translation = ""
	}
	doc := &message.Document{
		Source:      "Weverse",
		Title:       author,
		Author:      author,
		Body:        body,
		Translation: translation,
		Link:        e.URL,
		CreatedAt:   time.UnixMilli(e.Time),
		SourceID:    e.PostID,
		// ★ 飞书侧的 @全体成员 只认 Document.MentionAll（渲染成 <at id=all></at>）。
		//   QQ 路径靠 weverseMessageSegments 插 napcat.AtSegment，两者互不相通 ——
		//   此前只做了 QQ 那边，于是「@了指定成员」的订阅在飞书上永远不 @。
		MentionAll: s.MentionsAll(e.MemberID),
	}
	for _, img := range e.Images {
		if img != "" {
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: img})
		}
	}
	// 视频必须一起带上：帖子一旦漏了 Videos，飞书侧就只剩图片和正文，
	// 视频既不显示也不显示封面（此前只处理 Images，帖子视频整段丢失）。
	// 没解析出直链时仍要把封面作为图片发出去，避免"什么都没发"。
	for _, video := range e.Videos {
		switch {
		case video.URL != "":
			doc.Media = append(doc.Media, message.Media{Kind: "video", Source: video.URL, Cover: video.CoverURL})
		case video.CoverURL != "":
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: video.CoverURL})
		}
	}
	return doc
}

// sendWeversePost 投递主帖：飞书走 Document，QQ 保持原有文本格式。
func (b *Bot) sendWeversePost(s weverse.Subscription, e weverse.Event, doc *message.Document) {
	qqGroups := weverseMessageGroups(s, e)
	if strings.EqualFold(b.cfg.MediaDelivery, "local") {
		b.localizeMessageGroups(qqGroups)
	}
	for _, targetID := range s.TargetIDs {
		target := b.cfg.ResolveTarget(targetID)
		if target.ID == "" {
			continue
		}
		if target.Platform == "feishu" {
			b.sendTarget(target, doc)
		} else {
			for _, msg := range qqGroups {
				b.sendTarget(target, msg)
			}
		}
	}
}

// weverseReplyDocument builds a structured reply for a member comment that
// answers another comment we may have already delivered. Feishu threads it under
// the parent message when it knows the parent's message id. Returns nil when the
// event is not a reply we can thread (no parent comment id).
func weverseReplyDocument(s weverse.Subscription, e weverse.Event) *message.Document {
	if e.Kind != "comment" || e.CommentID == "" {
		return nil
	}
	// A reply must carry some parent context. When we know the parent comment id we
	// can thread natively; otherwise we still render 被回复 + 回复 as a quote block,
	// so member replies look the same whether or not we can thread them. Plain
	// top-level comments (no parent context) stay on the flat path.
	if strings.TrimSpace(e.ParentCommentID) == "" &&
		strings.TrimSpace(e.ParentBody) == "" &&
		strings.TrimSpace(e.ParentAuthor) == "" {
		return nil
	}
	replier := strings.TrimSpace(e.Author)
	if replier == "" {
		replier = "成员"
	}
	author := replier
	if e.ParentProfileType == "ARTIST" {
		// 成员回复成员：被回复者也是成员，顶栏用 "回复者 & 被回复成员"。
		if parentAuthor := strings.TrimSpace(e.ParentAuthor); parentAuthor != "" && parentAuthor != author {
			author += " & " + parentAuthor
		}
	} else if e.PostContext != nil && e.PostContext.AuthorIsArtist &&
		strings.TrimSpace(e.MemberID) != "" && strings.TrimSpace(e.PostContext.MemberID) != "" &&
		e.MemberID != e.PostContext.MemberID {
		// 成员在别的成员帖子下回复粉丝：仍标注帖子主人 "B（A）"。
		if postAuthor := strings.TrimSpace(e.PostContext.Author); postAuthor != "" {
			author += "（" + postAuthor + "）"
		}
	}
	parentAuthor := strings.TrimSpace(e.ParentAuthor)
	if parentAuthor == "" {
		parentAuthor = "原帖"
	}

	// Body carries the original text; Translation the machine translation. The
	// parent comment becomes a structured quote carrying its own original +
	// translation so Feishu can render 译文 above 原文 for both.
	body := strings.TrimSpace(e.Body)
	translation := strings.TrimSpace(e.Translation)
	if translation == body {
		translation = ""
	}
	// 卡片顶栏已经标明回复者是谁，正文再重复一次"回复者："是冗余的，
	// 飞书上尤其突兀（译文是主视觉大字）。故正文不再加该前缀。
	_ = replier
	parentBody := strings.TrimSpace(e.ParentBody)
	parentTranslation := strings.TrimSpace(e.ParentTranslation)
	if parentTranslation == parentBody {
		parentTranslation = ""
	}

	doc := &message.Document{
		Source:      "Weverse",
		Title:       author,
		Author:      author,
		Body:        body,
		Translation: translation,
		Link:        e.URL,
		CreatedAt:   time.UnixMilli(e.Time),
		SourceID:    e.CommentID,
		// 同上：@全体成员 必须由 Document 传给飞书。
		MentionAll: s.MentionsAll(e.MemberID),
	}
	// 挂载：优先挂到被回复的评论下（parentCommentId）。
	// 主帖下的顶层回复没有 parentCommentId——此时父上下文就是帖子本身，
	// 挂到 postId 即可原生挂载。早期只认 parentCommentId，导致爱豆回复自己
	// （或队友）的主帖时只能退化成「被回复/回复」分隔线排版。
	postID := strings.TrimSpace(e.PostID)
	memberPost := postID != "" && isWeversePostAuthoredByMember(e)
	parentCommentID := strings.TrimSpace(e.ParentCommentID)
	switch {
	case parentCommentID != "":
		doc.ReplyToSourceID = parentCommentID
	case memberPost:
		doc.ReplyToSourceID = postID
	}
	// 成员绝大多数是回复**粉丝**的评论，而粉丝评论我们从不转发，所以父评论在
	// ReplyMap 里查不到，挂载会静默失效。成员主帖一定转发过，拿它当兜底：
	// 父评论挂不上时至少挂在帖子下面，读起来仍是「在这个帖子里回复了一条」。
	//
	// 判据是「父评论是否由成员发布」（ParentProfileType 已由解析层按成员名单
	// 归一为 ARTIST），而不是「主帖是否等于首选目标」——回复队友评论时父评论
	// 本身就是转发过的成员评论，能直接挂上，无需兜底，引用块也不必保留
	//（原生 reply 上方已经是被回复内容）。
	if parentCommentID != "" && e.ParentProfileType != "ARTIST" && memberPost {
		doc.ReplyToFallbackSourceIDs = []string{postID}
		// 兜底挂到的是**帖子**而不是被回复的那条评论，原生 reply 上方不会自动
		// 带出该评论。引用块必须保留，否则「回复了谁」就彻底丢了。
		doc.KeepQuoteWhenThreaded = true
	}
	// 有父上下文就带上引用块。挂载成功时是否重复，由适配器结合
	// KeepQuoteWhenThreaded 决定——这里无法预知兜底目标能否命中。
	if parentBody != "" || parentTranslation != "" {
		doc.Quote = &message.Quote{Author: parentAuthor, Text: parentBody, Translation: parentTranslation}
		// 只要渲染了「被回复 + 回复」两段，回复段就带上成员名，让两行成为
		// 「粉丝昵称：… / 成员名：…」的并列关系。
		//
		// 判据是「有没有引用块」，而不是「有没有兜底挂载目标」：成员在自己
		// 主帖下的顶层回复没有 parentCommentId，早先把它和"挂到父评论"的
		// 场景绑在一起，导致这类回复（最常见的一种）反而没有名字前缀。
		doc.ReplyAuthorPrefix = replier
	}
	for _, img := range e.Images {
		if img != "" {
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: img})
		}
	}
	for _, video := range e.Videos {
		if video.URL != "" {
			doc.Media = append(doc.Media, message.Media{Kind: "video", Source: video.URL, Cover: video.CoverURL})
		}
	}
	return doc
}
