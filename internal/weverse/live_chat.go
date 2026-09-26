package weverse

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func liveChatTranslation(extras Object) string {
	for _, raw := range list(extras["translation"]) {
		row := obj(raw)
		lang := strings.ToLower(strings.ReplaceAll(str(row["lang"]), "_", "-"))
		if lang == "zh" || lang == "zh-cn" || lang == "zh-hans" {
			return plain(text(row, "content", "body"))
		}
	}
	return ""
}

func liveChatExtras(v any) Object {
	if value := obj(v); len(value) > 0 {
		return value
	}
	if raw := str(v); raw != "" {
		var value Object
		if json.Unmarshal([]byte(raw), &value) == nil {
			return value
		}
	}
	return Object{}
}

func eventFromLiveChat(raw Object, live Event, chatID, slug string, cid int64, memberNames map[string]string) (Event, bool) {
	profile := obj(raw["profile"])
	memberID := text(profile, "memberId", "id")
	if memberID == "" {
		memberID = text(raw, "userId", "uid")
	}
	// The dedicated artistMessages endpoint is already artist-only, but retain
	// both checks so a server-side response change cannot forward fan traffic.
	if memberID == "" || strings.ToUpper(str(profile["profileType"])) != "ARTIST" || memberNames[memberID] == "" {
		return Event{}, false
	}
	if memberID == live.MemberID {
		return Event{}, false
	}
	stamp := num(raw["messageTime"])
	if stamp == 0 {
		stamp = num(raw["createTime"])
	}
	body := plain(text(raw, "content", "message", "body"))
	if stamp == 0 || body == "" || chatID == "" || live.PostID == "" {
		return Event{}, false
	}
	hostName := live.Author
	if hostName == "" {
		hostName = memberNames[live.MemberID]
	}
	digest := sha256.Sum256([]byte(body))
	return Event{
		ID:               fmt.Sprintf("live_chat:%s:%s:%d:%x", chatID, memberID, stamp, digest[:6]),
		Kind:             "live_chat",
		CommunityID:      cid,
		MemberID:         memberID,
		Author:           memberNames[memberID],
		Body:             body,
		Translation:      liveChatTranslation(liveChatExtras(raw["extras"])),
		URL:              "https://weverse.io/" + slug + "/live/" + live.PostID,
		Time:             stamp,
		PostID:           live.PostID,
		LiveChatID:       chatID,
		LiveHostMemberID: live.MemberID,
		LiveHostAuthor:   hostName,
	}, true
}

func (c *Client) liveChatEvents(ctx context.Context, post Object, live Event, slug string, cid int64, memberNames map[string]string, since time.Time, maxPages int) ([]Event, error) {
	chat := obj(obj(obj(post["extension"])["mediaInfo"])["chat"])
	chatID := text(chat, "chatId", "channelId")
	if chatID == "" {
		return nil, nil
	}
	actions := list(chat["availableActions"])
	if len(actions) > 0 {
		canRead := false
		for _, action := range actions {
			if str(action) == "READ_CHAT" {
				canRead = true
				break
			}
		}
		if !canRead {
			return nil, nil
		}
	}
	if !idRE.MatchString(chatID) {
		return nil, fmt.Errorf("直播聊天室标识无效")
	}
	items, err := c.pagesLimit(ctx, "/chat/v1.0/chat-"+chatID+"/artistMessages?limit=50", "after", "messageTime", since, maxPages)
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(items))
	for _, raw := range items {
		if event, ok := eventFromLiveChat(obj(raw), live, chatID, slug, cid, memberNames); ok {
			events = append(events, event)
		}
	}
	return events, nil
}
