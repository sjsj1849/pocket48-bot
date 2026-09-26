package weverse

import "testing"

func TestEventFromLiveChatKeepsOnlyOtherKnownArtistsAndChineseTranslation(t *testing.T) {
	live := Event{Kind: "live", CommunityID: 235, MemberID: "host", Author: "HOST", PostID: "1-2"}
	names := map[string]string{"host": "HOST", "guest": "GUEST"}
	raw := Object{
		"messageTime": float64(1234),
		"content":     "안녕",
		"profile":     Object{"memberId": "guest", "profileType": "ARTIST", "profileName": "게스트"},
		"extras": Object{"translation": []any{
			Object{"lang": "en", "content": "Hello"},
			Object{"lang": "zh-CN", "content": "你好"},
		}},
	}
	event, ok := eventFromLiveChat(raw, live, "chat", "hearts2hearts", 235, names)
	if !ok || event.Kind != "live_chat" || event.MemberID != "guest" || event.Author != "GUEST" || event.LiveHostMemberID != "host" || event.LiveHostAuthor != "HOST" || event.Translation != "你好" || event.URL != "https://weverse.io/hearts2hearts/live/1-2" {
		t.Fatalf("unexpected live chat event: %+v ok=%v", event, ok)
	}
	host := raw
	host["profile"] = Object{"memberId": "host", "profileType": "ARTIST"}
	if _, ok := eventFromLiveChat(host, live, "chat", "hearts2hearts", 235, names); ok {
		t.Fatal("host chat must not be forwarded as a visiting-member message")
	}
	fan := raw
	fan["profile"] = Object{"memberId": "fan", "profileType": "FAN"}
	if _, ok := eventFromLiveChat(fan, live, "chat", "hearts2hearts", 235, names); ok {
		t.Fatal("fan chat was accepted")
	}
}
