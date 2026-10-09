package extract

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// BilibiliResolver 通过 B 站公开接口提取投稿内容。
//
// 复用 internal/bilibili 的 Client（那边已实现 WBI 签名与风控退避），
// 不重复造轮子。关键是 view 接口必须带 Cookie —— 机房 IP 匿名请求会拿到
// 412 反爬 HTML 页面而非 JSON。
type BilibiliResolver struct {
	// Client 是 *bilibili.Client 的最小接口，便于测试替换。
	Client BilibiliClient
}

// BilibiliClient 是本解析器依赖的接口（由 *bilibili.Client 实现）。
type BilibiliClient interface {
	VideoView(ctx context.Context, bvid string) (BilibiliView, error)
	VideoPlayURL(ctx context.Context, bvid string) (BilibiliPlay, error)
}

// BilibiliView 是稿件元信息。
type BilibiliView struct {
	BVID     string
	Title    string
	Owner    string
	Duration int
	CID      int64
}

// BilibiliPlay 是可下载的视频源。
type BilibiliPlay struct {
	// URL 是 CDN 直链，降级路径下使用。
	URL string
	// LocalPath 是已在服务器上合并好的 mp4（DASH 高画质路径）。
	// 非空时应优先用它，不必再下载。
	LocalPath string
	Seconds   int
	Size      int64
	// Width/Height 是分辨率，0 表示未知。
	Width, Height int
}

// Matches 判断 host+path 是否属于 B 站。
func (r *BilibiliResolver) Matches(hostPath string) bool {
	lower := strings.ToLower(hostPath)
	for _, marker := range []string{"bilibili.com", "b23.tv", "bili2233.cn"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (r *BilibiliResolver) Platform() Platform { return PlatformBilibili }

// Resolve 解析 B 站链接。
func (r *BilibiliResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	bvid := extractBVID(rawURL)
	if bvid == "" {
		return nil, fmt.Errorf("没能从链接里认出 BV 号：%s", rawURL)
	}
	if r.Client == nil {
		return nil, fmt.Errorf("B 站解析器未配置 Cookie，无法取稿件信息")
	}

	view, err := r.Client.VideoView(ctx, bvid)
	if err != nil {
		return nil, fmt.Errorf("读取稿件信息失败：%w", err)
	}

	post := &Post{
		Platform: PlatformBilibili,
		Author:   firstNonEmpty(view.Owner, "B 站 UP 主"),
		Text:     strings.TrimSpace(view.Title),
		Cover:    bilibiliCoverURL(bvid),
	}
	if view.Duration > 0 {
		post.Text = fmt.Sprintf("%s\n时长 %s", post.Text, FormatSeconds(view.Duration))
	}

	play, err := r.Client.VideoPlayURL(ctx, bvid)
	if err != nil {
		// 取不到视频不该让整条失败：封面和链接仍然有用。
		// 长视频、付费视频都会走到这里。
		return post, nil
	}
	// DASH 高画质路径会返回已合并好的本地文件，直接复用，不必再下载。
	if play.LocalPath != "" {
		post.Items = append(post.Items, Item{
			Kind:     "video",
			URL:      play.LocalPath,
			Cover:    post.Cover,
			Duration: firstPositive(play.Seconds, view.Duration),
			Width:    play.Width,
			Height:   play.Height,
			Size:     play.Size,
		})
	} else if strings.HasPrefix(play.URL, "http") {
		post.Items = append(post.Items, Item{
			Kind:     "video",
			URL:      play.URL,
			Cover:    post.Cover,
			Duration: firstPositive(play.Seconds, view.Duration),
			Width:    0,
			Height:   0,
			Size:     play.Size,
		})
	}
	return post, nil
}

// extractBVID 从各种形态的 B 站链接里抽出 BV 号。
func extractBVID(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if m := bvPattern.FindString(rawURL); m != "" {
		return m
	}
	// av 号形式：/video/av12345
	if strings.Contains(rawURL, "/av") {
		if idx := strings.Index(rawURL, "/av"); idx >= 0 {
			rest := rawURL[idx+3:]
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return "av" + rest[:end]
			}
		}
	}
	return ""
}

// bilibiliCoverURL 拼出封面图直链。pic 由 view 接口给出，
// 这里退回官方静态规则（HASH_2/1/0.webp），避免再发一次请求。
func bilibiliCoverURL(bvid string) string {
	return "https://i0.hdslb.com/bvid/" + strings.TrimPrefix(bvid, "av") + ".jpg"
}

// FormatSeconds 把秒数格式化成 mm:ss / hh:mm:ss。
func FormatSeconds(total int) string {
	if total <= 0 {
		return ""
	}
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// TrimUserinfo 去掉 URL 里的 user:pass@ 前缀（分享链接偶尔带），
// 避免凭据混进日志。
func TrimUserinfo(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User == nil {
		return rawURL
	}
	u.User = nil
	return u.String()
}
