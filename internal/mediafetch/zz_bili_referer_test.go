package mediafetch

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestLiveBilibiliAkamaiRefererProbe 2026-10-09 实测：
// B站视频在飞书侧下载返回 HTTP 403，URL 域名是
// upos-hz-mirrorakam.akamaized.net —— 不在 refererFor 的域名表里，
// 于是没有 Referer，直接被 CDN 拒绝。
//
// 这个探针取一条新鲜的 B站直链，对比「带 Referer / 不带」两种请求。
func TestLiveBilibiliAkamaiRefererProbe(t *testing.T) {
	if os.Getenv("LIVE_BILI") == "" {
		t.Skip("设置 LIVE_BILI=1 才跑")
	}
	raw := os.Getenv("BILI_VIDEO_URL")
	if raw == "" {
		t.Skip("需要 BILI_VIDEO_URL=<新鲜直链>")
	}

	client := &http.Client{Timeout: 25 * time.Second}
	cases := []struct {
		name    string
		referer string
		ua      string
	}{
		{"原样(无Referer)", "", ""},
		{"Referer=bilibili", "https://www.bilibili.com/", ""},
		{"Referer=bilibili+移动UA", "https://www.bilibili.com/",
			"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"},
		{"Referer=bilibili视频页", "https://www.bilibili.com/video/BV1Uypx6mEqN", ""},
	}
	for _, c := range cases {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		if c.ua != "" {
			req.Header.Set("User-Agent", c.ua)
		}
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Logf("%-26s ERR %v", c.name, err)
			continue
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		t.Logf("%-26s HTTP %d  读到的字节=%d  耗时=%v", c.name, resp.StatusCode, n, time.Since(start).Round(time.Millisecond))
	}
}
