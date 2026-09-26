package logic

import (
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestWeverseAllMentionHasOwnLine(t *testing.T) {
	s := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}}
	for _, kind := range []string{"post", "comment", "live", "live_end", "live_replay", "live_chat", "moment"} {
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

func TestMembershipOnlyMomentHasPlaceholderAndNoRestrictedMedia(t *testing.T) {
	e := weverse.Event{Kind: "moment", MemberID: "stella", Author: "STELLA", MembershipOnly: true, Body: "", URL: "https://weverse.io/hearts2hearts/moment/stella/post/3-membership"}
	segments := weverseMessageSegments(weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}}, e)
	if segments[0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("membership-only Stella Moment must retain normal forwarding mention rule")
	}
	var body string
	for _, raw := range segments {
		segment := raw.(napcat.MessageSegment)
		if segment.Type == "image" || segment.Type == "video" {
			t.Fatal("restricted media was forwarded")
		}
		if segment.Type == "text" {
			body += segment.Data["text"]
		}
	}
	if !strings.Contains(body, "发布了会员专属 Moment（当前账号无权查看内容）") {
		t.Fatal(body)
	}
}

func TestPasswordProtectedContentHasPlaceholder(t *testing.T) {
	for _, kind := range []string{"post", "moment"} {
		e := weverse.Event{Kind: kind, Author: "STELLA", PasswordProtected: true}
		body := weverseEventBody(e)
		want := "发布了密码保护帖子（尚未配置密码）"
		if kind == "moment" {
			want = "发布了密码保护 Moment（尚未配置密码）"
		}
		if !strings.Contains(body, want) {
			t.Fatalf("kind=%s body=%q", kind, body)
		}
	}
}
