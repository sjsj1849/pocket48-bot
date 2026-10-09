package dedupe

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestIndex(t *testing.T) *Index {
	t.Helper()
	return NewIndex(filepath.Join(t.TempDir(), "titles.json"), 14*24*time.Hour, 4096)
}

// 复现 2026-10-04 线上那次重复推送的真实场景。
//
// 抖音侧标题：like if I can't help falling in love with you?
//
//	#Hearts2Hearts #H2H #CARMEN #STE...
//
// B站侧标题：【Hearts2Hearts】like if I can't help falling in love with you?
//
// 归一化后两边差一个 "hearts2hearts" 前缀，**仅靠指纹相等判不出来**。
// 这正是线上重复推送的原因，必须靠「词重合 + 时长」救回。
func TestMatchRealWorldBiliPrefix(t *testing.T) {
	ix := newTestIndex(t)
	douyinAt := int64(1791092331000)
	seconds := 116

	ix.Record("like if I can't help falling in love with you? #Hearts2Hearts #H2H #CARMEN",
		douyinAt, "douyin", seconds)

	biliTitle := "【Hearts2Hearts】like if I can't help falling in love with you?"

	// 一级判定确实匹配不上（这是线上漏判的直接原因）
	if _, ok := ix.Lookup(biliTitle); ok {
		t.Fatal("前置条件变了：一级指纹本应匹配不上，请重新评估本测试")
	}

	// 二级判定必须匹配上，且给出抖音的发布时间
	got, ok := ix.Match(biliTitle, seconds)
	if !ok {
		t.Fatal("二级判定（词重合+时长）应匹配上，实际未匹配")
	}
	if got != douyinAt {
		t.Fatalf("首发时间应为抖音的 %d，实际 %d", douyinAt, got)
	}
}

// 时长不一致时，绝不能判为同一条 —— 这是二级判定的安全阀。
func TestMatchRequiresDurationAgreement(t *testing.T) {
	ix := newTestIndex(t)
	ix.Record("like if I can't help falling in love with you? #Hearts2Hearts",
		int64(1791092331000), "douyin", 116)

	// 同系列但不同一集（时长差很多）
	if _, ok := ix.Match("【Hearts2Hearts】like if I can't help falling in love with you?", 210); ok {
		t.Fatal("时长 210s 与 116s 相差过大，不应判为同一条")
	}
	// 差 2 秒在容差内，应判为同一条
	if _, ok := ix.Match("【Hearts2Hearts】like if I can't help falling in love with you?", 118); !ok {
		t.Fatal("时长 118s 与 116s 在容差内，应判为同一条")
	}
	// 差 5 秒超出容差
	if _, ok := ix.Match("【Hearts2Hearts】like if I can't help falling in love with you?", 121); ok {
		t.Fatal("时长 121s 与 116s 相差 5s 超出容差，不应判为同一条")
	}
}

// 时长未知（0）时二级判定必须关闭，避免只凭标题就误判。
func TestMatchDisabledWhenDurationUnknown(t *testing.T) {
	ix := newTestIndex(t)
	ix.Record("like if I can't help falling in love with you? #Hearts2Hearts",
		int64(1791092331000), "douyin", 0)

	if _, ok := ix.Match("【Hearts2Hearts】like if I can't help falling in love with you?", 0); ok {
		t.Fatal("登记侧时长为 0 时不应触发二级判定")
	}
}

// 完全不同的内容不得误判。
func TestMatchRejectsUnrelated(t *testing.T) {
	ix := newTestIndex(t)
	base := int64(1791092331000)
	ix.Record("it’s oct 3rd yk #Hearts2Hearts #H2H", base, "douyin", 30)
	ix.Record("魔法少女、出撃 #Hearts2Hearts", base+1000, "douyin", 45)
	ix.Record("closer #Hearts2Hearts", base+2000, "douyin", 60)

	cases := []struct {
		title string
		secs  int
	}{
		{"今天的穿搭分享 #H2H", 30},
		{"跑步 #Hearts2Hearts", 30},
		{"完全不相干的一条内容 hello world", 30},
	}
	for _, tc := range cases {
		if _, ok := ix.Match(tc.title, tc.secs); ok {
			t.Fatalf("不该判为同一条: %q", tc.title)
		}
	}
}

// 同一系列但文案相近的，必须靠时长区分。
func TestSimilarTitleSameSeriesDifferentEpisode(t *testing.T) {
	ix := newTestIndex(t)
	// 同一系列两集，文案高度相似，只有时长不同
	ix.Record("hearts2hearts ep1 dance practice #H2H #Hearts2Hearts",
		int64(1791092331000), "douyin", 116)
	if _, ok := ix.Match("【Hearts2Hearts】hearts2hearts ep1 dance practice", 230); ok {
		t.Fatal("同系列不同集（116s vs 230s）不应判为同一条")
	}
}

func TestSimilarity(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
		desc string
	}{
		{"like if I can't help falling in love with you? #H2H",
			"【Hearts2Hearts】like if I can't help falling in love with you?", true, "仅多前缀"},
		{"it’s oct 3rd yk #Hearts2Hearts #H2H",
			"Hearts2Hearts it's oct 3rd yk", true, "标签顺序不同"},
		{"完全不同的第一条内容 abc def ghi",
			"另一条毫不相干 jkl mno pqr", false, "毫无重合"},
	}
	for _, tc := range cases {
		if got := SimilarTitle(tc.a, tc.b); got != tc.want {
			t.Fatalf("SimilarTitle(%q, %q) = %v, want %v (%s)",
				tc.a, tc.b, got, tc.want, tc.desc)
		}
	}
}

func TestDurationMatch(t *testing.T) {
	cases := []struct {
		a, b int
		want bool
		desc string
	}{
		{116, 116, true, "完全相等"},
		{116, 118, true, "差 2 秒在容差内"},
		{116, 119, true, "差 3 秒恰在容差边界内"},
		{116, 120, false, "差 4 秒超出容差"},
		{0, 116, false, "左侧未知"},
		{116, 0, false, "右侧未知"},
		{0, 0, false, "两侧都未知"},
		{116, 121, false, "差 5 秒"},
		{1, 1, true, "最小有效值相等"},
	}
	for _, tc := range cases {
		if got := DurationMatch(tc.a, tc.b); got != tc.want {
			t.Fatalf("DurationMatch(%d, %d) = %v, want %v (%s)",
				tc.a, tc.b, got, tc.want, tc.desc)
		}
	}
}

func TestCoreWords(t *testing.T) {
	got := CoreWords("like if I can't help falling in love with you? #H2H")
	if len(got) < 6 {
		t.Fatalf("应切出至少 6 个词，实际 %d: %v", len(got), got)
	}
	// 过短的片段不应成为「词」
	for _, w := range got {
		if len(w) < 3 {
			t.Fatalf("过短的词 %q 不该被保留: %v", w, got)
		}
	}
}

// 持久化后 Title 与 Seconds 必须一起恢复，否则重启后二级判定会失效。
func TestIndexPersistsTitleAndDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "titles.json")

	ix := NewIndex(path, 14*24*time.Hour, 4096)
	ix.Record("like if I can't help falling in love with you? #H2H",
		int64(1791092331000), "douyin", 116)

	// 重新开一个实例，模拟进程重启
	ix2 := NewIndex(path, 14*24*time.Hour, 4096)
	if _, ok := ix2.Match("【Hearts2Hearts】like if I can't help falling in love with you?", 116); !ok {
		t.Fatal("重启后二级判定应仍生效（Title/Seconds 需持久化）")
	}
}

// 首发方较晚登记时，应保留更早的时间戳。
func TestRecordKeepsEarliestTimestamp(t *testing.T) {
	ix := newTestIndex(t)
	early := int64(1791092331000)
	late := int64(1791092772000)
	ix.Record("一样的标题内容 abc def", late, "bilibili", 100)
	ix.Record("一样的标题内容 abc def", early, "douyin", 100)

	got, ok := ix.Lookup("一样的标题内容 abc def")
	if !ok || got != early {
		t.Fatalf("应保留更早的时间戳 %d，实际 %d (ok=%v)", early, got, ok)
	}
}
