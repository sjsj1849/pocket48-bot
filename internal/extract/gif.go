package extract

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 视频转 GIF 动图。
//
// 为什么不是真正的「实况图」：苹果 Live Photo 是一张 HEIC 图 + 一段 MOV，
// 两者共享 asset identifier。而飞书图片上传接口文档明确写着
//「TIFF、HEIC 上传后会被转为 JPG 格式」—— 动画帧会被丢弃，只剩静态首帧。
// GIF 是飞书原生支持的（msg_type=image），会原地循环播放，观感最接近。
//
// 体积是主要约束：飞书图片上限 10MB。直接把一段 16 秒视频转 GIF 会得到
// 18MB，超限。Live 图的观感核心是「短 + 循环」，所以只取前几秒，
// 并准备阶梯参数在超限时自动降级。

// GifOptions 是 GIF 转换参数。
type GifOptions struct {
	// MaxBytes 是体积上限，默认 9MB（留 1MB 余量给飞书侧处理）。
	MaxBytes int64
	// FPS 默认 10。
	FPS int
	// Seconds 是截取的时长，默认 2.5 秒。
	Seconds float64
	// Width 默认 360。
	Width int
	// Colors 默认 64。
	Colors int
}

const (
	defaultGifMaxBytes = 9 << 20
	gifTempDir         = "/tmp/bot48/gifcache"
)

// gifLadder 是体积回退阶梯：从「观感最好」到「最保守」。
// 每级依次降低 帧率、时长、分辨率、色数。
var gifLadder = []struct {
	FPS     int
	Seconds float64
	Width   int
	Colors  int
}{
	{FPS: 10, Seconds: 2.5, Width: 360, Colors: 64},
	{FPS: 10, Seconds: 2.0, Width: 360, Colors: 64},
	{FPS: 8, Seconds: 2.0, Width: 320, Colors: 48},
	{FPS: 8, Seconds: 1.5, Width: 320, Colors: 48},
	{FPS: 6, Seconds: 1.5, Width: 280, Colors: 32},
	{FPS: 5, Seconds: 1.0, Width: 240, Colors: 32},
}

// MakeGIF 把本地视频转成循环 GIF，超限时自动降级参数。
//
// 返回 GIF 的本地路径。ctx 取消会终止 ffmpeg。
func MakeGIF(ctx context.Context, videoPath string, opts GifOptions) (string, error) {
	if strings.TrimSpace(videoPath) == "" {
		return "", fmt.Errorf("视频路径为空")
	}
	if _, err := os.Stat(videoPath); err != nil {
		return "", fmt.Errorf("找不到视频文件：%s", filepath.Base(videoPath))
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", fmt.Errorf("服务器上没有 ffmpeg，无法转换动图")
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultGifMaxBytes
	}

	if err := os.MkdirAll(gifTempDir, 0o755); err != nil {
		return "", fmt.Errorf("创建动图缓存目录失败：%w", err)
	}

	// 输出文件名与输入视频绑定，便于排查与清理。
	base := strings.TrimSuffix(filepath.Base(videoPath), filepath.Ext(videoPath))
	outPath := filepath.Join(gifTempDir, base+".gif")

	// 已有命中缓存就直接复用，避免重复转码。
	if info, err := os.Stat(outPath); err == nil && info.Size() <= opts.MaxBytes {
		return outPath, nil
	}

	lastErr := ""
	for _, step := range gifLadder {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		_ = os.Remove(outPath)
		runErr := runFFmpegGIF(ctx, videoPath, outPath, step.FPS, step.Seconds, step.Width, step.Colors)
		if runErr != nil {
			lastErr = runErr.Error()
			continue
		}
		info, err := os.Stat(outPath)
		if err != nil || info.Size() == 0 {
			lastErr = "ffmpeg 没有产出文件"
			continue
		}
		if info.Size() <= opts.MaxBytes {
			return outPath, nil
		}
		lastErr = fmt.Sprintf("%.1fMB 超过上限", float64(info.Size())/1048576)
	}
	// 全部阶梯都失败：清掉半成品，避免留下损坏文件被误用。
	_ = os.Remove(outPath)
	if lastErr == "" {
		lastErr = "未知原因"
	}
	return "", fmt.Errorf("转成动图失败：%s", lastErr)
}

// runFFmpegGIF 执行一次转码。
func runFFmpegGIF(ctx context.Context, src, dst string, fps int, seconds float64, width, colors int) error {
	// palettegen/paletteuse 两段滤镜是标准做法：
	// 先统计全局调色板，再按调色板量化。直接转 GIF 会得到 256 色的噪点画面。
	filter := fmt.Sprintf(
		"fps=%d,scale=%d:-1:flags=lanczos,split[s0][s1];"+
			"[s0]palettegen=max_colors=%d:stats_mode=diff[p];"+
			"[s1][p]paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle",
		fps, width, colors,
	)
	// 限时：ffmpeg 偶尔会在异常输入上挂住，不设上限会一直占着 CPU。
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "ffmpeg",
		"-y", "-loglevel", "error",
		"-i", src,
		"-t", fmt.Sprintf("%.2f", seconds),
		"-vf", filter,
		"-loop", "0",
		dst,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ffmpeg 超时（%s）", filepath.Base(src))
		}
		msg := strings.TrimSpace(string(output))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg 退出码错误：%s", msg)
	}
	return nil
}

// GIFCacheDir 暴露动图缓存目录，便于运维清理。
func GIFCacheDir() string { return gifTempDir }

// CleanGIFCache 清理动图缓存中超过 maxAge 的文件，返回释放的字节数。
// 目录不存在不算错误 —— 定时清理任务不该因为首次运行没缓存而中断。
func CleanGIFCache(maxAge time.Duration) (int64, error) {
	return CleanGIFCacheDir(GIFCacheDir(), maxAge)
}

// CleanGIFCacheDir 清理指定目录下的过期动图。
func CleanGIFCacheDir(dir string, maxAge time.Duration) (int64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	var freed int64
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil || info.IsDir() {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		size := info.Size()
		if os.Remove(path) == nil {
			freed += size
		}
	}
	return freed, nil
}
