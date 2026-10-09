package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DouyinResolver 通过项目已有的浏览器签名通道提取抖音作品。
//
// 为什么不自己写签名：抖音的 aweme/detail 需要 uifid/msToken 系列签名
// （见 sidecar/weibo-auth/douyin-websign.mjs），逆向成本高且易失效。
// 项目里 douyin-native-fetch.py 已经实现了 detail 模式（mode=detail +
// awemeIds），实测 status_code=0 能稳定取到数据，直接复用。
//
// 实测确认（2026-10-04）：
//   - 匿名 aweme/detail 被ArgusSecurityPlugin 拦截（Uifid Not Found）
//   - www.douyin.com/video/{id} 的 HTML 是空壳，无 RENDER_DATA，不可行
//   - fetch_detail 走浏览器 cookie + 签名，稳定返回
type DouyinResolver struct {
	// Cookies 是浏览器 profile 里的抖音 cookie 键值对。
	Cookies map[string]string
	// ScriptDir 是 douyin-native-fetch.py 所在目录。
	ScriptDir string
	// Timeout 限制单次取数时长。
	Timeout time.Duration
}

const defaultDouyinScriptDir = "sidecar/weibo-auth"

// douyinIDPattern 匹配抖音 aweme_id（19 位数字）。
var douyinIDPattern = regexp.MustCompile(`\b(\d{18,20})\b`)

// Matches 判断 host+path 是否属于抖音。
func (r *DouyinResolver) Matches(hostPath string) bool {
	lower := strings.ToLower(hostPath)
	for _, marker := range []string{"douyin.com", "iesdouyin.com", "v.douyin.com"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (r *DouyinResolver) Platform() Platform { return PlatformDouyin }

// douyinFetchResult 对应 douyin-native-fetch.py 的输出（detail 模式）。
type douyinFetchResult struct {
	AwemeID    string `json:"awemeId"`
	HTTP       int    `json:"http"`
	StatusCode int    `json:"status_code"`
	Message    string `json:"message"`
	Post       *struct {
		ID              string   `json:"id"`
		Nickname        string   `json:"nickname"`
		Desc            string   `json:"desc"`
		CreateTime      int64    `json:"createTime"`
		Type            string   `json:"type"`
		URL             string   `json:"url"`
		Cover           string   `json:"cover"`
		Images          []string `json:"images"`
		VideoURL        string   `json:"videoUrl"`
		VideoBitrate    int64    `json:"videoBitrate"`
		LivePhotoVideos []string `json:"livePhotoVideos"`
	} `json:"post"`
}

// Resolve 解析抖音链接。
func (r *DouyinResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	awemeID := extractDouyinID(rawURL)
	if awemeID == "" {
		return nil, fmt.Errorf("没能从链接里认出抖音作品 ID：%s", TrimUserinfo(rawURL))
	}
	if len(r.Cookies) == 0 {
		return nil, fmt.Errorf("抖音解析器未配置 Cookie，无法读取内容（需浏览器 profile 里的抖音 cookie）")
	}

	result, err := r.fetchDetail(ctx, awemeID)
	if err != nil {
		return nil, err
	}
	if result.Post == nil {
		msg := firstNonEmpty(result.Message, "内容不存在、已删除，或需要登录才能查看")
		if result.StatusCode != 0 && result.Message == "" {
			msg = fmt.Sprintf("抖音接口返回 status_code=%d", result.StatusCode)
		}
		return nil, fmt.Errorf("抖音接口返回：%s", msg)
	}

	p := result.Post
	post := &Post{
		Platform: PlatformDouyin,
		Author:   firstNonEmpty(p.Nickname, "抖音用户"),
		Text:     strings.TrimSpace(p.Desc),
		URL:      firstNonEmpty(p.URL, rawURL),
		Cover:    p.Cover,
	}

	// 实况图（Live Photo）：数据层就是 mp4 直链，可直接发。
	// 这是用户要的「Live 图」—— 苹果原生Live Photo 飞书不支持，但抖音这套能发。
	for _, lv := range p.LivePhotoVideos {
		if u := strings.TrimSpace(lv); u != "" {
			post.Items = append(post.Items, Item{
				Kind: "video", URL: u, Cover: p.Cover,
			})
		}
	}
	// 正文视频。highest_bitrate_video 已在Python 侧取最高码率档，
	// 实测多为 4K/1080p，不存在 B 站那种锁死低清的问题。
	if u := strings.TrimSpace(p.VideoURL); u != "" {
		post.Items = append(post.Items, Item{
			Kind: "video", URL: u, Cover: p.Cover,
		})
	}
	// 图文帖。
	for _, img := range p.Images {
		if u := strings.TrimSpace(img); u != "" {
			post.Items = append(post.Items, Item{Kind: "image", URL: u, Cover: u})
		}
	}
	return post, nil
}

// fetchDetail 调用 douyin-native-fetch.py 的 detail 模式。
func (r *DouyinResolver) fetchDetail(ctx context.Context, awemeID string) (*douyinFetchResult, error) {
	dir := r.ScriptDir
	if dir == "" {
		dir = defaultDouyinScriptDir
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	payload := map[string]interface{}{
		"mode":     "detail",
		"awemeIds": []string{awemeID},
		"cookies":  r.Cookies,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "python3", "douyin-native-fetch.py")
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if cctx.Err() != nil {
			return nil, fmt.Errorf("抖音取数超时（%s）：可能需要先在面板刷新抖音登录态", timeout)
		}
		return nil, fmt.Errorf("调用抖音取数脚本失败：%w（%s）", err, strings.TrimSpace(stderr.String()))
	}

	var results []douyinFetchResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("抖音脚本返回的数据无法解析：%w", err)
	}
	if len(results) == 0 {
		return nil, ErrNotFound
	}
	return &results[0], nil
}

// extractDouyinID 从各种链接形态里抽出 aweme_id。
func extractDouyinID(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	// 优先匹配路径里的 /video/{id} 或 /note/{id}。
	if m := regexp.MustCompile(`/(?:video|note|share/video|share/note)/(\d{15,20})`).FindStringSubmatch(rawURL); len(m) == 2 {
		return m[1]
	}
	if m := douyinIDPattern.FindString(rawURL); m != "" {
		return m
	}
	return ""
}
