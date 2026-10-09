package logic

// 「发链接 → 提取多媒体」功能的接入层。
//
// 触发方式（与用户约定）：
//   - 私聊：直接发链接即触发，无需指令
//   - 群聊：必须 @机器人，避免机器人对群里每条链接都做出反应
//
// 输出形态：正文 + 封面 + 视频。视频体积超限时只发正文与链接，
// 而不是发一个注定失败的大文件。
//
// 动图：链接之外额外说了「gif / 动图 / 表情包」之类关键词时，
// 视频会被转成循环 GIF 再发（体积自适应降级，见 extract.MakeGIF）。

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/extract"
	"pocket48-bot/internal/napcat"
)

// extractMaxVideoBytes 是内嵌视频的体积上限。
//
// 飞书文件上传上限 30MB，留 5MB 余量。X 同一条推文最高画质可达 34MB，
// 不做限制必然发送失败，所以这一层是真实生效的约束。
const extractMaxVideoBytes = 25 << 20

var (
	extractOnce sync.Once
	extractMu   sync.Mutex
	extractCli  *extract.Client
)

// extractClient 返回全局提取器（懒初始化）。
//
// 为什么要单例：每次调用都重建会重复读 B 站设置文件，而解析一个链接
// 可能耗时数十秒，B 站风控对高频新建连接也敏感。
func (b *Bot) extractClient() *extract.Client {
	extractOnce.Do(func() {
		dir := bilibili.Dir(b.cfg.ConfigPath())
		settings, err := bilibili.LoadSettings(dir)
		if err != nil {
			log.Printf("[Extract] 读取 B 站设置失败，B 站链接将不可用: %v", err)
		}
		cli := extract.New(
			extract.Options{
				Timeout:         90 * time.Second,
				MaxItems:        4,
				MaxVideoSeconds: -1, // 不做时长限制，时长由发送侧单独判断
			},
			&extract.BilibiliResolver{Client: bilibiliExtractAdapter{
				dir:    dir,
				cookie: settings.Cookie,
			}},
			&extract.XResolver{},
			&extract.DouyinResolver{
				Cookies:   b.douyinCookies(),
				ScriptDir: "sidecar/weibo-auth",
				Timeout:   60 * time.Second,
			},
			&extract.WeiboResolver{
				Cookie: b.cfg.WeiboMWeiboCookie,
			},
			// TikTok（2026-10-04 新增）。必须走 sidecar 浏览器通道：
			// TikTok CDN 拒绝一切外部客户端，视频只能由页面内 fetch 取得；
			// 列表接口又有 Android UA 硬要求。详见 extract.TiktokResolver 注释。
			&extract.TiktokResolver{
				Dir:     filepath.Join(storageRootOf(b.cfg.ConfigPath()), "tiktok"),
				Timeout: 180 * time.Second,
			},
		)
		extractMu.Lock()
		extractCli = cli
		extractMu.Unlock()
	})
	extractMu.Lock()
	defer extractMu.Unlock()
	return extractCli
}

// douyinCookies 从浏览器 profile 的 storage state 里读出抖音 cookie。
//
// 为什么不放 config.json：抖音签名依赖浏览器会话态cookie（uifid / verifyFp），
// 由 sidecar 的浏览器会话维护，写进 config 只会与实际会话脱节。
//
// 实测（2026-10-04）：msToken / uifid 缺失也能取到数据，
// s_v_web_id + ttwid + odin_tt 已足够签名通过。
func (b *Bot) douyinCookies() map[string]string {
	// ★ ConfigPath() 返回的是 **config.json 这个文件路径**，不是项目目录！
	//   直接 filepath.Join(cfgPath, "storage", ...) 会拼出
	//       config.json/storage/...  -> "not a directory"
	//   于是抖音链接提取永远拿不到 cookie（2026-10-04 线上一直报
	//   「抖音解析器未配置 Cookie」，而文件里其实有 57 个抖音 cookie）。
	//
	//   修法是先上跳一级拿项目根。这与 crossdedupe.storageRootOf 是同一件事，
	//   本项目已经栽过一次（去重索引曾被写到项目根）。
	//
	//   ★ 别再写 filepath.Dir(cfg.ConfigPath()) 以外的用法 ——
	//   ConfigPath 是文件，Dir 才是目录，语义别搞混。
	path := filepath.Join(
		filepath.Dir(b.cfg.ConfigPath()),
		"storage", "weibo-browser-profile", "weibo-storage-state.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[Extract] 读取抖音 cookie 失败，抖音链接将不可用: %v", err)
		return nil
	}
	var state struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		log.Printf("[Extract] 解析抖音 cookie 失败: %v", err)
		return nil
	}
	out := make(map[string]string, len(state.Cookies))
	for _, c := range state.Cookies {
		if strings.Contains(c.Domain, "douyin") && c.Name != "" {
			out[c.Name] = c.Value
		}
	}
	if len(out) == 0 {
		log.Printf("[Extract] 抖音 cookie 为空，请先在面板完成抖音登录")
	}
	return out
}

// bilibiliExtractAdapter 把 bilibili.Client 适配成 extract 需要的接口。
//
// Cookie 是必需的：机房IP 匿名请求 view 接口会拿到 412 反爬 HTML 而非 JSON。
type bilibiliExtractAdapter struct {
	dir    string
	cookie string
}

func (a bilibiliExtractAdapter) VideoView(ctx context.Context, bvid string) (extract.BilibiliView, error) {
	info, err := (&bilibili.Client{Dir: a.dir, Cookie: a.cookie}).VideoView(ctx, bvid)
	if err != nil {
		return extract.BilibiliView{}, err
	}
	return extract.BilibiliView{
		BVID:     info.BVID,
		Title:    info.Title,
		Owner:    info.Owner,
		Duration: info.Duration,
		CID:      info.CID,
	}, nil
}

func (a bilibiliExtractAdapter) VideoPlayURL(ctx context.Context, bvid string) (extract.BilibiliPlay, error) {
	src, err := (&bilibili.Client{Dir: a.dir, Cookie: a.cookie}).VideoPlayURL(ctx, bvid)
	if err != nil {
		return extract.BilibiliPlay{}, err
	}
	return extract.BilibiliPlay{
		URL:       src.URL,
		LocalPath: src.LocalPath(),
		Seconds:   src.Seconds,
		Size:      src.Size,
		Width:     src.Width,
		Height:    src.Height,
	}, nil
}

// extractLinkPattern 匹配消息里的 http/https 链接。
//
// 贪心到空白符为止（并排除中文标点），这样
// 「看看这个 https://x.com/a/status/1 谢谢」能正确只取到链接部分。
var extractLinkPattern = regexp.MustCompile(`https?://[^\s"'<>，。！？、）】]+`)

// tryHandleExtractLink 尝试把消息当作链接提取请求处理。
//
// atBot 表示这条消息是否@ 了机器人：群里必须为真，私聊不参与判断。
// 返回 true 表示已受理（不论成功失败），调用方应停止后续分派 —— 否则
// 一条含链接的群消息会既走这里、又走自然语言分支，机器人说两遍。
func (b *Bot) tryHandleExtractLink(event *napcat.Event, msg string, atBot bool) bool {
	link := extractFirstLink(stripAtSegments(msg))
	if link == "" {
		return false
	}
	if !atBot && event.MessageType == "group" {
		return false
	}
	// 同一链接正在处理中就不重复触发，避免连发多条时刷屏。
	if _, loaded := extractInFlight.LoadOrStore(link, time.Now()); loaded {
		return true
	}

	wantGIF := extractWantsGIF(msg)
	go func() {
		defer extractInFlight.Delete(link)
		b.runExtract(event, link, wantGIF)
	}()
	return true
}

// extractInFlight 记录正在处理的链接，避免并发重复。
var extractInFlight sync.Map

// extractGIFWords 是触发 GIF 转换的关键词。
var extractGIFWords = []string{"gif", "动图", "表情包", "gif化", "转gif"}

// extractWantsGIF 判断消息里是否有转 GIF 的意思。
func extractWantsGIF(msg string) bool {
	lower := strings.ToLower(msg)
	for _, w := range extractGIFWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// runExtract 执行提取并把结果发回原处。
//
// 放在独立 goroutine 里：解析 + 下载可能耗时数十秒，绝不能阻塞
// NapCat 的消息回调，否则会连累群里其它消息的处理。
func (b *Bot) runExtract(event *napcat.Event, link string, wantGIF bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	post, err := b.extractClient().Extract(ctx, link)
	if err != nil {
		b.reply(event, "🔗 "+friendlyExtractError(err))
		log.Printf("[Extract] 解析失败 %s: %v", link, err)
		return
	}

	b.sendExtracted(event, post, wantGIF)
	log.Printf("[Extract] 已处理 %s 平台=%s 媒体=%d", link, post.Platform, len(post.Items))
}

// friendlyExtractError 把内部错误翻译成用户能懂的话。
func friendlyExtractError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "还不支持"):
		return "这个链接的平台还不支持，目前支持 B 站、X、抖音、微博与 TikTok。"
	case strings.Contains(msg, "不存在") || strings.Contains(msg, "已删除"):
		return "没能取到内容，可能是帖子已删除或需要登录。"
	case strings.Contains(msg, "无法解析"):
		return "解析服务返回了看不懂的数据，稍后再试试。"
	case strings.Contains(msg, "超时") || strings.Contains(msg, "deadline"):
		return "取这个链接超时了，对方平台可能暂时不稳定。"
	case strings.Contains(msg, "412") || strings.Contains(msg, "风控"):
		return "被对方平台的风控挡住了，过一会儿再试。"
	default:
		return "提取失败：" + msg
	}
}

// sendExtracted 把提取结果发回原处：正文 → 封面 → 媒体。
func (b *Bot) sendExtracted(event *napcat.Event, post *extract.Post, wantGIF bool) {
	// 1. 正文。始终发 —— 即使后面媒体全失败，用户也拿到了文字和链接。
	b.reply(event, formatExtractedText(post))

	// 2. 封面。纯图片帖已经把图片当媒体发了，不必重复。
	if post.Cover != "" && !hasImageItem(post) {
		if local, err := downloadMediaFile(post.Cover); err == nil {
			b.sendEventContent(event, []napcat.MessageSegment{napcat.ImageSegment(local)})
		} else {
			log.Printf("[Extract] 封面下载失败 %s: %v", post.Cover, err)
		}
	}

	// 3. 媒体。
	for _, item := range post.Items {
		switch item.Kind {
		case "video":
			b.sendExtractedVideo(event, item, wantGIF)
		case "image":
			if local, err := resolveExtractMedia(item); err == nil {
				b.sendEventContent(event, []napcat.MessageSegment{napcat.ImageSegment(local)})
			} else {
				log.Printf("[Extract] 图片下载失败 %s: %v", item.URL, err)
			}
		}
	}
}

// sendExtractedVideo 发单个视频，按需先转 GIF。
func (b *Bot) sendExtractedVideo(event *napcat.Event, item extract.Item, wantGIF bool) {
	if wantGIF && b.sendExtractedGIF(event, item) {
		return
	}
	local, err := resolveExtractMedia(item)
	if err != nil {
		log.Printf("[Extract] 视频准备失败 %s: %v", item.URL, err)
		b.reply(event, "⚠️ 视频处理失败了，链接在这里：\n"+item.URL)
		return
	}
	b.sendEventContent(event, []napcat.MessageSegment{napcat.VideoSegment(local, "")})
}

// resolveExtractMedia 把 item 变成一个可发送的本地视频路径。
//
// ★ item.URL 有两种来源，必须都支持：
//  1. http(s) 直链（X / 微博 / 抖音）→ 需要下载
//  2. **本地临时文件路径**（B站 DASH）→ B站音视频是分轨的，
//     必须先合并才能播，合并产物在 /tmp/bot48/bilibili-dash/ 下，
//     解析器把这条本地路径塞进 item.URL。
//     以前这里无条件 downloadMediaFile，本地路径会报
//     `unsupported protocol scheme ""`，导致 B站链接提取在线上是坏的。
//
// 拿到文件后再探测编码：抖音大量视频是 H.265/HEVC，
// 飞书部分客户端播不了，遇到就转成 H.264 再发。
func resolveExtractMedia(item extract.Item) (string, error) {
	local := strings.TrimSpace(item.URL)
	if local == "" {
		return "", fmt.Errorf("空的媒体地址")
	}

	// 情形 2：已经是本地文件
	if isLocalFilePath(local) {
		if _, err := os.Stat(local); err != nil {
			return "", fmt.Errorf("本地文件不存在: %w", err)
		}
		return transcodeH265IfNeeded(local)
	}

	// 情形 1：远程直链。体积超限就别下了。
	if item.Size > extractMaxVideoBytes {
		return "", fmt.Errorf("视频 %.1fMB 超过上限 %dMB",
			float64(item.Size)/(1<<20), extractMaxVideoBytes>>20)
	}
	local, err := downloadMediaFile(local)
	if err != nil {
		return "", err
	}
	return transcodeH265IfNeeded(local)
}

// isLocalFilePath 判断 item.URL 是不是本地文件路径（而不是 http(s) 直链）。
func isLocalFilePath(raw string) bool {
	lower := strings.ToLower(raw)
	return !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")
}

// transcodeH265IfNeeded 探测视频编码，H.265/HEVC 时转成 H.264。
//
// 2026-10-04 实测：抖音下载下来是 hevc,2160,3840。
// 之前以为「bit_rate 里有 codec 字段，可以优先选 h264 档」——
// 探测发现**抖音根本不返回 codec 字段**，那个排序启发式完全没起作用。
// 所以只能在下载后看真实编码再决定要不要转。
//
// 非 H.265 时原样返回（不做无谓的转码，保留原画质）。
func transcodeH265IfNeeded(path string) (string, error) {
	codec, err := probeVideoCodec(path)
	if err != nil {
		// 探测失败不阻塞发送：原样发出去，让用户自己判断能不能播。
		log.Printf("[Extract] 编码探测失败，按原样发送 %s: %v", path, err)
		return path, nil
	}
	lower := strings.ToLower(codec)
	if !strings.Contains(lower, "hevc") && !strings.Contains(lower, "h265") {
		return path, nil
	}

	log.Printf("[Extract] 检测到 H.265，转码为 H.264 %s", path)
	out, err := transcodeToH264(path)
	if err != nil {
		log.Printf("[Extract] H.265 转 H.264 失败，按原样发送: %v", err)
		return path, nil
	}
	if info, statErr := os.Stat(out); statErr == nil && info.Size() > 0 {
		// 原文件是缓存，转换成功后删掉避免占磁盘
		_ = os.Remove(path)
	}
	return out, nil
}

// sendExtractedGIF 把视频转成 GIF 再发，成功返回 true。
//
// 注意：转 GIF 只取前几秒，这是体积换来的（飞书图片上限 10MB）。
// 用户要完整视频时应该直接说「视频」而不是「gif」。
func (b *Bot) sendExtractedGIF(event *napcat.Event, item extract.Item) bool {
	local, err := resolveExtractMedia(item)
	if err != nil {
		log.Printf("[Extract] GIF 前置准备失败 %s: %v", item.URL, err)
		return false
	}
	gifPath, err := extract.MakeGIF(context.Background(), local, extract.GifOptions{})
	if err != nil {
		log.Printf("[Extract] GIF 转换失败 %s: %v", item.URL, err)
		return false
	}
	defer os.Remove(gifPath)
	b.sendEventContent(event, []napcat.MessageSegment{napcat.ImageSegment(gifPath)})
	return true
}

// sendEventContent 把消息段发到事件对应的会话（群或私聊）。
func (b *Bot) sendEventContent(event *napcat.Event, content interface{}) {
	if event.MessageType == "group" && event.GroupID != 0 {
		b.sendGroup(event.GroupID, content)
		return
	}
	userID := event.UserID
	if userID == 0 {
		userID = event.Sender.UserID
	}
	if userID != 0 {
		b.sendPrivate(userID, content)
	}
}

// formatExtractedText 组织正文。
func formatExtractedText(post *extract.Post) string {
	var sb strings.Builder
	sb.WriteString("🔗 " + platformLabel(post.Platform))
	if post.Author != "" {
		sb.WriteString(" · " + post.Author)
	}
	if text := strings.TrimSpace(post.Text); text != "" {
		sb.WriteString("\n" + text)
	}
	if post.URL != "" {
		sb.WriteString("\n原链接：" + post.URL)
	}
	return sb.String()
}

func platformLabel(p extract.Platform) string {
	switch p {
	case extract.PlatformBilibili:
		return "B 站"
	case extract.PlatformX:
		return "X"
	case extract.PlatformDouyin:
		return "抖音"
	case extract.PlatformWeibo:
		return "微博"
	case extract.PlatformTiktok:
		return "TikTok"
	default:
		return "链接内容"
	}
}

// hasImageItem 判断媒体里是否已有图片（有的话封面就不必重复发）。
func hasImageItem(post *extract.Post) bool {
	for _, it := range post.Items {
		if it.Kind == "image" {
			return true
		}
	}
	return false
}

// extractFirstLink 从消息里取出第一条链接。
func extractFirstLink(msg string) string {
	return strings.TrimSpace(extractLinkPattern.FindString(msg))
}

// stripAtSegments 去掉 CQ 码形式的 @机器人 标记。
func stripAtSegments(msg string) string {
	msg = strings.ReplaceAll(msg, "[CQ:at,qq=3808515247]", " ")
	msg = strings.ReplaceAll(msg, "[CQ:at,all]", " ")
	return strings.TrimSpace(msg)
}

// extractHelpText 是帮助文案。
func extractHelpText() string {
	return "\n• 发链接提取内容（私聊直接发／群里 @机器人 + 链接）\n" +
		"  支持 B 站、X、抖音、微博与 TikTok，短链会自动跟随\n" +
		"• 链接后加「gif」→ 把视频转成循环动图\n" +
		"  例：https://x.com/xxx/status/123 gif"
}
