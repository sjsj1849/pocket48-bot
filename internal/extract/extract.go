// Package extract 实现「发链接 → 提取正文与多媒体文件」。
//
// 动机：有些帖子平台本身不允许下载，只能自己想办法。机器人收到链接后
// 代替用户去平台把内容取回来，重新发一份（文字 + 图片/视频）。
//
// 设计要点：
//   - 平台识别同时看域名与 URL 形态。短链（v.douyin.com、t.co）必须先
//     跟随跳转拿到最终地址，否则无从判断平台。
//   - 每个平台的解析器独立成一个 Resolver，新增平台不影响既有代码。
//   - 解析失败要给出「人话」错误，而不是把 HTTP 状态码甩给用户。
package extract

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Platform 是识别出的平台标识。
type Platform string

const (
	PlatformUnknown  Platform = ""
	PlatformBilibili Platform = "bilibili"
	PlatformX        Platform = "x"
	PlatformDouyin   Platform = "douyin"
	PlatformWeibo    Platform = "weibo"
	// PlatformTiktok 2026-10-04 新增，走 sidecar 浏览器通道。
	PlatformTiktok Platform = "tiktok"
)

// Item 是提取出的一条媒体。
type Item struct {
	// Kind 目前只有 video 与 image。
	Kind string
	// URL 是可直接下载的直链。
	URL string
	// Cover 是封面图（视频用）。
	Cover string
	// Duration 是视频时长（秒），0 表示未知。
	Duration int
	// Width/Height 仅作展示用，可能为 0。
	Width, Height int
	// Size 是预估体积（字节），0 表示未知。
	//
	// 为什么要有这个：X 同一条推文有 6 档画质，最高能到 34MB，
	// 而飞书文件上传上限 30MB。上层需要在发送前判断能不能发得出去，
	// 而不是等上传失败才报错。
	Size int64
}

// Post 是提取出的一条完整内容。
type Post struct {
	Platform Platform
	// Author 是作者名。
	Author string
	// Text 是正文。
	Text string
	// URL 是原帖链接，用于在消息里给出出处。
	URL string
	// Cover 是封面图。
	Cover string
	// Items 是媒体列表，视频排在前面。
	Items []Item
}

// ErrUnsupported 表示链接不是受支持的平台。
var ErrUnsupported = errors.New("这个链接的平台还不支持")

// ErrNotFound 表示平台识别了，但内容已删除或不存在。
var ErrNotFound = errors.New("内容不存在、已删除，或需要登录才能查看")

// Resolver 是单个平台的解析器。
type Resolver interface {
	// Platform 返回该解析器负责的平台。
	Platform() Platform
	// Resolve 解析出一个 Post。ctx 会被施加整体超时。
	Resolve(ctx context.Context, rawURL string) (*Post, error)
}

// Options 是解析选项。
type Options struct {
	// Timeout 是单次解析的总超时。抖音/微博走浏览器时需要给足时间。
	Timeout time.Duration
	// MaxItems 限制返回的媒体数量，0 表示用默认值。
	// 上限存在的理由：一次发十几条视频会把群刷爆。
	MaxItems int
	// MaxVideoSeconds 限制单个视频时长，0 表示用默认值。
	// 超出时只发封面和链接，不发视频本体。
	MaxVideoSeconds int
	// HTTPClient 供解析器复用，便于测试注入。
	HTTPClient *http.Client
}

const (
	defaultTimeout         = 60 * time.Second
	defaultMaxItems        = 4
	defaultMaxVideoSeconds = 600 // 10 分钟，与 B 站内嵌阈值保持一致
)

func (o *Options) normalize() {
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
	if o.MaxItems <= 0 {
		o.MaxItems = defaultMaxItems
	}
	if o.MaxVideoSeconds <= 0 {
		o.MaxVideoSeconds = defaultMaxVideoSeconds
	}
	//负值表示「不做时长限制」，调用方显式要求时才生效。
	if o.MaxVideoSeconds < 0 {
		o.MaxVideoSeconds = 0
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{
			Timeout: 30 * time.Second,
			// 短链必须跟随跳转，否则识别不出真实平台。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return nil
			},
		}
	}
}

// Client 是提取器入口。
type Client struct {
	opts      Options
	resolvers map[Platform]Resolver
}

// New 创建提取器。传入的解析器按平台去重，后注册的不覆盖先注册的。
func New(opts Options, resolvers ...Resolver) *Client {
	opts.normalize()
	c := &Client{opts: opts, resolvers: map[Platform]Resolver{}}
	for _, r := range resolvers {
		if r == nil {
			continue
		}
		c.resolvers[r.Platform()] = r
	}
	return c
}

// SupportedPlatforms 返回已注册的���台列表，用于帮助文案。
func (c *Client) SupportedPlatforms() []Platform {
	order := []Platform{PlatformBilibili, PlatformX, PlatformDouyin, PlatformWeibo}
	out := make([]Platform, 0, len(order))
	for _, p := range order {
		if _, ok := c.resolvers[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Detect 识别链接所属平台。会跟随短链跳转。
func (c *Client) Detect(ctx context.Context, rawURL string) (Platform, string, error) {
	target, err := c.resolveShortLink(ctx, rawURL)
	if err != nil {
		return PlatformUnknown, "", err
	}
	u, err := url.Parse(target)
	if err != nil {
		return PlatformUnknown, "", fmt.Errorf("链接格式不对：%v", err)
	}
	host := strings.ToLower(u.Hostname())
	// 去掉 www. 前缀，便于统一比较。
	host = strings.TrimPrefix(host, "www.")
	for platform, resolver := range c.resolvers {
		if matcher, ok := resolver.(interface{ Matches(string) bool }); ok {
			if matcher.Matches(host + u.Path) {
				return platform, target, nil
			}
		}
	}
	return PlatformUnknown, target, nil
}

// Extract 是最常用的入口：识别平台 → 解析内容 → 返回 Post。
func (c *Client) Extract(ctx context.Context, rawURL string) (*Post, error) {
	platform, target, err := c.Detect(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	resolver, ok := c.resolvers[platform]
	if !ok {
		return nil, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	post, err := resolver.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	if post == nil || (len(post.Items) == 0 && strings.TrimSpace(post.Text) == "") {
		return nil, ErrNotFound
	}
	post.URL = target
	if len(post.Items) > c.opts.MaxItems {
		post.Items = post.Items[:c.opts.MaxItems]
	}
	c.dropOverlongVideos(post)
	return post, nil
}

// dropOverlongVideos 丢掉超过时长阈值的视频本体，只保留正文与封面。
//
// 为什么要做：一条 40 分钟的视频有几百 MB，飞书发不出去，用户只会
// 收到一句失败提示，体验比「只给链接」差得多。长视频本来就该自己去看。
//
// 这里的判断只看显式的 Duration；解析器没给出时长的（例如 X 图片帖）
// 不受影响，因为它们的 Kind 本来就不是 video。
func (c *Client) dropOverlongVideos(post *Post) {
	if post == nil || c.opts.MaxVideoSeconds <= 0 {
		return
	}
	kept := post.Items[:0]
	for _, it := range post.Items {
		if it.Kind == "video" && it.Duration > c.opts.MaxVideoSeconds {
			continue
		}
		kept = append(kept, it)
	}
	post.Items = kept
}

// resolveShortLink 跟随短链跳转，返回最终地址。
// 非短链原样返回，不额外发请求。
func (c *Client) resolveShortLink(ctx context.Context, rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", errors.New("链接为空")
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", errors.New("这不是一个有效的链接")
	}
	// 只对已知短链域名发跳转请求，避免对普通链接多打一次。
	if !isShortLinkHost(u.Hostname()) {
		return rawURL, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return rawURL, nil // 拿不到就按原样处理，交给后面识别
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := c.opts.HTTPClient.Do(req)
	if err != nil {
		return rawURL, nil
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String(), nil
	}
	return rawURL, nil
}

var shortLinkHosts = map[string]bool{
	"v.douyin.com":     true,
	"t.co":             true,
	"x.com":            false, // x.com 是正式域名，不跳转
	"twitter.com":      false,
	"b23.tv":           true,
	"bili2233.cn":      true,
	"momo.zhaobiao.cn": true,
}

func isShortLinkHost(host string) bool {
	host = strings.ToLower(strings.TrimPrefix(host, "www."))
	if v, ok := shortLinkHosts[host]; ok {
		return v
	}
	// 未知短链域名单独判断：极短的域名大概率是短链服务。
	return false
}

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// bvPattern 匹配 B 站 BV 号（BV + 10 位，字符集为 Base58 变体）。
var bvPattern = regexp.MustCompile(`BV[0-9A-Za-z]{10}`)

// avPattern 匹配 B 站 avid（纯数字，常见于 api.bilibili.com/x/web-interface/view?aid=）。
var avPattern = regexp.MustCompile(`(?i)(?:^|[?&/])aid=(\d+)`)

// tweetIDPattern 匹配 X 推文 ID（19 位数字，2022 年后均为 19 位）。
var tweetIDPattern = regexp.MustCompile(`/(?:status|statuses)/(\d{15,25})`)
