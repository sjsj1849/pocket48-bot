package logic

import (
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestWeverseAllMentionHasOwnLine(t *testing.T) {
	s := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}}
	for _, kind := range []string{"post", "comment", "live", "live_end", "live_replay", "moment"} {
		e := weverse.Event{Kind: kind, MemberID: "stella", Author: "STELLA", Body: "内容"}
		segments := weverseMessageSegments(s, e)
		if segments[0].(napcat.MessageSegment).Type != "at" || segments[1].(napcat.MessageSegment).Data["text"] != "\n" || !strings.HasPrefix(segments[2].(napcat.MessageSegment).Data["text"], "【STELLA|Weverse】") {
			t.Fatal(segments)
		}
		e.MemberID = "ian"
		if weverseMessageSegments(s, e)[0].(napcat.MessageSegment).Type == "at" {
			t.Fatal("other member mentioned all")
		}
	}
}
func TestWeverseReplayShowsFinalDuration(t *testing.T) {
	e := weverse.Event{Kind: "live_replay", Author: "STELLA", LiveDuration: 2076}
	if got := weverseEventBody(e); !strings.Contains(got, "直播时长：0小时34分36秒") {
		t.Fatal(got)
	}
	e.LiveDuration = 0
	if got := weverseEventBody(e); strings.Contains(got, "直播时长") {
		t.Fatal("invented duration", got)
	}
}
