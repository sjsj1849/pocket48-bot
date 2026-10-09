package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// XResolver 通过公开的第三方接口提取推文内容（含视频与图片）。
//
// 为什么用第三方：X 官方 API 需要付费且要企业认证，而 fxtwitter 这类
// 服务已经把公开推文解析好了，实测在机房 IP 上稳定返回 code=200。
// 换来的是「无需登录态、不会碰账号风控」，这对一个自用机器人是划算的。
//
// 风险与对策：第三方服务可能挂。解析失败时返回可读错误，让用户知道
// 是「X 那边暂时取不到」而不是「机器人坏了」。
type XResolver struct {
	// Endpoint 是 JSON API 模板，%s 会被替换成推文 ID。
	Endpoint string
	// MaxVideoBytes 是单个视频的体积上限，0 表示用默认值。
	MaxVideoBytes int64
	// Client 复用注入的 HTTP 客户端。
	Client *http.Client
}

const defaultXEndpoint = "https://api.fxtwitter.com/status/%s"

// 飞书文件上传上限是 30MB，留 5MB 余量。
const defaultXVideoMaxBytes = 25 << 20

func (r *XResolver) Platform() Platform { return PlatformX }

// Matches 判断 host+path 是否属于 X / Twitter。
func (r *XResolver) Matches(hostPath string) bool {
	lower := strings.ToLower(hostPath)
	for _, marker := range []string{"x.com", "twitter.com", "t.co", "fxtwitter.com", "vxtwitter.com", "fixupx.com"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (r *XResolver) maxVideoBytes() int64 {
	if r.MaxVideoBytes > 0 {
		return r.MaxVideoBytes
	}
	return defaultXVideoMaxBytes
}

// xAPIResponse 是 fxtwitter 的返回结构（只声明用到的字段）。
type xAPIResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Tweet   struct {
		URL    string `json:"url"`
		Text   string `json:"text"`
		Author struct {
			Name       string `json:"name"`
			ScreenName string `json:"screen_name"`
		} `json:"author"`
		CreatedAt string `json:"created_at"`
		Media     struct {
			All    []xMedia `json:"all"`
			Videos []xMedia `json:"videos"`
			Photos []xMedia `json:"photos"`
		} `json:"media"`
	} `json:"tweet"`
}

// xMedia 对应一条媒体项。
//
// ★★★ 字段类型必须与实际 JSON 严格对齐，否则 json.Unmarshal 会对
// 整个响应失败（不是「某个字段为 0」，而是整体报错）。踩过的坑：
//   - duration 是浮点数（实测 10.758），声明成 int 必炸
//   - formats[].bitrate 是数字（632000），声明成 string 必炸
//   - formats[] 里区分音视频的字段叫 container（"mp4"/"m3u8"），
//     不是 format；format 字段在媒体顶层且值是 MIME（"video/mp4"）
type xMedia struct {
	URL          string    `json:"url"`
	ThumbnailURL string    `json:"thumbnail_url"`
	Type         string    `json:"type"`
	Format       string    `json:"format"`
	Duration     float64   `json:"duration"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Formats      []xFormat `json:"formats"`
}

// xFormat 是一档画质。
type xFormat struct {
	URL       string `json:"url"`
	Container string `json:"container"`
	Codec     string `json:"codec"`
	Bitrate   int    `json:"bitrate"`
}

// Resolve 解析 X 链接。
func (r *XResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	id := extractTweetID(rawURL)
	if id == "" {
		return nil, fmt.Errorf("没能从链接里认出推文 ID：%s", rawURL)
	}
	endpoint := r.Endpoint
	if strings.TrimSpace(endpoint) == "" {
		endpoint = defaultXEndpoint
	}
	endpoint = strings.Replace(endpoint, "%s", id, 1)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json")

	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问 X 解析服务失败（可能是网络或对方暂时不可用）：%w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}
	var api xAPIResponse
	if err := json.Unmarshal(body, &api); err != nil {
		// 把真实错误带出来，对方改字段类型时能一眼看出是哪一维出错。
		return nil, fmt.Errorf("X 解析服务返回了无法解析的数据（对方可能改接口了）：%v", err)
	}
	// fxtwitter 用 HTTP 200 + body 里的 code 表示业务错误。
	if api.Code != 0 && api.Code != 200 {
		msg := strings.TrimSpace(api.Message)
		if msg == "" {
			msg = "未知错误"
		}
		return nil, fmt.Errorf("X 那边返回：%s（%d）", msg, api.Code)
	}

	tw := api.Tweet
	post := &Post{
		Platform: PlatformX,
		Author:   firstNonEmpty(tw.Author.Name, tw.Author.ScreenName),
		Text:     strings.TrimSpace(tw.Text),
		URL:      firstNonEmpty(tw.URL, rawURL),
	}

	budget := r.maxVideoBytes()
	// 视频优先。
	for _, m := range tw.Media.Videos {
		u, size := pickXVideoURL(m, budget)
		if u == "" {
			continue
		}
		post.Items = append(post.Items, Item{
			Kind:     "video",
			URL:      u,
			Cover:    m.ThumbnailURL,
			Duration: secondsFromFloat(m.Duration),
			Width:    m.Width,
			Height:   m.Height,
			Size:     size,
		})
		if post.Cover == "" {
			post.Cover = m.ThumbnailURL
		}
	}
	// 图片。
	for _, m := range tw.Media.Photos {
		if u := normalizeXPhotoURL(m.URL); u != "" {
			post.Items = append(post.Items, Item{
				Kind: "image",
				URL:  u,
				Size: sizeFromURL(u, m.Width, m.Height),
			})
			if post.Cover == "" {
				post.Cover = u
			}
		}
	}
	// 有些推文只在 all 里有媒体（videos/photos 均为空），兜底扫一遍。
	if len(post.Items) == 0 {
		for _, m := range tw.Media.All {
			kind := strings.ToLower(strings.TrimSpace(m.Type))
			if kind != "" && strings.Contains(kind, "video") {
				u, size := pickXVideoURL(m, budget)
				if u == "" {
					continue
				}
				post.Items = append(post.Items, Item{
					Kind: "video", URL: u, Cover: m.ThumbnailURL,
					Duration: secondsFromFloat(m.Duration),
					Width:    m.Width, Height: m.Height, Size: size,
				})
				if post.Cover == "" {
					post.Cover = m.ThumbnailURL
				}
				continue
			}
			if u := normalizeXPhotoURL(m.URL); u != "" {
				post.Items = append(post.Items, Item{
					Kind: "image",
					URL:  u,
					Size: sizeFromURL(u, m.Width, m.Height),
				})
				if post.Cover == "" {
					post.Cover = u
				}
			}
		}
	}
	return post, nil
}

// pickXVideoURL 在 formats 里挑一个「体积在预算内、画质最高」的 MP4 直链。
//
// 为什么不能无脑挑最高画质：实测同一条推文有 6 档，最高是 34.19MB，
// 而飞书文件上传上限 30MB —— 无脑挑最大必然发送失败。
//
// 为什么不能只按分辨率挑：最高档经常超限，次高档才是正解。
// 所以这里按「体积过滤 → 分辨率取最大」两步走。
// 体积用 bitrate × duration ÷ 8 估算，实测误差 3%~25% 且偏大，
// 宁可高估（少发一档画质）也不要低估（发送时炸掉）。
//
// 返回空串表示没有可用视频，此时上层会退化成「只发正文+封面」。
func pickXVideoURL(m xMedia, maxBytes int64) (string, int64) {
	type candidate struct {
		url    string
		pixels int
		bytes  int64
	}
	var cands []candidate
	for _, f := range m.Formats {
		u := strings.TrimSpace(f.URL)
		if !strings.HasPrefix(u, "http") {
			continue
		}
		lower := strings.ToLower(u)
		// m3u8 是流媒体，下载器不认。
		if strings.Contains(lower, ".m3u8") || strings.Contains(lower, "/manifest") {
			continue
		}
		// container 才是区分 mp4/m3u8 的字段；为空时靠上面的 .m3u8 兜底。
		if f.Container != "" && !strings.Contains(strings.ToLower(f.Container), "mp4") {
			continue
		}
		cands = append(cands, candidate{
			url:    u,
			pixels: parseXResolution(lower),
			bytes:  estimateXBytes(f.Bitrate, m.Duration),
		})
	}
	if len(cands) == 0 {
		// 没有 formats 时退回顶层 url。它是原始画质直链，可能超限，
		// 这种情况交给上层按体积决定发不发。
		if strings.HasPrefix(m.URL, "http") && !strings.Contains(strings.ToLower(m.URL), ".m3u8") {
			return m.URL, 0
		}
		return "", 0
	}
	best := -1
	for i, c := range cands {
		// 体积未知（bytes==0）时不过滤，避免误杀。
		if maxBytes > 0 && c.bytes > maxBytes {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		b := cands[best]
		// 画质高优先；画质相同则体积小的优先（发得更快）。
		if c.pixels > b.pixels || (c.pixels == b.pixels && c.bytes > 0 && (b.bytes == 0 || c.bytes < b.bytes)) {
			best = i
		}
	}
	if best < 0 {
		// 全部超预算：宁可只发封面，也不发一个注定失败的大文件。
		return "", 0
	}
	return cands[best].url, cands[best].bytes
}

// estimateXBytes 用码率与时长估算体积（字节）。
func estimateXBytes(bitrate int, duration float64) int64 {
	if bitrate <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bitrate) * duration / 8)
}

var (
	// X 的画质直链形如 .../vid/avc1/720x1080/xxx.mp4（WxH）。
	xWHPattern = regexp.MustCompile(`/(\d{2,5})x(\d{2,5})(?:[./?]|$)`)
	// 兼容只带短边的旧形态 .../vid/avc1/720.mp4。
	xOnlyPattern = regexp.MustCompile(`/(\d{3,4})\.mp4`)
)

// parseXResolution 从 URL 里解析出像素数，用来比较画质高低。
func parseXResolution(lowerURL string) int {
	if m := xWHPattern.FindStringSubmatch(lowerURL); len(m) == 3 {
		w, h := atoiSafe(m[1]), atoiSafe(m[2])
		if w > 0 && h > 0 {
			return w * h
		}
	}
	if m := xOnlyPattern.FindStringSubmatch(lowerURL); len(m) == 2 {
		if v := atoiSafe(m[1]); v > 0 {
			// 按正方形估算，够用于档位间比较。
			return v * v
		}
	}
	return 0
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func secondsFromFloat(sec float64) int {
	if sec <= 0 {
		return 0
	}
	return int(math.Round(sec))
}

// normalizeXPhotoURL 把 ?name=orig 换成 ?name=large。
//
// 原图实测是 4096x3072，体积可能是 large 的 4 倍以上，而飞书图片
// 上限 10MB。2048px 在聊天窗口里看不出差别，却能显著降低上传失败率。
func normalizeXPhotoURL(raw string) string {
	u := strings.TrimSpace(raw)
	if !strings.HasPrefix(u, "http") {
		return ""
	}
	if idx := strings.Index(u, "?name="); idx >= 0 {
		return u[:idx] + "?name=large"
	}
	return u
}

// sizeFromURL 对图片做一个粗略体积估算（4 字节/像素，高质量 JPEG 的量级）。
// 估算值只用于提示和超限判断，不参与任何精确计算。
func sizeFromURL(_ string, w, h int) int64 {
	if w <= 0 || h <= 0 {
		return 0
	}
	return int64(w) * int64(h) / 2
}

// extractTweetID 从 X 链接里抽推文 ID，兼容多种形态。
func extractTweetID(rawURL string) string {
	if m := tweetIDPattern.FindStringSubmatch(rawURL); len(m) == 2 {
		return m[1]
	}
	// 分享短链里没有 /status/，靠 query 里的 s=
	if idx := strings.Index(rawURL, "?s="); idx >= 0 {
		rest := rawURL[idx+3:]
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		if end >= 15 {
			return rest[:end]
		}
	}
	// 兜底：找一段 15~25 位数字。
	for _, seg := range strings.FieldsFunc(rawURL, func(r rune) bool {
		return !(r >= '0' && r <= '9')
	}) {
		if len(seg) >= 15 && len(seg) <= 25 {
			return seg
		}
	}
	return ""
}
