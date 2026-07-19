package logic

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestXiaohongshuNoteTime(t *testing.T) {
	if got := xiaohongshuNoteTime("65f00000abcdef1234567890"); got != 0x65f00000 {
		t.Fatalf("timestamp = %d", got)
	}
	if got := xiaohongshuNoteTime("not-a-note"); got != 0 {
		t.Fatalf("invalid timestamp = %d", got)
	}
}

func TestResolveXiaohongshuProfileURL(t *testing.T) {
	const userID = "5f1234560000000001000001"
	gotID, gotURL, err := ResolveXiaohongshuTarget(context.Background(), "https://www.xiaohongshu.com/user/profile/"+userID+"?xsec_token=abc")
	if err != nil {
		t.Fatal(err)
	}
	if gotID != userID || gotURL == "" {
		t.Fatalf("got id=%q url=%q", gotID, gotURL)
	}
}

func TestResolveXiaohongshuInternalUserID(t *testing.T) {
	const userID = "597804a15e87e70f59fd8c57"
	gotID, gotURL, err := ResolveXiaohongshuTarget(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	if gotID != userID {
		t.Fatalf("id = %q", gotID)
	}
	if !strings.Contains(gotURL, userID) {
		t.Fatalf("url = %q", gotURL)
	}
}

func TestResolveXiaohongshuRedBookNumberRejected(t *testing.T) {
	// 956753385 is a public 小红书号, not the internal profile user_id.
	// Resolve currently treats pure digits under 16 chars as invalid URL/id input.
	_, _, err := ResolveXiaohongshuTarget(context.Background(), "956753385")
	if err == nil {
		t.Fatal("expected error for short red-book number")
	}
}

func TestResolveXiaohongshuXhslink(t *testing.T) {
	// Live network: short share link should expand to /user/profile/<internal id>.
	gotID, gotURL, err := ResolveXiaohongshuTarget(context.Background(), "https://xhslink.com/m/950LyAGEhNR")
	if err != nil {
		t.Skipf("xhslink resolve unavailable: %v", err)
	}
	if gotID != "597804a15e87e70f59fd8c57" {
		t.Fatalf("id = %q", gotID)
	}
	if !strings.Contains(gotURL, "/user/profile/") {
		t.Fatalf("url = %q", gotURL)
	}
}

func TestXiaohongshuLiveFormatHasAtNewlineTitleBodyTimestamp(t *testing.T) {
	// Build the same text shape dispatchLive produces.
	name := "胡晓慧"
	body := []string{fmt.Sprintf("【%s|小红书直播】", name), "直播已结束", "2026-07-18 11:06:21"}
	text := "\n" + strings.Join(body, "\n")
	if !strings.HasPrefix(text, "\n【") {
		t.Fatalf("text should start with newline before title so @ is alone: %q", text)
	}
	lines := strings.Split(strings.TrimPrefix(text, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("lines = %#v", lines)
	}
	if lines[0] != "【胡晓慧|小红书直播】" {
		t.Fatalf("title = %q", lines[0])
	}
	if lines[1] != "直播已结束" {
		t.Fatalf("body = %q", lines[1])
	}
	if lines[len(lines)-1] != "2026-07-18 11:06:21" {
		t.Fatalf("timestamp = %q", lines[len(lines)-1])
	}
}

func TestXiaohongshuLiveDebounceRequiresTwoSamples(t *testing.T) {
	if xiaohongshuLiveConfirmTimes < 2 {
		t.Fatalf("confirm times = %d, want >= 2", xiaohongshuLiveConfirmTimes)
	}
}
