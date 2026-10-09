package logic

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
)

// pocket48SourceID builds a stable business id for a Pocket48 message from the
// room plus the speaker and the text.
//
// Why content-addressed: a REPLY frame does not carry the id of the message it
// answers, so the only way to thread the reply under the original on Feishu is to
// derive the same key on both sides. Using room + nickname + body means the
// original message and the reply that quotes it land on the same key, and
// ReplyMap can then resolve it to the original's platform message id.
func pocket48SourceID(room *pocket48.RoomInfo, speaker, text string) string {
	roomKey := ""
	if room != nil {
		roomKey = fmt.Sprintf("%d|%d", room.ServerID, room.ChannelID)
	}
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	sum := sha256.Sum256([]byte(roomKey + "\x00" + strings.TrimSpace(speaker) + "\x00" + normalized))
	return "p48:" + hex.EncodeToString(sum[:8])
}

func (b *Bot) mediaDeliveryMode() string {
	if b == nil || b.cfg == nil {
		return "local"
	}
	mode := strings.ToLower(strings.TrimSpace(b.cfg.MediaDelivery))
	if mode == "url" || mode == "direct" {
		return "remote"
	}
	if mode == "remote" {
		return "remote"
	}
	return "local"
}

func (b *Bot) mediaPathForMessage(msg *pocket48.Message, mediaURL string) string {
	mediaURL = normalizeMediaURL(strings.TrimSpace(mediaURL))
	if mediaURL == "" {
		return ""
	}
	// MEDIA_DELIVERY=remote: pass CDN URL straight to NapCat (NapCat downloads).
	// default/local: bot downloads first — faster on same CDN, avoids NapCat reordering.
	if b.mediaDeliveryMode() == "remote" {
		if msg != nil {
			log.Printf("[Media] Remote delivery id=%s type=%s url=%s", msg.MsgIDServer, msg.Type, mediaURL)
		}
		return mediaURL
	}
	local := b.localMediaPath(mediaURL)
	if local != "" && local != mediaURL {
		if msg != nil {
			log.Printf("[Media] Local path for NapCat id=%s type=%s path=%s", msg.MsgIDServer, msg.Type, local)
		}
		return local
	}
	if msg != nil {
		log.Printf("[Media] Fallback remote URL id=%s type=%s url=%s", msg.MsgIDServer, msg.Type, mediaURL)
	}
	return mediaURL
}

func (b *Bot) shouldPollRoomMessages() bool {
	return !b.cfg.NIMRoomMessageEnabled || b.cfg.NIMRoomMessagePollFallback
}

// shouldPollRoomMessagesFor decides REST polling for one room.
// When QChat is connected and recently delivering for this room, skip REST
// entirely — fan traffic must not keep the REST path warm as a fallback.
func (b *Bot) shouldPollRoomMessagesFor(roomID int64) bool {
	if !b.shouldPollRoomMessages() {
		return false
	}
	if b.cfg.NIMRoomMessageEnabled && b.nimDanmaku != nil && b.nimDanmaku.RoomRealtimeActive(roomID) {
		return false
	}
	return true
}

func (b *Bot) pollLoop() {
	sem := make(chan struct{}, 20)

	for {
		if !b.isMonitoring {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		start := time.Now()

		// Collect all unique room IDs to poll
		roomIDs := make(map[int64]bool)
		for _, rooms := range b.cfg.GroupSubscriptions {
			for _, roomID := range rooms {
				roomIDs[roomID] = true
			}
		}

		var anyNewMsgs bool
		var wg sync.WaitGroup
		var mu sync.Mutex
		for roomID := range roomIDs {
			wg.Add(1)
			sem <- struct{}{}
			go func(id int64) {
				defer wg.Done()
				defer func() { <-sem }()
				if b.pollRoom(id) {
					mu.Lock()
					anyNewMsgs = true
					mu.Unlock()
				}
			}(roomID)
		}
		wg.Wait()

		// Adaptive polling: adjust sleep time based on message activity
		b.adjustPollInterval(anyNewMsgs)

		// Log queue depth periodically for monitoring
		if b.outbound != nil {
			if qd := b.outbound.QueueDepth(); qd > 5 {
				log.Printf("[QUEUE] outbound depth=%d", qd)
			}
		}

		elapsed := time.Since(start)
		currentInterval := b.currentPollInterval()
		if elapsed < currentInterval {
			time.Sleep(currentInterval - elapsed)
		}
	}
}

func (b *Bot) pollRoom(roomID int64) (hadNewMsgs bool) {
	if b.isPocketAuthExpired() {
		return false
	}
	// QChat is the low-latency path. REST remains a safety net only while
	// realtime is down/idle for this room — never as a fan-message fallback.
	if !b.shouldPollRoomMessagesFor(roomID) {
		return false
	}

	roomInfo, err := b.getCachedRoomInfo(roomID)
	if err != nil {
		if !b.handlePocketAuthError(err) {
			log.Printf("Failed to get room info for %d: %v", roomID, err)
		}
		return false
	}
	b.clearPocketAuthExpired()

	// On-mic is handled by onMicLoop (independent REST), not gated by QChat.

	// Smart limit: polling interval short → fewer messages needed
	limit := b.smartMessageLimit()
	msgs, err := b.pocket.GetMessages(roomInfo, limit)
	if err != nil {
		if !b.handlePocketAuthError(err) {
			log.Printf("Failed to get messages for %d: %v", roomID, err)
		}
		return false
	}
	b.observeRESTQChatIdentities(roomInfo, msgs)

	// Filter by time logic
	b.mu.RLock()
	loaded := b.cursorLoaded[roomID]
	b.mu.RUnlock()
	if !loaded && b.storage != nil {
		if cursor, err := b.storage.GetCursor(roomID); err == nil && cursor != nil && cursor.LastMsgTime > 0 {
			b.mu.Lock()
			b.lastMsgTime[roomID] = cursor.LastMsgTime
			b.cursorLoaded[roomID] = true
			b.mu.Unlock()
			log.Printf("[Cursor] Restored cursor for room %d: last_time=%d", roomID, cursor.LastMsgTime)
		} else {
			b.mu.Lock()
			b.cursorLoaded[roomID] = true
			b.mu.Unlock()
		}
	}

	b.mu.RLock()
	lastTime := b.lastMsgTime[roomID]
	b.mu.RUnlock()

	// Initial Fetch Window Logic
	// If lastTime is 0, it means this is the first time we are checking this room (since bot start)
	// We should only fetch messages from NOW - InitialFetchWindow minutes.
	if lastTime == 0 {
		// Default to 60 min if 0 (though config should have default)
		window := b.cfg.InitialFetchWindow
		if window <= 0 {
			window = 60
		}
		startTime := time.Now().Add(-time.Duration(window) * time.Minute).UnixMilli()

		// If the oldest message in the list is newer than startTime, that's fine.
		// If we set lastTime to startTime, we will pick up everything after startTime.
		lastTime = startTime
		// Update map so we don't repeat this check
		b.mu.Lock()
		b.lastMsgTime[roomID] = lastTime
		b.mu.Unlock()
	}

	var newLastTime int64 = lastTime
	var validMsgs []*pocket48.Message

	// Messages from API are usually Newest -> Oldest (index 0 is newest)
	// We want to process Oldest -> Newest.
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Time <= lastTime {
			continue
		}
		if msg.Time > newLastTime {
			newLastTime = msg.Time
		}

		validMsgs = append(validMsgs, msg)
	}

	if b.isAnnualScoreEnabled(roomID) {
		allMsgs, err := b.pocket.GetAllMessages(roomInfo, 100)
		if err != nil {
			log.Printf("Failed to get all messages for annual score room %d: %v", roomID, err)
		} else {
			for i := len(allMsgs) - 1; i >= 0; i-- {
				msg := allMsgs[i]
				if msg.Time <= lastTime || msg.Type != pocket48.MsgGiftText {
					continue
				}
				if _, ok := parseAnnualScoreGiftMessage(msg.Body); !ok {
					continue
				}
				if msg.Time > newLastTime {
					newLastTime = msg.Time
				}
				validMsgs = append(validMsgs, msg)
			}
		}
	}

	if len(validMsgs) > 0 {
		hadNewMsgs = true
		b.processMessages(validMsgs)

		b.mu.Lock()
		b.lastMsgTime[roomID] = newLastTime
		b.mu.Unlock()

		// 保存游标到 storage
		if b.storage != nil && newLastTime > 0 {
			b.storage.SaveCursor(roomID, "", newLastTime)
		}
	}
	return
}

func (b *Bot) processMessages(msgs []*pocket48.Message) {
	if len(msgs) == 0 {
		return
	}

	filtered := make([]*pocket48.Message, 0, len(msgs))
	for _, msg := range msgs {
		if b.markMessageSeen(msg) {
			filtered = append(filtered, msg)
		}
	}
	msgs = filtered
	if len(msgs) == 0 {
		return
	}

	// Sort messages by time to ensure order
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Time < msgs[j].Time
	})

	var targets []config.DeliveryTarget
	sampleMsg := msgs[0]

	targets = b.getTargetsForRoom(sampleMsg.Room.ChannelID)

	if len(targets) == 0 {
		return
	}

	// Process each message immediately without batching
	for _, msg := range msgs {
		b.processSinglePocketMessage(msg, targets)
	}
}

// markMessageSeen atomically deduplicates the same message delivered through
// QChat and the REST fallback during a reconnect boundary.
func (b *Bot) markMessageSeen(msg *pocket48.Message) bool {
	if msg == nil {
		return false
	}
	id := strings.TrimSpace(msg.MsgIDServer)
	if id == "" {
		id = strings.TrimSpace(msg.MsgIDClient)
	}
	if id == "" {
		return true
	}
	roomID := int64(0)
	if msg.Room != nil {
		roomID = msg.Room.ChannelID
	}
	key := fmt.Sprintf("%d:%s", roomID, id)
	now := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.seenMessageIDs[key]; exists {
		return false
	}
	b.seenMessageIDs[key] = now
	if len(b.seenMessageIDs) > 5000 {
		cutoff := now.Add(-30 * time.Minute)
		for seenKey, seenAt := range b.seenMessageIDs {
			if seenAt.Before(cutoff) {
				delete(b.seenMessageIDs, seenKey)
			}
		}
	}
	return true
}

func (b *Bot) isAnnualScoreEnabled(roomID int64) bool {
	if roomID == 0 || b.cfg.AnnualScoreSpecific == nil {
		return false
	}
	return b.cfg.AnnualScoreSpecific[strconv.FormatInt(roomID, 10)]
}

func (b *Bot) annualScoreSegments(room *pocket48.RoomInfo, sender string, gift *AnnualScoreGift, msgTimeMs int64) []interface{} {
	if room == nil {
		room = &pocket48.RoomInfo{}
	}
	if gift == nil {
		gift = &AnnualScoreGift{GiftName: "礼物", GiftNum: 1}
	}
	owner := strings.TrimSpace(room.OwnerName)
	if owner == "" {
		owner = strings.TrimSpace(gift.ReceiverName)
	}
	if owner == "" {
		owner = "成员"
	}
	channelName := strings.TrimSpace(room.ChannelName)
	if channelName == "" {
		channelName = owner + "的房间"
	}
	sender = strings.TrimSpace(sender)
	if sender == "" {
		sender = "用户"
	}
	giftName := strings.TrimSpace(gift.GiftName)
	if giftName == "" {
		giftName = "礼物"
	}
	giftNum := normalizeGiftNum(gift.GiftNum)
	score := gift.TotalScore
	if score <= 0 {
		score = gift.UnitScore
	}
	timeStr := time.UnixMilli(normalizeMessageTimestampMs(msgTimeMs)).Format("2006-01-02 15:04:05")

	segments := []interface{}{napcat.TextSegment(fmt.Sprintf("【%s|%s】\n", owner, channelName))}
	segments = appendTextWithQQFaces(segments, fmt.Sprintf("%s 送出 %d 个 %s\n", sender, giftNum, giftName))
	segments = append(segments, napcat.TextSegment(fmt.Sprintf("积分：+%s\n时间：%s", formatScoreValue(score), timeStr)))
	return segments
}

func (b *Bot) processSinglePocketMessage(msg *pocket48.Message, targets []config.DeliveryTarget) {
	room := msg.Room
	roomIDStr := strconv.FormatInt(room.ChannelID, 10)

	// Filter: GiftReply — gift replies and gift notifications
	if msg.Type == pocket48.MsgGiftReply || msg.Type == pocket48.MsgAudioGiftReply || msg.Type == pocket48.MsgGiftText {
		allowed := false
		if b.cfg.GiftSpecific != nil {
			if val, ok := b.cfg.GiftSpecific[roomIDStr]; ok {
				allowed = val
			}
		}
		if !allowed {
			return
		}
	}

	timeObj := time.Unix(msg.Time/1000, 0)
	timeStr := timeObj.Format("2006-01-02 15:04:05")

	// Header: 【OwnerName|ChannelName】
	header := fmt.Sprintf("【%s|%s】\n", room.OwnerName, room.ChannelName)

	// Build message content based on type
	var segments []interface{}
	segments = append(segments, napcat.TextSegment(header))
	// A reply message may carry a structured Document (with Quote) so Feishu can
	// render a native reply block; QQ falls back to ToSegments.
	var doc *message.Document

	// Name logic - determine sender by message userId, not room owner
	realName := ""
	nickName := msg.NickName

	if nickName == "" {
		nickName = msg.ExtInfo.User.Nickname
	}

	// channelRole: "2" = 偶像(房间主人), "3" = 其他小偶像, "0"或空 = 粉丝
	channelRole := msg.ExtInfo.ChannelRole
	senderUserID := msg.ExtInfo.User.UserID
	isOwnerMessage := channelRole == "2" || (senderUserID != 0 && senderUserID == room.OwnerID)
	isStarMessage := channelRole == "3"

	// Filter: only forward messages from room owner or other idols, skip fans.
	if !isOwnerMessage && !isStarMessage {
		if senderUserID == 0 {
			return // cannot determine sender, skip
		}
		if !b.isKnownStar(senderUserID) {
			return // not a known star (idol), skip fan message
		}
	}

	if isOwnerMessage {
		realName = room.OwnerName
		if nickName == "" {
			nickName = room.OwnerName
		}
	} else if msg.StarName != "" {
		// Message already has starName from API (works for cross-room posts)
		realName = msg.StarName
		if nickName == "" {
			nickName = msg.StarName
		}
	} else if senderUserID != 0 {
		// Fallback: try API for user details
		if detailInfo, err := b.getCachedUserDetail(senderUserID); err == nil && detailInfo != nil && detailInfo.IsStar {
			if detailInfo.StarName != "" {
				realName = detailInfo.StarName
			}

			if nickName == "" {
				if detailInfo.Nickname != "" {
					nickName = detailInfo.Nickname
				} else if detailInfo.StarName != "" {
					nickName = detailInfo.StarName
				}
			}
		}
	}

	if nickName == "" {
		nickName = "未知用户"
	}

	if msg.Type == pocket48.MsgGiftText {
		if scoreGift, ok := parseAnnualScoreGiftMessage(msg.Body); ok {
			if !b.isAnnualScoreEnabled(room.ChannelID) {
				return
			}
			segments = b.annualScoreSegments(room, nickName, scoreGift, msg.Time)
			for _, target := range targets {
				b.sendTarget(target, segments)
			}
			return
		}
	}

	prefix := ""
	nickNameTrimmed := strings.TrimSpace(nickName)
	realNameTrimmed := strings.TrimSpace(realName)
	if realNameTrimmed != "" && realNameTrimmed != nickNameTrimmed {
		prefix = fmt.Sprintf("%s(%s): ", nickName, realName)
	} else {
		prefix = fmt.Sprintf("%s: ", nickName)
	}

	switch msg.Type {
	case pocket48.MsgText, pocket48.MsgGiftText:
		body := msg.Body
		// QChat GIFT_TEXT messages carry raw JSON — extract gift name for display.
		if msg.Type == pocket48.MsgGiftText && strings.HasPrefix(strings.TrimSpace(body), "{") {
			if formatted := formatGiftTextBody(body); formatted != "" {
				body = formatted
			}
		}
		displayText := b.extractTextBody(body)
		if quotedText, answerText, ok := parseEmbeddedReplyMessage(msg.Body); ok {
			if quotedText != "" {
				segments = appendTextWithQQFaces(segments, quotedText+"\n")
			}
			if answerText != "" {
				segments = appendTextWithQQFaces(segments, prefix+answerText+"\n")
			} else {
				segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
			}
		} else {
			// Give plain messages a content-addressed SourceID so a later REPLY
			// that quotes this exact text can thread under it on Feishu. Without
			// this the reply has nothing to attach to and degrades to a detached
			// quote. The sender is taken from the nickname already resolved into
			// prefix, so both sides derive the same key.
			if sender := strings.TrimSuffix(prefix, ": "); sender != "" && displayText != "" {
				doc = &message.Document{
					Source:    room.ChannelName,
					Title:     sender,
					Author:    sender,
					Body:      displayText,
					CreatedAt: timeObj,
					SourceID:  pocket48SourceID(room, sender, displayText),
				}
			}
			segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
		}

	case pocket48.MsgGiftReply:
		giftText, replyText, ok := parseGiftReplyMessage(msg.Body)
		if ok && giftText != "" {
			segments = appendTextWithQQFaces(segments, giftText+"\n")
			if replyText != "" {
				segments = appendTextWithQQFaces(segments, prefix+replyText+"\n")
			}
		} else {
			displayText := b.extractTextBody(msg.Body)
			segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
		}

	case pocket48.MsgAudioGiftReply:
		giftText, voiceURL, duration, ok := parseAudioGiftReplyMessage(msg.Body)
		if !ok || voiceURL == "" {
			displayText := b.extractTextBody(msg.Body)
			segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
			break
		}
		voiceURL = b.mediaPathForMessage(msg, voiceURL)
		textSegments := append([]interface{}{}, segments...)
		if giftText != "" {
			textSegments = appendTextWithQQFaces(textSegments, giftText+"\n")
		}

		audioLabel := "语音回复"
		if duration > 0 {
			audioLabel = fmt.Sprintf("语音回复 %ds", duration)
		}
		textSegments = appendTextWithQQFaces(textSegments, fmt.Sprintf("%s[%s]\n", prefix, audioLabel))
		textSegments = append(textSegments, napcat.TextSegment(timeStr))
		// QQ keeps the legacy two-message form (text then record). Feishu gets
		// one combined Document so the card and the voice bubble arrive
		// together instead of a placeholder plus a separate attachment.
		b.sendVoiceReply(targets, voiceURL, textSegments, strings.TrimSpace(giftText), audioLabel, msg)
		return

	case pocket48.MsgReply:
		displayText := b.extractTextBody(msg.Body)
		if quotedText, answerText, replyName, ok := parseEmbeddedReplyDetail(msg.Body); ok {
			if quotedText != "" {
				segments = appendTextWithQQFaces(segments, quotedText+"\n")
			}
			body := answerText
			if body == "" {
				body = displayText
			}
			// Carry reply-to context as a structured Document so Feishu renders
			// a native quote block; QQ falls back to the flat segments above.
			//
			// Author 必须是**真实发送人**（prefix 里那个昵称），不能写死
			// room.OwnerName：包间里成员互相回复时，写死会把别人的话标成房间
			// 主人说的，QQ 与飞书表现还不一致。
			if strings.TrimSpace(quotedText) != "" {
				sender := strings.TrimSuffix(prefix, ": ")
				if sender == "" {
					sender = room.OwnerName
				}
				// 引用块署名用被回复者昵称，缺省才退回通用标签，这样卡片上
				// 「哼唧小虎：…」与回复行「金兔牙子：…」能对齐成两个说话人。
				quoteAuthor := replyName
				if quoteAuthor == "" {
					quoteAuthor = "被回复"
				}
				// ★ 口袋48 的被回复原文里**已经带了说话人前缀**（如
				//   「convolk1:fixx好用」），渲染层还会再拼一次「昵称：」，
				//   于是线上出现「convolk1：convolk1:fixx好用」—— 同一个昵称两遍。
				// 这里在数据源处判定：原文自带就别再署名。
				if speakerAlreadyInText(quoteAuthor, quotedText) {
					quoteAuthor = ""
				}
				doc = &message.Document{
					Source: room.ChannelName,
					Title:  sender,
					Author: sender,
					Body:   body,
					Quote:  &message.Quote{Author: quoteAuthor, Text: quotedText},
					// ★ 补上回复者昵称：否则飞书侧回复行前面什么都没有，
					//   引用块有名字、回复行没名字，两行不像对话。
					//   只有存在引用块时渲染层才会用它（见 feishu.go deliver）。
					//   此前只有 weverse 设了这一项，口袋48 一直漏着。
					ReplyAuthorPrefix: sender,
					CreatedAt:         timeObj,
					// 挂到被回复的那条原始消息下面。口袋48 回复帧不带被回复
					// 消息 id，因此用「被回复者昵称 + 被回复内容」作为 SourceID；
					// 那条原始消息转发时也用同样的键记录，两边即可对齐。
					SourceID:        pocket48SourceID(room, replyName, quotedText),
					ReplyToSourceID: pocket48SourceID(room, replyName, quotedText),
				}
			}
			if answerText != "" {
				segments = appendTextWithQQFaces(segments, prefix+answerText+"\n")
			} else {
				segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
			}
		} else {
			segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
		}

	case pocket48.MsgImage, pocket48.MsgExpressImage:
		imageURL := b.mediaPathForMessage(msg, b.extractImageURL(msg.Body))
		if imageURL != "" {
			segments = append(segments, napcat.TextSegment(prefix))
			segments = append(segments, napcat.ImageSegment(imageURL))
		}

	case pocket48.MsgVideo:
		videoURL := b.mediaPathForMessage(msg, b.extractVideoURL(msg.Body))
		if videoURL != "" {
			segments = append(segments, napcat.VideoSegment(videoURL, ""))
		}

	case pocket48.MsgFlipCard:
		question, answer, _, ok := parseFlipCardBody(msg.Body)
		idolName := room.OwnerName
		if idolName == "" {
			idolName = "成员"
		}
		if ok && question != "" {
			flipText := fmt.Sprintf("【公开翻牌】\n粉丝提问: %s\n%s: %s", question, idolName, answer)
			segments = appendTextWithQQFaces(segments, flipText+"\n")
			// Text flip card is a reply-to (fan question → idol answer); carry
			// the question as Quote so Feishu renders a native reply block.
			doc = &message.Document{
				Source:    room.ChannelName,
				Title:     idolName,
				Author:    idolName,
				Body:      answer,
				Quote:     &message.Quote{Author: "粉丝提问", Text: question},
				CreatedAt: timeObj,
			}
		}

	case pocket48.MsgLivePush:
		shouldSend := b.cfg.LiveMonitoring
		if b.cfg.LiveSpecific != nil {
			if val, ok := b.cfg.LiveSpecific[roomIDStr]; ok {
				shouldSend = val
			}
		}
		title, cover, liveID, roomID := parseLivePushBody(msg.Body)
		// NIM monitoring is independent from whether the QQ live-start notice is
		// enabled. Join immediately on LIVEPUSH instead of waiting for discovery.
		if b.cfg.NIMEnabled && b.nimDanmaku != nil {
			go b.connectDanmakuForLive(liveID, roomID, room)
		}
		if !shouldSend {
			return
		}
		if title == "" {
			title = "📺 直播开始了"
		}
		cover = b.mediaPathForMessage(msg, cover)
		// Same layout as room message forward: @全体成员 / 【Owner|Channel】 / body / time
		idolName := strings.TrimSpace(room.OwnerName)
		if idolName == "" {
			idolName = strings.TrimSuffix(strings.TrimSpace(room.ChannelName), "的房间")
		}
		channelName := strings.TrimSpace(room.ChannelName)
		if channelName == "" {
			channelName = "包间"
		}
		segments = []interface{}{
			napcat.AtSegment("all"),
			napcat.TextSegment(fmt.Sprintf("\n【%s|%s】\n%s直播啦！——%s\n", idolName, channelName, idolName, title)),
		}
		if cover != "" {
			segments = append(segments, napcat.ImageSegment(cover))
		}
		segments = append(segments, napcat.TextSegment("\n"+timeStr))
		b.sendTarget(targets[0], segments)

		return

	case pocket48.MsgAudio:
		audioURL := b.mediaPathForMessage(msg, b.extractAudioURL(msg.Body))
		if audioURL != "" {
			// ★★★ 纯语音，不建 Document（2026-10-08 用户第三次要求）：
			//
			// 用户看到的是：顶栏「胡晓慧」+ 中间「语音消息」+ 底栏「Pocket 48」和
			// 时间，还得把语音挂在这条卡片下面。诉求原话：「为什么要先发一个文本
			// 占位……直接把语音发出来不就行了！」
			//
			// 根因：走 Document 通道时，飞书侧 sendCard 先发卡片（把 Sender/Body/
			// CreatedAt 渲染成顶栏+正文+底栏），再把媒体当**原生回复**挂上去 ——
			// 于是必然是「一条文本占位 + 一条挂载语音」。
			//
			// 现在只给飞书一段裸语音（flattenFeishuSegments 会单独走
			// sendMediaSegment → uploadVoice → msg_type=audio），
			// 飞书原生语音气泡直接落地，没有占位卡片、没有挂载。
			segments = []interface{}{napcat.RecordSegment(audioURL)}
		}

	case pocket48.MsgFlipCardAudio:
		question, _, _, _ := parseFlipCardBody(msg.Body)
		idolName := room.OwnerName
		audioURL := b.mediaPathForMessage(msg, b.extractAudioURL(msg.Body))
		if audioURL == "" {
			return
		}
		flipText := buildFlipCardAudioIntroText(idolName, question)
		segments = appendTextWithQQFaces(segments, flipText+"\n")
		segments = append(segments, napcat.RecordSegment(audioURL))
		if question != "" {
			doc = &message.Document{
				Source:    room.ChannelName,
				Title:     idolName,
				Author:    idolName,
				Body:      "语音翻牌",
				Quote:     &message.Quote{Author: "粉丝提问", Text: question},
				CreatedAt: timeObj,
				Media:     []message.Media{{Kind: "audio", Source: audioURL}},
			}
		}

	case pocket48.MsgFlipCardVideo:
		question, _, _, ok := parseFlipCardBody(msg.Body)
		idolName := room.OwnerName
		if idolName == "" {
			idolName = "成员"
		}
		videoURL := b.mediaPathForMessage(msg, b.extractVideoURL(msg.Body))
		if videoURL != "" {
			flipText := ""
			if ok && question != "" {
				flipText = fmt.Sprintf("【公开翻牌】\n粉丝提问: %s\n%s: 【回复见下方】", question, idolName)
			} else {
				flipText = fmt.Sprintf("【公开翻牌】\n%s: [视频翻牌]", idolName)
			}
			segments = []interface{}{
				napcat.TextSegment(flipText + "\n"),
				napcat.VideoSegment(videoURL, ""),
			}
			if question != "" {
				doc = &message.Document{
					Source:    room.ChannelName,
					Title:     idolName,
					Author:    idolName,
					Body:      "视频翻牌",
					Quote:     &message.Quote{Author: "粉丝提问", Text: question},
					CreatedAt: timeObj,
					Media:     []message.Media{{Kind: "video", Source: videoURL}},
				}
			}
		}

	default:
		return
	}

	// A structured reply carries a Document so Feishu renders a native quote
	// block; otherwise send the flat segments (QQ + Feishu linear fallback).
	//
	// The Document is **Feishu-only**. Sending it to QQ as well made every plain
	// Pocket48 message render in the card layout (【发送者】title, body,
	// timestamp) instead of the long-standing QQ format, and the speaker prefix
	// got duplicated because the Document carries its own Author. QQ must keep
	// receiving the flat segments it has always received.
	if doc != nil {
		for _, target := range targets {
			if target.Platform == "feishu" {
				b.sendTarget(target, doc)
			}
		}
		// Non-Feishu targets still get the original text, timestamp included.
		segments = append(segments, napcat.TextSegment(timeStr))
		for _, target := range targets {
			if target.Platform != "feishu" {
				b.sendTarget(target, segments)
			}
		}
		return
	}

	// Send to QQ
	segments = append(segments, napcat.TextSegment(timeStr))
	for _, target := range targets {
		b.sendTarget(target, segments)
	}
}

func (b *Bot) extractImageURL(body string) string {
	return b.extractMediaURL(body, []string{"url", "image", "img", "pic", "cover", "path", "originUrl", "sourceUrl", "thumbUrl", "emotionRemote", "emotionUrl"})
}

func (b *Bot) extractAudioURL(body string) string {
	return b.extractMediaURL(body, []string{"url", "audio", "voice", "path", "originUrl", "sourceUrl"})
}

func (b *Bot) extractVideoURL(body string) string {
	return b.extractMediaURL(body, []string{"url", "video", "videoUrl", "path", "originUrl", "sourceUrl", "playUrl"})
}

func (b *Bot) extractMediaURL(body string, keys []string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		var payload interface{}
		if err := json.Unmarshal([]byte(body), &payload); err == nil {
			if mediaURL := findStringField(payload, keys); mediaURL != "" {
				return normalizeMediaURL(mediaURL)
			}
		}
	}

	return normalizeMediaURL(body)
}

func (b *Bot) extractTextBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}

	if strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[") {
		var payload interface{}
		if err := json.Unmarshal([]byte(body), &payload); err == nil {
			if text := findStringField(payload, []string{"text", "content", "msg", "message", "desc", "title", "notice"}); text != "" {
				return normalizePocketText(text)
			}
		}
	}

	return normalizePocketText(body)
}

// formatGiftTextBody tries to parse QChat GIFT_TEXT JSON into a human-readable
// gift notification. Returns empty string if body is not gift JSON.
func formatGiftTextBody(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "{") {
		return ""
	}
	var raw struct {
		GiftInfo struct {
			Name string `json:"giftName"`
			Num  int    `json:"giftNum"`
		} `json:"giftInfo"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return ""
	}
	if raw.GiftInfo.Name == "" {
		return ""
	}
	num := raw.GiftInfo.Num
	if num <= 1 {
		return fmt.Sprintf("🎁 送了一个%s", raw.GiftInfo.Name)
	}
	return fmt.Sprintf("🎁 送了%d个%s", num, raw.GiftInfo.Name)
}

func (b *Bot) getTargetsForRoom(roomID int64) []config.DeliveryTarget {
	var targets []config.DeliveryTarget
	seen := make(map[string]bool)
	for targetID, roomIDs := range b.cfg.GroupSubscriptions {
		for _, id := range roomIDs {
			if id != roomID {
				continue
			}
			target := b.cfg.ResolveTarget(targetID)
			if target.ID == "" || seen[target.ID] {
				break
			}
			seen[target.ID] = true
			targets = append(targets, target)
			break
		}
	}
	return targets
}

// getTargetGroupsForRoom keeps the legacy QQ-only view for the danmaku and
// live-push paths, which still deliver to numeric QQ groups. The main room
// message flow uses getTargetsForRoom to support Feishu.
func (b *Bot) getTargetGroupsForRoom(roomID int64) []int64 {
	var groupIDs []int64
	for _, target := range b.getTargetsForRoom(roomID) {
		if target.Platform != "qq" || target.Kind != "group" {
			continue
		}
		if id, err := strconv.ParseInt(target.Address, 10, 64); err == nil {
			groupIDs = append(groupIDs, id)
		}
	}
	return groupIDs
}

func (b *Bot) getTargetsForOwner(ownerUserID int64) []config.DeliveryTarget {
	if ownerUserID <= 0 {
		return nil
	}

	targetSet := make(map[string]config.DeliveryTarget)
	checkedRoom := make(map[int64]struct{})

	for _, roomIDs := range b.cfg.GroupSubscriptions {
		for _, roomID := range roomIDs {
			if _, ok := checkedRoom[roomID]; ok {
				continue
			}
			checkedRoom[roomID] = struct{}{}

			info, err := b.getCachedRoomInfo(roomID)
			if err != nil || info == nil || info.OwnerID != ownerUserID {
				continue
			}

			for _, target := range b.getTargetsForRoom(roomID) {
				targetSet[target.ID] = target
			}
		}
	}

	targets := make([]config.DeliveryTarget, 0, len(targetSet))
	for _, target := range targetSet {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets
}

func (b *Bot) getTargetsByOwnerName(ownerName string) []config.DeliveryTarget {
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		return nil
	}

	targetSet := make(map[string]config.DeliveryTarget)
	checkedRoom := make(map[int64]struct{})

	for _, roomIDs := range b.cfg.GroupSubscriptions {
		for _, roomID := range roomIDs {
			if _, ok := checkedRoom[roomID]; ok {
				continue
			}
			checkedRoom[roomID] = struct{}{}

			info, err := b.getCachedRoomInfo(roomID)
			if err != nil || info == nil {
				continue
			}
			if strings.TrimSpace(info.OwnerName) != ownerName {
				continue
			}

			for _, target := range b.getTargetsForRoom(roomID) {
				targetSet[target.ID] = target
			}
		}
	}

	targets := make([]config.DeliveryTarget, 0, len(targetSet))
	for _, target := range targetSet {
		targets = append(targets, target)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	return targets
}

func (b *Bot) checkRoomOnMic(roomInfo *pocket48.RoomInfo) {
	if roomInfo == nil || roomInfo.OwnerID == 0 {
		return
	}

	const onMicCheckInterval = 15 * time.Second
	b.mu.RLock()
	lastCheck := b.onMicLastCheck[roomInfo.ChannelID]
	b.mu.RUnlock()
	if !lastCheck.IsZero() && time.Since(lastCheck) < onMicCheckInterval {
		return
	}

	voiceUsers, err := b.pocket.GetRoomVoiceList(roomInfo.ChannelID, roomInfo.ServerID)
	if err != nil {
		// Auth expiry is handled by the caller loop when applicable; keep this quiet on transient fails.
		return
	}

	isOnMic := false
	for _, uid := range voiceUsers {
		if uid == roomInfo.OwnerID {
			isOnMic = true
			break
		}
	}

	b.mu.Lock()
	prev, ok := b.onMicState[roomInfo.ChannelID]
	b.onMicState[roomInfo.ChannelID] = isOnMic
	b.onMicLastCheck[roomInfo.ChannelID] = time.Now()
	b.mu.Unlock()

	// Seed state on first observation (do not spam "上麦了" for already-on-mic after restart).
	if !ok {
		return
	}
	if prev == isOnMic || !isOnMic {
		return
	}

	idolName := roomInfo.OwnerName
	if idolName == "" {
		idolName = strings.TrimSuffix(roomInfo.ChannelName, "的房间")
	}
	if idolName == "" {
		idolName = "成员"
	}

	targets := b.getTargetsForRoom(roomInfo.ChannelID)
	if len(targets) == 0 {
		return
	}

	now := time.Now()
	msg := fmt.Sprintf("【%s|%s】\n%s上麦了\n%s", idolName, roomInfo.ChannelName, idolName, now.Format("2006-01-02 15:04:05"))
	log.Printf("[OnMic] room=%d owner=%s(%d) detected on-mic → notify targets=%d", roomInfo.ChannelID, idolName, roomInfo.OwnerID, len(targets))
	for _, target := range targets {
		b.sendTarget(target, napcat.TextSegment(msg))
	}
}

// onMicLoop polls Pocket48 REST voice list independently of QChat message realtime.
// Previously checkRoomOnMic only ran inside pollRoom, which is skipped while QChat is
// healthy — so on-mic announcements could lag tens of minutes vs bots that always REST-poll.
func (b *Bot) onMicLoop() {
	const tick = 15 * time.Second
	log.Printf("[OnMic] REST voice-list loop started (interval=%s, independent of QChat)", tick)
	for {
		if !b.isMonitoring {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if b.isPocketAuthExpired() {
			time.Sleep(tick)
			continue
		}

		roomIDs := make(map[int64]bool)
		for _, rooms := range b.cfg.GroupSubscriptions {
			for _, roomID := range rooms {
				roomIDs[roomID] = true
			}
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, 8)
		for roomID := range roomIDs {
			wg.Add(1)
			sem <- struct{}{}
			go func(id int64) {
				defer wg.Done()
				defer func() { <-sem }()
				roomInfo, err := b.getCachedRoomInfo(id)
				if err != nil {
					if !b.handlePocketAuthError(err) {
						// silent: room closed / transient are common
					}
					return
				}
				b.clearPocketAuthExpired()
				b.checkRoomOnMic(roomInfo)
			}(roomID)
		}
		wg.Wait()
		time.Sleep(tick)
	}
}

func (b *Bot) sendLivePush(targets []config.DeliveryTarget, msg *pocket48.Message, timeStr string) {
	title, cover, _, _ := parseLivePushBody(msg.Body)
	if title == "" || cover == "" {
		extTitle, extCover, _, _ := parseLivePushBody(msg.RawExt)
		if title == "" {
			title = extTitle
		}
		if cover == "" {
			cover = extCover
		}
	}
	cover = b.mediaPathForMessage(msg, cover)
	if title == "" {
		title = "📺 直播开始了"
	}

	idolName := strings.TrimSpace(msg.Room.OwnerName)
	if idolName == "" {
		idolName = strings.TrimSuffix(strings.TrimSpace(msg.Room.ChannelName), "的房间")
	}
	channelName := strings.TrimSpace(msg.Room.ChannelName)
	if channelName == "" {
		channelName = "包间"
	}

	// Same layout as room message forward: @全体成员 / 【Owner|Channel】 / body / time
	textTop := fmt.Sprintf("\n【%s|%s】\n%s直播啦！——%s\n", idolName, channelName, idolName, title)
	textBottom := fmt.Sprintf("\n%s", timeStr)

	var segments []interface{}
	segments = append(segments, napcat.AtSegment("all"))
	segments = append(segments, napcat.TextSegment(textTop))
	if cover != "" {
		segments = append(segments, napcat.ImageSegment(cover))
	}
	segments = append(segments, napcat.TextSegment(textBottom))

	for _, target := range targets {
		b.sendTarget(target, segments)
	}
}

// smartMessageLimit returns the optimal message fetch limit based on polling pace.
// Fast mode (sub-second): only need a few messages per fetch.
// Normal mode (1s): need slightly more to avoid gaps.
func (b *Bot) smartMessageLimit() int {
	b.mu.RLock()
	fast := b.pollFastMode
	b.mu.RUnlock()

	if fast {
		return 5
	}
	return 10
}

// adjustPollInterval responds to room activity.
// NEW MESSAGES: switch to fast polling (300ms) for immediate delivery, extending burst on
//
//	each new message.
//
// QUIET: count down fast-remaining cycles, then fall back to normal 1s polling.
func (b *Bot) adjustPollInterval(anyNewMsgs bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if anyNewMsgs {
		b.pollFastMode = true
		b.pollFastRemaining = 15 // stay fast for ~15 cycles, extend on new msg
		return
	}

	if b.pollFastMode {
		b.pollFastRemaining--
		if b.pollFastRemaining <= 0 {
			b.pollFastMode = false
		}
	}
}

// currentPollInterval returns the dynamically adjusted polling interval.
// ACTIVE (fast mode): use fastInterval (~300ms) for near-real-time delivery.
// NORMAL: use the configured base polling interval (default 1s).
func (b *Bot) currentPollInterval() time.Duration {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.pollFastMode {
		return b.fastInterval
	}

	base := b.pollingInterval
	if base <= 0 {
		base = 1 * time.Second
	}
	return base
}

// sendVoiceReply delivers a Pocket48 voice reply (语音回复).
//
// QQ keeps the historical two-message form: a text line carrying the duration,
// then the record segment. Feishu instead gets one combined Document, because
// sending them separately produced a placeholder card plus a detached audio
// attachment — two messages reading as one reply. The Document's audio Media
// renders as a native voice bubble threaded under its card, so the pair looks
// like a single notification.
func (b *Bot) sendVoiceReply(targets []config.DeliveryTarget, voiceURL string, qqSegments []interface{}, giftText, audioLabel string, msg *pocket48.Message) {
	if msg == nil {
		return
	}
	author := strings.TrimSpace(msg.StarName)
	if author == "" {
		author = strings.TrimSpace(msg.NickName)
	}
	body := giftText
	if body == "" {
		body = audioLabel
	}
	var createdAt time.Time
	if msg.Time > 0 {
		createdAt = time.UnixMilli(msg.Time)
	}
	for _, target := range targets {
		if target.ID == "" {
			continue
		}
		if target.Platform != "feishu" {
			b.sendTarget(target, qqSegments)
			time.Sleep(50 * time.Millisecond)
			b.sendTarget(target, []interface{}{napcat.RecordSegment(voiceURL)})
			continue
		}
		b.sendTarget(target, &message.Document{
			Source:    "Pocket48",
			Title:     author,
			Author:    author,
			Body:      body,
			Media:     []message.Media{{Kind: "audio", Source: voiceURL}},
			CreatedAt: createdAt,
		})
	}
}
