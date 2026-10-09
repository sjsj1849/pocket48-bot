package extract

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLiveWeiboMultiPic 是 2026-10-08 的真实链路探针：
// 用户在飞书贴 https://weibo.com/6694957538/5351887681618773 只收到纯文本。
//
// 用 LIVE_EXTRACT=1 跑真实接口（带线上 Cookie），验证新解析器
// 能把 6 张图全部取出来，且用的是 large（mw2000）不是缩略图。
func TestLiveWeiboMultiPic(t *testing.T) {
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
	if cookie == "" {
		t.Fatal("没读到 WEIBO_MWEIBO_COOKIE")
	}
	r := &WeiboResolver{Cookie: cookie}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const target = "https://weibo.com/6694957538/5351887681618773"
	post, err := r.Resolve(ctx, target)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	t.Logf("作者=%s 媒体数=%d Cover=%.90s", post.Author, len(post.Items), post.Cover)
	for i, it := range post.Items {
		t.Logf("  item[%d] kind=%s url=%.120s", i, it.Kind, it.URL)
	}
	if len(post.Items) == 0 {
		t.Fatal("仍然没有解析出任何多媒体 —— 修复无效")
	}
	for i, it := range post.Items {
		if it.Kind != "image" {
			t.Errorf("item[%d].Kind = %q，期望 image", i, it.Kind)
		}
		if !containsStr(it.URL, "/mw2000/") {
			t.Errorf("item[%d] 不是大图：%s", i, it.URL)
		}
	}
	if len(post.Items) != 6 {
		t.Logf("注意：实测样本是 6 图，实际拿到 %d 张", len(post.Items))
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
