package extract

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"pocket48-bot/internal/tiktokmonitor"
)

// TiktokResolver 通过项目已有的 TikTok 采集通道提取单条作品。
//
// 为什么必须复用 sidecar 而不是自己写 HTTP 请求（2026-10-04 实测）：
//
//  1. **CDN 拒绝一切外部客户端**。TikTok 视频地址带签名，外部 curl
//     （无论加什么 Referer/UA）一律 403；而**页面内**
//     `fetch(url, {credentials:'include'})` 返回 200。根因不是缺登录态，
//     而是客户端指纹校验 —— 这条对抖音也成立，只是抖音另有 Argus 签名问题。
//  2. **元数据同样拿不到**。列表接口 item_list 有 UA 硬要求（Android Chrome
//     + is_mobile），桌面 UA 返回 0 字节；作品页 HTML 又是异步填充的，
//     __UNIVERSAL_DATA_FOR_REHYDRATION__ 存在但常为空。
//
// 所以正确做法是复用 sidecar/tiktok-monitor/collector.py 的 detail 操作：
// 它一次会话里打开作品页、拿到正文/封面、并把视频下到本地。
//
// 顺带的好处：sidecar 返回的是**本地路径**，与 B站 DASH 一样属于
// resolveExtractMedia 的「情形 2」，不需要在这里处理直链下载。
type TiktokResolver struct {
	// Dir 是 storage/tiktok 目录（sidecar 的 storageDir）。
	Dir string
	// Timeout 限制单次取数时长，0 时用 180 秒。
	Timeout time.Duration
}

const defaultTiktokTimeout = 180 * time.Second

// tiktokHosts 是 TikTok 的合法域名。
//
// ★ 为什么必须逐个列举而不能用裸后缀（实测被测试抓到过）：
// 用 `strings.Contains(host, "tiktok.com")` 会让 nottiktok.com、
// faketiktok.com、tiktok.com.evil.com 全部误命中 —— 这与记忆里
// 抖音 CDN 那条（notdouyinvod.com 匹配 douyinvod.com）是同一类错误。
// 判定必须是 host==d || HasSuffix(host, "."+d)。
var tiktokHosts = []string{
	"tiktok.com",       // 主域（含 www./m. 前缀由后缀规则覆盖）
	"vm.tiktok.com",    // 短链
	"vt.tiktok.com",    // 短链变体
	"vm.tiktokcdn.com", // 短链跳转偶见
}

// Matches 判断 host+path 是否属于 TikTok。
//
// 入参是 Detect 拼好的 host+path（Detect 已去掉 www. 前缀），
// 这里取斜杠前的主机名做严格匹配。
func (r *TiktokResolver) Matches(hostPath string) bool {
	lower := strings.ToLower(hostPath)
	host := lower
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}

	matched := false
	for _, d := range tiktokHosts {
		if host == d || strings.HasSuffix(host, "."+d) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}

	// 短链（vm./vt.）路径里没有 /video/，先放行，由 Resolve 跟随跳转后再判断。
	if host == "vm.tiktok.com" || host == "vt.tiktok.com" ||
		strings.HasPrefix(host, "vm.") || strings.HasPrefix(host, "vt.") {
		return true
	}

	// 作品链接必须带 /video/，否则主页链接会走进 Resolve 却取不到 ID。
	return strings.Contains(lower, "/video/")
}

func (r *TiktokResolver) Platform() Platform { return PlatformTiktok }

// tiktokVideoID 匹配作品 ID（19 位数字）。
//
// 为什么不用固定的 19 位：TikTok 的 ID 是雪花算法生成的，长期会变长。
// 这里放宽成「至少 15 位纯数字」，宁可宽松匹配也不能漏。
var tiktokVideoID = regexp.MustCompile(`\b(\d{15,25})\b`)

// cleanTiktokDesc 清洗 TikTok 的 OG description。
//
// ★ 为什么必须清洗（实测踩坑，2026-10-04）：
// TikTok 的 og:description 不是纯文案，而是一整段带统计数字与
// 引号包裹的宣传语，实测长这样：
//
//	171.4K 获赞，1291 评论。来自 Hearts2Hearts (@hearts2hearts) 的 TikTok 视频：
//	"ダンスしよう！ @あの  #Hearts2Hearts #하츠투하츠 #ICONICHEART ..."。
//	ICONIC HEART - Hearts2Hearts。
//
// 直接当正文发出去又长又乱，还带一堆用户没要求的统计数字。
// 这里把「N 获赞，N 评论/分享/收藏。」的前缀去掉，并把尾部
// 「曲名 - 作者名。」的广告尾巴也去掉，只留真正的文案。
var tiktokDescStats = regexp.MustCompile(
	`^[\d\.,]+\s*(?:K|M|W)?\s*(?:likes?|获赞)[，,]?\s*` +
		`(?:[\d\.,]+\s*(?:K|M|W)?\s*(?:comments?|评论)[，,]?\s*)?` +
		`(?:[\d\.,]+\s*(?:K|M|W)?\s*(?:shares?|分享)[，,]?\s*)?`)

// ★ 昵称部分用 .+? 而不是 \S+：TikTok 昵称可能带空格，
//
//	用 \S+ 会在这类昵称上失配（曾实测过带空格的名字）。
var tiktokDescIntro = regexp.MustCompile(
	`^来自\s+.+?\s*\(@[^)]+\)\s*的\s*TikTok\s*视频[：:]\s*`)

var tiktokDescTail = regexp.MustCompile(`。\s*[^。]{1,60}\s*-\s*[^。]{1,40}。?\s*$`)

// cleanTiktokDesc 把 OG description 还原成用户真正写的文案。
func cleanTiktokDesc(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}

	text = tiktokDescStats.ReplaceAllString(text, "")
	// ★ 统计前缀后面那个句号必须一并吃掉。
	// 实测踩坑：真实文本是「171.4K 获赞，1291 评论。**。**来自 X (@y) 的 TikTok 视频：」
	// —— 注意是「评论**。**」外加另一个「。」，统计正则只吃掉到数字为止，
	// 剩下「。来自...」，而 Intro 正则用 ^ 锚定「来自」，于是失配、
	// 整段宣传语原样留在输出里。去掉残留的首个句号再匹配。
	text = strings.TrimLeft(text, "。. 	")
	text = tiktokDescIntro.ReplaceAllString(text, "")

	// 去掉 TikTok 自动加的引号包裹（中文弯引号与英文直引号都要）。
	text = strings.Trim(text, "\u201c\u201d\u2018\u2019\"")
	text = strings.TrimSpace(text)

	// 尾部「曲名 - 作者名。」的广告尾巴。
	if m := tiktokDescTail.FindString(text); len(m) > 0 && len(m) < len(text) {
		text = strings.TrimSpace(text[:len(text)-len(m)])
	}

	return strings.TrimSpace(text)
}

// tiktokUserFromPath 从路径里取用户名。
//
// 典型分享链接：https://www.tiktok.com/@hearts2hearts/video/7688182139010977025
// 短链跟随后也是这个形态。取不到就返回空串，由上层回落到默认账号。
var tiktokUserFromPath = regexp.MustCompile(`/@([A-Za-z0-9_.]+)`)

func (r *TiktokResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	videoID := tiktokVideoID.FindString(rawURL)
	if videoID == "" {
		return nil, fmt.Errorf("没能从链接里认出 TikTok 作品 ID：%s", TrimUserinfo(rawURL))
	}
	username := ""
	if m := tiktokUserFromPath.FindStringSubmatch(rawURL); len(m) > 1 {
		username = m[1]
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = defaultTiktokTimeout
	}
	// 解析器的整体超时由 Client.Extract 施加，这里再兜一层，
	// 避免调用方绕过 Extract 时把 sidecar 挂住。
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := tiktokmonitor.Client{Dir: r.Dir}
	detail, err := client.Detail(ctx, videoID, username, true)
	if err != nil {
		return nil, err
	}

	// 清洗 OG description，但清洗到空时保留原文：
	// 宁可让用户看到一整段带统计数字的原始文案，也不要凭空少一段。
	text := cleanTiktokDesc(detail.Desc)
	if text == "" {
		text = strings.TrimSpace(detail.Desc)
	}

	post := &Post{
		Platform: PlatformTiktok,
		Author:   strings.TrimSpace(detail.Author),
		Text:     text,
		URL:      firstNonEmpty(detail.URL, rawURL),
		Cover:    strings.TrimSpace(detail.Cover),
	}
	if post.Author == "" {
		// 昵称没取到时至少让用户知道是谁的，别留空。
		post.Author = firstNonEmpty(detail.AuthorID, username)
	}

	// 视频本体。下载失败时仍把正文与封面发出去 —— 用户至少拿到了内容，
	// 而不是一句「提取失败」。这一点与 TikTok CDN 的硬限制有关：
	// 它拒绝外部客户端，偶发失败只能靠重试。
	if path := detail.LocalPath(); path != "" {
		post.Items = append(post.Items, Item{
			Kind:     "video",
			URL:      path, // 本地路径，由 resolveExtractMedia 直接使用
			Cover:    post.Cover,
			Duration: int(detail.Duration),
			Width:    detail.Width,
			Height:   detail.Height,
			Size:     detail.Bytes(),
		})
	}
	return post, nil
}
