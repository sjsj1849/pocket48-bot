package extract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这个测试会打真实网络，只在显式设置 LIVE_EXTRACT 时跑。
// 目的：验证「解析器 -> 下载 -> 真实分辨率」这条链路真的通，
// 因为纯函数测试覆盖不到 CDN 的 Referer 要求这类问题。
func liveCookies(t *testing.T) map[string]string {
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
	t.Logf("cookie 数=%d", len(out))
	return out
}

func TestLiveWeiboResolve(t *testing.T) {
	if os.Getenv("LIVE_EXTRACT") == "" {
		t.Skip("设置 LIVE_EXTRACT=1 才跑")
	}
	cookie := ""
	if raw, err := os.ReadFile("/root/pocket48-bot/config.json"); err == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(raw, &cfg) == nil {
			cookie, _ = cfg["WEIBO_MWEIBO_COOKIE"].(string)
		}
	}
	r := &WeiboResolver{Cookie: cookie}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	post, err := r.Resolve(ctx, "https://weibo.com/6694957538/5350064312812042")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	b, _ := json.MarshalIndent(post, "", "  ")
	t.Logf("微博解析结果:\n%s", b)
	if len(post.Items) == 0 {
		t.Fatal("没有解析出任何多媒体")
	}
	for i, it := range post.Items {
		t.Logf("  item[%d] kind=%s url=%.100s", i, it.Kind, it.URL)
	}
}

func TestLiveDouyinResolve(t *testing.T) {
	if os.Getenv("LIVE_EXTRACT") == "" {
		t.Skip("设置 LIVE_EXTRACT=1 才跑")
	}
	wd, _ := os.Getwd()
	scriptDir := filepath.Join(filepath.Dir(filepath.Dir(wd)), "sidecar", "weibo-auth")
	if _, err := os.Stat(filepath.Join(scriptDir, "douyin-native-fetch.py")); err != nil {
		t.Skipf("找不到脚本目录 %s: %v", scriptDir, err)
	}

	r := &DouyinResolver{
		Cookies:   liveCookies(t),
		ScriptDir: scriptDir,
		Timeout:   120 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	for _, raw := range []string{
		"https://www.douyin.com/video/7675209447762131172",
		"https://www.douyin.com/video/7648670943941934582",
	} {
		post, err := r.Resolve(ctx, raw)
		if err != nil {
			t.Errorf("解析 %s 失败: %v", raw, err)
			continue
		}
		b, _ := json.MarshalIndent(post, "", "  ")
		t.Logf("抖音 %s ->\n%s", raw, b)
		if len(post.Items) == 0 {
			t.Errorf("%s 没有解析出多媒体", raw)
		}
	}
}
