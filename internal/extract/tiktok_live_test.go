package extract

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestZZTiktokExtractLive 真实端到端探针：拿一条真实的 TikTok 作品链接，
// 跑完整的解析 → 下载链路。
//
// ★ 为什么必须实测（记忆里的教训 #1）：
// 「代码看起来已修复」≠ 线上生效，纯函数测试 ≠ 覆盖真实链路。
// 这里的每一步都依赖真实浏览器、真实 TikTok 页面、真实 CDN 响应，
// 任何一步变了单测都测不出来。跑法：
//
//	LIVE_PIPELINE=1 go test ./internal/extract/ -run TestZZTiktokExtractLive -v -timeout 8m
func TestZZTiktokExtractLive(t *testing.T) {
	if os.Getenv("LIVE_PIPELINE") == "" {
		t.Skip("需要 LIVE_PIPELINE=1")
	}
	// 心连心真实作品（2026-10-04 实测存在的 ID）
	const url = "https://www.tiktok.com/@hearts2hearts/video/7692716586116992257"

	client := New(Options{Timeout: 4 * time.Minute}, &TiktokResolver{
		Dir:     "/root/pocket48-bot/storage/tiktok",
		Timeout: 4 * time.Minute,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	post, err := client.Extract(ctx, url)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("提取失败（耗时 %s）：%v", elapsed.Round(time.Second), err)
	}

	t.Logf("耗时       = %s", elapsed.Round(time.Second))
	t.Logf("平台       = %s", post.Platform)
	t.Logf("作者       = %q", post.Author)
	t.Logf("正文       = %q", post.Text)
	t.Logf("封面       = %s", post.Cover)
	t.Logf("原链接     = %s", post.URL)
	t.Logf("媒体数     = %d", len(post.Items))

	if post.Author == "" {
		t.Error("作者为空 —— 面板与推送里会出现「TikTok ·」这种残缺署名")
	}
	if post.Text == "" {
		t.Error("正文为空 —— 用户发链接过来只想看文案")
	}
	if post.Cover == "" {
		t.Error("封面为空 —— 输出形态是正文+封面+视频")
	}
	if len(post.Items) == 0 {
		t.Log("★ 媒体为空：视频没下到（正文封面仍可用，这是设计上的降级）")
		return
	}

	item := post.Items[0]
	t.Logf("媒体 kind  = %s", item.Kind)
	t.Logf("媒体 URL   = %s", item.URL)
	t.Logf("媒体 时长  = %d 秒", item.Duration)
	t.Logf("媒体 分辨率 = %dx%d", item.Width, item.Height)
	t.Logf("媒体 体积  = %d 字节", item.Size)

	// 关键：URL 必须是**本地路径**（sidecar 下载到 storage/tiktok/videos/），
	// 而不是 CDN 直链 —— 外部客户端取 TikTok CDN 一律 403。
	if item.Kind == "video" && strings.HasPrefix(item.URL, "http") {
		t.Errorf("视频 URL 不是本地路径：%s（TikTok CDN 拒绝外部客户端，直链必然失败）", item.URL)
	}
	if item.Size <= 0 {
		t.Error("视频体积为 0，无法判断是否真的下到了内容")
	}
}
