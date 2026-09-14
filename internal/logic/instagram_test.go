package logic

import (
	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/napcat"
	"strings"
	"testing"
)

func TestInstagramMediaCardFooterAndSeparateVideo(t *testing.T) {
	e := instagram.Event{ID: "123", Body: "hello", URL: "https://www.instagram.com/p/abc/", Time: 1789137110000, Author: instagram.User{Name: "nekomo"}, Media: []instagram.Media{{Kind: "photo", URL: "https://image"}, {Kind: "video", Cover: "https://cover", Variants: []instagram.Variant{{URL: "https://4k", Bitrate: 25000000}, {URL: "https://720", Bitrate: 2176000}}}}}
	groups := instagramMessageGroups(instagram.Subscription{AtAll: true}, e)
	if len(groups) != 2 {
		t.Fatalf("one card plus one standalone video expected: %d", len(groups))
	}
	if groups[0][0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("at-all must be a real OneBot segment")
	}
	if groups[0][1].(napcat.MessageSegment).Data["text"] != "\n" {
		t.Fatal("at-all must be on its own line")
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
