package logic

import (
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestFormatWeverseOriginalAndChinese(t *testing.T) {
	e := weverse.Event{Kind: "comment", Author: "CARMEN", Body: "안녕", Translation: "你好", ParentBody: "잘 지내?", ParentTranslation: "最近好吗？", URL: "https://weverse.io/hearts2hearts/fanpost/1/comment/2"}
	got := formatWeverseEvent(e)
	for _, part := range []string{"【CARMEN|Weverse回复】", "原文上下文：\n잘 지내?\n中文（机器翻译）：\n最近好吗？", "回复：\n안녕\n中文（机器翻译）：\n你好", e.URL} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
	e.Kind = "live"
	got = formatWeverseEvent(e)
	if !strings.Contains(got, "【CARMEN|Weverse直播】\n已开播") {
		t.Fatal(got)
	}
	e.Translation = ""
	e.TranslationError = "failed"
	if !strings.Contains(formatWeverseEvent(e), "翻译暂不可用，已保留原文") {
		t.Fatal("missing fallback")
	}
}
