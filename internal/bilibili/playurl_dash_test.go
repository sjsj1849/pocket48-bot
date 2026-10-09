package bilibili

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ★ 以下用例锁死 2026-10-04 修复的清晰度问题。
//
// 旧实现用 platform=html5&fnval=0，qn 参数被完全无视，
// 永远返回 quality=16（360P）。这里用纯函数用例把正确行为固定下来，
// 防止有人日后「简化」回旧写法。

func mkTrack(width, height, bw int, codecs string) dashTrack {
	return dashTrack{
		URL:       stringOrList{"https://upos-x.akamaized.net/upgcxcode/75/31/41727233175/41727233175-1-30120.m4s"},
		Width:     width,
		Height:    height,
		Bandwidth: bw,
		Codecs:    codecs,
	}
}

// TestDashTrackURLAcceptsArray 锁死 2026-10-04 撞到的真实坑：
// B 站的 baseUrl / backupUrl 会返回数组而不是字符串。
//
// 第一版把 backupUrl 声明成 string，解析直接失败 → DASH 整体降级回
// quality=16 的 360P。表面上「已经改成DASH 了」，线上却仍然是 360P，
// 根因就是这条。任何对 dashTrack 的结构改动都必须先过这个用例。
func TestDashTrackURLAcceptsArray(t *testing.T) {
	payload := `{
		"id": 80,
		"baseUrl": ["https://cdn.example.com/v1.m4s", "https://cdn.example.com/v1-backup.m4s"],
		"backupUrl": ["https://backup.example.com/v1.m4s"],
		"width": 1080, "height": 1920,
		"bandwidth": 3549436,
		"codecs": "avc1.640032"
	}`
	var tr dashTrack
	if err := json.Unmarshal([]byte(payload), &tr); err != nil {
		t.Fatalf("数组形态的 baseUrl 不应导致解析失败: %v", err)
	}
	if tr.Height != 1920 || tr.Bandwidth != 3549436 || tr.Codecs != "avc1.640032" {
		t.Fatalf("字段解析错误: %+v", tr)
	}
	if got := tr.URL.firstNonEmpty(); got != "https://cdn.example.com/v1.m4s" {
		t.Fatalf("firstNonEmpty 应取主链, got %q", got)
	}
	if len(tr.BackupURL) != 1 {
		t.Fatalf("backupUrl 应解析出 1 条, got %d", len(tr.BackupURL))
	}
}

// TestDashTrackURLAcceptsStringAndNull 确认单字符串与 null 也不炸。
func TestDashTrackURLAcceptsStringAndNull(t *testing.T) {
	var one dashTrack
	if err := json.Unmarshal([]byte(`{"baseUrl":"https://a/1.m4s","backupUrl":null}`), &one); err != nil {
		t.Fatalf("字符串形态不应报错: %v", err)
	}
	if got := one.URL.firstNonEmpty(); got != "https://a/1.m4s" {
		t.Fatalf("firstNonEmpty = %q", got)
	}
	if got := one.BackupURL.firstNonEmpty(); got != "" {
		t.Fatalf("null 应解析为空, got %q", got)
	}
	// 全空数组不应 panic，且视为不可用。
	var empty dashTrack
	if err := json.Unmarshal([]byte(`{"baseUrl":[],"backupUrl":[]}`), &empty); err != nil {
		t.Fatalf("空数组不应报错: %v", err)
	}
	if empty.URL.firstNonEmpty() != "" {
		t.Fatal("空数组应视为无地址")
	}
}

// 实测某稿件的真实 DASH 轨道列表（15秒视频）。
func realTracks() []dashTrack {
	return []dashTrack{
		mkTrack(2160, 3840, 14457407, "avc1.640032"),     // 4Kavc1
		mkTrack(2160, 3840, 5086316, "hvc1.1.6.L153.90"), // 4K h265
		mkTrack(1080, 1920, 3549436, "avc1.640032"),      // 1080P avc1
		mkTrack(1080, 1920, 1676490, "hvc1.1.6.L120.90"), // 1080P h265
		mkTrack(1080, 1920, 2092780, "avc1.640028"),
		mkTrack(720, 1280, 879782, "avc1.640028"), // 720P
		mkTrack(720, 1280, 372970, "hvc1.1.6.L90.90"),
		mkTrack(480, 852, 458372, "avc1.64001F"), // 480P
		mkTrack(480, 852, 217560, "hvc1.1.6.L60.90"),
		mkTrack(360, 640, 293874, "avc1.64001E"), // 360P
		mkTrack(360, 640, 156400, "hvc1.1.6.L30.90"),
	}
}

func TestCodecFamily(t *testing.T) {
	cases := map[string]string{
		"avc1.640032":      "avc1",
		"avc3.640032":      "avc1",
		"hvc1.1.6.L153.90": "hvc1",
		"hev1.1.6.L120.90": "hvc1",
		"av01.0.08M.08":    "av01",
		"AVC1.640032":      "avc1", // 大小写不敏感
		"":                 "",
	}
	for in, want := range cases {
		if got := codecFamily(in); got != want {
			t.Errorf("codecFamily(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPickBestVideoTrackPrefersAVC1 是最关键的一条：
// 最高画质同时有hvc1 和 av01，但飞书/QQ 客户端兼容性优先，必须选 avc1。
//
// 注意这里刻意用宽松预算（64MiB），把「编码偏好」和「体积预算」两个变量隔离开：
// 真实轨道里4K avc1 估算 25.85MiB，用生产预算时它会被正确地排除掉，
// 那样就测不到编码优先级了。体积由下面那条用例单独负责。
func TestPickBestVideoTrackPrefersAVC1(t *testing.T) {
	got := pickBestVideoTrack(realTracks(), 15, 64<<20)
	if got.Codecs != "avc1.640032" {
		t.Fatalf("必须选 avc1（H.264），实际 %q", got.Codecs)
	}
	if got.Height != 3840 {
		t.Fatalf("预算充足时应取最高分辨率的 avc1，实际 %dx%d", got.Width, got.Height)
	}
}

// TestPickBestVideoTrackRespectsSizeBudget 验证体积预算生效。
// 4K avc1 档估算 25.85MiB > 25MiB 预算，应自动降到 1080P。
func TestPickBestVideoTrackRespectsSizeBudget(t *testing.T) {
	got := pickBestVideoTrack(realTracks(), 15, dashMaxBytes)
	if got.Height == 3840 {
		t.Fatalf("4K档（%.2fMiB）应超 %d 预算被跳过",
			float64(got.Bandwidth)*15/8/(1<<20), dashMaxBytes>>20)
	}
	// 降档后仍必须是 avc1，不能为了塞进预算就放弃编码兼容性。
	if codecFamily(got.Codecs) != "avc1" {
		t.Fatalf("降档后仍应保持 avc1，实际 %q", got.Codecs)
	}
}

// TestPickBestVideoTrackNeverPicksHEVCOrAV1 逐档验证不会选中非 H.264。
func TestPickBestVideoTrackNeverPicksHEVCOrAV1(t *testing.T) {
	tracks := []dashTrack{
		mkTrack(2160, 3840, 5086316, "hvc1.1.6.L153.90"),
		mkTrack(2160, 3840, 14457407, "av01.0.08M.08"),
		mkTrack(1080, 1920, 3549436, "avc1.640032"),
	}
	got := pickBestVideoTrack(tracks, 15, dashMaxBytes)
	if codecFamily(got.Codecs) != "avc1" {
		t.Fatalf("只能选 avc1，实际 %q", got.Codecs)
	}
}

func TestPickBestVideoTrackAllOversizedPicksSmallest(t *testing.T) {
	// 预算极小时应退到体积最小的一档，而不是空返回。
	tracks := realTracks()
	got := pickBestVideoTrack(tracks, 15, 1)
	if got.URL.firstNonEmpty() == "" {
		t.Fatal("应至少返回一条可用轨道")
	}
	// 最小 avc1 是 360x640(bw=293874)
	if got.Height != 640 {
		t.Fatalf("预算极小应退到最低档，实际 %dx%d", got.Width, got.Height)
	}
}

func TestPickBestVideoTrackNoAVCFallsBack(t *testing.T) {
	// 老稿件只有 h265，也要有结果而不是报错。
	tracks := []dashTrack{
		mkTrack(720, 1280, 372970, "hvc1.1.6.L90.90"),
	}
	got := pickBestVideoTrack(tracks, 15, dashMaxBytes)
	if got.URL.firstNonEmpty() == "" {
		t.Fatal("无 avc1 时应回退到任意可用轨")
	}
}

func TestPickBestAudioTrackPicksHighestBitrate(t *testing.T) {
	// 实测某稿件的 3 条音频轨。
	audios := []dashTrack{
		{URL: stringOrList{"https://x/a.m4s"}, Bandwidth: 116727},
		{URL: stringOrList{"https://x/b.m4s"}, Bandwidth: 236417},
		{URL: stringOrList{"https://x/c.m4s"}, Bandwidth: 66445},
	}
	got := pickBestAudioTrack(audios)
	if got.Bandwidth != 236417 {
		t.Fatalf("应选最大码率音频轨，实际 %d", got.Bandwidth)
	}
}

func TestBetterTrack(t *testing.T) {
	// 分辨率优先于码率：低分辨率高码率不应胜出。
	small := mkTrack(360, 640, 5000000, "avc1")
	big := mkTrack(1080, 1920, 1000000, "avc1")
	if !betterTrack(big, small) {
		t.Error("1080P 应优于 360P，即使码率更低")
	}
	// 同分辨率比码率。
	a := mkTrack(1080, 1920, 3549436, "avc1")
	b := mkTrack(1080, 1920, 2092780, "avc1")
	if !betterTrack(a, b) {
		t.Error("同分辨率下高码率应更优")
	}
}

func TestEstimateDashBytes(t *testing.T) {
	// 实测：14457407 bps × 15.2s ÷ 8 ≈ 26.20MB，与服务端声明一致。
	got := estimateDashBytes(14457407, 15)
	if got < 26_000_000 || got > 28_000_000 {
		t.Fatalf("估算应接近实测 27.47MB，实际 %d", got)
	}
	if estimateDashBytes(0, 15) != 0 {
		t.Error("码率未知时应返回 0")
	}
	if estimateDashBytes(1000, 0) != 0 {
		t.Error("时长未知时应返回 0")
	}
}

func TestDashMaxBytesUnderFeishuLimit(t *testing.T) {
	const feishuLimit = 30 << 20
	if dashMaxBytes >= feishuLimit {
		t.Fatalf("预算 %d 应小于飞书上限 %d", dashMaxBytes, feishuLimit)
	}
}

// TestVideoRefererUsesBVIDArg锁死 Referer 来自调用方传入的 bvid。
//
// 这里曾有一个 dashBVIDFromURL，试图从 m4s 路径反推 BV 号，
// 但该路径段其实是 aid（view 接口的 aid 字段），根本推不出 BV号，
// Referer 一直是拼错的。已改为直接透传 bvid。
func TestVideoRefererUsesBVIDArg(t *testing.T) {
	c := &Client{}
	if got := c.videoReferer("BV1UMYx6BEP8"); got != "https://www.bilibili.com/video/BV1UMYx6BEP8" {
		t.Errorf("videoReferer(bvid) = %q", got)
	}
	// bvid 未知时退到站点根，不能拼出畸形 URL。
	if got := c.videoReferer(""); got != "https://www.bilibili.com/" {
		t.Errorf("videoReferer(\"\") = %q", got)
	}
}

func TestPlaySourceLocalPathPrefersMergedFile(t *testing.T) {
	// DASH 路径应优先用本地文件。
	src := PlaySource{Path: "/tmp/bot48/bilibili-dash/BV1/1080x1920_avc1.mp4", URL: "https://x/a.mp4"}
	if got := src.LocalPath(); got != "/tmp/bot48/bilibili-dash/BV1/1080x1920_avc1.mp4" {
		t.Fatalf("应优先本地合并文件，实际 %q", got)
	}
	// 降级路径应退回直链。
	src2 := PlaySource{URL: "https://x/a.mp4"}
	if got := src2.LocalPath(); got != "https://x/a.mp4" {
		t.Fatalf("无本地文件时应退回直链，实际 %q", got)
	}
}

func TestCleanDASHCacheMissingDirIsNotError(t *testing.T) {
	// 目录不存在必须返回 0 且无错误，否则定时清理任务会中断。
	freed, err := CleanDASHCache(time.Hour)
	if err != nil {
		t.Fatalf("目录不存在不应报错: %v", err)
	}
	_ = freed
}

func TestMergeTracksRejectsGarbageInput(t *testing.T) {
	// 非视频输入必须报错，不能静默产出坏文件。
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.m4s")
	if err := os.WriteFile(bad, []byte("not a real mp4 stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.mp4")
	if err := mergeTracks(context.Background(), bad, bad, out); err == nil {
		t.Fatal("垃圾输入应报错")
	}
}
