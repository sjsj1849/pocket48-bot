package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/tiktokmonitor"
)

// Regression: TikTok cards showed "pocket 48" as the source (both the header
// and the footer dot) because the sender only pushed a bare text segment with
// no 【昵称|来源】 header, so Feishu's source fell back to the "Pocket48"
// placeholder.
func TestZZTiktokDocumentSource(t *testing.T) {
	v := tiktokmonitor.Video{
		ID:         "7300000000000000000",
		Desc:       "심쿡 하트조트 영상",
		AuthorName: "자이昂贵",
		CreateTime: 1759000000,
		Stats: tiktokmonitor.Stats{
			Play: 123456,
			Like: 7890,
		},
	}
	doc := buildTiktokDocument(v)

	if doc.Source != "TikTok" {
		t.Fatalf("Source = %q, want TikTok (never Pocket48)", doc.Source)
	}
	if doc.Author != "자이昂贵" {
		t.Fatalf("Author = %q, want 자이昂贵", doc.Author)
	}
	if doc.Title != "자이昂贵" {
		t.Fatalf("Title = %q, want 자이昂贵", doc.Title)
	}
	if tiktokmonitor.Link(v) == "" {
		t.Fatal("Link must be set so the card gets a jump button")
	}
	if doc.Link != tiktokmonitor.Link(v) {
		t.Fatalf("Link = %q, want %q", doc.Link, tiktokmonitor.Link(v))
	}
	if doc.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be set from CreateTime")
	}
	if doc.CreatedAt.Unix() != v.CreateTime {
		t.Fatalf("CreatedAt = %v, want unix %d", doc.CreatedAt, v.CreateTime)
	}
}

// The card body must be the original title only. Duration / play / like were
// dropped on explicit user request: they churn every poll and push the actual
// title below the fold.
func TestZZTiktokBodyHasNoRedundantStats(t *testing.T) {
	v := tiktokmonitor.Video{
		Desc:       "심쿡 하트조트 영상",
		AuthorName: "자이昂贵",
		Stats: tiktokmonitor.Stats{
			Play: 123456,
			Like: 7890,
		},
	}
	body := buildTiktokBody(v)
	if body != "심쿡 하트조트 영상" {
		t.Fatalf("Body = %q, want the bare title", body)
	}
	for _, banned := range []string{"时长", "播放", "点赞", "----"} {
		if strings.Contains(body, banned) {
			t.Fatalf("body must not contain %q, got %q", banned, body)
		}
	}
}

// Even a stats-free video must not grow any extra line.
func TestZZTiktokBodyMinimal(t *testing.T) {
	body := buildTiktokBody(tiktokmonitor.Video{})
	if body != "(无标题)" {
		t.Fatalf("Body = %q, want (无标题)", body)
	}
}

// An empty author must not collapse the card header into an empty string —
// Feishu would then fall back to the platform name as the "sender".
func TestZZTiktokDocumentAuthorFallback(t *testing.T) {
	doc := buildTiktokDocument(tiktokmonitor.Video{Desc: "x"})
	if strings.TrimSpace(doc.Author) == "" {
		t.Fatal("Author must never be empty")
	}
}
