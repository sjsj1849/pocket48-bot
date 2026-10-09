package outbound

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPersistentReplyMapSurvivesRestart 覆盖线上故障：主帖先发、成员稍后在该帖下
// 回复。ReplyMap 纯内存时，任何一次重启都会让这条线程的父消息 id 消失，回复
// 于是既不挂载也不带引用块。持久化后重启仍能挂上。
func TestPersistentReplyMapSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")

	first := NewPersistentReplyMap(100, path, "")
	first.Record("post:3-241985852", "om_post_card")
	first.Record("comment:2-389864561", "om_reply")

	// 模拟重启：全新实例从同一文件加载
	second := NewPersistentReplyMap(100, path, "")
	if got, ok := second.Lookup("post:3-241985852"); !ok || got != "om_post_card" {
		t.Fatalf("重启后主帖映射应仍在，实际 %q %v", got, ok)
	}
	if got, ok := second.Lookup("comment:2-389864561"); !ok || got != "om_reply" {
		t.Fatalf("重启后评论映射应仍在，实际 %q %v", got, ok)
	}
	if second.Len() != 2 {
		t.Errorf("重启后应有 2 条映射，实际 %d", second.Len())
	}
}

// TestPersistentReplyMapMissingFile 首次运行没有文件是正常的，不能因此启动失败。
func TestPersistentReplyMapMissingFile(t *testing.T) {
	m := NewPersistentReplyMap(10, filepath.Join(t.TempDir(), "absent.jsonl"), "")
	if m.Len() != 0 {
		t.Errorf("缺文件应得到空映射，实际 %d", m.Len())
	}
	m.Record("a", "1")
	if got, ok := m.Lookup("a"); !ok || got != "1" {
		t.Errorf("写入后应可查，实际 %q %v", got, ok)
	}
}

// TestPersistentReplyMapSkipsCorruptLines 写了一半的末行不能连带丢掉前面的数据。
func TestPersistentReplyMapSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")
	good := `{"sourceId":"a","messageId":"1"}` + "\n" +
		`{"sourceId":"b","messageId":"2"}` + "\n"
	if err := os.WriteFile(path, []byte(good+`{"sourceId":"c","mess`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewPersistentReplyMap(10, path, "")
	if got, ok := m.Lookup("a"); !ok || got != "1" {
		t.Errorf("a 应可查，实际 %q %v", got, ok)
	}
	if got, ok := m.Lookup("b"); !ok || got != "2" {
		t.Errorf("b 应可查，实际 %q %v", got, ok)
	}
}

// TestPersistentReplyMapTrimsOnLoad 超容量的旧文件加载后要按上限裁剪。
func TestPersistentReplyMapTrimsOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")
	if err := os.WriteFile(path, []byte(
		`{"sourceId":"a","messageId":"1"}`+"\n"+
			`{"sourceId":"b","messageId":"2"}`+"\n"+
			`{"sourceId":"c","messageId":"3"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewPersistentReplyMap(2, path, "")
	if _, ok := m.Lookup("a"); ok {
		t.Error("最旧的 a 应被裁掉")
	}
	if got, ok := m.Lookup("c"); !ok || got != "3" {
		t.Errorf("最新的 c 应保留，实际 %q %v", got, ok)
	}
}

// TestPersistentReplyMapUpdateKeepsSingleEntry 重复写同一 source 不应产生重复行，
// 否则文件会无限膨胀。
func TestPersistentReplyMapUpdateKeepsSingleEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")
	m := NewPersistentReplyMap(10, path, "")
	m.Record("a", "1")
	m.Record("a", "2")
	if m.Len() != 1 {
		t.Fatalf("重复写应只有 1 条，实际 %d", m.Len())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("文件应只有 1 行，实际 %d 行：%s", lines, data)
	}
	if got, _ := m.Lookup("a"); got != "2" {
		t.Errorf("应保留最新值，实际 %q", got)
	}
}

// TestReplyMapLenNilSafe nil 映射不应 panic。
func TestReplyMapLenNilSafe(t *testing.T) {
	var m *ReplyMap
	if m.Len() != 0 {
		t.Errorf("nil 映射 Len 应为 0，实际 %d", m.Len())
	}
}
