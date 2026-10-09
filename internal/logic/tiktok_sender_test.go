package logic

import (
	"testing"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/tiktokmonitor"
)

// ★ 回归（2026-10-04）：TikTok 推送曾经「只有标题没有封面图」。
//
// 根因不是采集侧 —— sidecar一直在抓封面，Video.Cover 字段也早就有，
// 是**推送侧从来没读过它**。抖音/X/小红书/B站 都有
// `if len(images) == 0 && Cover != ""` 的兜底，唯独 TikTok 漏了。
//
// 这个测试盯住buildTiktokDocument 的产出：Document 里不能凭空多出
// image media —— 封面是**下载后**才追加的（downloadMediaFile 需要真实网络），
// 所以这里只能验结构；真正的下载失败降级由日志兜底。
func TestZZTiktokDocumentHasNoPreloadedCover(t *testing.T) {
	doc := buildTiktokDocument(tiktokmonitor.Video{
		ID: "1", Desc: "x", AuthorName: "Hearts2Hearts",
		Cover: "https://example.invalid/cover.jpg",
	})
	for _, m := range doc.Media {
		if m.Kind == "image" {
			t.Error("buildTiktokDocument 不该自行塞图片（应走下载后的追加路径）")
		}
	}
	if doc.Source != "TikTok" {
		t.Errorf("Source = %q，期望 TikTok", doc.Source)
	}
}

// 封面URL 为空时不能产生任何 media，且不 panic。
func TestZZTiktokEmptyCoverSafe(t *testing.T) {
	doc := buildTiktokDocument(tiktokmonitor.Video{ID: "1", Desc: "y"})
	if len(doc.Media) != 0 {
		t.Errorf("无封面时 Media 应为空，实际 %d 项", len(doc.Media))
	}
}

// 文档转segment 时，image media 必须真的渲染成图片 segment。
// 这条锁住 message 层：新加的封面最终要变成图片发出去。
func TestZZDocumentRendersImageMedia(t *testing.T) {
	doc := message.Document{
		Source: "TikTok", Author: "a", Body: "b",
		Media: []message.Media{{Kind: "image", Source: "/tmp/x.jpg"}},
	}
	segs, _ := message.ToSegments(doc).([]message.Segment)
	found := false
	for _, s := range segs {
		if s.Type == "image" {
			found = true
			if s.Data["file"] != "/tmp/x.jpg" {
				t.Errorf("图片 segment 未带上路径: %#v", s)
			}
		}
	}
	if !found {
		t.Error("image media 没有被渲染成 image segment")
	}
}
