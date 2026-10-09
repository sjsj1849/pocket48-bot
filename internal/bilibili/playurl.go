package bilibili

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 视频直链提取。
//
// 为什么不直接用合集接口里的地址：合集只给 bvid 和时长，不给可播放地址。
// 要拿到能下载的视频，需要两步：
//
//  1. x/web-interface/view —— 拿 cid（分 P 标识）与精确时长。
//     **必须带 Cookie**：机房IP 匿名请求会拿到 412 反爬 HTML 页面（不是 JSON）。
//  2. x/player/playurl —— 拿播放地址。
//
// ★★★ 关于清晰度（2026-10-04 重写，勿再退回旧实现）
//
// 旧实现用 `platform=html5&fnval=0` 拿 durl 单文件，有两个致命问题：
//
//	a) 它是 B站给 HTML5播放器的**移动端低码率通道**。无论 qn 传 32（1080P）
//	   还是别的，接口永远返回 quality=16，即**流畅 360P**。实测同一稿件：
//	   旧参数 0.66MB / 360x640；DASH 最高轨 26.20MB / 2160x3840。
//	b) 旧注释写的「durl 按清晰度降序排列，取首个可用」在该通道下根本不成立，
//	   因为它只返回一档。
//
// 所以现在改用 `fnval=16&platform=pc` 拿 DASH。视频与音频分轨，且同一分辨率下
// 还有 avc1（H.264）/ hvc1（H.265）/ AV1 三种编码并存 —— 选轨规则见
// pickDASHTracks，其中编码兼容性优先于画质。
const (
	videoViewEndpoint    = "https://api.bilibili.com/x/web-interface/view"
	videoPlayURLEndpoint = "https://api.bilibili.com/x/player/playurl"
)

// PlaySource 是一个可用的视频源。
type PlaySource struct {
	// Path 是本地已合并好的 mp4 绝对路径（DASH 模式）。
	Path string
	// URL 是 CDN 直链，仅在降级到单文件 mp4 时有值。
	URL string
	// Seconds 是视频总时长（秒），0 表示未知。
	Seconds int
	// Size 是文件字节数，-1 表示未知。
	Size int64
	// Width/Height 是分辨率，0 表示未知。
	Width, Height int
	// Codec 是实际编码家族（avc1 / hvc1 / av01），空表示未探测。
	Codec string
	// BVID 回显，便于日志排查。
	BVID string
}

// LocalPath 返回可用于发送的本地路径，优先返回已合并的 DASH 文件。
func (p PlaySource) LocalPath() string {
	if p.Path != "" {
		return p.Path
	}
	return p.URL
}

// dashMaxBytes 是 DASH 视频体积上限。
//
// B 站最高档（2160x3840）实测 26.20MB，接近飞书 30MB 上限。
// 留余量后定为 25MB，超限时降一档而不是硬发。
const dashMaxBytes = 25 << 20

// dashCacheDir 是 DASH 合并结果的缓存目录。
const dashCacheDir = "/tmp/bot48/bilibili-dash"

// VideoPlayURL 返回指定稿件的最佳可用视频。
//
// 优先返回 DASH 合并后的本地文件（高画质）；失败时退回单文件 mp4 直链。
func (c *Client) VideoPlayURL(ctx context.Context, bvid string) (PlaySource, error) {
	bvid = strings.TrimSpace(bvid)
	if bvid == "" {
		return PlaySource{}, fmt.Errorf("BV 号为空")
	}
	info, err := c.VideoView(ctx, bvid)
	if err != nil {
		return PlaySource{}, err
	}
	if info.CID <= 0 {
		return PlaySource{}, fmt.Errorf("稿件 %s 没有可用的分 P（cid）", bvid)
	}

	src, dashErr := c.videoViaDASH(ctx, bvid, info)
	if dashErr == nil {
		return src, nil
	}
	log.Printf("[Bilibili] DASH 高画质不可用，回退单文件 bvid=%s: %v", bvid, dashErr)
	return c.videoViaDurl(ctx, bvid, info)
}

// dashTrack 是一条 DASH 轨道。
type dashTrack struct {
	URL       stringOrList `json:"baseUrl"`
	BackupURL stringOrList `json:"backupUrl"`
	ID        int          `json:"id"`
	Width     int          `json:"width"`
	Height    int          `json:"height"`
	Bandwidth int          `json:"bandwidth"`
	Codecs    string       `json:"codecs"`
}

// stringOrList 承接「字符串或字符串数组」两种形态的 JSON 字段。
type stringOrList []string

// UnmarshalJSON 兼容 B 站轨道地址字段的多种形态。
//
// 实测 baseUrl/backupUrl 都会返回数组（2026-10-04 在 BV1UMYx6BEP8 上
// 撞到 backupUrl 是 array）。第一版把 backupUrl 声明成 string，
// 碰上数组时 json.Unmarshal 整个 DASH 响应失败 → videoViaDASH 报错 →
// VideoPlayURL 静默降级回 quality=16 的 360P，
// 也就是「改了清晰度代码但线上依然 360P」的真正根因。
//
// 绝不能让地址字段的形态差异影响选轨。
func (s *stringOrList) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var list []string
		if err := json.Unmarshal(data, &list); err != nil {
			return err
		}
		*s = list
		return nil
	}
	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*s = []string{one}
	return nil
}

// firstNonEmpty 返回第一个非空地址。
func (l stringOrList) firstNonEmpty() string {
	for _, v := range l {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// videoViaDASH 取 DASH 高画质，下载并合并音视频。
func (c *Client) videoViaDASH(ctx context.Context, bvid string, info VideoViewInfo) (PlaySource, error) {
	raw := url.Values{
		"bvid": {bvid},
		"cid":  {fmt.Sprintf("%d", info.CID)},
		// qn=0 + fourk=1：请求接口允许的最高画质。
		"qn": {"0"}, "fnver": {"0"}, "fnval": {"16"}, "fourk": {"1"},
		// ★ platform 必须用 pc。用 html5 会把画质锁死在 360P。
		"platform": {"pc"}, "high_quality": {"1"},
	}
	var play struct {
		DASH struct {
			Video []dashTrack `json:"video"`
			Audio []dashTrack `json:"audio"`
		} `json:"dash"`
	}
	endpoint := videoPlayURLEndpoint + "?" + raw.Encode()
	if err := c.get(ctx, endpoint, c.videoReferer(bvid), &play); err != nil {
		return PlaySource{}, err
	}
	if len(play.DASH.Video) == 0 || len(play.DASH.Audio) == 0 {
		return PlaySource{}, fmt.Errorf("稿件 %s 未返回 DASH 结构", bvid)
	}

	video := pickBestVideoTrack(play.DASH.Video, info.Duration, dashMaxBytes)
	audio := pickBestAudioTrack(play.DASH.Audio)
	if video.URL.firstNonEmpty() == "" {
		return PlaySource{}, fmt.Errorf("稿件 %s 没有可用的视频轨", bvid)
	}
	if audio.URL.firstNonEmpty() == "" {
		return PlaySource{}, fmt.Errorf("稿件 %s 没有可用的音频轨", bvid)
	}
	//体积已超限且无法再降档 → 退回单文件路径。
	if est := estimateDashBytes(video.Bandwidth, info.Duration); est > dashMaxBytes {
		return PlaySource{}, fmt.Errorf("最低可用画质仍有 %.1fMB，超过上限", float64(est)/(1<<20))
	}

	local, size, err := c.mergeDASH(ctx, bvid, video, audio)
	if err != nil {
		return PlaySource{}, err
	}
	return PlaySource{
		Path:    local,
		Seconds: info.Duration,
		Size:    size,
		Width:   video.Width,
		Height:  video.Height,
		Codec:   codecFamily(video.Codecs),
		BVID:    bvid,
	}, nil
}

// pickBestVideoTrack 选视频轨。规则优先级从高到低：
//
//  1. 编码必须是 avc1（H.264）—— 这是兼容性要求，不是画质偏好。
//     飞书与 QQ 客户端对 H.265/AV1 支持不一致，发过去可能显示异常。
//  2. 体积在预算内。
//  3. 分辨率高者优，同分辨率比码率。
func pickBestVideoTrack(videos []dashTrack, seconds int, maxBytes int64) dashTrack {
	avc := make([]dashTrack, 0, len(videos))
	for _, v := range videos {
		if v.URL.firstNonEmpty() == "" {
			continue
		}
		if codecFamily(v.Codecs) != "avc1" {
			continue
		}
		avc = append(avc, v)
	}
	if len(avc) > 0 {
		// 只在预算内择优；全超限时取体积最小的一档（保证至少可用）。
		var affordable []dashTrack
		for _, v := range avc {
			if estimateDashBytes(v.Bandwidth, seconds) <= maxBytes {
				affordable = append(affordable, v)
			}
		}
		if len(affordable) > 0 {
			sort.Slice(affordable, func(i, j int) bool { return betterTrack(affordable[i], affordable[j]) })
			return affordable[0]
		}
		sort.Slice(avc, func(i, j int) bool { return avc[i].Bandwidth < avc[j].Bandwidth })
		return avc[0]
	}
	// 老稿件可能没有 avc1，退而取任意一条可用轨。
	for _, v := range videos {
		if v.URL.firstNonEmpty() != "" {
			return v
		}
	}
	return dashTrack{}
}

func pickBestAudioTrack(audios []dashTrack) dashTrack {
	var best dashTrack
	for _, a := range audios {
		if a.URL.firstNonEmpty() == "" {
			continue
		}
		if best.URL.firstNonEmpty() == "" || a.Bandwidth > best.Bandwidth {
			best = a
		}
	}
	return best
}

// betterTrack 判断 a 是否优于 b：先比像素，同像素比码率。
func betterTrack(a, b dashTrack) bool {
	pa, pb := a.Width*a.Height, b.Width*b.Height
	if pa != pb {
		return pa > pb
	}
	return a.Bandwidth > b.Bandwidth
}

// codecFamily 从 codecs 串取编码家族名。
// B站返回形如 "avc1.640032"、"hvc1.1.6.L153.90"、"av01.0.08M.08"。
func codecFamily(codecs string) string {
	c := strings.ToLower(strings.TrimSpace(codecs))
	switch {
	case strings.HasPrefix(c, "avc1"), strings.HasPrefix(c, "avc3"):
		return "avc1"
	case strings.HasPrefix(c, "hvc1"), strings.HasPrefix(c, "hev1"):
		return "hvc1"
	case strings.HasPrefix(c, "av01"):
		return "av01"
	}
	if i := strings.Index(c, "."); i > 0 {
		return c[:i]
	}
	return c
}

// estimateDashBytes 用码率与时长估算体积（字节）。误差约 5%，偏高估。
func estimateDashBytes(bandwidth, seconds int) int64 {
	if bandwidth <= 0 || seconds <= 0 {
		return 0
	}
	return int64(bandwidth) * int64(seconds) / 8
}

// mergeDASH 下载两条流并用 ffmpeg 免转码合并。
//
// -c copy 是关键：不重编码，CPU 占用接近零。
func (c *Client) mergeDASH(ctx context.Context, bvid string, video, audio dashTrack) (string, int64, error) {
	dir := filepath.Join(dashCacheDir, bvid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", 0, fmt.Errorf("创建缓存目录失败：%w", err)
	}
	out := filepath.Join(dir, fmt.Sprintf("%dx%d_%s.mp4", video.Width, video.Height, codecFamily(video.Codecs)))
	if st, err := os.Stat(out); err == nil && st.Size() > 0 {
		return out, st.Size(), nil
	}

	vPath := filepath.Join(dir, "v.m4s")
	aPath := filepath.Join(dir, "a.m4s")
	if err := c.downloadTrack(ctx, bvid, video, vPath); err != nil {
		return "", 0, err
	}
	if err := c.downloadTrack(ctx, bvid, audio, aPath); err != nil {
		return "", 0, err
	}
	defer os.Remove(vPath)
	defer os.Remove(aPath)

	if err := mergeTracks(ctx, vPath, aPath, out); err != nil {
		return "", 0, err
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", 0, fmt.Errorf("读取合并结果失败：%w", err)
	}
	return out, st.Size(), nil
}

// downloadTrack 下载一条轨道。主链失败自动切备用链（CDN 主链常被限速）。
func (c *Client) downloadTrack(ctx context.Context, bvid string, t dashTrack, dest string) error {
	candidates := make([]string, 0, 1+len(t.BackupURL))
	if u := t.URL.firstNonEmpty(); u != "" {
		candidates = append(candidates, u)
	}
	for _, u := range t.BackupURL {
		u = strings.TrimSpace(u)
		if u != "" && !containsString(candidates, u) {
			candidates = append(candidates, u)
		}
	}
	var lastErr error
	for _, u := range candidates {
		if err := c.downloadFile(ctx, bvid, u, dest); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("下载轨道失败：%w", lastErr)
}

// downloadFile 把 CDN 文件流式落盘。
//
// 必须校验下载字节数：DASH 的 .m4s 分片如果只下到一半，
// ffmpeg 仍可能「合并成功」但画面只有开头几帧，是很难发现的静默故障。
func (c *Client) downloadFile(ctx context.Context, bvid, rawURL, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", browserUA)
	// CDN 实测不校验 Referer，但仍带上以防风控策略变化。
	req.Header.Set("Referer", c.videoReferer(bvid))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	written, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(dest)
		return err
	}
	// 校验完整性：声明大小与实际下载必须一致。
	if cl := resp.ContentLength; cl > 0 && written != cl {
		os.Remove(dest)
		return fmt.Errorf("下载不完整：期望 %d 字节，实得 %d", cl, written)
	}
	if written == 0 {
		os.Remove(dest)
		return fmt.Errorf("下载到 0 字节")
	}
	return nil
}

// mergeTracks 用 ffmpeg 免转码合并音视频。
func mergeTracks(ctx context.Context, videoPath, audioPath, outPath string) error {
	// ffmpeg 合并大文件需要时间，给足超时。
	mctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(mctx, "ffmpeg",
		"-y", "-v", "error",
		"-i", videoPath,
		"-i", audioPath,
		"-c", "copy", // 不重编码
		"-movflags", "+faststart", // moov 前置，预览更快
		outPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg 合并失败：%w（%s）", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// videoViaDurl 是降级路径：拿单文件 mp4 直链。
//
// 画质明显低于 DASH（platform=pc 不带 html5 时通常给到 720P durl），
// 仅在 DASH 路径失败时使用。
func (c *Client) videoViaDurl(ctx context.Context, bvid string, info VideoViewInfo) (PlaySource, error) {
	raw := url.Values{
		"bvid": {bvid}, "cid": {fmt.Sprintf("%d", info.CID)},
		"qn": {"32"}, "fnver": {"0"}, "fnval": {"0"}, "fourk": {"0"},
		// ★ 绝不能用 html5 —— 那会把画质锁死在 360P。
		"platform": {"pc"}, "high_quality": {"1"},
	}
	var play struct {
		Durl []struct {
			URL  string `json:"url"`
			Size int64  `json:"size"`
		} `json:"durl"`
	}
	endpoint := videoPlayURLEndpoint + "?" + raw.Encode()
	if err := c.get(ctx, endpoint, c.videoReferer(bvid), &play); err != nil {
		return PlaySource{}, err
	}
	for _, d := range play.Durl {
		if strings.HasPrefix(d.URL, "http") {
			return PlaySource{URL: d.URL, Seconds: info.Duration, Size: d.Size, BVID: bvid}, nil
		}
	}
	return PlaySource{}, fmt.Errorf("稿件 %s 没有返回可播放地址（可能是付费/限制视频）", bvid)
}

// VideoViewInfo 是稿件的元信息。
type VideoViewInfo struct {
	BVID     string
	AID      int64
	CID      int64
	Title    string
	Duration int
	// Owner 是 UP 主昵称。
	Owner string
}

// VideoView 返回稿件元信息（cid / 时长 / 标题）。
func (c *Client) VideoView(ctx context.Context, bvid string) (VideoViewInfo, error) {
	bvid = strings.TrimSpace(bvid)
	if bvid == "" {
		return VideoViewInfo{}, fmt.Errorf("BV 号为空")
	}
	var raw struct {
		BVID  string `json:"bvid"`
		AID   int64  `json:"aid"`
		Title string `json:"title"`
		Owner struct {
			Name string `json:"name"`
		} `json:"owner"`
		Duration int `json:"duration"`
		Pages    []struct {
			CID      int64 `json:"cid"`
			Duration int   `json:"duration"`
		} `json:"pages"`
	}
	endpoint := videoViewEndpoint + "?bvid=" + url.QueryEscape(bvid)
	if err := c.get(ctx, endpoint, c.videoReferer(bvid), &raw); err != nil {
		return VideoViewInfo{}, err
	}
	info := VideoViewInfo{
		BVID:     raw.BVID,
		AID:      raw.AID,
		Title:    strings.TrimSpace(raw.Title),
		Duration: raw.Duration,
		Owner:    strings.TrimSpace(raw.Owner.Name),
	}
	if len(raw.Pages) > 0 {
		info.CID = raw.Pages[0].CID
		if raw.Pages[0].Duration > 0 {
			info.Duration = raw.Pages[0].Duration
		}
	}
	return info, nil
}

// videoReferer 返回带稿件路径的 Referer。
//
// view/playurl 接口确实校验 Referer（缺了会 412/403）；CDN 下载实测不校验，
// 但仍带上以防风控策略变化。
func (c *Client) videoReferer(bvid string) string {
	if bvid == "" {
		return "https://www.bilibili.com/"
	}
	return "https://www.bilibili.com/video/" + bvid
}

// CleanDASHCache 清理过期缓存，返回释放的字节数。目录不存在时返回 0 且无错误。
func CleanDASHCache(maxAge time.Duration) (int64, error) {
	entries, err := os.ReadDir(dashCacheDir)
	if err != nil {
		return 0, nil
	}
	cutoff := time.Now().Add(-maxAge)
	var freed int64
	for _, e := range entries {
		path := filepath.Join(dashCacheDir, e.Name())
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		var size int64
		_ = filepath.Walk(path, func(_ string, fi os.FileInfo, err error) error {
			if err == nil && fi != nil && !fi.IsDir() {
				size += fi.Size()
			}
			return nil
		})
		if os.RemoveAll(path) == nil {
			freed += size
		}
	}
	return freed, nil
}

// containsString 判断切片中是否已有某元素（去重候选下载链用）。
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
