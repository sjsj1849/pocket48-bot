package logic

import (
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
