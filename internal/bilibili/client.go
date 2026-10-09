package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 经实测确认的公开端点。
//   - 投稿列表 / opus 图文流 / 专栏：服务器（机房 IP）可访问，但**有频率限制**，
//     密集调用会返回 412 风控页或 code -799「请求过于频繁」，必须低频轮询 + 退避。
//   - 直播状态 / UP 主资料：稳定，无频率问题。
//   - 综合动态流(feed/space)与视频详情(view)：机房 IP 一律 412，故不使用。
const (
	// 投稿列表与用户资料都必须走 WBI 签名，否则风控返回 -352。
	spaceVideoEndpoint = "https://api.bilibili.com/x/space/wbi/arc/search"
	profileEndpoint    = "https://api.bilibili.com/x/space/wbi/acc/info"
	// 直播域接口对机房 IP 宽松得多，用作空间接口被风控时的兜底。
	liveMasterEndpoint = "https://api.live.bilibili.com/live_user/v1/Master/info"
	navnumEndpoint     = "https://api.bilibili.com/x/space/navnum"
	opusFeedEndpoint   = "https://api.bilibili.com/x/polymer/web-dynamic/v1/opus/feed/space"
	// 空间动态流（综合）。与 opus/feed 同域，但返回的是**完整动态对象**，
	// 视频投稿以 DYNAMIC_TYPE_AV + MAJOR_TYPE_ARCHIVE 出现，带 bvid/时长/播放量。
	// 机房 IP 匿名一律 412，必须带登录 Cookie。
	feedSpaceEndpoint  = "https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space"
	articleEndpoint    = "https://api.bilibili.com/x/space/article"
	liveStatusEndpoint = "https://api.live.bilibili.com/room/v1/Room/get_status_info_by_uids"
	fingerEndpoint     = "https://api.bilibili.com/x/frontend/finger/spi"

	// 合集/系列列表端点。**这是目前唯一在机房 IP 上匿名可用、且能拿到
	// 完整投稿元数据（标题、时长、bvid、发布时间、播放量）的入口**：
	// x/space/wbi/arc/search 匿名态返回 -403，navnum 只能拿到计数。
	// 团综类长视频都收在合集里，因此长视频检测走这里。
	seasonListEndpoint = "https://api.bilibili.com/x/polymer/web-space/seasons_series_list"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

const liveChunk = 50

// RateLimitError marks a transient B 站 throttle (412 anti-crawler page or the
// -799/-352 business codes). Callers should back off instead of surfacing it as a
// hard failure.
type RateLimitError struct{ Reason string }

func (e *RateLimitError) Error() string {
	if e.Reason == "" {
		return "B站风控/频率限制，稍后自动重试"
	}
	return "B站风控/频率限制：" + e.Reason
}

// IsRateLimited 判断错误是否属于「可退避重试」的瞬时问题：既包括 -799/412
// 频率限制，也包括 -352 风控校验失败（密钥轮换后即便刷新签名仍可能失败）。
func IsRateLimited(err error) bool {
	if err == nil {
		return false
	}
	switch err.(type) {
	case *RateLimitError, *RiskControlError:
		return true
	default:
		return false
	}
}

// RiskControlError 表示 -352「风控校验失败」，通常意味着 WBI 密钥轮换或
// 需要重新签名；调用方可刷新签名后重试。
type RiskControlError struct{ Message string }

func (e *RiskControlError) Error() string {
	if e.Message == "" {
		return "B站风控校验失败"
	}
	return "B站风控校验失败：" + e.Message
}

type credentials struct {
	Buvid3 string `json:"buvid3"`
	Buvid4 string `json:"buvid4"`
}

type Client struct {
	HTTP *http.Client
	// Dir persists the device fingerprint so the identity stays stable across
	// restarts (a rotating buvid looks more suspicious to B 站风控).
	Dir string
	// Cookie is an optional user-supplied SESSDATA string; logged-in requests get
	// a more generous quota.
	Cookie string

	mu     sync.Mutex
	creds  credentials
	loaded bool
	// wbi 缓存当天的 WBI 签名密钥（B 站午夜轮换，遇到 -352 会强制重取）。
	wbi wbiKeys
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) ensureCreds(ctx context.Context) credentials {
	c.mu.Lock()
	if c.loaded {
		creds := c.creds
		c.mu.Unlock()
		return creds
	}
	c.mu.Unlock()

	// 锁只在读写缓存时短暂持有：网络请求放在锁外，避免与非重入互斥锁自锁。
	creds := credentials{}
	if c.Dir != "" {
		var saved credentials
		if Read(c.Dir, "creds.json", &saved) == nil && saved.Buvid3 != "" {
			creds = saved
		}
	}
	if creds.Buvid3 == "" {
		// finger/spi 不需要任何参数，返回可用于风控识别的设备指纹。
		var spi struct {
			Data struct {
				B3 string `json:"b_3"`
				B4 string `json:"b_4"`
			} `json:"data"`
		}
		if err := c.do(ctx, fingerEndpoint, "", false, &spi); err == nil {
			creds = credentials{Buvid3: strings.TrimSpace(spi.Data.B3), Buvid4: strings.TrimSpace(spi.Data.B4)}
			if c.Dir != "" && creds.Buvid3 != "" {
				_ = Write(c.Dir, "creds.json", creds)
			}
		}
	}
	if creds.Buvid3 != "" {
		c.mu.Lock()
		c.creds = creds
		c.loaded = true
		c.mu.Unlock()
	}
	return creds
}

func (c *Client) cookieHeader(ctx context.Context) string {
	creds := c.ensureCreds(ctx)
	parts := make([]string, 0, 4)
	if creds.Buvid3 != "" {
		parts = append(parts, "buvid3="+creds.Buvid3)
	}
	if creds.Buvid4 != "" {
		parts = append(parts, "buvid4="+creds.Buvid4)
	}
	parts = append(parts, "b_nut=1")
	if extra := strings.TrimSpace(c.Cookie); extra != "" {
		parts = append(parts, strings.TrimSuffix(extra, ";"))
	}
	return strings.Join(parts, "; ")
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *Client) get(ctx context.Context, rawURL, referer string, out any) error {
	return c.do(ctx, rawURL, referer, true, out)
}

// do is the low-level request. withCookie=false is used while resolving the
// device fingerprint, because building the Cookie header would re-enter
// ensureCreds and deadlock on a non-reentrant mutex.
func (c *Client) do(ctx context.Context, rawURL, referer string, withCookie bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if withCookie {
		req.Header.Set("Cookie", c.cookieHeader(ctx))
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("B站请求失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("B站响应读取失败：%v", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPreconditionFailed:
		// 412 是 B 站反爬页面，不是接口错误，按可重试的限流处理。
		return &RateLimitError{Reason: "HTTP 412"}
	default:
		return fmt.Errorf("B站返回 HTTP %d", resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("B站返回了无法解析的数据")
	}
	switch env.Code {
	case 0:
	case -799, -352:
		if env.Code == -352 {
			// -352 是「风控校验失败」，多半是 WBI 密钥过期，可刷新签名重试。
			return &RiskControlError{Message: strings.TrimSpace(env.Message)}
		}
		return &RateLimitError{Reason: strings.TrimSpace(env.Message)}
	default:
		message := strings.TrimSpace(env.Message)
		if message == "" {
			message = "未知错误"
		}
		return fmt.Errorf("B站接口错误 %d：%s", env.Code, message)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// User resolves the UP 主 display name and avatar. 优先走 WBI 签名的 acc/info；
// 机房 IP 突发限流时回退到直播域 Master/info（该接口对机房 IP 稳定得多）。
func (c *Client) User(ctx context.Context, uid string) (Up, error) {
	var info struct {
		Name string `json:"name"`
		Face string `json:"face"`
	}
	err := c.wbiGet(ctx, profileEndpoint, "https://space.bilibili.com/"+uid,
		map[string]string{"mid": uid}, &info)
	if name := strings.TrimSpace(info.Name); err == nil && name != "" {
		return Up{UID: uid, Name: name, Avatar: info.Face}, nil
	}

	var live struct {
		Data struct {
			Info struct {
				Uname string `json:"uname"`
				Face  string `json:"face"`
			} `json:"info"`
		} `json:"data"`
	}
	if lerr := c.get(ctx, liveMasterEndpoint+"?uid="+url.QueryEscape(uid), "https://live.bilibili.com/", &live); lerr == nil {
		if name := strings.TrimSpace(live.Data.Info.Uname); name != "" {
			return Up{UID: uid, Name: name, Avatar: live.Data.Info.Face}, nil
		}
	}
	if err != nil {
		return Up{}, err
	}
	return Up{}, fmt.Errorf("无法解析该 UID 的账号信息，请确认 UID 是否正确")
}

type liveStatusEntry struct {
	UID        string `json:"uid"`
	RoomID     int64  `json:"room_id"`
	LiveStatus int    `json:"live_status"`
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	Online     int64  `json:"online"`
}

// LiveStatus batches the live-room query by UID (50 per request).
func (c *Client) LiveStatus(ctx context.Context, uids []string) (map[string]LiveRoom, error) {
	result := map[string]LiveRoom{}
	for start := 0; start < len(uids); start += liveChunk {
		end := start + liveChunk
		if end > len(uids) {
			end = len(uids)
		}
		query := make([]string, 0, end-start)
		for _, uid := range uids[start:end] {
			query = append(query, "uids[]="+url.QueryEscape(uid))
		}
		// data 是数组而不是 map：{"code":0,"data":[{"uid":"...","room_id":...}]}。
		// 无直播间的 UP 不会出现在数组里，这里按请求顺序补齐，
		// 让调用方能区分「查不到直播间」与「未开播」。
		var data []liveStatusEntry
		if err := c.get(ctx, liveStatusEndpoint+"?"+strings.Join(query, "&"), "https://live.bilibili.com/", &data); err != nil {
			return result, err
		}
		for _, uid := range uids[start:end] {
			if _, ok := result[uid]; !ok {
				result[uid] = LiveRoom{UID: uid}
			}
		}
		for _, entry := range data {
			uid := strings.TrimSpace(entry.UID)
			if uid == "" {
				continue
			}
			result[uid] = LiveRoom{
				UID:        uid,
				RoomID:     entry.RoomID,
				LiveStatus: entry.LiveStatus,
				Title:      strings.TrimSpace(entry.Title),
				Cover:      strings.TrimSpace(entry.Cover),
				Online:     entry.Online,
			}
		}
	}
	return result, nil
}

// SpaceVideos returns the newest 投稿 (order=pubdate)。WBI 签名后机房 IP 可用。
func (c *Client) SpaceVideos(ctx context.Context, uid string) ([]Dynamic, error) {
	// 翻页：登录态下 arc/search 稳定可用，只取前 10 条会漏掉刚发的长视频
	// （短视频刷屏时新投稿很容易被挤出第一页）。最多翻 3 页覆盖最近 150 条。
	const perPage = 50
	out := make([]Dynamic, 0, perPage*2)
	seen := make(map[string]bool, perPage*2)
	for page := 1; page <= 3; page++ {
		raw := struct {
			List struct {
				VList []struct {
					BVID        string `json:"bvid"`
					Title       string `json:"title"`
					Description string `json:"description"`
					Created     int64  `json:"created"`
					Pic         string `json:"pic"`
					Length      string `json:"length"`
				} `json:"vlist"`
				Page struct {
					Count int `json:"count"`
				} `json:"page"`
			} `json:"list"`
		}{}
		params := map[string]string{
			"mid":   uid,
			"ps":    strconv.Itoa(perPage),
			"pn":    strconv.Itoa(page),
			"order": "pubdate",
		}
		if err := c.wbiGet(ctx, spaceVideoEndpoint, "https://space.bilibili.com/"+uid, params, &raw); err != nil {
			// 投稿列表接口(arc/search)对机房 IP 是硬风控（412），登录 Cookie 才会放行。
			// 匿名态下退化为「投稿计数」探测：计数变化即视为有新投稿，推送一条不带
			// 标题的提示，用户点进空间查看。这样至少不会漏掉「发了新视频」这件事。
			if len(out) == 0 && IsRateLimited(err) {
				if count, cerr := c.SpaceVideoCount(ctx, uid); cerr == nil && count > 0 {
					return []Dynamic{{
						ID:   fmt.Sprintf("nav:%d", count),
						Kind: "video",
						URL:  "https://space.bilibili.com/" + uid + "/video",
						Text: "（B 站投稿接口被风控拦截，仅提示有新投稿，点链接前往空间查看）",
					}}, nil
				}
			}
			// 已经翻到有内容时，后续分页失败就保留已有结果。
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		for _, v := range raw.List.VList {
			bvid := strings.TrimSpace(v.BVID)
			if bvid == "" || seen[bvid] {
				continue
			}
			seen[bvid] = true
			seconds := ParseDurationSeconds(v.Length)
			out = append(out, Dynamic{
				ID:      "av:" + bvid,
				Kind:    "video",
				Title:   strings.TrimSpace(v.Title),
				Text:    strings.TrimSpace(v.Description),
				Cover:   strings.TrimSpace(v.Pic),
				URL:     "https://www.bilibili.com/video/" + bvid,
				Time:    v.Created * 1000,
				Length:  strings.TrimSpace(v.Length),
				Seconds: seconds,
				View:    -1,
			})
		}
		if len(raw.List.VList) < perPage || page*perPage >= raw.List.Page.Count {
			break
		}
	}
	return out, nil
}

// SpaceVideoCount returns the UP 主投稿总数（navnum 接口，轻量且机房 IP 可用）。
// 仅用于投稿列表被风控时的降级探测。
func (c *Client) SpaceVideoCount(ctx context.Context, uid string) (int, error) {
	var raw struct {
		Video int `json:"video"`
	}
	if err := c.get(ctx, navnumEndpoint+"?mid="+url.QueryEscape(uid), "https://space.bilibili.com/"+uid, &raw); err != nil {
		return 0, err
	}
	return raw.Video, nil
}

// SpaceOpus returns the newest 图文/opus 动态。该接口在机房 IP 上稳定，且**不带发布时间**，
// 因此依赖返回顺序（最新在前）判新旧，Time 留 0。
func (c *Client) SpaceOpus(ctx context.Context, uid string) ([]Dynamic, error) {
	var raw struct {
		Items []struct {
			JumpURL string `json:"jump_url"`
			OpusID  string `json:"opus_id"`
			Content string `json:"content"`
			Cover   *struct {
				URL string `json:"url"`
			} `json:"cover"`
			PubTime string `json:"pub_time"`
		} `json:"items"`
	}
	endpoint := fmt.Sprintf("%s?host_mid=%s&page=1", opusFeedEndpoint, url.QueryEscape(uid))
	if err := c.get(ctx, endpoint, "https://space.bilibili.com/"+uid, &raw); err != nil {
		return nil, err
	}
	out := make([]Dynamic, 0, len(raw.Items))
	for _, item := range raw.Items {
		id := strings.TrimSpace(item.OpusID)
		if id == "" {
			continue
		}
		cover := ""
		if item.Cover != nil {
			cover = strings.TrimSpace(item.Cover.URL)
		}
		out = append(out, Dynamic{
			ID:    "opus:" + id,
			Kind:  "draw",
			Text:  strings.TrimSpace(item.Content),
			Cover: cover,
			URL:   normaliseJumpURL(strings.TrimSpace(item.JumpURL)),
			Time:  parseFlexibleTime(item.PubTime),
		})
	}
	// 接口按「最新在前」返回，且不带发布时间；倒序成「最早在前」，让同批多条的
	// 推送顺序与发布时间一致（去重排序对 Time=0 的项只能依赖输入顺序）。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// flexibleInt parses B 站动态流里的数字字段。
//
// 该接口的数值字段是**二态**的：module_author.pub_ts、archive.stat.play 等有时
// 返回数字、有时返回字符串（实测 pub_ts 是 "1791203401"）。用 int64 直解会让
// 整个响应 json.Unmarshal 失败，等于整个来源静默失效——所以统一容错，
// 解析不了就当 0，绝不让单字段毁掉整条响应。
type flexibleInt int64

func (f *flexibleInt) UnmarshalJSON(b []byte) error {
	text := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		*f = flexibleInt(n)
		return nil
	}
	if fl, err := strconv.ParseFloat(text, 64); err == nil {
		*f = flexibleInt(fl)
	}
	return nil
}

// SpaceFeedVideos returns the UP 主最近投稿的视频，来源是**空间动态流**。
//
// 为什么需要它：合集 (seasons_series_list) 与投稿列表 (arc/search) 都有明显的
// 索引延迟——实测投稿发布后最长 15 分钟才出现在列表里，而动态流是投稿动作
// 本身产生的，通常一轮轮询内就可见。动态流带登录 Cookie 时在机房 IP 上稳定，
// 匿名则必定 412。
//
// 只取 DYNAMIC_TYPE_AV + MAJOR_TYPE_ARCHIVE（UP 自己投的稿）；图文/转发动态
// 不在这里处理（图文走 SpaceOpus）。
func (c *Client) SpaceFeedVideos(ctx context.Context, uid string) ([]Dynamic, error) {
	var raw struct {
		HasMore bool            `json:"has_more"`
		Offset  string          `json:"offset"`
		Items   []spaceFeedItem `json:"items"` // gofmt 会整理对齐
	}
	endpoint := fmt.Sprintf("%s?host_mid=%s&offset=", feedSpaceEndpoint, url.QueryEscape(uid))
	if err := c.get(ctx, endpoint, "https://space.bilibili.com/"+uid, &raw); err != nil {
		return nil, err
	}
	return feedDynamics(raw.Items), nil
}

// feedDynamics maps 动态流 entries onto 投稿视频 items, dropping everything that is
// not a plain video post. 单独成函数是为了让解析逻辑可以脱离网络做单测。
func feedDynamics(items []spaceFeedItem) []Dynamic {
	out := make([]Dynamic, 0, len(items))
	for _, item := range items {
		arc := item.archive()
		if arc == nil {
			continue
		}
		bvid := strings.TrimSpace(arc.BVID)
		if bvid == "" {
			continue
		}
		length := strings.TrimSpace(arc.DurationText)
		view := int64(arc.Stat.Play)
		if view == 0 {
			view = -1
		}
		out = append(out, Dynamic{
			ID:      "av:" + bvid,
			Kind:    "video",
			Title:   strings.TrimSpace(arc.Title),
			Text:    item.descText(),
			Cover:   httpsCover(strings.TrimSpace(arc.Cover)),
			URL:     "https://www.bilibili.com/video/" + bvid,
			Time:    int64(item.Modules.ModuleAuthor.PubTS) * 1000,
			Length:  length,
			Seconds: ParseDurationSeconds(length),
			View:    view,
		})
	}
	return out
}

// spaceFeedItem 是动态流里的一条动态。字段只挑用得上的，其余交给 encoding/json 忽略。
type spaceFeedItem struct {
	Type    string `json:"type"`
	Modules struct {
		ModuleAuthor struct {
			Name  string      `json:"name"`
			PubTS flexibleInt `json:"pub_ts"`
		} `json:"module_author"`
		ModuleDynamic struct {
			Desc *struct {
				Text string `json:"text"`
			} `json:"desc"`
			Major struct {
				Type    string            `json:"type"`
				Archive *spaceFeedArchive `json:"archive"`
			} `json:"major"`
		} `json:"module_dynamic"`
	} `json:"modules"`
}

type spaceFeedArchive struct {
	BVID         string `json:"bvid"`
	Title        string `json:"title"`
	Cover        string `json:"cover"`
	DurationText string `json:"duration_text"`
	Stat         struct {
		Play flexibleInt `json:"play"`
	} `json:"stat"`
}

// archive returns the 投稿视频 payload, or nil when this 动态 is not a video post.
func (it spaceFeedItem) archive() *spaceFeedArchive {
	if it.Type != "DYNAMIC_TYPE_AV" {
		return nil
	}
	if it.Modules.ModuleDynamic.Major.Type != "MAJOR_TYPE_ARCHIVE" {
		return nil
	}
	return it.Modules.ModuleDynamic.Major.Archive
}

func (it spaceFeedItem) descText() string {
	if d := it.Modules.ModuleDynamic.Desc; d != nil {
		return strings.TrimSpace(d.Text)
	}
	return ""
}

// httpsCover 把 B 站返回的 http 封面统一成 https，避免下载端被 302/混合内容拦。
func httpsCover(u string) string {
	if strings.HasPrefix(u, "http://") {
		return "https://" + strings.TrimPrefix(u, "http://")
	}
	if strings.HasPrefix(u, "//") {
		return "https:" + u
	}
	return u
}

// SpaceArticles returns the newest 专栏文章.
func (c *Client) SpaceArticles(ctx context.Context, uid string) ([]Dynamic, error) {
	var raw struct {
		Articles []struct {
			ID          int64    `json:"id"`
			Title       string   `json:"title"`
			Summary     string   `json:"summary"`
			ImageURLs   []string `json:"image_urls"`
			BannerURL   string   `json:"banner_url"`
			PublishTime int64    `json:"publish_time"` // unix seconds
		} `json:"articles"`
	}
	endpoint := fmt.Sprintf("%s?mid=%s&pn=1&ps=10&sort=publish_time", articleEndpoint, url.QueryEscape(uid))
	if err := c.get(ctx, endpoint, "https://space.bilibili.com/"+uid, &raw); err != nil {
		return nil, err
	}
	out := make([]Dynamic, 0, len(raw.Articles))
	for _, a := range raw.Articles {
		if a.ID <= 0 {
			continue
		}
		cover := strings.TrimSpace(a.BannerURL)
		if len(a.ImageURLs) > 0 && strings.TrimSpace(a.ImageURLs[0]) != "" {
			cover = strings.TrimSpace(a.ImageURLs[0])
		}
		out = append(out, Dynamic{
			ID:    fmt.Sprintf("art:%d", a.ID),
			Kind:  "article",
			Title: strings.TrimSpace(a.Title),
			Text:  strings.TrimSpace(a.Summary),
			Cover: cover,
			URL:   fmt.Sprintf("https://www.bilibili.com/read/cv%d", a.ID),
			Time:  a.PublishTime * 1000,
		})
	}
	return out, nil
}

// parseFlexibleTime accepts unix seconds/ms or "2006-01-02 15:04:05" (CST).
func parseFlexibleTime(value string) int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		if n < 1_000_000_000_000 {
			return n * 1000
		}
		return n
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("CST", 8*3600)); err == nil {
		return t.UnixMilli()
	}
	return 0
}

func normaliseJumpURL(jump string) string {
	switch {
	case jump == "":
		return ""
	case strings.HasPrefix(jump, "//"):
		return "https:" + jump
	case strings.HasPrefix(jump, "http"):
		return jump
	default:
		return "https://www.bilibili.com" + jump
	}
}

// seasonArchive 是合集列表接口里的单条投稿元数据。
// 经实测确认：duration 单位为**秒**，pubdate 为 unix 秒。
type seasonArchive struct {
	BVID     string `json:"bvid"`
	AID      int64  `json:"aid"`
	Title    string `json:"title"`
	Pic      string `json:"pic"`
	Pubdate  int64  `json:"pubdate"`
	Duration int    `json:"duration"`
	State    int    `json:"state"`
	Stat     struct {
		View int64 `json:"view"`
	} `json:"stat"`
}

// SpaceSeasonVideos 通过「合集/系列列表」端点抓取 UP 主的投稿。
//
// 为什么不用 x/space/wbi/arc/search：该接口在机房 IP 上匿名态稳定返回
// code -403「访问权限不足」，必须配登录 Cookie 才能放行。但实测
// seasons_series_list **匿名即可返回完整列表**，包含标题、时长、bvid、
// 发布时间与播放量——这正是长视频检测所需的一切，且团综都收在合集里。
//
// minDurationSeconds 为 0 表示不过滤。调用方可据此只保留长视频，
// 过滤掉与抖音重复的短视频。
func (c *Client) SpaceSeasonVideos(ctx context.Context, uid string, minDurationSeconds int) ([]Dynamic, error) {
	seen := map[string]bool{}
	out := make([]Dynamic, 0, 32)
	page := 1
	// 合集数量可能很多，最多翻 10 页（每页 20 个合集）以防死循环。
	for page <= 10 {
		query := "mid=" + url.QueryEscape(uid) +
			"&page_num=" + strconv.Itoa(page) +
			"&page_size=20"
		var raw struct {
			ItemsLists struct {
				Page struct {
					Total int `json:"total"`
				} `json:"page"`
				SeasonsList []struct {
					Archives []seasonArchive `json:"archives"`
				} `json:"seasons_list"`
			} `json:"items_lists"`
		}
		endpoint := seasonListEndpoint + "?" + query
		if err := c.get(ctx, endpoint, "https://space.bilibili.com/"+uid, &raw); err != nil {
			// 已经有结果时不因后续分页失败而丢弃已抓到的内容。
			if len(out) > 0 {
				return out, nil
			}
			return nil, err
		}
		for _, season := range raw.ItemsLists.SeasonsList {
			for _, a := range season.Archives {
				bvid := strings.TrimSpace(a.BVID)
				if bvid == "" || seen[bvid] {
					continue
				}
				// state != 0 表示稿件在审核/不可见，跳过。
				if a.State != 0 {
					continue
				}
				// 只要视频够长的（长视频 / 团综）。
				if minDurationSeconds > 0 && a.Duration < minDurationSeconds {
					continue
				}
				seen[bvid] = true
				out = append(out, Dynamic{
					ID:      "av:" + bvid,
					Kind:    "video",
					Title:   strings.TrimSpace(a.Title),
					Cover:   strings.TrimSpace(a.Pic),
					URL:     "https://www.bilibili.com/video/" + bvid,
					Time:    a.Pubdate * 1000,
					Length:  FormatDuration(a.Duration),
					Seconds: a.Duration,
					View:    a.Stat.View,
				})
			}
		}
		// 没有更多合集了就停。
		if len(raw.ItemsLists.SeasonsList) == 0 || page*20 >= raw.ItemsLists.Page.Total {
			break
		}
		page++
	}
	return out, nil
}

// FormatDuration 把秒数格式化成 m:ss / h:mm:ss。
func FormatDuration(total int) string {
	if total <= 0 {
		return ""
	}
	h, m, s := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
