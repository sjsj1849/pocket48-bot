// Package mediafetch 统一处理从各平台 CDN 下载视频时需要的请求头。
//
// 为什么需要它：不同平台的 CDN 对 Referer 的要求差别很大，
// 漏设 Referer 会直接拿到 403，而错误信息里完全看不出是 Referer 的问题。
//
// 2026-10-04 实测结论：
//
//	抖音 *.douyinvod.com  → 必须 Referer: https://www.douyin.com/，否则 403
//	B站 *.bilivideo.com   → 需要 Referer: https://www.bilibili.com/
//	X  *.video.twimg.com  → 不需要
//	微博 *.weibocdn.com   → 不需要（但历史 URL 会因过期而 403，与 Referer 无关）
//
// ⚠ 只设 Referer，不要顺手加移动端 User-Agent。
// 实测抖音 CDN 加上完整 iPhone UA 后反而返回 0 字节，
// 它在校验 UA + Referer 的组合，Go 默认 UA 反而是被接受的那个。
package mediafetch

import (
	"net/http"
	"net/url"
	"strings"
)

// ApplyReferer 按域名给请求补上正确的 Referer。
//
// 匹配不上的域名不做任何改动，保持原有行为（零回归风险）。
//
// ⚠ 已经设过 Referer 时**不覆盖**：采集链路（B站 playurl.go）会按具体
// 稿件设更精确的 Referer，覆盖它等于把人家的防盗链措施弄失效。
func ApplyReferer(req *http.Request, rawURL string) {
	if req.Header.Get("Referer") != "" {
		return
	}
	ref := refererFor(rawURL)
	if ref == "" {
		return
	}
	req.Header.Set("Referer", ref)
}

// refererFor 返回该 URL 需要的 Referer，不需要时返回空串。
func refererFor(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Host == "" {
		return ""
	}
	host := strings.ToLower(parsed.Host)
	// 去掉端口
	if idx := strings.IndexByte(host, ':'); idx > 0 {
		host = host[:idx]
	}
	// ⚠ 必须用 hostMatches 而不是 strings.HasSuffix。
	// HasSuffix 不检查点边界，notdouyinvod.com 会以 douyinvod.com 结尾
	// 从而被误判（referer_test.go 里的防误匹配用例专门盯这个）。
	if matchesAny(host, "douyinvod.com", "douyinpic.com", "douyin.com", "iesdouyin.com") {
		return "https://www.douyin.com/"
	}
	if matchesAny(host, "bilivideo.com", "bilibili.com", "biliapi.net") {
		return "https://www.bilibili.com/"
	}
	return ""
}

// matchesAny 判断 host 是否等于 domain 或是其子域。
//
// 只认 "example.com" 与 "a.example.com"（按点分段），
// 不认 "notexample.com"。
func matchesAny(host string, domains ...string) bool {
	for _, domain := range domains {
		if host == domain {
			return true
		}
		if strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
