package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSavePreservesPanelOnlyKeys 防止 Bot 侧 Save() 再次整体覆盖 config.json。
//
// config.Config 是固定结构体，只声明了 Bot 关心的字段；管理面板还维护着
// BILIBILI_ENABLED / XIAOHONGSHU_ENABLED 等只存在于 config.json、不在结构体里
// 的键。Save() 曾用 json.MarshalIndent(c) 整体覆盖，导致 Bot 任何一次 Save
// （改 token、微博 Cookie、群组订阅等）都会把面板开关静默删掉，表现为
// 「B 站开关莫名其妙又变回未启用」。
func TestSavePreservesPanelOnlyKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 一份含面板独有键的初始配置。
	initial := map[string]any{
		"QQ_ENABLED":           true,
		"POCKET_TOKEN":         "token-abc",
		"BILIBILI_ENABLED":     true,
		"XIAOHONGSHU_ENABLED":  false,
		"DELIVERY_TARGETS":     []any{map[string]any{"name": "测试群"}},
		"DOUYIN_LIVE_SUMMARY":  true,
		"ADMIN_PANEL_ONLY_KEY": "keep-me",
	}
	raw, err := json.MarshalIndent(initial, "", "    ")
	if err != nil {
		t.Fatalf("序列化初始配置失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatalf("写入初始配置失败: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig 失败: %v", err)
	}

	// 触发一次 Save（模拟 Bot 更新 token）。
	cfg.UpdateToken("token-xyz")

	after := map[string]any{}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取保存后的配置失败: %v", err)
	}
	if err := json.Unmarshal(saved, &after); err != nil {
		t.Fatalf("解析保存后的配置失败: %v", err)
	}

	// 结构体里的键应写入新值。
	if after["POCKET_TOKEN"] != "token-xyz" {
		t.Errorf("POCKET_TOKEN 未更新: %v", after["POCKET_TOKEN"])
	}
	if after["QQ_ENABLED"] != true {
		t.Errorf("QQ_ENABLED 不应被改动: %v", after["QQ_ENABLED"])
	}

	// 面板独有键必须原样保留。
	for _, key := range []string{"BILIBILI_ENABLED", "XIAOHONGSHU_ENABLED", "DOUYIN_LIVE_SUMMARY", "ADMIN_PANEL_ONLY_KEY"} {
		if _, ok := after[key]; !ok {
			t.Errorf("Save() 把面板独有键 %s 删掉了", key)
		}
	}
	if after["BILIBILI_ENABLED"] != true {
		t.Errorf("BILIBILI_ENABLED 值被改动: %v", after["BILIBILI_ENABLED"])
	}
	if after["ADMIN_PANEL_ONLY_KEY"] != "keep-me" {
		t.Errorf("ADMIN_PANEL_ONLY_KEY 值被改动: %v", after["ADMIN_PANEL_ONLY_KEY"])
	}
	if _, ok := after["DELIVERY_TARGETS"]; !ok {
		t.Error("Save() 把 DELIVERY_TARGETS 删掉了")
	}

	// 重新加载仍应能读回这些键。
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("重新 LoadConfig 失败: %v", err)
	}
	if reloaded.PocketToken != "token-xyz" {
		t.Errorf("重新加载后 token 不正确: %q", reloaded.PocketToken)
	}
}
