package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// flexString 接受字符串、数字或 null，统一转成字符串。
//
// 微博接口有多个「二态字段」（同一字段有时是 string 有时是 number），
// 声明成 string 会让整个 Unmarshal 失败，进而整条内容都取不出来 ——
// 而这种失败在错误信息里只显示字段路径，极难联想到是类型问题。
type flexString string

func (f *flexString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*f = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = flexString(s)
		return nil
	}
	// 数字 / 布尔：直接存字面量即可，我们只关心非空判断
	*f = flexString(trimmed)
	return nil
}

func (f flexString) String() string { return string(f) }

// weiboPagePic 兼容 page_pic 字段的三种形态：对象 {url:...}、纯字符串、null。
//
// 2026-10-04 端到端实测踩过：真实响应里它是**对象**，
// 而代码声明成 string，导致整个响应 Unmarshal 失败、一条内容都取不出来。
// 监控侧 internal/monitor/weibo.go 早已有同样的处理，这里补齐。
type weiboPagePic struct {
	URL string
}

func (p *weiboPagePic) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		p.URL = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		p.URL = strings.TrimSpace(s)
		return nil
	}
	// 对象形态
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	p.URL = strings.TrimSpace(obj.URL)
	return nil
}

// WeiboResolver 通过 m.weibo.cn 移动端接口提取单条微博。
//
// 为什么用 m.weibo.cn 而不是 weibo.com/ajax：
// 后者对机房 IP 一律返回 {"error":"Forbidden"}，实测无解。
// 前者带 Cookie 稳定返回 ok=1。注意路径**不能**加 /api/ 前缀，
// 那个变体已废弃，对所有 mid 都返回「无数据」。
const weiboShowAPI = "https://m.weibo.cn/statuses/show?id="

// weiboShowURL 便于测试替换成假服务器地址。
var weiboShowURL = weiboShowAPI

// WeiboResolver 提取微博内容。
type WeiboResolver struct {
	// Cookie 是 m.weibo.cn 用的登录态。
	Cookie string
	// Client 可选，便于测试注入。
	Client *http.Client
}

// weiboIDPattern 匹配形如 weibo.com/6694957538/5350064312812042 的 mid。
var weiboIDPattern = regexp.MustCompile(`\d{10,}`)

type weiboShowResponse struct {
	OK   int    `json:"ok"`
	Msg  string `json:"msg"`
	Data struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		CreatedAt string `json:"created_at"`
		User      struct {
			ScreenName string `json:"screen_name"`
		} `json:"user"`
		// ★★ 2026-10-08 新增。图文帖的图片**只**在这里，正文里没有 jpg。
		//   此前完全没解析这个字段，导致 6 图微博只能发出纯文本。
		Pics     []weiboPic `json:"pics"`
		PageInfo struct {
			Title   string       `json:"title"`
			PagePic weiboPagePic `json:"page_pic"`
			// object_type 是二态字段：有时是 "video" 这样的字符串，
			// 有时是数字。声明成 string 会让整个响应反序列化失败
			// （2026-10-04 实测踩过），所以用容错类型。
			ObjectType flexString     `json:"object_type"`
			MediaInfo  weiboMediaInfo `json:"media_info"`
			URLs       weiboURLs      `json:"urls"`
			Content1   string         `json:"content1"`
			Content2   string         `json:"content2"`
		} `json:"page_info"`
	} `json:"data"`
}

// weiboURLBox 兼容「url 字段可能是字符串、也可能是 {url:...} 对象」。
//
// 与 weiboPagePic 同理：声明成 string 会让整个响应 Unmarshal 失败。
type weiboURLBox struct {
	URL string
}

func (b *weiboURLBox) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		b.URL = ""
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		b.URL = strings.TrimSpace(s)
		return nil
	}
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	b.URL = strings.TrimSpace(obj.URL)
	return nil
}

// weiboPic 是 data.pics[] 的一项。
//
// 2026-10-08 实测（mid=5351887681618773，6 图微博）真实字段只有：
//
//	{"geo":{...}, "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/xxx.jpg","geo":{...}},
//	 "pid":"...", "size":"l", "url":"https://wx3.sinaimg.cn/orj360/xxx.jpg"}
//
// ★ url 是 orj360 缩略图，large.url 才是 mw2000 大图 —— 取错就发模糊图。
//
// type / videoSrc 来自监控侧 internal/monitor/weibo.go 已验证的形状
// （实况图会把视频地址放在 videoSrc）。statuses/show 实测未下发，
// 一旦哪天开始下发就能自动生效，不会再退化成纯文本。
type weiboPic struct {
	URL      weiboURLBox `json:"url"`
	Large    weiboURLBox `json:"large"`
	Largest  weiboURLBox `json:"largest"`
	Original weiboURLBox `json:"original"`
	MW2000   weiboURLBox `json:"mw2000"`
	Type     string      `json:"type"`
	VideoSrc string      `json:"videoSrc"`
}

// best 返回这张图可用的最大尺寸地址。
func (p weiboPic) best() string {
	return firstNonEmpty(
		p.Large.URL,
		p.Largest.URL,
		p.MW2000.URL,
		p.Original.URL,
		p.URL.URL,
	)
}

// weiboMediaInfo 是 page_info.media_info。
//
// ★★ 这里有个非常反直觉的坑，2026-10-04 实测确认：
//
//	stream_url_hd  → 实际 540x1172（不是 HD！）
//	stream_url     → 实际 540x1172
//	urls.mp4_720p_mp4 → 实际 720x1564（这才是高清）
//
// 微博的字段命名与实际画质**完全相反**，名为 hd 的其实是低清。
// 实测样本：stream_url_hd = 2.33MB/540x1172，
// urls.mp4_720p_mp4 = 4.00MB/720x1564。
//
// 现有监控代码（pickBestWeiboVideoURL）优先取 stream_url_hd，
// 因此长期只发540P。**本解析器必须优先 urls 里的 720p。**
type weiboMediaInfo struct {
	Duration    json.Number `json:"duration"`
	StreamURL   string      `json:"stream_url"`
	StreamURLHD string      `json:"stream_url_hd"`
	MP4HDURL    string      `json:"mp4_hd_url"`
	MP4SDURL    string      `json:"mp4_sd_url"`
}

// weiboURLs 是 page_info.urls，实测含 mp4_720p_mp4 / mp4_hd_mp4 / mp4_ld_mp4。
type weiboURLs struct {
	MP4720p string `json:"mp4_720p_mp4"`
	MP4HD   string `json:"mp4_hd_mp4"`
	MP4LD   string `json:"mp4_ld_mp4"`
}

// Matches 判断 host+path 是否属于微博。
func (r *WeiboResolver) Matches(hostPath string) bool {
	lower := strings.ToLower(hostPath)
	for _, marker := range []string{"weibo.com", "weibo.cn", "t.cn"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (r *WeiboResolver) Platform() Platform { return PlatformWeibo }

// Resolve 解析微博链接。
func (r *WeiboResolver) Resolve(ctx context.Context, rawURL string) (*Post, error) {
	return r.resolveWith(ctx, rawURL)
}

// resolveWith 是可替换端点的实现，供测试注入 httptest 服务器。
func (r *WeiboResolver) resolveWith(ctx context.Context, rawURL string) (*Post, error) {
	mid := extractWeiboID(rawURL)
	if mid == "" {
		return nil, fmt.Errorf("没能从链接里认出微博 ID：%s", TrimUserinfo(rawURL))
	}
	if strings.TrimSpace(r.Cookie) == "" {
		return nil, fmt.Errorf("微博解析器未配置 Cookie，无法读取内容")
	}

	resp, err := r.fetchShow(ctx, mid)
	if err != nil {
		return nil, err
	}
	data := resp.Data

	post := &Post{
		Platform: PlatformWeibo,
		Author:   firstNonEmpty(data.User.ScreenName, "微博博主"),
		Text:     cleanWeiboHTML(firstNonEmpty(data.Text, data.PageInfo.Content1, data.PageInfo.Content2)),
		URL:      rawURL,
		Cover:    data.PageInfo.PagePic.URL,
	}

	// ★ 关键：优先 urls.mp4_720p_mp4，而不是 media_info.stream_url_hd。
	// 后者名字叫 hd 但实测只有 540P，详见 weiboMediaInfo 注释。
	videoURL := pickBestWeiboVideo(data.PageInfo.URLs, data.PageInfo.MediaInfo)
	if videoURL == "" {
		// 无视频的图文帖：把 pics[] 全部收集出来，逐张成条。
		//
		// ★★ 2026-10-08 修正。此前只做两件事：
		//   ① Cover 取 page_info.page_pic —— 实测 6 图微博这里是 **null**；
		//   ② extractWeiboPics(data.Text) 从正文正则抓 jpg —— 实测
		//      text_has_jpg = False，微博正文里根本没有图片地址。
		// 两条路都落空 ⇒ post.Items 为空 ⇒ 飞书/QQ 只收到一条纯文本卡片。
		// 现在改为解析 data.pics（实测 large.url = mw2000 大图）。
		for _, item := range collectWeiboMedia(data.Pics, data.Text) {
			post.Items = append(post.Items, item)
		}
		if len(post.Items) > 0 {
			post.Cover = firstNonEmpty(post.Cover, post.Items[0].URL)
		}
		return post, nil
	}

	dur := 0
	if d, err := strconv.ParseFloat(string(data.PageInfo.MediaInfo.Duration), 64); err == nil {
		dur = int(d)
	}
	item := Item{
		Kind:     "video",
		URL:      videoURL,
		Cover:    post.Cover,
		Duration: dur,
	}
	// 微博直链的体积需要实际探测，这里不猜；上层按未知处理。
	post.Items = append(post.Items, item)
	return post, nil
}

// pickBestWeiboVideo 按「实测画质」而非字段名选视频地址。
//
// 顺序即画质顺序（2026-10-04 实测）：
// urls.mp4_720p_mp4 (720x1564) > media_info.* (540x1172)
//
// 之所以不靠字段名，是因为微博命名与实际相反，见 weiboMediaInfo 注释。
func pickBestWeiboVideo(urls weiboURLs, mi weiboMediaInfo) string {
	// 第一优先：urls 里的 720p，这是实测最高档。
	if u := strings.TrimSpace(urls.MP4720p); u != "" {
		return u
	}
	// 第二优先：media_info 各字段，HD 优先（虽然可能只有 540P）。
	return firstNonEmpty(
		mi.StreamURLHD,
		mi.MP4HDURL,
		urls.MP4HD,
		mi.StreamURL,
		mi.MP4SDURL,
		urls.MP4LD,
	)
}

// fetchShow 调用 m.weibo.cn/statuses/show。
func (r *WeiboResolver) fetchShow(ctx context.Context, mid string) (*weiboShowResponse, error) {
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, weiboShowURL+url.QueryEscape(mid), nil)
	if err != nil {
		return nil, err
	}
	// 必须伪装成移动端，桌面 UA 会被导向验证页。
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 "+
			"(KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	req.Header.Set("Cookie", r.Cookie)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://m.weibo.cn/")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问微博接口失败：%w", err)
	}
	defer resp.Body.Close()

	var out weiboShowResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("微博返回的数据无法解析（HTTP %d）：%w", resp.StatusCode, err)
	}
	if out.OK != 1 {
		msg := firstNonEmpty(out.Msg, "内容不存在、已删除，或需要登录才能查看")
		return nil, fmt.Errorf("微博接口返回：%s", msg)
	}
	if out.Data.ID == "" && out.Data.PageInfo.ObjectType.String() == "" {
		return nil, ErrNotFound
	}
	return &out, nil
}

// extractWeiboID 从各种链接形态里抽出 mid。
//
// 链接形态：weibo.com/{uid}/{mid}、m.weibo.cn/status/{mid}、weibo.com/detail/{mid}。
//
// ★ 必须取路径里的**最后一段** —— 第一段是 uid（用户 id）。
// 误把 uid 当mid 不会报错，会静默返回**另一个用户**的微博，极难发现。
func extractWeiboID(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)

	// 只看 URL 路径，忽略 query —— 分享链接的 query 里常混着 tracking 参数。
	path := rawURL
	if u, err := url.Parse(rawURL); err == nil && u.Path != "" {
		path = u.Path
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	// 从后往前找第一段「纯数字且足够长」的，即 mid。
	for i := len(segments) - 1; i >= 0; i-- {
		seg := strings.TrimSpace(segments[i])
		if len(seg) >= 10 && isAllDigits(seg) {
			return seg
		}
	}
	// 兜底：整串里抓最长的数字段（短链跟随后路径可能不规范）。
	best := ""
	for _, seg := range weiboIDPattern.FindAllString(rawURL, -1) {
		if len(seg) > len(best) {
			best = seg
		}
	}
	return best
}

// isAllDigits 判断是否全是数字。
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cleanWeiboHTML 去掉微博正文里的 HTML 标签与实体。
func cleanWeiboHTML(s string) string {
	if s == "" {
		return ""
	}
	s = regexp.MustCompile(`(?s)<br\s*/?>`).ReplaceAllString(s, "\n")
	s = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	// 微博正文末尾常跟一个不可见控制字符 + 短链。
	// 微博正文用 U+0006 切断正文并附上分享短链，去掉后正文才干净。
	if idx := strings.IndexRune(s, '\u0006'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// extractWeiboPics 从正文里取第一张图片地址（**兜底**，正常走 pics[]）。
func extractWeiboPics(html string) string {
	m := regexp.MustCompile(`https?://[^"'\\\s]+\.(?:jpg|jpeg|png|gif)(?:\?[^"'\\\s]*)?`).FindString(html)
	return m
}

// collectWeiboMedia 把图文帖的 pics[] 转成媒体条目。
//
// 顺序：pics[] 优先（实测权威来源），全空时才退回正文正则。
// 实况图（pics[].videoSrc）会额外产出一条视频条目 —— 静态图仍发，
// 这样「实况图」既能看图也能看动态。
func collectWeiboMedia(pics []weiboPic, text string) []Item {
	items := make([]Item, 0, len(pics))
	for _, p := range pics {
		still := p.best()
		// 实况图：视频地址存在时，视频单独成条。
		if vs := strings.TrimSpace(p.VideoSrc); vs != "" {
			items = append(items, Item{Kind: "video", URL: vs, Cover: still})
			continue
		}
		if still == "" {
			continue
		}
		items = append(items, Item{Kind: "image", URL: still, Cover: still})
	}
	if len(items) > 0 {
		return items
	}
	// 兜底：正文里抓图（微博绝大多数图文帖走不到这里）。
	if pic := extractWeiboPics(text); pic != "" {
		return []Item{{Kind: "image", URL: pic, Cover: pic}}
	}
	return nil
}
