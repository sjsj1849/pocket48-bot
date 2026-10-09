package logic

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pocket48-bot/internal/extract"
)

// TestIsLocalFilePath 覆盖「远程直链 vs 本地临时路径」的判定。
//
// 2026-10-04 端到端实测踩过：B站 DASH 合并产物是**本地路径**
// /tmp/bot48/bilibili-dash/<bvid>/<分辨率>_<编码>.mp4，
// 而发送侧无条件 downloadMediaFile(URL)，只认 http(s)，
// 于是报 `unsupported protocol scheme ""`，B站链接提取在线上是坏的。
func TestIsLocalFilePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"B站 DASH 本地路径", "/tmp/bot48/bilibili-dash/BV1x/1080x1920_avc1.mp4", true},
		{"普通本地路径", "/tmp/whatever.mp4", true},
		{"相对路径", "sidecar/x.mp4", true},
		{"Windows 风格路径", `C:\temp\a.mp4`, true},
		{"https 直链", "https://v26-webf.douyinvod.com/a/b.mp4", false},
		{"http 直链", "http://example.com/a.mp4", false},
		{"大写 HTTPS", "HTTPS://example.com/a.mp4", false},
		{"大写 HTTP", "HTTP://example.com/a.mp4", false},
		{"空串", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isLocalFilePath(tc.in); got != tc.want {
				t.Fatalf("isLocalFilePath(%q) = %v, 期望 %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveExtractMediaAcceptsLocalFile 验证已存在的本地文件能直接被接受，
// 不该再去下载（这是 B站 DASH 的正常路径）。
func TestResolveExtractMediaAcceptsLocalFile(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "already.mp4")
	// 造一个最小的合法 mp4 头，ffprobe 能识别出视频流
	if err := os.WriteFile(local, tinyMP4(t), 0o644); err != nil {
		t.Fatalf("写测试文件失败: %v", err)
	}

	got, err := resolveExtractMedia(fakeItem(local))
	if err != nil {
		t.Fatalf("本地文件应被直接接受，却报错: %v", err)
	}
	// 非 H.265 时应原样返回
	if got != local {
		t.Fatalf("非 H.265 应原样返回，期望 %q 实际 %q", local, got)
	}
}

// TestResolveExtractMediaRejectsMissingLocal 不存在的本地路径要明确报错，
// 而不是去尝试下载（那会得到一个莫名其妙的协议错误）。
func TestResolveExtractMediaRejectsMissingLocal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.mp4")
	if _, err := resolveExtractMedia(fakeItem(missing)); err == nil {
		t.Fatal("不存在的本地路径应当报错")
	}
}

// TestResolveExtractMediaRejectsOversizeRemote 体积超限要在下载前拦掉，
// 否则白白浪费一次大文件下载。
func TestResolveExtractMediaRejectsOversizeRemote(t *testing.T) {
	item := fakeItem("https://example.com/big.mp4")
	item.Size = extractMaxVideoBytes + 1
	if _, err := resolveExtractMedia(item); err == nil {
		t.Fatal("超限视频应在下载前被拦下")
	}
}

// TestProbeVideoCodecDetectsH265 是 H.265 转码分支的关键前置：
// 必须能真的探测出 hevc，否则转码逻辑永远不会触发。
func TestProbeVideoCodecDetectsH265(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.mp4")

	// 造一个 2 帧的 64x64 H.265 视频
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=1:duration=1",
		"-c:v", "libx265", "-pix_fmt", "yuv420p", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("本机 ffmpeg 不支持 libx265，跳过: %s", out)
	}

	codec, err := probeVideoCodec(src)
	if err != nil {
		t.Fatalf("探测 H.265 失败: %v", err)
	}
	if !strings.Contains(strings.ToLower(codec), "hevc") &&
		!strings.Contains(strings.ToLower(codec), "h265") {
		t.Fatalf("探测结果应为 hevc/h265，实际 %q", codec)
	}
}

// TestTranscodeH265ToH264IsIdempotentForH264 已经��� H.264 的不该被转码。
func TestTranscodeH265ToH264IsIdempotentForH264(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in264.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=64x64:rate=1:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("本机 ffmpeg 不支持 libx264，跳过: %s", out)
	}

	got, err := transcodeH265IfNeeded(src)
	if err != nil {
		t.Fatalf("H.264 视频不该报错: %v", err)
	}
	if got != src {
		t.Fatalf("H.264 应原样返回，期望 %q 实际 %q", src, got)
	}
}

// tinyMP4 造一个最小的合法 mp4（黑帧 H.264）。
//
// 落盘再读，而不是用 pipe:1 —— ffmpeg 写管道时偶发 exit 234
// （管道缓冲区在 movflags 收尾阶段被提前关闭），落盘稳定得多。
func tinyMP4(t *testing.T) []byte {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("没有 ffmpeg，跳过")
	}
	path := filepath.Join(t.TempDir(), "tiny.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:size=32x32:rate=1:duration=0.2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", path).CombinedOutput(); err != nil {
		t.Skipf("造测试视频失败: %v %s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回测试视频失败: %v", err)
	}
	return data
}

// fakeItem 构造一个只带 URL 的 extract.Item。
func fakeItem(url string) extract.Item {
	return extract.Item{Kind: "video", URL: url}
}
