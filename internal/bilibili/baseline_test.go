package bilibili

import "testing"

// 基线机制的回归测试。
//
// 背景：为了不再一刀切过滤短视频，bilibiliFetchVideos 放开了时长门槛。
// 副作用是历史上被过滤掉的投稿会突然变成「未见过的新内容」，
// 首轮轮询会把它们一次性全推出去。用户明确要求「不回溯，从现在开始记」，
// 因此 Cursor 引入 BaselineAt 做增量基线。

func TestPendingDynamicsDropsHistoryBeforeBaseline(t *testing.T) {
	now := int64(1_700_000_000_000)
	items := []Dynamic{
		{ID: "av:old1", Kind: "video", Time: now - 3600_000, Seconds: 8},
		{ID: "av:old2", Kind: "video", Time: now - 60_000, Seconds: 84},
		{ID: "av:new1", Kind: "video", Time: now + 1000, Seconds: 19},
	}
	c := Cursor{Ready: true, BaselineAt: now}
	got := PendingDynamics(c, items)
	if len(got) != 1 || got[0].ID != "av:new1" {
		t.Fatalf("基线前的投稿应被丢弃，只留下基线后的 1 条，实际 %d 条: %+v", len(got), got)
	}
}

// BaselineAt == 0 表示还没建立基线，此时不做任何过滤（首次运行的原行为）。
func TestPendingDynamicsNoBaselineMeansNoFilter(t *testing.T) {
	now := int64(1_700_000_000_000)
	items := []Dynamic{
		{ID: "av:old1", Kind: "video", Time: now - 3600_000},
		{ID: "av:new1", Kind: "video", Time: now + 1000},
	}
	got := PendingDynamics(Cursor{Ready: true}, items)
	if len(got) != 2 {
		t.Fatalf("未建立基线时不应过滤，实际 %d 条", len(got))
	}
}

// Advance 必须保留 BaselineAt，否则每轮都会重新建立基线，历史投稿会被反复判定为「新」。
func TestAdvancePreservesBaseline(t *testing.T) {
	now := int64(1_700_000_000_000)
	c := Cursor{Ready: true, UID: "123", BaselineAt: now}
	items := []Dynamic{
		{ID: "av:new1", Kind: "video", Time: now + 1000},
		{ID: "av:old1", Kind: "video", Time: now - 1000},
	}
	next := Advance(c, "123", items)
	if next.BaselineAt != now {
		t.Fatalf("Advance 应保留 BaselineAt=%d，实际 %d", now, next.BaselineAt)
	}
	// 基线前的 id 也必须进 Seen，否则下轮又会把它当新的。
	if len(next.Seen) != 2 {
		t.Fatalf("历史投稿也应记入 Seen，实际 %d 条: %v", len(next.Seen), next.Seen)
	}
	// Watermark 推进后，仍应只推基线后的内容。
	if got := PendingDynamics(next, items); len(got) != 0 {
		t.Fatalf("已见过的内容不应再次成为 pending，实际 %d 条", len(got))
	}
}
