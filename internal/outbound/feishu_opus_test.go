package outbound

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestIsOpusAudioOnlyRecognisesOpus 确认只有真正的 Opus/OGG 才跳过转码。
// 口袋48 给的是 .aac，必须走转码才能发成语音条。
func TestIsOpusAudioOnlyRecognisesOpus(t *testing.T) {
	opus := []string{
		"https://x/voice.opus",
		"https://x/voice.opus?token=abc",
		"https://x/VOICE.OPUS",
		"https://x/voice.ogg",
	}
	for _, s := range opus {
		if !isOpusAudio(s) {
			t.Errorf("%q 应识别为 Opus（免转码）", s)
		}
	}
	needsTranscode := []string{
		"https://x/voice.aac",
		"https://x/voice.mp3",
		"https://x/voice.m4a",
		"https://x/voice.wav",
		"https://x/voice",
	}
	for _, s := range needsTranscode {
		if isOpusAudio(s) {
			t.Errorf("%q 不应被当成 Opus，必须转码", s)
		}
	}
}

// TestTranscodeToOpusProducesPlayableOpus 端到端验证转码产物真的是 Opus。
// 跳过条件：服务器没装 ffmpeg（此时无法验证，且线上同样会降级）。
func TestTranscodeToOpusProducesPlayableOpus(t *testing.T) {
	if !opusToolAvailable() {
		t.Skip("ffmpeg 不可用，跳过转码验证")
	}
	// 造一个 2 秒 AAC 源，模拟口袋48的语音回复
	src := t.TempDir() + "/in.aac"
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "aac", "-b:a", "64k", src).CombinedOutput(); err != nil {
		t.Fatalf("准备 AAC 测试源失败: %v: %s", err, out)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	encoded, name, durationMS, err := transcodeToOpus(context.Background(), data, "in.aac")
	if err != nil {
		t.Fatalf("转码失败: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("转码产物为空")
	}
	if name != "voice.opus" {
		t.Errorf("上传文件名应为 voice.opus，实际 %q", name)
	}
	// 飞书不传 duration 就不显示时长，这里必须拿到合理值
	if durationMS < 1500 || durationMS > 3000 {
		t.Errorf("时长应在 1500~3000ms 之间，实际 %d", durationMS)
	}
	// 校验编码确实是 opus（OGG 容器）
	probe := t.TempDir() + "/out.opus"
	if err := os.WriteFile(probe, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	// ffprobe 的 default 输出是分行纯值：codec_name 一行、channels 一行
	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,channels",
		"-of", "csv=p=0", probe).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe 失败: %v: %s", err, out)
	}
	fields := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(fields) != 2 {
		t.Fatalf("ffprobe 输出应含 codec 与声道两列，实际 %q", out)
	}
	if fields[0] != "opus" {
		t.Errorf("转码结果应为 opus，实际 %q", fields[0])
	}
	if fields[1] != "1" {
		t.Errorf("飞书要求单声道，实际 %q", fields[1])
	}
}

// TestTranscodeToOpusRejectsEmpty 防御空载荷，避免把空文件喂给 ffmpeg。
func TestTranscodeToOpusRejectsEmpty(t *testing.T) {
	if _, _, _, err := transcodeToOpus(context.Background(), nil, "x.aac"); err == nil {
		t.Fatal("空载荷应报错")
	}
}