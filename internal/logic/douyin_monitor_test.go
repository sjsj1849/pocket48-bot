package logic

import (
	"context"
	"testing"
	"time"
)

func TestUnseenDouyinPosts(t *testing.T) {
	now := time.Unix(1_000, 0)
	posts := []douyinPost{
		{ID: "pinned-old", CreateTime: 100},
		{ID: "3", CreateTime: 900},
		{ID: "2", CreateTime: 800},
		{ID: "future", CreateTime: 2_000},
	}
	got := unseenDouyinPosts(posts, 700, now)
	if len(got) != 2 || got[0].ID != "2" || got[1].ID != "3" {
		t.Fatalf("unexpected posts: %#v", got)
	}
	latest, ok := latestTimestampedDouyinPost(posts, now)
	if !ok || latest.ID != "3" {
		t.Fatalf("latest timestamped post: %#v, ok=%v", latest, ok)
	}
}

func TestCanonicalDouyinPostURL(t *testing.T) {
	if got := canonicalDouyinPostURL(douyinPost{ID: "123", Type: "note", URL: "https://example.com/long"}); got != "https://www.douyin.com/note/123" {
		t.Fatalf("note URL=%q", got)
	}
	if got := canonicalDouyinPostURL(douyinPost{ID: "456", Type: "video"}); got != "https://www.douyin.com/video/456" {
		t.Fatalf("video URL=%q", got)
	}
}

func TestExtractDouyinOnline(t *testing.T) {
	body := map[string]interface{}{"payload": map[string]interface{}{"onlineUserForAnchor": "321"}}
	if got := extractDouyinOnline(body); got != 321 {
		t.Fatalf("online=%d", got)
	}
}

func TestResolveDouyinTargetSecUserID(t *testing.T) {
	sec, profile, err := ResolveDouyinTarget(context.Background(), "MS4wLjABAAAA_test-user")
	if err != nil || sec != "MS4wLjABAAAA_test-user" || profile == "" {
		t.Fatalf("resolve=(%q,%q,%v)", sec, profile, err)
	}
}

func TestResolveDouyinTargetDirectProfileDoesNotNeedNetwork(t *testing.T) {
	sec, _, err := ResolveDouyinTarget(context.Background(), "https://www.douyin.com/user/MS4wLjABAAAA_direct")
	if err != nil || sec != "MS4wLjABAAAA_direct" {
		t.Fatalf("resolve direct=(%q,%v)", sec, err)
	}
	if _, _, err := ResolveDouyinTarget(context.Background(), "https://example.com/user/MS4wLjABAAAA_direct"); err == nil {
		t.Fatal("non-Douyin URL should be rejected")
	}
}

func TestParseDouyinCommandLine(t *testing.T) {
	got, err := parseDouyinCommandLine(`node "./sidecar/path with space/index.mjs"`, "")
	if err != nil || len(got) != 2 || got[1] != "./sidecar/path with space/index.mjs" {
		t.Fatalf("parse=%#v err=%v", got, err)
	}
}

func TestClassifyDouyinIMEvent(t *testing.T) {
	group := douyinBrowserEvent{ConversationType: 2, ConversationID: "target", SenderUID: "owner"}
	if got := classifyDouyinIMEvent(group, "target", "owner", "self"); got != "group_owner" {
		t.Fatalf("group owner classification=%q", got)
	}
	group.SenderUID = "member"
	if got := classifyDouyinIMEvent(group, "target", "owner", "self"); got != "" {
		t.Fatalf("ordinary group member must be ignored, got %q", got)
	}
	private := douyinBrowserEvent{ConversationType: 1, SenderUID: "peer"}
	if got := classifyDouyinIMEvent(private, "target", "owner", "self"); got != "private_incoming" {
		t.Fatalf("incoming private classification=%q", got)
	}
	private.SenderUID = "self"
	if got := classifyDouyinIMEvent(private, "target", "owner", "self"); got != "" {
		t.Fatalf("outgoing private message must be ignored, got %q", got)
	}
}

func TestFormatDouyinIMTime(t *testing.T) {
	if got := formatDouyinIMTime(1784251775, 0); got != "2026-07-17 09:29:35" {
		t.Fatalf("seconds timestamp=%q", got)
	}
	if got := formatDouyinIMTime(0, 1784251775000); got != "2026-07-17 09:29:35" {
		t.Fatalf("millisecond fallback=%q", got)
	}
}

func TestFormatDouyinIMNotification(t *testing.T) {
	got := formatDouyinIMNotification("【肥家｜抖音群】", "群主", "消息正文", "2026-07-17 10:00:00")
	want := "【肥家｜抖音群】\n来自：群主\n消息正文\n2026-07-17 10:00:00"
	if got != want {
		t.Fatalf("notification=%q", got)
	}
}

func TestFormatDouyinReplyText(t *testing.T) {
	got := formatDouyinReplyText("发送人", "回复内容", "原发送人", "原消息")
	if got != "原发送人:原消息\n发送人:回复内容" {
		t.Fatalf("reply=%q", got)
	}
}

func TestClassifyDouyinIMEventRejectsExplicitSelfUID(t *testing.T) {
	event := douyinBrowserEvent{ConversationType: 1, SenderUID: "self", SelfUID: "self"}
	if got := classifyDouyinIMEvent(event, "", "", event.SelfUID); got != "" {
		t.Fatalf("self message classified as %q", got)
	}
}
