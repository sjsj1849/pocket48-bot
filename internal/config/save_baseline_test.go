package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveKeepsDiskValueWhenProcessDidNotTouchField 锁住「配置回滚」修复。
//
// 背景：Config 是 LoadConfig 时一次性加载、长期驻留的结构体。
// Save() 原来「结构体有的键无条件用内存值覆盖」，于是像
// douyin_monitor.go:1770 那样每 10 秒调一次 Save() 的模块，
// 会把进程加载那一刻的旧值写回磁盘，把之后面板/脚本写入的新值悄悄顶掉。
// 实际表现：换绑飞书群 ID，脚本写成功后一次重启就被冲回旧值。
//
// 这里模拟那条链路：加载 -> 外部改磁盘 -> Bot 侧 Save -> 磁盘应保留外部新值。
func TestSaveKeepsDiskValueWhenProcessDidNotTouchField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Bot 启动时磁盘上的内容：飞书群是旧 ID
	initial := `{"DELIVERY_TARGETS":[{"id":"feishu:group:old","platform":"feishu","kind":"group","address":"oc_old","name":"哈图哈"}],"PANEL_ONLY_KEY":"keep-me"}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.DeliveryTargets[0].Address; got != "oc_old" {
		t.Fatalf("加载时应读到 oc_old，实际 %q", got)
	}

	// 模拟「面板/脚本在运行期把飞书群换成新 ID」
	updated := `{"DELIVERY_TARGETS":[{"id":"feishu:group:new","platform":"feishu","kind":"group","address":"oc_new","name":"哈图哈"}],"PANEL_ONLY_KEY":"keep-me"}`
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}

	// Bot 侧某个无关模块（如抖音监控）调 Save()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	var targets []DeliveryTarget
	if err := json.Unmarshal(got["DELIVERY_TARGETS"], &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Address != "oc_new" {
		t.Fatalf("外部写入的新 ID 被回滚了，实际 = %+v", targets)
	}

	var panelOnly string
	if err := json.Unmarshal(got["PANEL_ONLY_KEY"], &panelOnly); err != nil {
		t.Fatalf("面板独有键丢失：%v", err)
	}
	if panelOnly != "keep-me" {
		t.Fatalf("面板独有键被改动，值 = %q", panelOnly)
	}
}

// TestSaveStillPersistsBotSideChanges 确认修复没有牺牲原有能力：
// Bot 侧真正改过的字段仍然要能落盘。
func TestSaveStillPersistsBotSideChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	initial := `{"DELIVERY_TARGETS":[],"SUPER_ADMIN":1001}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	// Bot 侧改了 SuperAdmin（等价于 UpdateToken 之类）
	cfg.SuperAdmin = 2002
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "2002") {
		t.Fatalf("Bot 侧改动没有落盘：%s", raw)
	}
}

// TestSaveTwiceInSameProcessStaysStable 确认同进程多次 Save 不会自我扰动。
func TestSaveTwiceInSameProcessStaysStable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	initial := `{"DELIVERY_TARGETS":[{"id":"feishu:group:new","platform":"feishu","kind":"group","address":"oc_new","name":"哈图哈"}]}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(path)
		if !strings.Contains(string(raw), "oc_new") {
			t.Fatalf("第 %d 次 Save 后 ID 变了：%s", i+1, raw)
		}
	}
}

var _ = io.Discard
