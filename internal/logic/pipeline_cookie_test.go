package logic

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// douyinCookiesForTest 从浏览器 profile 的 storage state 里读出抖音 cookie。
// 与 extract_cmd.go 里的 (b *Bot).douyinCookies 同一份数据源，
// 但独立实现避免依赖 Bot 实例（测试里不想构造整个 Bot）。
func douyinCookiesForTest(t *testing.T) map[string]string {
	t.Helper()
	path := "/root/pocket48-bot/storage/weibo-browser-profile/weibo-storage-state.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 cookie 失败: %v", err)
	}
	var state struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("解析 cookie 失败: %v", err)
	}
	out := map[string]string{}
	for _, c := range state.Cookies {
		if strings.Contains(c.Domain, "douyin") {
			out[c.Name] = c.Value
		}
	}
	if len(out) == 0 {
		t.Fatal("没取到抖音 cookie")
	}
	t.Logf("抖音 cookie 数=%d", len(out))
	return out
}
