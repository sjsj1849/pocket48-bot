package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocket48-bot/internal/config"
)

// 保存飞书投递目标不能顺手抹掉面板里其它平台的主开关。
//
// config.Config 是固定结构体，只声明了 Bot 关心的字段；早期实现直接把它
// json.Marshal 回 config.json，于是 BILIBILI_ENABLED 这类只由面板维护的键
// 会在改一次投递目标后凭空消失，表现为「B 站开关莫名其妙又变回未启用」。
func TestOutboundTargetsPreserveUnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	seed := map[string]any{
		"DELIVERY_TARGETS":    []any{},
		"BILIBILI_ENABLED":    true,
		"XIAOHONGSHU_ENABLED": true,
		"NIM_ENABLED":         true,
	}
	encoded, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}

	s := &Server{opts: Options{ConfigPath: configPath}}
	body := `{"targets":[{"platform":"feishu","id":"oc_test","name":"测试群"}]}`
	rec := httptest.NewRecorder()
	s.handleOutboundTargets(rec, httptest.NewRequest("PUT", "/api/outbound-targets", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	raw := map[string]any{}
	saved, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(saved, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"BILIBILI_ENABLED", "XIAOHONGSHU_ENABLED", "NIM_ENABLED"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("key %q was dropped by a delivery-target save; remaining keys: %v", key, keysOfRaw(raw))
		}
	}
	if raw["BILIBILI_ENABLED"] != true {
		t.Errorf("BILIBILI_ENABLED = %v, want true", raw["BILIBILI_ENABLED"])
	}
	targets, ok := raw["DELIVERY_TARGETS"].([]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("DELIVERY_TARGETS not updated: %#v", raw["DELIVERY_TARGETS"])
	}
	// 目标本身仍要能被 LoadConfig 正常读回
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.DeliveryTargets) != 1 || cfg.DeliveryTargets[0].Name != "测试群" {
		t.Fatalf("DeliveryTargets round-trip failed: %#v", cfg.DeliveryTargets)
	}
}

func keysOfRaw(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
