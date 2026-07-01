package logic

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
)

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
		if b.napcat != nil {
			if qd := b.napcat.QueueDepth(); qd > 5 {
				log.Printf("[QUEUE] napcat sendChan depth=%d", qd)
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

	roomInfo, err := b.getCachedRoomInfo(roomID)
	if err != nil {
		if !b.handlePocketAuthError(err) {
			log.Printf("Failed to get room info for %d: %v", roomID, err)
		}
		return false
	}
	b.clearPocketAuthExpired()

	b.checkRoomOnMic(roomInfo)

	// Smart limit: polling interval short → fewer messages needed
	limit := b.smartMessageLimit()
	msgs, err := b.pocket.GetMessages(roomInfo, limit)
	if err != nil {
		if !b.handlePocketAuthError(err) {
			log.Printf("Failed to get messages for %d: %v", roomID, err)
		}
		return false
	}

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

	// Sort messages by time to ensure order
	sort.Slice(msgs, func(i, j int) bool {
		return msgs[i].Time < msgs[j].Time
	})

	var targetGroups []int64
	sampleMsg := msgs[0]

	targetGroups = b.getTargetGroupsForRoom(sampleMsg.Room.ChannelID)

	if len(targetGroups) == 0 {
		return
	}

	// Process each message immediately without batching
	for _, msg := range msgs {
		b.processSinglePocketMessage(msg, targetGroups)
	}
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

func (b *Bot) processSinglePocketMessage(msg *pocket48.Message, targetGroups []int64) {
	room := msg.Room
	roomIDStr := strconv.FormatInt(room.ChannelID, 10)

	// Filter: GiftReply
	if msg.Type == pocket48.MsgGiftReply || msg.Type == pocket48.MsgAudioGiftReply {
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
			for _, gid := range targetGroups {
				b.napcat.SendGroupMessage(gid, segments)
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
		displayText := b.extractTextBody(msg.Body)
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
		voiceURL = b.localMediaPath(voiceURL)
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
		for _, gid := range targetGroups {
			b.napcat.SendGroupMessage(gid, textSegments)
			time.Sleep(50 * time.Millisecond)
			b.napcat.SendGroupMessage(gid, []interface{}{napcat.RecordSegment(voiceURL)})
		}
		return

	case pocket48.MsgReply:
		displayText := b.extractTextBody(msg.Body)
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
			segments = appendTextWithQQFaces(segments, prefix+displayText+"\n")
		}

	case pocket48.MsgImage, pocket48.MsgExpressImage:
		imageURL := b.localMediaPath(b.extractImageURL(msg.Body))
		if imageURL != "" {
			segments = append(segments, napcat.TextSegment(prefix))
			segments = append(segments, napcat.ImageSegment(imageURL))
		}

	case pocket48.MsgVideo:
		videoURL := b.localMediaPath(b.extractVideoURL(msg.Body))
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
		}

	case pocket48.MsgLivePush:
		shouldSend := b.cfg.LiveMonitoring
		if b.cfg.LiveSpecific != nil {
			if val, ok := b.cfg.LiveSpecific[roomIDStr]; ok {
				shouldSend = val
			}
		}
		if !shouldSend {
			return
		}
		title, cover, liveID, roomID := parseLivePushBody(msg.Body)
		if title == "" {
			title = "直播开始了"
		}
		cover = b.localMediaPath(normalizeMediaURL(cover))
		segments = []interface{}{
			napcat.AtSegment("all"),
			napcat.TextSegment(fmt.Sprintf("\n%s直播啦！—— %s\n", room.OwnerName, title)),
		}
		if cover != "" {
			segments = append(segments, napcat.ImageSegment(cover))
		}
		segments = append(segments, napcat.TextSegment("\n"+timeStr))
		b.napcat.SendGroupMessage(targetGroups[0], segments)

		// Connect NIM danmaku bridge to this live stream
		if b.nimDanmaku != nil {
			go b.connectDanmakuForLive(liveID, roomID, room)
		}
		return

	case pocket48.MsgAudio:
		audioURL := b.localMediaPath(b.extractAudioURL(msg.Body))
		if audioURL != "" {
			segments = []interface{}{
				napcat.TextSegment("【语音消息】"),
				napcat.RecordSegment(audioURL),
			}
		}

	case pocket48.MsgFlipCardAudio:
		question, _, _, _ := parseFlipCardBody(msg.Body)
		idolName := room.OwnerName
		audioURL := b.localMediaPath(b.extractAudioURL(msg.Body))
		if audioURL == "" {
			return
		}
		flipText := buildFlipCardAudioIntroText(idolName, question)
		segments = appendTextWithQQFaces(segments, flipText+"\n")
		segments = append(segments, napcat.RecordSegment(audioURL))

	case pocket48.MsgFlipCardVideo:
		question, _, _, ok := parseFlipCardBody(msg.Body)
		idolName := room.OwnerName
		if idolName == "" {
			idolName = "成员"
		}
		videoURL := b.localMediaPath(b.extractVideoURL(msg.Body))
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
		}

	default:
		return
	}

	// Send to QQ
	segments = append(segments, napcat.TextSegment(timeStr))
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, segments)
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

func (b *Bot) getTargetGroupsForRoom(roomID int64) []int64 {
	var targetGroups []int64
	for groupIDStr, roomIDs := range b.cfg.GroupSubscriptions {
		for _, id := range roomIDs {
			if id == roomID {
				groupID, _ := strconv.ParseInt(groupIDStr, 10, 64)
				targetGroups = append(targetGroups, groupID)
				break
			}
		}
	}
	return targetGroups
}

func (b *Bot) getTargetGroupsForOwner(ownerUserID int64) []int64 {
	if ownerUserID <= 0 {
		return nil
	}

	groupSet := make(map[int64]struct{})
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

			for _, gid := range b.getTargetGroupsForRoom(roomID) {
				groupSet[gid] = struct{}{}
			}
		}
	}

	groups := make([]int64, 0, len(groupSet))
	for gid := range groupSet {
		groups = append(groups, gid)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	return groups
}

func (b *Bot) getTargetGroupsByOwnerName(ownerName string) []int64 {
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		return nil
	}



	groupSet := make(map[int64]struct{})
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

			for _, gid := range b.getTargetGroupsForRoom(roomID) {
				groupSet[gid] = struct{}{}
			}
		}
	}

	groups := make([]int64, 0, len(groupSet))
	for gid := range groupSet {
		groups = append(groups, gid)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	return groups
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

	if !ok || prev == isOnMic || !isOnMic {
		return
	}

	idolName := roomInfo.OwnerName
	if idolName == "" {
		idolName = strings.TrimSuffix(roomInfo.ChannelName, "的房间")
	}
	if idolName == "" {
		idolName = "成员"
	}

	targetGroups := b.getTargetGroupsForRoom(roomInfo.ChannelID)
	if len(targetGroups) == 0 {
		return
	}

	msg := fmt.Sprintf("【%s|%s】\n%s上麦了\n%s", idolName, roomInfo.ChannelName, idolName, time.Now().Format("2006-01-02 15:04:05"))
	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, napcat.TextSegment(msg))
	}
}

func (b *Bot) sendLivePush(targetGroups []int64, msg *pocket48.Message, timeStr string) {
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
	cover = b.localMediaPath(normalizeMediaURL(cover))
	if title == "" {
		title = "直播开始了"
	}

	idolName := msg.Room.OwnerName
	if idolName == "" {
		idolName = strings.TrimSuffix(msg.Room.ChannelName, "的房间")
	}

	textTop := fmt.Sprintf("\n%s直播啦！—— %s\n", idolName, title)
	textBottom := fmt.Sprintf("\n%s", timeStr)

	var segments []interface{}
	segments = append(segments, napcat.AtSegment("all"))
	segments = append(segments, napcat.TextSegment(textTop))
	if cover != "" {
		segments = append(segments, napcat.ImageSegment(cover))
	}
	segments = append(segments, napcat.TextSegment(textBottom))

	for _, gid := range targetGroups {
		b.napcat.SendGroupMessage(gid, segments)
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
//               each new message.
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

