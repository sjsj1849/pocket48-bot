package outbound

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
)

func TestRenderFeishuContentMovesSenderAndTimestamp(t *testing.T) {
	content := renderFeishuContent([]message.Segment{
		message.Text("【胡晓慧 | 包间】\n哼唧小虎（胡晓慧）：今天也要开心呀\nhttps://example.com/post/1\n2026-10-01 22:31:05"),
		message.Image("/tmp/a.jpg"),
	})
	if content.sender != "哼唧小虎（胡晓慧）" {
		t.Fatalf("sender = %q", content.sender)
	}
	if content.source != "包间" {
		t.Fatalf("source = %q", content.source)
	}
	if content.timestamp != "2026-10-01 22:31:05" {
		t.Fatalf("timestamp = %q", content.timestamp)
	}
	if strings.Contains(content.text, "2026-10-01") {
		t.Fatalf("timestamp must not stay in the body: %q", content.text)
	}
	if strings.Contains(content.text, "哼唧小虎") {
		t.Fatalf("sender must not stay in the body: %q", content.text)
	}
	if len(content.images) != 1 {
		t.Fatalf("images = %#v", content.images)
	}
	if note := footerNote(content); note != "包间 · 2026-10-01 22:31:05" {
		t.Fatalf("footer = %q", note)
	}
}

func TestToFeishuLinesForcesParagraphBreaks(t *testing.T) {
	got := toFeishuLines("第一行\n第二行\n\n第三行")
	if got != "第一行\n\n第二行\n\n第三行" {
		t.Fatalf("unexpected lines: %q", got)
	}
}

func TestStripURLRemovesLinkKeptByButton(t *testing.T) {
	got := stripURL("正文内容\nhttps://example.com/post/1", "https://example.com/post/1")
	if got != "正文内容" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestStripURLDropsDanglingSourceLabel(t *testing.T) {
	// Every platform that appends "<来源>链接：" before the URL must not leave the
	// label behind once the URL becomes a jump button.
	cases := []struct {
		label    string
		link     string
		expected string
	}{
		{"微博", "https://weibo.com/123/456", "正文内容"},
		{"抖音", "https://www.douyin.com/video/7691", "正文内容"},
		{"小红书", "https://www.xiaohongshu.com/explore/abc", "正文内容"},
	}
	for _, c := range cases {
		in := "正文内容\n\n" + c.label + "链接：" + c.link + "\n"
		if got := stripURL(in, c.link); got != c.expected {
			t.Fatalf("%s: got %q want %q", c.label, got, c.expected)
		}
	}
	// Body copy that merely mentions 链接 must survive untouched.
	kept := stripURL("这里提到了链接：请看上文\nhttps://example.com/x", "https://example.com/x")
	if kept != "这里提到了链接：请看上文" {
		t.Fatalf("body copy was damaged: %q", kept)
	}
}

func TestQuotePartsSplitTranslationAndOriginal(t *testing.T) {
	head, grey := turnParts("粉丝A", "胡晓慧今天好漂亮！", "胡晓慧今天真漂亮")
	if head != "粉丝A：胡晓慧今天真漂亮" {
		t.Fatalf("head = %q", head)
	}
	if grey != "胡晓慧今天好漂亮！" {
		t.Fatalf("grey = %q", grey)
	}
	head2, grey2 := turnParts("粉丝A", "胡晓慧今天好漂亮！", "")
	if head2 != "粉丝A：胡晓慧今天好漂亮！" || grey2 != "" {
		t.Fatalf("head=%q grey=%q", head2, grey2)
	}
}

func TestCardColorMapping(t *testing.T) {
	cases := map[string]string{
		"微博动态":     "carmine",
		"Weverse":  "wathet",
		"Melon":    "green",
		"胡晓慧 | 包间": "purple",
		"抖音":       "red",
	}
	for title, want := range cases {
		if got := cardColor(title); got != want {
			t.Fatalf("cardColor(%q) = %q, want %q", title, got, want)
		}
	}
}
