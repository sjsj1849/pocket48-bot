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
	// ★ 2026-10-09 实测补：B站 CDN 会返回 akam 镜像域名，
	// 不在上面的表里 ⇒ 没设 Referer ⇒ 直接 403。
	//
	//	症状：飞书收到卡片但没有视频，日志只有一行
	//	      [Outbound:feishu] status=media_failed type=video error=media download HTTP 403
	//	      QQ 侧同时 Delivery failed。封面图正常（i1.hdslb.com 走的是另一条路）。
	//	实测样本 bvid=BV1Uypx6mEqN：
	//	    无 Referer                → HTTP 403 Forbidden
	//	    Referer=www.bilibili.com → HTTP 200, 2464372 字节
	//	    Referer=视频页           → HTTP 200, 2464372 字节
	//
	// akamaized.net 是 Akamai 的通用 CDN 域名，别的平台也可能用它，
	// 所以这里必须与 B站域名表合并判断，不能单独放行。
	if matchesAny(host, "akamaized.net", "akamaized.net.edgesuite.net", "akamaized.net.akadns.net") {
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
