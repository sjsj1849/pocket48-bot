package logic

// 锁死 2026-10-04 修复的采集侧清晰度问题。
//
// 旧实现对 X 和 Instagram 都是 `v.Bitrate <= 2500000` 硬编码，
// 把上限钉死在约 720P，高清档永远选不中。
// 这里用纯函数用例把正确行为固定下来。

import (
	"testing"

	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/xmonitor"
)

func vURL(url string, bitrate int64) videoVariant {
	return videoVariant{URL: url, Bitrate: bitrate}
}

// 旧代码的硬编码上限。
const legacyBitrateCap = 2500000

// TestPickBestMonitorVideoBeatsLegacyCap 是最关键的一条：
// 一条 10 秒短视频，5Mbps 的高清档体积完全在预算内，
// 旧代码会因为 Bitrate <= 2500000 而跳过它，退到 720P。
func TestPickBestMonitorVideoBeatsLegacyCap(t *testing.T) {
	variants := []videoVariant{
		vURL("https://v/720p.mp4", 2000000),
		vURL("https://v/1080p.mp4", 5000000), // 旧代码永远选不到这一档
	}
	got := pickBestMonitorVideo(variants, 10000) // 10 秒
	if got != "https://v/1080p.mp4" {
		t.Fatalf("应选1080p 高清档，实际 %q", got)
	}
}

// TestPickBestMonitorVideoRespectsDuration 验证时长参与决策：
// 同一条5Mbps 档，10 秒能选，10 分钟就超预算必须跳过。
func TestPickBestMonitorVideoRespectsDuration(t *testing.T) {
	variants := []videoVariant{
		vURL("https://v/720p.mp4", 2000000),
		vURL("https://v/1080p.mp4", 5000000),
	}
	// 5Mbps × 600s ÷ 8 = 375MB，远超 25MiB 预算。
	got := pickBestMonitorVideo(variants, 600000)
	if got != "https://v/720p.mp4" {
		t.Fatalf("长视频应降到 720p，实际 %q", got)
	}
}

func TestPickBestMonitorVideoAllOversizedPicksCheapest(t *testing.T) {
	// 全都超预算时取码率最低的一档，而不是返回空。
	variants := []videoVariant{
		vURL("https://v/high.mp4", 20000000),
		vURL("https://v/low.mp4", 3000000),
		vURL("https://v/mid.mp4", 9000000),
	}
	got := pickBestMonitorVideo(variants, 600000)
	if got != "https://v/low.mp4" {
		t.Fatalf("全超限应退到最低档，实际 %q", got)
	}
}

func TestPickBestMonitorVideoEmptyInputs(t *testing.T) {
	if got := pickBestMonitorVideo(nil, 10000); got != "" {
		t.Fatalf("nil 应返回空，实际 %q", got)
	}
	if got := pickBestMonitorVideo([]videoVariant{{URL: "", Bitrate: 100}}, 10000); got != "" {
		t.Fatalf("全空地址应返回空，实际 %q", got)
	}
	// 地址带空白应被清理后仍可用。
	variants := []videoVariant{vURL("  https://v/only.mp4  ", 1000)}
	if got := pickBestMonitorVideo(variants, 10000); got != "https://v/only.mp4" {
		t.Fatalf("应清理地址两端空白，实际 %q", got)
	}
}

// TestPickBestMonitorVideoUnknownBitrate 验证码率未知时的行为：
// 不参与择优，但兜底可用。
func TestPickBestMonitorVideoUnknownBitrate(t *testing.T) {
	variants := []videoVariant{
		vURL("https://v/nobitrate.mp4", 0),
		vURL("https://v/known.mp4", 1000000),
	}
	got := pickBestMonitorVideo(variants, 10000)
	if got != "https://v/known.mp4" {
		t.Fatalf("码率未知的档不应择优胜出，实际 %q", got)
	}
	// 只有码率未知的档时，仍应返回它而不是空。
	only := []videoVariant{vURL("https://v/nobitrate.mp4", 0)}
	if got := pickBestMonitorVideo(only, 10000); got != "https://v/nobitrate.mp4" {
		t.Fatalf("唯一可用档应被返回，实际 %q", got)
	}
}

// TestPickBestMonitorVideoUnknownDuration 用保守默认时长，
// 避免时长缺失时把最大的那档放进来。
func TestPickBestMonitorVideoUnknownDuration(t *testing.T) {
	variants := []videoVariant{
		vURL("https://v/small.mp4", 1000000),
		vURL("https://v/huge.mp4", 50000000), // 按 30 秒估也有 187MB
	}
	got := pickBestMonitorVideo(variants, 0)
	if got != "https://v/small.mp4" {
		t.Fatalf("时长未知时应保守选择，实际 %q", got)
	}
}

// TestXVideoURLUsesDurationMS 确认 xVideoURL 真的把 DurationMS 传了下去。
func TestXVideoURLUsesDurationMS(t *testing.T) {
	mk := func(ms int64) xmonitor.Media {
		return xmonitor.Media{
			Kind:       "video",
			DurationMS: ms,
			Variants: []xmonitor.Variant{
				{URL: "https://v/720p.mp4", Bitrate: 2000000},
				{URL: "https://v/1080p.mp4", Bitrate: 5000000},
			},
		}
	}
	if got := xVideoURL(mk(10000)); got != "https://v/1080p.mp4" {
		t.Errorf("短视频应选 1080p，实际 %q", got)
	}
	if got := xVideoURL(mk(600000)); got != "https://v/720p.mp4" {
		t.Errorf("长视频应选 720p，实际 %q", got)
	}
}

// TestInstagramVideoURLBeatsLegacyCap 确认 Instagram 侧同样修复。
func TestInstagramVideoURLBeatsLegacyCap(t *testing.T) {
	media := instagram.Media{
		Kind:       "video",
		DurationMS: 10000,
		Variants: []instagram.Variant{
			{URL: "https://ig/720p.mp4", Bitrate: 2000000},
			{URL: "https://ig/1080p.mp4", Bitrate: 5000000},
		},
	}
	if got := instagramVideoURL(media); got != "https://ig/1080p.mp4" {
		t.Fatalf("Instagram 应选 1080p，实际 %q", got)
	}
}

// TestMonitorVideoMaxBytesIsSane 预算本身要合理。
func TestMonitorVideoMaxBytesIsSane(t *testing.T) {
	if monitorVideoMaxBytes <= 0 {
		t.Fatalf("预算必须为正，实际 %d", monitorVideoMaxBytes)
	}
	// 不应超过飞书硬上限，留出余量。
	if monitorVideoMaxBytes >= 30<<20 {
		t.Fatalf("预算 %d 应低于飞书 30MB 上限", monitorVideoMaxBytes)
	}
	// 旧上限 2.5Mbps 下，10 秒视频只能到约 3MB，
	// 说明旧实现对短视频同样浪费了画质。
	legacyCeiling := int64(legacyBitrateCap) * 10 / 8
	if legacyCeiling >= monitorVideoMaxBytes {
		t.Fatalf("测试前提失效：旧上限 10 秒可到 %d，已不小于预算", legacyCeiling)
	}
}
