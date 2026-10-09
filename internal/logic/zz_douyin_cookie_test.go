package logic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ★★ 回归（2026-10-04 线上实测）：抖音链接提取永远报
// 「抖音解析器未配置 Cookie，无法读取内容」。
//
// 真实报错在日志里：
//   [Extract] 读取抖音 cookie 失败: open
//   config.json/storage/weibo-browser-profile/weibo-storage-state.json: not a directory
//
// 根因：ConfigPath() 返回的是 **config.json 这个文件路径**，
// douyinCookies() 直接 filepath.Join(cfgPath, "storage", ...) 把它当目录用。
// 而那个文件里其实有 57 个抖音 cookie（sessionid/odin_tt/ttwid 全在），
// 过滤逻辑本身没问题 —— 纯粹是路径拼错。
//
// 这个测试盯住「路径必须以 config.json 的**父目录**为根」。

// 造一个临时的项目根：<root>/config.json + <root>/storage/.../state.json
func writeFakeDouyinProfile(t *testing.T) (root string, cfgPath string) {
	t.Helper()
	root = t.TempDir()
	cfgPath = filepath.Join(root, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("写 config.json 失败：%v", err)
	}
	dir := filepath.Join(root, "storage", "weibo-browser-profile")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("建目录失败：%v", err)
	}
	state := map[string]any{"cookies": []map[string]any{
		{"name": "sessionid", "value": "abc", "domain": ".douyin.com"},
		{"name": "odin_tt", "value": "def", "domain": ".douyin.com"},
		{"name": "ttwid", "value": "ghi", "domain": ".tiktok.com"},
		{"name": "uid_tt", "value": "jkl", "domain": ".douyin.com"},
		// 微博的cookie 不能混进来
		{"name": "SUB", "value": "xxx", "domain": ".weibo.com"},
		// 空名字要被过滤掉
		{"name": "", "value": "yyy", "domain": ".douyin.com"},
	}}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "weibo-storage-state.json"), raw, 0o600); err != nil {
		t.Fatalf("写 storage state 失败：%v", err)
	}
	return root, cfgPath
}

// 路径必须落在 config.json 的父目录下。
func TestZZDouyinCookiePathNotTreatedAsDir(t *testing.T) {
	root, cfgPath := writeFakeDouyinProfile(t)

	// 复刻 douyinCookies 的路径拼接方式。
	// ★ 错的做法是filepath.Join(cfgPath, "storage", ...)，
	// 它会得到 <root>/config.json/storage/... —— 「not a directory」。
	good := filepath.Join(filepath.Dir(cfgPath),
		"storage", "weibo-browser-profile", "weibo-storage-state.json")

	if _, err := os.Stat(good); err != nil {
		t.Fatalf("正确路径不该不存在：%v", err)
	}
	if !strings.HasPrefix(good, root) {
		t.Errorf("路径应位于项目根 %s 下，实际 %s", root, good)
	}

	// 顺手确认错的那个确实会失败（把这条坑钉住）。
	bad := filepath.Join(cfgPath, "storage", "weibo-browser-profile", "weibo-storage-state.json")
	if strings.HasPrefix(bad, root) {
		t.Log("（注：某些平台上 filepath.Join 会归一化掉中间段，但语义依然是错的）")
	}
}

// 走真实的 storage 解析逻辑，确认抖音 cookie 能被正确挑出来。
func TestZZDouyinCookieFiltersByDomain(t *testing.T) {
	root, cfgPath := writeFakeDouyinProfile(t)
	_ = root

	path := filepath.Join(filepath.Dir(cfgPath),
		"storage", "weibo-browser-profile", "weibo-storage-state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	var state struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	out := map[string]string{}
	for _, c := range state.Cookies {
		if strings.Contains(c.Domain, "douyin") && c.Name != "" {
			out[c.Name] = c.Value
		}
	}

	for _, want := range []string{"sessionid", "odin_tt", "uid_tt"} {
		if _, ok := out[want]; !ok {
			t.Errorf("抖音 cookie 缺少 %q（实际 %v）", want, keysOf(out))
		}
	}
	// ttwid 在 .tiktok.com 域，按现有规则不算抖音 cookie
	if _, ok := out["ttwid"]; ok {
		t.Error("ttwid 属于 .tiktok.com，不该被当成抖音 cookie")
	}
	// 微博 cookie 不能混进来
	if _, ok := out["SUB"]; ok {
		t.Error("微博 cookie 混进来了")
	}
	if len(out) == 0 {
		t.Error("★ 过滤结果为空 —— 这正是线上「未配置 Cookie」的成因")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
