package logic

import (
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestFormatWeverseOriginalAndChinese(t *testing.T) {
	e := weverse.Event{Kind: "comment", Author: "CARMEN", ParentAuthor: "粉丝昵称", Body: "안녕", Translation: "你好", ParentBody: "잘 지내?", ParentTranslation: "最近好吗？", URL: "https://weverse.io/hearts2hearts/fanpost/1/comment/2"}
	got := formatWeverseEvent(e)
	want := "【CARMEN|Weverse回复】\n\n中文\nCARMEN：你好\n粉丝昵称：最近好吗？\n\n────────\n原文\nCARMEN：안녕\n粉丝昵称：잘 지내?\n\n────────\n" + e.URL
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	e.ParentTranslation = ""
	got = formatWeverseEvent(e)
	if !strings.Contains(got, "粉丝昵称：（译文暂不可用，请看下方原文）") || !strings.Contains(got, "粉丝昵称：잘 지내?") {
		t.Fatal(got)
	}
	e.Translation = ""
	e.TranslationError = "failed"
	if !strings.Contains(formatWeverseEvent(e), "翻译暂不可用，已保留原文") {
		t.Fatal("missing fallback")
	}
	e.Kind = "live"
	if !strings.Contains(formatWeverseEvent(e), "【CARMEN|Weverse直播】\n已开播") {
		t.Fatal("missing live notice")
	}
}

func TestWeverseLiveEndNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_end", Author: "JIWOO", Body: "방송", Translation: "直播标题", LiveDuration: 2076, Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【JIWOO|Weverse直播结束】", "直播已结束", "直播时长：0小时34分36秒", "JIWOO：直播标题", "JIWOO：방송", "检测到结束：", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	e.LiveEndedAt = e.Time
	if !strings.Contains(formatWeverseEvent(e), "结束时间：") {
		t.Fatal("official end time not labeled")
	}
}

func TestWeverseOnlyStellaMentionsAll(t *testing.T) {
	sub := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}, AtAllMemberNames: []string{"STELLA"}, Enabled: true, CommunityID: 235, Posts: true, Comments: true, Live: true}
	for _, kind := range []string{"post", "comment", "live", "live_end", "live_replay"} {
		for _, member := range []string{"stella", "carmen"} {
			e := weverse.Event{Kind: kind, MemberID: member, CommunityID: 235, Images: []string{"https://img.example/a.jpg"}}
			if !weverse.Matches(sub, e) {
				t.Fatal("mention restriction filtered subscription", kind, member)
			}
			segments := weverseMessageSegments(sub, e)
			mentions, images := 0, 0
			for _, v := range segments {
				m := v.(napcat.MessageSegment)
				if m.Type == "at" {
					mentions++
				}
				if m.Type == "image" {
					images++
				}
			}
			if images != 1 || (member == "stella" && mentions != 1) || (member != "stella" && mentions != 0) {
				t.Fatal(kind, member, segments)
			}
		}
	}
	sub.AtAll = false
	if sub.MentionsAll("stella") {
		t.Fatal("disabled mentions still enabled")
	}
	sub.AtAll = true
	sub.AtAllMemberIDs = nil
	if !sub.MentionsAll("carmen") {
		t.Fatal("legacy all-member mentions changed")
	}
}
func TestWeverseReplayNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_replay", Author: "STELLA", Body: "방송", Translation: "直播标题", Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【STELLA|Weverse直播回放】", "直播回放已生成", "STELLA：直播标题", "检测到回放：", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatal(want, got)
		}
	}
}
