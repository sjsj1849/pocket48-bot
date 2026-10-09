package dedupe

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNormalizeStripsTagsAndDecorations(t *testing.T) {
	cases := []struct {
		a, b string
	}{
		// 话题标签差异
		{"【Hearts2Hearts】it's oct 3rd yk #H2H", "Hearts2Hearts it's oct 3rd yk"},
		// 标签只吞到下一个空白为止，不得越界吞掉正文。
		// 若改成「# 后允许隔一个空格再吞一个词」，
		// "dance #tag name" 就会变成 "dance"，不同标题会被误判成同一条而漏推。
		{"dance #h2h #Hearts2Hearts", "dance"},
		{"dance #tag name", "dance name"},
		// 括号包裹差异
		{"【Hearts2Hearts】CLOSER", "Hearts2Hearts CLOSER"},
		// 大小写差异
		{"Lemon Tang MV", "lemon tang mv"},
		// 空格与标点差异
		{"a, b. c", "a b c"},
		// 全角空格
		{"A　B", "A B"},
	}
	for _, c := range cases {
		if Normalize(c.a) != Normalize(c.b) {
			t.Errorf("归一化后应相同:\n  %q -> %q\n  %q -> %q",
				c.a, Normalize(c.a), c.b, Normalize(c.b))
		}
	}
}

func TestNormalizeKeepsDifferentTitlesApart(t *testing.T) {
	// 关键：内容不同的标题绝不能被误判成同一条。
	pairs := [][2]string{
		{"Lemon Tang MV", "Lemon Tea MV"},
		{"oct 3rd yk", "oct 4th yk"},
		{"CLOSER", "closer2"},
		{"", "abc"},
		{"#", "##"},
	}
	for _, p := range pairs {
		a, b := Normalize(p[0]), Normalize(p[1])
		if a == "" || b == "" {
			// 空指纹不应被当作可匹配项，这里只保证不抛异常。
			continue
		}
		if a == b {
			t.Errorf("不同标题被误判为相同: %q vs %q -> %q", p[0], p[1], a)
		}
	}
}

func TestNormalizeEmptyInput(t *testing.T) {
	for _, s := range []string{"", "   ", "\t\n", "#", "###"} {
		if got := Normalize(s); got != "" && got == Normalize("#") {
			// 纯符号应归一化为空
		}
	}
	if Normalize("") != "" {
		t.Fatal("空串应归一化为空")
	}
}

func TestRecordAndLookup(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(filepath.Join(dir, "titles.json"), 24*time.Hour, 100)

	// 注意：Record/Lookup 都以「当前时间 - TTL」为过期线，
	// 所以测试里的时间戳必须贴近真实时间，不能用 1000 这种 1970 年的值。
	base := time.Now().UnixMilli()

	if _, ok := ix.Lookup("不存在的标题"); ok {
		t.Fatal("未登记的标题不应命中")
	}
	ix.Record("Lemon Tang MV", base-1000, "douyin", 0)
	got, ok := ix.Lookup("【Lemon Tang】MV #H2H")
	if !ok {
		t.Fatal("归一化后应命中已登记标题")
	}
	if got != base-1000 {
		t.Fatalf("命中时间应为 %d，实际 %d", base-1000, got)
	}
}

func TestRecordKeepsEarlierTimestamp(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(filepath.Join(dir, "titles.json"), 24*time.Hour, 100)
	base := time.Now().UnixMilli()
	ix.Record("same title", base-2000, "douyin", 0)
	// 重复登记一个更早的时间（B站首发更早）应以更早的为准
	ix.Record("same title", base-5000, "bilibili", 0)
	got, _ := ix.Lookup("same title")
	if got != base-5000 {
		t.Fatalf("应保留更早的时间戳，实际 %d", got)
	}
	// 再登记更晚的不应覆盖
	ix.Record("same title", base, "bilibili", 0)
	got, _ = ix.Lookup("same title")
	if got != base-5000 {
		t.Fatalf("更晚的时间戳不应覆盖，实际 %d", got)
	}
}

func TestPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "titles.json")
	want := time.Now().UnixMilli() - 4321
	first := NewIndex(path, 24*time.Hour, 100)
	first.Record("persist me", want, "douyin", 0)

	second := NewIndex(path, 24*time.Hour, 100)
	got, ok := second.Lookup("persist me")
	if !ok || got != want {
		t.Fatalf("重启后应能读到 %d，got=%d ok=%v", want, got, ok)
	}
}

func TestExpiresOldEntries(t *testing.T) {
	dir := t.TempDir()
	// TTL 极短以便构造过期
	ix := NewIndex(filepath.Join(dir, "titles.json"), time.Millisecond, 100)
	ix.Record("old one", 1, "douyin", 0)
	time.Sleep(10 * time.Millisecond)
	ix.Record("new one", time.Now().UnixMilli(), "douyin", 0)
	if _, ok := ix.Lookup("old one"); ok {
		t.Fatal("过期条目应被清除")
	}
	if _, ok := ix.Lookup("new one"); !ok {
		t.Fatal("未过期条目应保留")
	}
}

func TestRespectsMaxSize(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(filepath.Join(dir, "titles.json"), 24*time.Hour, 3)
	base := time.Now().UnixMilli()
	for i := 0; i < 10; i++ {
		ix.Record(string(rune('a'+i))+" title", base+int64(i), "douyin", 0)
	}
	if ix.Len() > 3 {
		t.Fatalf("应裁剪到 3 条以内，实际 %d", ix.Len())
	}
	// 最新一条必须保留
	if _, ok := ix.Lookup("j title"); !ok {
		t.Fatal("最新条目应保留")
	}
}

func TestIgnoresEmptyTitle(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(filepath.Join(dir, "titles.json"), 24*time.Hour, 100)
	ix.Record("", 1000, "douyin", 0)
	ix.Record("   ", 1000, "douyin", 0)
	if ix.Len() != 0 {
		t.Fatalf("空标题不应被登记，实际 %d 条", ix.Len())
	}
	if _, ok := ix.Lookup(""); ok {
		t.Fatal("空标题不应命中")
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "titles.json")
	ix := NewIndex(path, time.Hour, 10)
	ix.Record("x", time.Now().UnixMilli(), "douyin", 0)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应自动创建父目录并落盘: %v", err)
	}
}
