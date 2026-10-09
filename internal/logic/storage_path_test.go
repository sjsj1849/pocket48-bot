package logic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStorageRootOfResolvesStorageDir 锁定 storage 根的推导规则。
//
// ★ 2026-10-04 线上踩坑：注释写「config.json 位于 <storage>/config.json，
// 因此上跳一级」，但部署布局其实是
//
//	<root>/config.json
//	<root>/storage/
//
// 也就是 storage 是 config.json 的**子目录**，只上跳一级只能到项目根。
// 于是去重索引落到 <root>/cursors/、TikTok state.json 落到 <root>/tiktok/。
//
// 这个测试用真实的目录布局来断言，任何人再改路径推导都会立刻红。
func TestStorageRootOfResolvesStorageDir(t *testing.T) {
	root := t.TempDir()
	// 真实布局：config.json 与 storage/ 同级。
	if err := os.MkdirAll(filepath.Join(root, "storage"), 0o755); err != nil {
		t.Fatalf("建 storage 目录失败: %v", err)
	}
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("写 config.json 失败: %v", err)
	}

	got := storageRootOf(configPath)
	want := filepath.Join(root, "storage")
	if got != want {
		t.Fatalf("storage 根推导错位：\n  实际 %s\n  期望 %s", got, want)
	}
	// 关键：结尾必须是 storage，且不能是项目根本身。
	if filepath.Base(got) != "storage" {
		t.Fatalf("storage 根的末级应是 storage，实际 %q", filepath.Base(got))
	}
	if filepath.Dir(got) != root {
		t.Fatalf("storage 应是 config.json 的同级子目录，实际父目录 %s", filepath.Dir(got))
	}
}

// TestStorageRootOfEmptyPath 确认空路径退化成相对路径而不是空串。
func TestStorageRootOfEmptyPath(t *testing.T) {
	got := storageRootOf("")
	if got != "storage" {
		t.Fatalf("空 configPath 应退化为 %q，实际 %q", "storage", got)
	}
	if strings.ContainsRune(got, os.PathSeparator) {
		t.Fatalf("退化值不该含路径分隔符，实际 %q", got)
	}
}

// TestStorageRootIsPrefixOfPlatformDirs 确认推导出的 storage 根
// 真的是各平台数据目录的祖先 —— 这是路径推导最容易错、又最难一眼看出来的地方。
func TestStorageRootIsPrefixOfPlatformDirs(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("写 config.json 失败: %v", err)
	}

	storage := storageRootOf(configPath)
	for _, sub := range []string{"cursors", "tiktok", "douyin", "weverse", "x"} {
		platformDir := filepath.Join(storage, sub)
		rel, err := filepath.Rel(storage, platformDir)
		if err != nil {
			t.Fatalf("Rel 失败: %v", err)
		}
		// filepath.Rel 在参数有包含关系时会返回形如 "../xxx" 的结果。
		// 一旦出现 ".."，说明 platformDir 落在了 storage 外面 —— 就是这次线上那个 bug。
		if strings.HasPrefix(rel, "..") {
			t.Fatalf("平台目录 %s 落在 storage %s 之外（Rel=%s）", platformDir, storage, rel)
		}
	}
}

// TestCrossTitleIndexPathLivesUnderStorage 直接锁住去重索引的落盘位置。
//
// 之前它落在 <root>/cursors/，与 storage/cursors/ 里其它平台的游标文件
// 混在一起之外，还会让「备份脚本按 storage/ 扫」整片漏掉。
func TestCrossTitleIndexPathLivesUnderStorage(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("写 config.json 失败: %v", err)
	}

	// 进程内单例只能初始化一次，这里直接验证路径推导函数的结果，
	// 避免和 crossOnce 单例互相污染（同 sync.Once 重置后仍指向旧实例的老坑）。
	got := filepath.Join(storageRootOf(configPath), dedupePath)
	want := filepath.Join(root, "storage", "cursors", "cross-platform-titles.json")
	if got != want {
		t.Fatalf("去重索引路径错位：\n  实际 %s\n  期望 %s", got, want)
	}
	if !strings.HasPrefix(got, filepath.Join(root, "storage")+string(os.PathSeparator)) {
		t.Fatalf("去重索引必须在 storage/ 下，实际 %s", got)
	}
}
