package logic

import (
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/xmonitor"
	"strings"
	"testing"
	"time"
)

func TestXRetryDelay(t *testing.T) {
	regular := 2 * time.Minute
	for _, test := range []struct {
		code string
		hits int
		want time.Duration
	}{
		{"account_unavailable", 1, 5 * time.Minute},
		{"account_unavailable", 2, 15 * time.Minute},
		{"account_unavailable", 3, 30 * time.Minute},
		{"timeout", 9, 30 * time.Minute},
		{"invalid_session", 1, regular},
	} {
		if got := xRetryDelay(test.code, test.hits, regular); got != test.want {
			t.Fatalf("xRetryDelay(%q, %d) = %s, want %s", test.code, test.hits, got, test.want)
		}
	}
}

func TestXMediaCardFooterAndSeparateVideo(t *testing.T) {
	e := xmonitor.Event{ID: "123", Body: "hello", URL: "https://x.com/nekomo_st/status/123", Time: 1789137110000, Author: xmonitor.User{Name: "nekomo"}, Media: []xmonitor.Media{{Kind: "photo", URL: "https://image"}, {Kind: "video", Cover: "https://cover", Variants: []xmonitor.Variant{{URL: "https://4k", Bitrate: 25000000}, {URL: "https://720", Bitrate: 2176000}}}}}
	groups := xMessageGroups(xmonitor.Subscription{AtAll: true}, e)
	if len(groups) != 2 {
		t.Fatalf("one card plus one standalone video expected: %d", len(groups))
	}
	if groups[0][0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("at-all must be a real OneBot segment")
	}
	last := groups[0][len(groups[0])-1].(napcat.MessageSegment)
	if !strings.HasPrefix(last.Data["text"], "\n\n"+e.URL+"\n\n") {
		t.Fatal("footer must be below media, with a blank line between URL and timestamp")
	}
	for _, segment := range groups[0] {
		if segment.(napcat.MessageSegment).Type == "video" {
			t.Fatal("video must not be mixed with text/images")
		}
	}
	video := groups[1][0].(napcat.MessageSegment)
	if len(groups[1]) != 1 || video.Type != "video" || video.Data["file"] != "https://720" {
		t.Fatalf("QQ receives a separate practical-resolution video: %+v", video)
	}
	texts := ""
	for _, raw := range groups[0] {
		segment := raw.(napcat.MessageSegment)
		if segment.Type == "text" {
			texts += segment.Data["text"]
		}
	}
	if !strings.Contains(texts, "[视频]") || strings.Contains(texts, "视频单独发送") {
		t.Fatalf("video placeholder contains redundant delivery note: %q", texts)
	}
}
