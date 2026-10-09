package tiktokmonitor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Client 拉起 sidecar/tiktok-monitor/collector.py 子进程，
// 通过 stdin 传一行 JSON、stdout 读一行 JSON。
//
// 为什么不直接用 Go 写：TikTok 的 X-Bogus 签名必须由真实浏览器计算
// （实测 TikTokApi 7.x 的 user.info() 直接返回空对象），
// 所以采集环节离不开 Playwright。Go 这边只负责编排。
type Client struct {
	Dir      string // storage/tiktok
	ProxyURL string
}

type Stats struct {
	Play    int64 `json:"play"`
	Like    int64 `json:"like"`
	Comment int64 `json:"comment"`
	Share   int64 `json:"share"`
}

// Video 是 TikTok 作品。注意这里**没有** playUrl / qualities：
// 那些是带 signature+expire 的短时效 CDN 地址（实测约 3 天过期），
// 留着没用还把响应撑到 90KB。下载一律走 Download() 重新取。
type Video struct {
	ID         string  `json:"id"`
	Desc       string  `json:"desc"`
	CreateTime int64   `json:"createTime"`
	Duration   float64 `json:"duration"`
	AuthorID   string  `json:"authorId"`
	AuthorName string  `json:"authorName"`
	AuthorNick string  `json:"authorNickname"`
	Stats      Stats   `json:"stats"`
	MusicTitle string  `json:"musicTitle"`
	Cover      string  `json:"cover"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
}

type User struct {
	Username string `json:"username"`
	UserID   string `json:"userId"`
	Nickname string `json:"nickname"`
}

// Detail 是单个作品的元数据（链接提取用）。
//
// 2026-10-04 新增：用户把 TikTok 链接发进 QQ 时需要正文与封面，
// 而 Video 只有列表接口的字段。Downloaded 为空表示视频没下载成
// （面板的「只读预览」就这么用），此时 VideoError 给出原因。
type Detail struct {
	ID       string  `json:"id"`
	Desc     string  `json:"desc"`
	Cover    string  `json:"cover"`
	Author   string  `json:"author"`
	AuthorID string  `json:"authorId"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	// Downloaded 是 sidecar 明确返回的布尔，区分「视频取到了」与
	// 「视频没取到但正文封面可用」——后者不是失败。
	Downloaded bool   `json:"downloaded"`
	VideoError string `json:"videoError,omitempty"`

	// dl 承载下载结果。
	//
	// ★ 这里**不能**声明自己的 Path/Bytes：response 同时内嵌了
	// *Detail 与 *Downloaded，两者都有 Path 时 Go 会直接报
	// `ambiguous selector r.Path`（实测踩过）。下载字段一律从
	// 同级的 *Downloaded 取，由 response 解析后回填进 dl。
	dl *Downloaded
}

// LocalPath 返回视频的本地路径（没下到视频时为空）。
func (d *Detail) LocalPath() string {
	if d == nil || !d.Downloaded || d.dl == nil {
		return ""
	}
	return d.dl.Path
}

// Bytes 返回视频体积字节数（未知时为 0）。
func (d *Detail) Bytes() int64 {
	if d == nil || d.dl == nil {
		return 0
	}
	return d.dl.Bytes
}

// Downloaded 是本地下载结果。
// Duration 由 <video> 元素的 duration 给出，实测与列表接口一致到小数点后 6 位。
type Downloaded struct {
	Path     string  `json:"path"`
	Bytes    int64   `json:"bytes"`
	Duration float64 `json:"duration"`
	Width    int     `json:"width"`
	Height   int     `json:"height"`
	Cached   bool    `json:"cached"`
}

type response struct {
	User   *User   `json:"user"`
	Videos []Video `json:"videos"`
	// Detail 是 detail 操作（链接提取）的产物。
	//
	// 内嵌而不是平铺：Call 已经把整个 data 解进 response，
	// 内嵌 *Detail 后 Detail 方法零成本拿到结果，不用再解一遍 JSON。
	*Detail
	*Downloaded
}

// UnmarshalJSON 手工解码 response。
//
// ★ 为什么必须手写（2026-10-04 线上踩坑，症状极具误导性）：
//
// sidecar 返回的是**扁平**的一层 JSON，detail 操作的 duration/width/height
// 与 download 操作的 path/bytes/duration/width/height 落在同一个层级。
// 上面同时内嵌 *Detail 与 *Downloaded，而这两个结构体**都声明了**
// `json:"duration"` / `json:"width"` / `json:"height"`。
//
// Go 的 encoding/json 规则是：同一深度出现多个同名候选字段时，
// **把它们全部丢弃**（不是「后者覆盖前者」）。于是这三个字段既没进
// Detail 也没进 Downloaded，静默变成零值。
//
// 实测症状：sidecar 明明返回了 duration=12.233333，
// Go 侧 Detail.Duration 却是 0，探针打印「媒体 时长 = 0 秒」。
// 而时长是**跨平台去重的判定维度**（团名 + 时长差 <= 3s），
// 为 0 等于二级判定永久关闭 —— 同一视频在多个平台被重复推送。
//
// 这种 bug 极难定位：sidecar 日志正常、ffprobe 正常、文件正常，
// 唯一异常是 Go 侧读不到字段，而且**没有任何报错**。
//
// 所以这里解两次：先按只有 *Detail 的形状解一次，
// 再单独把下载字段解进 Downloaded，两边都不丢。
func (r *response) UnmarshalJSON(data []byte) error {
	type detailOnly struct {
		User   *User   `json:"user"`
		Videos []Video `json:"videos"`
		*Detail
	}
	var first detailOnly
	if err := json.Unmarshal(data, &first); err != nil {
		return err
	}
	r.User = first.User
	r.Videos = first.Videos
	r.Detail = first.Detail

	// 下载字段单独解一次。Path 为空说明这次操作没下载（timeline / 详情预览），
	// 保持 nil，让调用方靠 r.Downloaded == nil 判断。
	var dl Downloaded
	if err := json.Unmarshal(data, &dl); err == nil && dl.Path != "" {
		r.Downloaded = &dl
	}
	return nil
}

// Error 把 sidecar 的错误码翻成人话。这些文案会直接进飞书告警，
// 所以要写清楚「下一步该做什么」，而不只是描述现象。
type Error struct{ Code string }

func (e *Error) Error() string {
	switch e.Code {
	case "rate_limited":
		return "TikTok 接口限流（返回空响应），需等待约 5 分钟后重试。建议把轮询间隔调大到 15 分钟以上"
	case "download_failed":
		return "TikTok 视频下载失败：CDN 拒绝非浏览器客户端，这是已知现象，不是登录态问题"
	case "download_truncated":
		return "TikTok 视频下载不完整，请重试"
	case "item_malformed":
		return "TikTok 作品数据结构异常"
	case "bad_request":
		return "TikTok 采集请求参数有误"
	case "internal":
		return "TikTok 采集模块内部错误，请查看 bot.log"
	default:
		return "TikTok 采集失败，请查看 bot.log"
	}
}

// limitedOutput 限制子进程 stderr 体积，防止 sidecar 日志刷爆内存。
//
// 现在只用在 stderr 上：stdout 走 readEnvelope 逐行读并单独限体积，
// 不再需要整体缓冲。
type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	// sidecar 的日志只有「启动 Chromium / 打开 URL / 失败原因」几行，
	// 256KB 足够宽松。
	if b.Len()+len(p) > 256<<10 {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// Call 执行一次采集。
//
// 超时分场景：
// - timeline 走列表接口，60 秒足够（实测约 8 秒）
// - download 走作品页 + 浏览器内 fetch，8.5MB 分 9 片，实测约 45 秒
//
// ★ 为什么不直接用 command.Run()（2026-10-04 线上踩坑）：
// Run() 会一直等到**子进程退出**为止。但 sidecar 拿到数据、打完 stdout 之后，
// 还要做浏览器收尾，而 Playwright 的收尾随时可能挂住 —— 实测过「数据 7 秒拿到、
// 进程 150 秒后才退出」的情况，于是每次调用都在 120s 超时，日志里最后一行
// 永远停在「打开主页」，看起来像 TikTok 慢，其实是收尾挂死。
//
// 现在改成：sidecar 的协议是「stdout 上一个完整 JSON 行」，
// 所以**读到能解析成 envelope 的那一行就立刻返回并 SIGKILL 掉进程**，
// 根本不关心它后面还收不收尾。这样超时语义回归「采集本身有没有超时」。
func (c Client) Call(ctx context.Context, request map[string]any, timeout time.Duration) (*response, error) {
	base := filepath.Dir(filepath.Dir(c.Dir))
	script := filepath.Join(base, "sidecar/tiktok-monitor/collector.py")
	return callScript(ctx, c, script, request, timeout)
}

// callScript 是 Call 的可注入实现，单独拆出来是为了让测试能塞进一个
// 「行为可控」的假 sidecar（尤其是会挂死的那种）。
func callScript(ctx context.Context, c Client, script string, request map[string]any, timeout time.Duration) (*response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	request["proxyURL"] = c.ProxyURL
	request["storageDir"] = c.Dir

	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	// 用系统 python3 而不是独立 venv：
	// 服务器的 playwright 装在系统环境里，且 Chromium 版本与之配套
	//（/root/.cache/ms-playwright/chromium-1228）。另建 venv 只会重复占用磁盘。
	command := exec.Command("python3", script)
	command.Stdin = bytes.NewReader(data)

	// sidecar 的日志走 stderr（stdout 是协议通道，绝不能混入日志）。
	stderr := &limitedOutput{}
	command.Stderr = stderr

	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("TikTok 采集管道创建失败：%w", err)
	}

	if err := command.Start(); err != nil {
		if _, ok := err.(*exec.Error); ok {
			return nil, fmt.Errorf("TikTok 采集环境未安装（需要 python3 + playwright）")
		}
		return nil, fmt.Errorf("TikTok 采集进程启动失败：%w", err)
	}

	// 拿到结果（或超时）后立刻杀掉子进程，避免它挂在收尾上白占一个 Chromium。
	// 用 Start 而不是 CommandContext，是为了精确控制「何时杀」——
	// CommandContext 只能等 ctx 超时时才杀，正好撞上我们最想避免的那个场景
	//（sidecar 早就把结果写完了，却还在慢慢收尾）。
	proc := command.Process
	// Wait 必须在别处调用，否则会产生僵尸进程。
	// 独立 goroutine 里调，返回值不关心（结果已经从 stdout 拿到了）。
	go func() { _ = command.Wait() }()
	// 看门狗：ctx 到点就把进程杀掉。
	// 因为 readEnvelope 是阻塞读 pipe，只有进程死了它才会返回 EOF。
	go func() {
		<-ctx.Done()
		if proc != nil {
			_ = proc.Kill()
		}
	}()

	envelope, overLimit, err := readEnvelope(stdout)
	if err != nil {
		if ctx.Err() != nil {
			// 超时时把 sidecar 的 stderr 带出来：它最后打印到哪一步
			// 是判断「是慢还是卡」的唯一线索。
			if msg := tail(stderr.String(), 400); msg != "" {
				return nil, fmt.Errorf("TikTok 采集超时（%s）：%s", timeout, msg)
			}
			return nil, &Error{Code: "timeout"}
		}
		// 没超时却读不到合法 JSON：把 stderr 端出来，
		// 否则排查时只能看到一句「未返回有效响应」。
		if msg := stderr.String(); msg != "" {
			return nil, fmt.Errorf("TikTok 采集模块未返回有效响应：%s", tail(msg, 500))
		}
		return nil, fmt.Errorf("TikTok 采集模块未返回有效响应")
	}
	if overLimit {
		return nil, fmt.Errorf("TikTok 响应超过大小限制")
	}
	if !envelope.OK {
		// ★ 未知错误码也要把 sidecar 的原文带出来。
		//   之前一律返回 &Error{code}，落到 default 分支只剩一句
		//   「请查看 bot.log」，线上排查时看不到任何有效信息。
		if envelope.Error.Message != "" && envelope.Error.Code != "internal" {
			return nil, fmt.Errorf("TikTok 采集失败（%s）：%s",
				envelope.Error.Code, tail(envelope.Error.Message, 300))
		}
		if envelope.Error.Code == "internal" {
			// internal 多半是 Python 侧 TypeError 之类，原文最有用。
			if envelope.Error.Message != "" {
				return nil, fmt.Errorf("TikTok 采集模块异常：%s", tail(envelope.Error.Message, 300))
			}
			// 消息为空时把 stderr 端出来 —— Python 的 traceback 只在 stderr。
			if msg := stderr.String(); msg != "" {
				return nil, fmt.Errorf("TikTok 采集模块异常：%s", tail(msg, 600))
			}
		}
		return nil, &Error{Code: envelope.Error.Code}
	}
	return &envelope.Data, nil
}

// envelopePayload 是 sidecar stdout 上那一个 JSON 行的结构。
type envelopePayload struct {
	OK    bool     `json:"ok"`
	Data  response `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// readEnvelope 从 stdout 逐行读，**一旦某一行能解析成带 ok 字段的 JSON 就返回**。
//
// 为什么逐行而不是 bytes.Buffer 一次性读：sidecar 协议保证 stdout 只有一个
// JSON 行（日志全走 stderr）。逐行读可以在拿到结果的那一刻就走，
// 不必等管道 EOF —— 而管道 EOF 要等子进程退出，那正是我们要避开的等待。
//
// 返回的第二个值表示「响应超限」，此时 envelope 不可信。
func readEnvelope(r io.Reader) (*envelopePayload, bool, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	var total int
	for {
		// 单行上限放宽到 4MB：download 走文件不进 stdout，
		// 这条限制只是为了防止 sidecar 日志误灌 stdout 时占内存。
		line, err := reader.ReadString('\n')
		total += len(line)
		if total > 4<<20 {
			return nil, true, fmt.Errorf("stdout 超过大小限制")
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var payload envelopePayload
			// 必须要求 `ok` 字段存在，否则 Python 的告警文本之类
			// 恰好是合法 JSON 时会被误当成响应。
			if json.Unmarshal([]byte(trimmed), &payload) == nil && hasOKField(trimmed) {
				return &payload, false, nil
			}
		}
		if err != nil {
			return nil, false, err
		}
	}
}

// hasOKField 判断 JSON 里是否真的带 "ok" 键。
func hasOKField(s string) bool {
	var probe map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &probe) != nil {
		return false
	}
	_, ok := probe["ok"]
	return ok
}

// Timeline 拉取用户最新作品。
func (c Client) Timeline(ctx context.Context, username string, limit int) ([]Video, error) {
	if username == "" {
		username = DefaultUser
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	r, err := c.Call(ctx, map[string]any{
		"operation": "timeline",
		"username":  username,
		"limit":     limit,
	}, TimelineTimeout)
	if err != nil {
		return nil, err
	}
	return r.Videos, nil
}

// Lookup 取用户信息（ID + 昵称）。
func (c Client) Lookup(ctx context.Context, username string) (*User, error) {
	if username == "" {
		username = DefaultUser
	}
	r, err := c.Call(ctx, map[string]any{
		"operation": "timeline",
		"username":  username,
		"limit":     1,
	}, TimelineTimeout)
	if err != nil {
		return nil, err
	}
	if r.User == nil {
		return nil, &Error{Code: "user_unavailable"}
	}
	return r.User, nil
}

// Download 下载单个作品。
func (c Client) Download(ctx context.Context, videoID string) (*Downloaded, error) {
	if videoID == "" {
		return nil, &Error{Code: "bad_request"}
	}
	r, err := c.Call(ctx, map[string]any{
		"operation": "download",
		"videoId":   videoID,
	}, DownloadTimeout)
	if err != nil {
		return nil, err
	}
	// 注意是 r.Downloaded.Path 而不是 r.Path：response 同时内嵌
	// *Detail 与 *Downloaded，直接 r.Path 会触发 ambiguous selector。
	if r.Downloaded == nil || r.Downloaded.Path == "" {
		return nil, &Error{Code: "download_failed"}
	}
	return r.Downloaded, nil
}

// Detail 取单个作品的元数据，并按需下载视频本体。
//
// withVideo=false 时只取元数据（面板「只读预览」用，省掉几十秒下载与磁盘占用）。
//
// ★ 视频下载失败**不算整体失败**：sidecar 会带回已取到的正文与封面，
// 这里把 VideoError 填上，让上层照常发文字+封面+链接 —— 这比整条失败友好得多。
// 只有正文与封面都没有、确实取不到内容时才返回错误。
func (c Client) Detail(ctx context.Context, videoID, username string, withVideo bool) (*Detail, error) {
	videoID = strings.TrimSpace(videoID)
	if videoID == "" {
		return nil, &Error{Code: "bad_request"}
	}
	if username == "" {
		username = DefaultUser
	}
	r, err := c.Call(ctx, map[string]any{
		"operation": "detail",
		"videoId":   videoID,
		"username":  strings.TrimPrefix(username, "@"),
		"withVideo": withVideo,
	}, DetailTimeout)
	if err != nil {
		return nil, err
	}
	if r.Detail == nil {
		return nil, &Error{Code: "detail_empty"}
	}
	// response 同时内嵌 *Detail 与 *Downloaded（后者带 path/bytes），
	// 这里把后者挂到 Detail 上，调用方就能通过 LocalPath() 拿本地路径。
	//
	// 时长/分辨率同理：sidecar 的 detail 结果里 duration/width/height 与
	// path/bytes 同层，Downloaded 那份才是 ffprobe 实测值，优先用它。
	if r.Detail != nil && r.Downloaded != nil {
		r.Detail.dl = r.Downloaded
		if r.Downloaded.Duration > 0 {
			r.Detail.Duration = r.Downloaded.Duration
		}
		if r.Downloaded.Width > 0 {
			r.Detail.Width = r.Downloaded.Width
			r.Detail.Height = r.Downloaded.Height
		}
	}
	// 只在「正文与封面都没有」时才算取不到内容。
	// 注意不能拿清洗前的 Desc 判空：OG description 清洗掉统计前缀后
	// 可能变空串（正文本来就很短的情况），那不该把封面也一起丢掉。
	if r.Detail.Cover == "" && strings.TrimSpace(r.Detail.Desc) == "" {
		return nil, &Error{Code: "detail_empty"}
	}
	return r.Detail, nil
}

// tail 取字符串末尾 n 个字符（用于截断超长错误信息）。
func tail(s string, n int) string {
	s = bytes.NewBufferString(s).String()
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
