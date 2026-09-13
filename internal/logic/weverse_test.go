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
	want := "【CARMEN|Weverse】\n粉丝昵称：最近好吗？\nCARMEN：你好\n\n粉丝昵称（原文）：잘 지내?\nCARMEN（原文）：안녕\n\n" + e.URL
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	e.ParentTranslation = ""
	got = formatWeverseEvent(e)
	if !strings.Contains(got, "粉丝昵称：（译文暂不可用，原文见下）") || !strings.Contains(got, "粉丝昵称（原文）：잘 지내?") {
		t.Fatal(got)
	}
	e.Translation = ""
	e.TranslationError = "failed"
	if !strings.Contains(formatWeverseEvent(e), "翻译暂不可用，已保留原文") {
		t.Fatal("missing fallback")
	}
	e.Kind = "live"
	if !strings.Contains(formatWeverseEvent(e), "【CARMEN|Weverse】\n已开播") {
		t.Fatal("missing live notice")
	}
}

func TestWeverseLiveEndNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_end", Author: "JIWOO", Body: "방송", Translation: "直播标题", LiveDuration: 2076, Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【JIWOO|Weverse】", "直播已结束", "直播时长：0小时34分36秒", "JIWOO：直播标题", "JIWOO（原文）：방송", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n1970-01-01 08:01:40") || strings.Contains(got, "时间：") || strings.Contains(got, "链接：") {
		t.Fatal("footer labels or timestamp spacing incorrect", got)
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
	for _, want := range []string{"【STELLA|Weverse】", "直播回放已生成", "STELLA：直播标题", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatal(want, got)
		}
	}
}

func TestWeverseMediaBeforeFooter(t *testing.T) {
	for _, images := range [][]string{nil, {"https://img.example/a.jpg"}, {"https://img.example/a.jpg", "https://img.example/b.jpg"}} {
		e := weverse.Event{Kind: "post", Author: "STELLA", Body: "안녕", Translation: "你好", URL: "https://weverse.io/hearts2hearts/artist/1-2", Time: 100000, Images: images}
		segments := weverseMessageSegments(weverse.Subscription{}, e)
		if len(segments) != len(images)+2 {
			t.Fatal(segments)
		}
		body := segments[0].(napcat.MessageSegment).Data["text"]
		if strings.Contains(body, e.URL) || strings.Contains(body, "────────") || !strings.Contains(body, "STELLA（原文）：안녕") {
			t.Fatal(body)
		}
		for i, image := range images {
			m := segments[i+1].(napcat.MessageSegment)
			if m.Type != "image" || m.Data["file"] != image {
				t.Fatal("media reordered", segments)
			}
		}
		footer := segments[len(segments)-1].(napcat.MessageSegment).Data["text"]
		if footer != "\n\n"+e.URL+"\n\n1970-01-01 08:01:40" {
			t.Fatal("ambiguous QQ link/date boundary", footer)
		}
	}
}
