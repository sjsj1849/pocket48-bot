package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/extract"
)

// TestLivePipelineBiliX 验证 B站 与 X 的「解析 → 下载」整条链路。
//
// 上一轮只验证了解析层，没验证下载。B站 DASH 合并、X 体积过滤、
// 抖音 Referer 都属于「解析成功但下载会失败」的问题，必须真跑一遍。
//
// 只在 LIVE_PIPELINE=1 时跑。
func TestLivePipelineBiliX(t *testing.T) {
	if os.Getenv("LIVE_PIPELINE") == "" {
		t.Skip("设置 LIVE_PIPELINE=1 才跑")
	}

	// B站 cookie 是生产必需项，测试也必须带上，否则解析器直接拒绝工作。
	cookie := ""
	if raw, err := os.ReadFile("/root/pocket48-bot/storage/bilibili/settings.json"); err == nil {
		var s map[string]interface{}
		if json.Unmarshal(raw, &s) == nil {
			cookie, _ = s["cookie"].(string)
		}
	}
	if strings.TrimSpace(cookie) == "" {
		t.Fatal("没读到 B站 cookie")
	}
	t.Logf("B站 cookie 长度=%d", len(cookie))

	client := extract.New(extract.Options{Timeout: 240 * time.Second},
		&extract.BilibiliResolver{Client: bilibiliExtractAdapter{
			dir:    "/root/pocket48-bot",
			cookie: cookie,
		}},
		&extract.XResolver{},
	)

	// X 用 bot.log 里真实出现过的链接（来自 nekomo_st 的采集记录）
	for _, raw := range []string{
		"https://www.bilibili.com/video/BV1UMYx6BEP8",
		"https://x.com/nekomo_st/status/2106311537233424570",
		"https://x.com/nekomo_st/status/2106015942895661108",
	} {
		label := "B站"
		if strings.Contains(raw, "bilibili") {
			label = "B站"
		} else {
			label = "X"
		}
		runOne(t, client, label, raw)
	}
}

// runOne 解析一条链接并真实下载其视频，报告体积与编码。
func runOne(t *testing.T, client *extract.Client, label, raw string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	post, err := client.Extract(ctx, raw)
	if err != nil {
		t.Errorf("[%s] 解析失败: %v", label, err)
		return
	}
	fmt.Printf("\n===== %s =====\n", label)
	fmt.Printf("  平台=%s 作者=%s\n", post.Platform, post.Author)
	text := post.Text
	if len(text) > 50 {
		text = text[:50]
	}
	fmt.Printf("  正文=%s\n", text)
	fmt.Printf("  多媒体 %d 项\n", len(post.Items))

	if len(post.Items) == 0 {
		t.Errorf("[%s] 没有解析出多媒体", label)
		return
	}

	for i, it := range post.Items {
		if it.Kind != "video" {
			fmt.Printf("    item[%d] kind=%s（非视频，跳过）\n", i, it.Kind)
			continue
		}
		local, err := resolveExtractMedia(it)
		if err != nil {
			t.Errorf("[%s] item[%d] 准备失败: %v", label, i, err)
			continue
		}
		info, statErr := os.Stat(local)
		if statErr != nil {
			t.Errorf("[%s] item[%d] 找不到下载文件: %v", label, i, statErr)
			continue
		}
		size := info.Size()
		verdict := "OK"
		if size < 50*1024 {
			verdict = "★ 疑似错误页"
			t.Errorf("[%s] item[%d] 只有 %d 字节，很可能不是视频而是错误页", label, i, size)
		}
		if size > 25<<20 {
			verdict += " ★ 超过 25MiB 预算"
			t.Errorf("[%s] item[%d] %.2fMB 超过 25MiB 预算", label, i, float64(size)/1048576.0)
		}
		if strings.HasPrefix(it.URL, "http") {
			fmt.Printf("    item[%d] 源=%s\n", i, localKind(it.URL))
		} else {
			fmt.Printf("    item[%d] 源=本地路径(B站DASH)\n", i)
		}
		codec := probeCodec(t, local)
		fmt.Printf("    item[%d] 下载成功 %.2fMB %s codec=%s\n",
			i, float64(size)/1048576.0, verdict, codec)
		os.Remove(local)
	}
}

func probeCodec(t *testing.T, path string) string {
	t.Helper()
	out, err := runCommand("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,codec_name", "-of", "csv=p=0", path)
	if err != nil {
		return "probe-failed"
	}
	return out
}

// TestLivePipelineDouyinWeibo 验证抖音与微博。
func TestLivePipelineDouyinWeibo(t *testing.T) {
	if os.Getenv("LIVE_PIPELINE") == "" {
		t.Skip("设置 LIVE_PIPELINE=1 才跑")
	}

	cookie := ""
	if raw, err := os.ReadFile("/root/pocket48-bot/config.json"); err == nil {
		var cfg map[string]interface{}
		if json.Unmarshal(raw, &cfg) == nil {
			cookie, _ = cfg["WEIBO_MWEIBO_COOKIE"].(string)
		}
	}

	client := extract.New(extract.Options{Timeout: 240 * time.Second},
		&extract.DouyinResolver{
			Cookies:   douyinCookiesForTest(t),
			ScriptDir: "/root/pocket48-bot/sidecar/weibo-auth",
			Timeout:   120 * time.Second,
		},
		&extract.WeiboResolver{Cookie: cookie},
	)

	runOne(t, client, "微博", "https://weibo.com/6694957538/5350064312812042")
	runOne(t, client, "抖音", "https://www.douyin.com/video/7675209447762131172")
}

// localKind 判断直链来自哪个 CDN，用于报告里区分。
func localKind(raw string) string {
	switch {
	case strings.Contains(raw, "douyinvod.com"):
		return "抖音CDN"
	case strings.Contains(raw, "weibocdn.com"):
		return "微博CDN"
	case strings.Contains(raw, "twimg.com"):
		return "X-CDN"
	default:
		return "其它CDN"
	}
}
