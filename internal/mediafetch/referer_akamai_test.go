package mediafetch

import (
	"net/http"
	"net/url"
	"testing"
)

// 回归：2026-10-09 B站视频在飞书下载 403。
//
// B站 playurl 返回的 CDN 域名不只有 *.bilivideo.com，还有 akam 镜像
// upos-hz-mirrorakam.akamaized.net —— 原先不在 refererFor 的表里，
// 于是请求不带 Referer，被 CDN 直接 403。飞书只收到卡片、没有视频。
//
// 实测（bvid=BV1Uypx6mEqN，同一条 2464372 字节的视频）：无 Referer → HTTP 403；
// Referer=www.bilibili.com → HTTP 200。
func TestRefererForBilibiliAkamaiMirror(t *testing.T) {
	cases := []string{
		"https://upos-hz-mirrorakam.akamaized.net/upgcxcode/09/83/42582608309/42582608309-1-16.mp4?e=xxx",
		"https://upos-sz-mirrorakam.akamaized.net/upgcxcode/xx/yy/zz.mp4",
		"https://xy123.akamaized.net/a/b/c.mp4",
	}
	for _, raw := range cases {
		if got := refererFor(raw); got != "https://www.bilibili.com/" {
			t.Errorf("refererFor(%s) = %q，期望 B站 Referer", raw, got)
		}
	}
	// 老的域名表不能被破坏。
	for _, raw := range []string{
		"https://upos-sz-mirrorcos.bilivideo.com/upgcxcode/a/b/c.m4s",
		"https://api.bilibili.com/x/web-interface/view",
	} {
		if got := refererFor(raw); got != "https://www.bilibili.com/" {
			t.Errorf("refererFor(%s) = %q，B站老域名应保持", raw, got)
		}
	}
	// B站图片 CDN（hdslb.com）本来就不设 Referer ——
	// 封面图一直是正常下载的，别把它误加进来。
	if got := refererFor("https://i0.hdslb.com/bfs/archive/x.jpg"); got != "" {
		t.Errorf("图片 CDN 不该被加 Referer，实际 %q", got)
	}
}

// akamaized.net 是 Akamai 通用域名，不能因此给所有站点都塞 B站 Referer ——
// matchesAny 的点边界必须生效。
func TestRefererForAkamaiDoesNotOverMatch(t *testing.T) {
	for _, raw := range []string{
		"https://notakamaized.net/a.mp4",
		"https://akamaized.net.evil.com/a.mp4",
	} {
		if got := refererFor(raw); got != "" {
			t.Errorf("refererFor(%s) = %q，不该匹配", raw, got)
		}
	}
}

// 抖音 / 微博规则不受影响。
func TestRefererForOtherPlatformsUnchanged(t *testing.T) {
	if got := refererFor("https://v3.douyinvod.com/abc/video.mp4"); got != "https://www.douyin.com/" {
		t.Errorf("抖音 Referer = %q", got)
	}
	if got := refererFor("https://wx1.sinaimg.cn/mw2000/a.jpg"); got != "" {
		t.Errorf("微博不需要 Referer，实际 %q", got)
	}
}

// ApplyReferer 仍不能覆盖采集链路已设的 Referer。
func TestApplyRefererDoesNotOverride(t *testing.T) {
	u, _ := url.Parse("https://upos-hz-mirrorakam.akamaized.net/a.mp4")
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Referer", "https://www.bilibili.com/video/BV1Uypx6mEqN")
	ApplyReferer(req, u.String())
	if got := req.Header.Get("Referer"); got != "https://www.bilibili.com/video/BV1Uypx6mEqN" {
		t.Errorf("已有 Referer 被覆盖成 %q", got)
	}

	req2, _ := http.NewRequest("GET", u.String(), nil)
	ApplyReferer(req2, u.String())
	if got := req2.Header.Get("Referer"); got != "https://www.bilibili.com/" {
		t.Errorf("未设时应补上 B站 Referer，实际 %q", got)
	}
}
