package extract

import (
	"context"
	"regexp"
	"strings"

	"pocket48-bot/internal/instagram"
)

// InstagramResolver 解析 instagram.com 的单帖与 Reels 链接。
//
// 为什么必须走 sidecar 而不是直接 HTTP：Instagram 的网页版对未登录请求
// 只返回骨架，媒体直链根本不在 HTML 里；移动端 API 才带 video_versions。
// 采集侧已经用同一套登录态跑通了，这里复用它，不另造一条链路。
//
// 视频选档口径**不复用** logic 包的 pickBestMonitorVideo：那个按 25MiB
// 预算给 QQ 直链用，而提取侧要把视频**下载下来再上传**（飞书上限 30MB），
// 预算与降级策略都不同。两处各算各的，但都遵循「预算内取码率最高，
// 全都超预算取码率最低」这条同一条规则。
type InstagramResolver struct {
	// Client 是 sidecar 采集客户端，Dir 必须指向 storage/instagram。
	Client instagram.Client
	// MaxVideoBytes 是单条视频的体积上限（字节），0 表示用默认 30MB。
	MaxVideoBytes int64
	// Timeout 是单次解析超时，0 表示用默认 60s。
	Timeout int
}

const (
	// defaultInstagramMaxVideoBytes 与飞书文件上传上限对齐（30MB）。
	defaultInstagramMaxVideoBytes = 30 << 20
	// instagramVideoSeconds 估算体积用的兜底时长。
	instagramGuessSeconds = 30
)

// instagramPathRE 匹配 /p/<code>/ 与 /reel/<code>/ 与 /reels/<code>/。
//
// ★ 三种前缀都要收：Instagram 的 Reels 分享链接历史上用过
// /reel/ 与 /reels/ 两种拼写，只认一种会让另一类链接直接报「不支持」。
var instagramPathRE = regexp.MustCompile(`^/(p|reel|reels|tv)/([A-Za-z0-9_-]+)/?$`)

// Matches 实现分发用的主机+路径匹配。
func (r *InstagramResolver) Matches(target string) bool {
	host, path, ok := splitHostPath(target)
	if !ok {
		return false
	}
	if host != "instagram.com" && host != "instagr.am" {
		return false
	}
	return instagramPathRE.MatchString(path)
}

// splitHostPath 把 "instagram.com/p/abc/" 拆成 ("instagram.com", "/p/abc/")。
//
// 无路径时 path 返回 "/" 而不是空串：Matches 拿空串去匹配正则虽然也不通过，
// 但会让「域名相等」与「路径合法」两件事纠缠在一起，容易在后续改动里出错。
func splitHostPath(target string) (string, string, bool) {
	idx := strings.Index(target, "/")
	if idx < 0 {
		return strings.TrimPrefix(strings.ToLower(target), "www."), "/", true
	}
	host := strings.TrimPrefix(strings.ToLower(target[:idx]), "www.")
	path := target[idx:]
	if path == "" {
		path = "/"
	}
	return host, path, true
}

// Platform 声明归属平台。
func (r *InstagramResolver) Platform() Platform { return PlatformInstagram }

// Resolve 解析出一条完整内容。
func (r *InstagramResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	_, path, _ := splitHostPath(rawURL)
	match := instagramPathRE.FindStringSubmatch(path)
	if match == nil {
		return nil, ErrUnsupported
	}
	code := match[2]

	client := r.Client
	if client.Dir == "" {
		return nil, ErrUnsupported
	}
	resp, err := client.Call(ctx, map[string]any{
		"operation": "post_detail",
		"code":      code,
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Events) == 0 {
		return nil, ErrNotFound
	}
	event := resp.Events[0]

	post := &Post{
		Platform: PlatformInstagram,
		Author:   event.Author.Name,
		Text:     event.Body,
		URL:      event.URL,
	}
	if post.Author == "" {
		post.Author = event.Author.Username
	}

	budget := r.MaxVideoBytes
	if budget <= 0 {
		budget = defaultInstagramMaxVideoBytes
	}
	for _, m := range event.Media {
		switch m.Kind {
		case "video", "gif", "animated":
			url, ok := pickInstagramVideo(m, budget)
			if !ok {
				// 体积超限时仍给出封面，让用户至少能看到内容。
				if m.Cover != "" {
					post.Items = append(post.Items, Item{Kind: "image", URL: m.Cover})
				}
				continue
			}
			seconds := 0
			if m.DurationMS > 0 {
				seconds = int((m.DurationMS + 999) / 1000)
			}
			item := Item{Kind: "video", URL: url, Cover: m.Cover, Duration: seconds}
			item.Size = estimateInstagramVideoSize(m)
			if post.Cover == "" {
				post.Cover = m.Cover
			}
			post.Items = append(post.Items, item)
		default:
			if m.URL == "" {
				continue
			}
			post.Items = append(post.Items, Item{Kind: "image", URL: m.URL})
			if post.Cover == "" {
				post.Cover = m.URL
			}
		}
	}
	if post.Cover == "" && len(post.Items) > 0 {
		post.Cover = post.Items[0].URL
	}
	// 视频排在前面（Post 文档约定），发送侧会先发视频。
	post.Items = instagramVideoFirst(post.Items)
	return post, nil
}

// pickInstagramVideo 在体积预算内选最优档。
//
// 规则与采集侧一致：预算内取码率最高；全都超预算时取码率最低的一档，
// 宁可糊也不能让用户什么都收不到。
func pickInstagramVideo(m instagram.Media, budget int64) (string, bool) {
	seconds := instagramGuessSeconds
	if m.DurationMS > 0 {
		seconds = int((m.DurationMS + 999) / 1000)
	}
	if seconds < 1 {
		seconds = 1
	}
	bestIdx, fallbackIdx := -1, -1
	for i, v := range m.Variants {
		if strings.TrimSpace(v.URL) == "" {
			continue
		}
		if fallbackIdx < 0 || v.Bitrate < m.Variants[fallbackIdx].Bitrate {
			fallbackIdx = i
		}
		if v.Bitrate <= 0 {
			continue
		}
		if v.Bitrate*int64(seconds)/8 > budget {
			continue
		}
		if bestIdx < 0 || v.Bitrate > m.Variants[bestIdx].Bitrate {
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return strings.TrimSpace(m.Variants[bestIdx].URL), true
	}
	if fallbackIdx >= 0 {
		return strings.TrimSpace(m.Variants[fallbackIdx].URL), true
	}
	return "", false
}

// estimateInstagramVideoSize 按选中档估算体积，0 表示无法估算。
func estimateInstagramVideoSize(m instagram.Media) int64 {
	seconds := instagramGuessSeconds
	if m.DurationMS > 0 {
		seconds = int((m.DurationMS + 999) / 1000)
	}
	if seconds < 1 {
		seconds = 1
	}
	best := int64(0)
	for _, v := range m.Variants {
		if v.Bitrate > best {
			best = v.Bitrate
		}
	}
	if best <= 0 {
		return 0
	}
	return best * int64(seconds) / 8
}

// instagramVideoFirst 稳定地把视频项排到最前，其余保持原顺序。
func instagramVideoFirst(items []Item) []Item {
	if len(items) < 2 {
		return items
	}
	hasVideo := false
	for _, it := range items {
		if it.Kind == "video" {
			hasVideo = true
			break
		}
	}
	if !hasVideo {
		return items
	}
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Kind == "video" {
			out = append(out, it)
		}
	}
	for _, it := range items {
		if it.Kind != "video" {
			out = append(out, it)
		}
	}
	return out
}
