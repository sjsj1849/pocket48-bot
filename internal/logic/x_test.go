package logic

import (
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/xmonitor"
	"strings"
	"testing"
)

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
}
