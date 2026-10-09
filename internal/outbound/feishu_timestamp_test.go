package outbound

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
)

func TestZZExtractTimestamp(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantBody string
		wantTime string
	}{
		{
			name:     "独占一行（文本消息）",
			in:       "胡晓慧: 今天的直播好精彩\n2026-10-02 17:52:03",
			wantBody: "胡晓慧: 今天的直播好精彩",
			wantTime: "2026-10-02 17:52:03",
		},
		{
			name:     "行尾粘连（纯图片：前缀无换行）",
			in:       "胡晓慧: 2026-10-02 17:52:03",
			wantBody: "胡晓慧:",
			wantTime: "2026-10-02 17:52:03",
		},
		{
			name:     "没有时间戳",
			in:       "胡晓慧: 今天的直播好精彩",
			wantBody: "胡晓慧: 今天的直播好精彩",
			wantTime: "",
		},
		{
			name:     "行内数字不应被误判",
			in:       "奖励 3000-01-01 12:00 加油",
			wantBody: "奖励 3000-01-01 12:00 加油",
			wantTime: "",
		},
	}
	for _, tc := range cases {
		body, stamp := extractTimestamp(tc.in)
		if body != tc.wantBody || stamp != tc.wantTime {
			t.Errorf("%s: extractTimestamp(%q) = (%q, %q), want (%q, %q)",
				tc.name, tc.in, body, stamp, tc.wantBody, tc.wantTime)
		}
	}
}

// TestZZPureImageHasNoBodyTime 复现口袋48 纯图片消息：发言前缀无换行 + 末尾时间戳。
// 期望正文被 extractSender 吃成空（只剩图片），底栏携带对方发送时间。
func TestZZPureImageHasNoBodyTime(t *testing.T) {
	segments := []message.Segment{
		{Type: "text", Data: map[string]string{"text": "【胡晓慧|XXX】\n"}},
		{Type: "text", Data: map[string]string{"text": "胡晓慧: "}},
		{Type: "image", Data: map[string]string{"file": "https://example.com/a.jpg"}},
		{Type: "text", Data: map[string]string{"text": "2026-10-02 17:52:03"}},
	}
	content := renderFeishuContent(segments)
	if content.timestamp != "2026-10-02 17:52:03" {
		t.Fatalf("timestamp = %q, want 2026-10-02 17:52:03", content.timestamp)
	}
	if strings.Contains(content.text, "2026-10-02") {
		t.Fatalf("正文仍带时间戳: %q", content.text)
	}
	if len(content.images) != 1 {
		t.Fatalf("images = %d, want 1", len(content.images))
	}
	note := footerNote(content)
	if strings.Count(note, "2026-10-02") != 1 {
		t.Fatalf("底栏应只出现一次时间戳: %q", note)
	}
}

// TestZZFooterNoDeliveryTime 底栏不得回落到本机当前时间。
func TestZZFooterNoDeliveryTime(t *testing.T) {
	note := footerNote(feishuContent{source: "包间", timestamp: ""})
	if note != "包间" {
		t.Fatalf("note = %q, want 包间", note)
	}
}
