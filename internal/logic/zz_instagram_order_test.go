package logic

import (
	"testing"

	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/napcat"
)

// 这个测试锁住用户 2026-10-09 明确要求的顺序：
//
//	先拿时长 → 做去重判断 → 再决定要不要发视频本体
//
// 关键点：DurationMS 由采集阶段（sidecar）返回，**不需要下载**就能拿到，
// 所以判重的边际成本是 0；被去重的视频连下载都不该发生。
func TestInstagramDedupeHappensBeforeAnyDownload(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 抖音 4 秒前发过同名同长（18 秒）的一条。
	ix.Record("同一条舞蹈翻跳", base, "douyin", 18)

	e := igReelEvent("order-1", "同一条舞蹈翻跳")

	// 先判重：此时不发生任何下载
	skip := instagramShouldSkipVideo(ix, e, base+4000)
	if !skip {
		t.Fatal("对方已发过同一条，必须判重跳过")
	}

	// 再组消息：视频本体不应出现在任何一个 group 里
	groups := instagramMessageGroups(instagram.Subscription{}, e, skip)

	videoSegments := 0
	remoteVideoURLs := 0
	for _, group := range groups {
		for _, item := range group {
			seg, ok := item.(napcat.MessageSegment)
			if !ok {
				continue
			}
			switch seg.Type {
			case "video":
				videoSegments++
			case "text":
				// 封面若还是 http 直链，localizeMessageGroups 会去下载它。
				// 这里只关心视频本体不出现。
			}
			if raw := seg.Data["file"]; len(raw) > 7 && (raw[:7] == "http://" || (len(raw) > 8 && raw[:8] == "https://")) {
				if seg.Type == "video" {
					remoteVideoURLs++
				}
			}
		}
	}
	if videoSegments != 0 {
		t.Fatalf("判重命中后不得出现任何视频段（否则会被下载），实际 %d 段", videoSegments)
	}
	if remoteVideoURLs != 0 {
		t.Fatalf("判重命中后不得引用远程视频直链（否则 localize 会下载），实际 %d 处", remoteVideoURLs)
	}
}

// 反向用例：未命中去重时，视频本体必须在 messages 里，且带直链，
// 否则就变成「永远只发封面」的退化行为。
func TestInstagramKeepsVideoWhenDedupeMisses(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()

	e := igReelEvent("order-2", "全新的一条内容")
	if instagramShouldSkipVideo(ix, e, base) {
		t.Fatal("索引为空时不该判重")
	}

	groups := instagramMessageGroups(instagram.Subscription{}, e, false)
	found := false
	for _, group := range groups {
		for _, item := range group {
			if seg, ok := item.(napcat.MessageSegment); ok && seg.Type == "video" {
				found = true
				if seg.Data["file"] != "https://720" {
					t.Fatalf("视频直链不对: %+v", seg.Data)
				}
			}
		}
	}
	if !found {
		t.Fatal("未命中去重时必须保留视频本体")
	}
}

// DurationMS 必须来自采集结果而不是下载 —— 用一个时长明确的样本锁住秒数换算。
func TestInstagramDedupeUsesCollectedDurationWithoutDownload(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 对方发了 18 秒的一条；我方这条也是 18 秒（18666ms 向上取整）。
	ix.Record("时长对齐样本", base, "tiktok", 18)

	e := igReelEvent("dur-1", "时长对齐样本")
	if got := instagramLongestVideoSeconds(e); got != 18 {
		t.Fatalf("18666ms 应换算成 18 秒, got %d", got)
	}
	if !instagramShouldSkipVideo(ix, e, base+2000) {
		t.Fatal("时长一致时应命中去重（证明时长取自采集结果，无需下载）")
	}
	// 时长不同则放行，证明判定真的用到了时长而不是碰巧命中。
	e2 := igReelEvent("dur-2", "时长对齐样本")
	e2.Media[0].DurationMS = 30000 // 30 秒
	if instagramShouldSkipVideo(ix, e2, base+2000) {
		t.Fatal("时长不同（30s vs 18s）必须放行")
	}
}
