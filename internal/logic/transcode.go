// 本文件负责视频编码探测与 H.265 → H.264 转码。
//
// 为什么需要（2026-10-04 端到端实测）：
// 抖音下载下来的视频是 **hevc,2160,3840**。飞书部分客户端播不了 H.265，
// 发过去用户看到的是一片绿或者转圈。
//
// 之前尝试过「在抖音接口返回的 bit_rate 里优先选 h264 档」，
// 但探测发现**抖音根本不返回 codec 字段**（bit_rate 里没有 codec_type），
// 那个排序启发式完全没起作用 —— 必须下载后看真实编码再决定。
//
// 为什么不无条件转码：
// 转码要重新编码 4K 视频，CPU 开销大、耗时以分钟计，还会掉画质。
// 非 H.265 时原样返回，保留原画质。转码失败也**不阻塞发送** ——
// 与其让用户什么都收不到，不如把原文件发过去让他自己判断。

package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// probeVideoCodec 用 ffprobe 读出视频流编码名。
func probeVideoCodec(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=codec_name",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("ffprobe 失败: %w", err)
	}
	var parsed struct {
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", fmt.Errorf("解析 ffprobe 输出失败: %w", err)
	}
	if len(parsed.Streams) == 0 {
		return "", fmt.Errorf("没有视频流")
	}
	return parsed.Streams[0].CodecName, nil
}

// transcodeToH264 把 H.265 视频转成 H.264。
//
// 参数选择：
//
//	-preset veryfast  转码速度优先（发消息是交互场景，等太久体验差）
//	-crf 26           画质够用且体积明显小于 veryslow 档
//	-vf scale         超过 1080p 降到 1080p：4K H.264 在手机上没意义，
//	                  却会让体积暴涨到接近飞书的 30MB 上限
//	-movflags +faststart  moov 前置，客户端可以边下边播
func transcodeToH264(src string) (string, error) {
	dir := filepath.Dir(src)
	base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	dst := filepath.Join(dir, base+"_h264.mp4")

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-i", src,
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-crf", "26",
		"-vf", "scale='min(1920,iw)':-2",
		"-c:a", "aac",
		"-b:a", "128k",
		"-movflags", "+faststart",
		dst,
	}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst)
		return "", fmt.Errorf("ffmpeg 转码失败: %w", err)
	}

	info, err := os.Stat(dst)
	if err != nil || info.Size() == 0 {
		_ = os.Remove(dst)
		return "", fmt.Errorf("转码产物无效")
	}
	log.Printf("[Extract] H.264 转码完成 %s -> %s (%.2fMB)",
		filepath.Base(src), filepath.Base(dst), float64(info.Size())/1048576.0)
	return dst, nil
}
