package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- B 站链接识别 ----------

func TestExtractBVID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://www.bilibili.com/video/BV1UMYx6BEP8", "BV1UMYx6BEP8"},
		{"https://www.bilibili.com/video/BV1UMYx6BEP8/?spm_id_from=333.999", "BV1UMYx6BEP8"},
		{"https://m.bilibili.com/video/BV18w7P6uEE9", "BV18w7P6uEE9"},
		{"https://b23.tv/abcDEF12", ""},
		{"BV1mEHq63Eb6", "BV1mEHq63Eb6"},
		{"https://www.bilibili.com/video/av12345", "av12345"},
		{"https://example.com/video/BV1UMYx6BEP8", "BV1UMYx6BEP8"},
	}
	for _, c := range cases {
		if got := extractBVID(c.in); got != c.want {
			t.Errorf("extractBVID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBilibiliMatches(t *testing.T) {
	r := &BilibiliResolver{}
	for _, in := range []string{
		"bilibili.com/video/BV1", "www.bilibili.com", "b23.tv/xxx", "bili2233.cn/xxx",
	} {
		if !r.Matches(in) {
			t.Errorf("应识别为 B 站: %q", in)
		}
	}
	for _, in := range []string{"x.com/foo/status/1", "douyin.com/video/1", "weibo.com/1"} {
		if r.Matches(in) {
			t.Errorf("不应识别为 B 站: %q", in)
		}
	}
}

// ---------- X 链接识别 ----------

func TestExtractTweetID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://x.com/Etoile_0618/status/2106288781729157547", "2106288781729157547"},
		{"https://twitter.com/a/status/2106288781729157547?s=20", "2106288781729157547"},
		{"https://mobile.twitter.com/a/status/2106288781729157547", "2106288781729157547"},
		{"https://x.com/i/status/2106288781729157547", "2106288781729157547"},
		{"https://fxtwitter.com/status/2106288781729157547", "2106288781729157547"},
		{"2106288781729157547", "2106288781729157547"},
	}
	for _, c := range cases {
		if got := extractTweetID(c.in); got != c.want {
			t.Errorf("extractTweetID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestXMatches(t *testing.T) {
	r := &XResolver{}
	for _, in := range []string{
		"x.com/a/status/1", "twitter.com/a/status/1", "t.co/abc", "mobile.twitter.com",
	} {
		if !r.Matches(in) {
			t.Errorf("应识别为 X: %q", in)
		}
	}
	for _, in := range []string{"bilibili.com/video/BV1", "douyin.com/x"} {
		if r.Matches(in) {
			t.Errorf("不应识别为 X: %q", in)
		}
	}
}

func TestPickXVideoURLPrefersHigherResolution(t *testing.T) {
	m := xMedia{
		Duration: 10,
		Formats: []xFormat{
			{URL: "https://video.twimg.com/amplify_video/1/vid/avc1/320x480/a.mp4", Container: "mp4"},
			{URL: "https://video.twimg.com/amplify_video/1/vid/avc1/720x1280/b.mp4", Container: "mp4"},
			{URL: "https://video.twimg.com/amplify_video/1/vid/avc1/480x854/c.mp4", Container: "mp4"},
		},
	}
	got, _ := pickXVideoURL(m, 0)
	if !strings.Contains(got, "720x1280") {
		t.Fatalf("应挑 720x1280，实际 %q", got)
	}
}

func TestPickXVideoURLSkipsM3U8(t *testing.T) {
	m := xMedia{
		Duration: 10,
		Formats: []xFormat{
			{URL: "https://video.twimg.com/x.m3u8", Container: "m3u8"},
			{URL: "https://video.twimg.com/y.m3u8"},
			{URL: "https://video.twimg.com/vid/avc1/480x854/z.mp4", Container: "mp4"},
		},
	}
	got, _ := pickXVideoURL(m, 0)
	if strings.Contains(got, "m3u8") {
		t.Fatalf("必须跳过 m3u8，实际 %q", got)
	}
	if !strings.Contains(got, "480x854") {
		t.Fatalf("应挑到唯一可用的 mp4，实际 %q", got)
	}
}

// TestPickXVideoURLDropsOversizedHighestQuality 是本轮最关键的回归用例。
//
// 背景：实测 fxtwitter 返回 6 档画质，最高是 34.19MB，而飞书文件
// 上传上限 30MB。若不做体积过滤，「挑最高画质」会必然发送失败。
// 这里要求必须自动降到预算内的最高档。
func TestPickXVideoURLDropsOversizedHighestQuality(t *testing.T) {
	m := xMedia{
		Duration: 10.758,
		Formats: []xFormat{
			// 25128000 bps × 10.758s ÷ 8≈ 33.8MB，超 25MB 预算
			{URL: "https://video.twimg.com/a/vid/avc1/2160x3240/max.mp4", Container: "mp4", Bitrate: 25128000},
			// 10368000 × 10.758 ÷ 8 ≈ 13.9MB，在预算内 → 应选这档
			{URL: "https://video.twimg.com/a/vid/avc1/1080x1620/ok.mp4", Container: "mp4", Bitrate: 10368000},
			{URL: "https://video.twimg.com/a/vid/avc1/720x1080/low.mp4", Container: "mp4", Bitrate: 2176000},
		},
	}
	got, size := pickXVideoURL(m, 25<<20)
	if !strings.Contains(got, "1080x1620") {
		t.Fatalf("应跳过超限的最高档并取预算内最高档，实际 %q", got)
	}
	if size <= 0 || size > 25<<20 {
		t.Fatalf("预估体积应落在预算内，实际 %d", size)
	}
	if strings.Contains(got, "2160x3240") {
		t.Fatal("34MB 的最高档绝不能被选中")
	}
}

func TestPickXVideoURLAllOversizedReturnsEmpty(t *testing.T) {
	// 全部超预算时返回空，让上层退化成「只发正文+封面」，
	// 而不是硬发一个注定失败的大文件。
	m := xMedia{
		Duration: 600,
		Formats: []xFormat{
			{URL: "https://video.twimg.com/a/vid/avc1/1920x1080/huge.mp4", Container: "mp4", Bitrate: 25000000},
		},
	}
	if got, _ := pickXVideoURL(m, 25<<20); got != "" {
		t.Fatalf("全部超预算时应返回空，实际 %q", got)
	}
}

func TestParseXResolutionHandlesWandH(t *testing.T) {
	// 真实URL 的分辨率是 WxH 形态。旧正则只匹配 720.mp4 这类形态，
	// 遇到 WxH 会全部返回 0 →「挑最高画质」静默退化成「挑第一档」。
	cases := map[string]int{
		"https://video.twimg.com/amplify_video/1/vid/avc1/720x1080/a.mp4":  720 * 1080,
		"https://video.twimg.com/amplify_video/1/vid/avc1/2160x3240/a.mp4": 2160 * 3240,
		"https://video.twimg.com/amplify_video/1/vid/avc1/320x480/a.mp4":   320 * 480,
	}
	for in, want := range cases {
		if got := parseXResolution(in); got != want {
			t.Errorf("parseXResolution(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeXPhotoURLDowngradesOriginal(t *testing.T) {
	got := normalizeXPhotoURL("https://pbs.twimg.com/media/HTsjGgibsAADWh0.jpg?name=orig")
	if strings.Contains(got, "orig") {
		t.Fatalf("4096x3072 原图应降为 large，实际 %q", got)
	}
	if !strings.Contains(got, "name=large") {
		t.Fatalf("应保留 name=large，实际 %q", got)
	}
	// 无参数时原样返回。
	if got := normalizeXPhotoURL("https://pbs.twimg.com/media/a.jpg"); !strings.HasSuffix(got, "a.jpg") {
		t.Fatalf("无参数时应原样返回，实际 %q", got)
	}
	// 非 http 直接判定无效。
	if got := normalizeXPhotoURL("ftp://x/a.jpg"); got != "" {
		t.Fatalf("非 http 应返回空，实际 %q", got)
	}
}

// ---------- Client 端到端（本地假服务器） ----------

type fakeBili struct {
	viewErr error
	playErr error
}

func (f fakeBili) VideoView(_ context.Context, bvid string) (BilibiliView, error) {
	if f.viewErr != nil {
		return BilibiliView{}, f.viewErr
	}
	return BilibiliView{BVID: bvid, Title: "测试标题", Owner: "测试UP", Duration: 42, CID: 1}, nil
}

func (f fakeBili) VideoPlayURL(_ context.Context, bvid string) (BilibiliPlay, error) {
	if f.playErr != nil {
		return BilibiliPlay{}, f.playErr
	}
	return BilibiliPlay{URL: "https://cdn.example.com/" + bvid + ".mp4", Seconds: 42, Size: 1024}, nil
}

func TestClientExtractBilibili(t *testing.T) {
	c := New(Options{}, &BilibiliResolver{Client: fakeBili{}})
	post, err := c.Extract(context.Background(), "https://www.bilibili.com/video/BV1UMYx6BEP8")
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if post.Platform != PlatformBilibili {
		t.Fatalf("平台应为 bilibili，实际 %s", post.Platform)
	}
	if post.Author != "测试UP" || !strings.Contains(post.Text, "测试标题") {
		t.Fatalf("正文/作者不对: %+v", post)
	}
	if len(post.Items) != 1 || post.Items[0].Kind != "video" {
		t.Fatalf("应有 1 个视频: %+v", post.Items)
	}
}

func TestClientExtractBilibiliPlayFailsStillReturnsText(t *testing.T) {
	// 取不到视频（长视频/付费视频）时仍应返回正文与封面。
	c := New(Options{}, &BilibiliResolver{Client: fakeBili{playErr: os.ErrPermission}})
	post, err := c.Extract(context.Background(), "https://www.bilibili.com/video/BV1UMYx6BEP8")
	if err != nil {
		t.Fatalf("取视频失败时不应整体失败: %v", err)
	}
	if post.Text == "" {
		t.Fatal("正文不应为空")
	}
	if len(post.Items) != 0 {
		t.Fatalf("取不到视频时不应有媒体项: %+v", post.Items)
	}
}

func TestClientRejectsUnsupported(t *testing.T) {
	c := New(Options{}, &BilibiliResolver{Client: fakeBili{}})
	if _, err := c.Extract(context.Background(), "https://weibo.com/u/123456"); err != ErrUnsupported {
		t.Fatalf("未支持平台应返回 ErrUnsupported，实际 %v", err)
	}
}

func TestClientRejectsInvalidURL(t *testing.T) {
	c := New(Options{}, &BilibiliResolver{Client: fakeBili{}})
	for _, in := range []string{"", "   ", "not a url at all", "://broken"} {
		if _, err := c.Extract(context.Background(), in); err == nil {
			t.Errorf("非法输入 %q 应报错", in)
		}
	}
}

func TestClientExtractXFromFakeServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "2106288781729157547") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"url":    "https://x.com/a/status/2106288781729157547",
				"text":   "测试正文 #标签",
				"author": map[string]string{"name": "某偶像"},
				"media": map[string]any{
					"videos": []map[string]any{{
						"url":           "https://video.twimg.com/x.m3u8",
						"thumbnail_url": "https://pbs.twimg.com/t.jpg",
						"duration":      33.0,
						"formats": []map[string]any{
							{
								"container": "mp4",
								"bitrate":   632000,
								"url":       "https://video.twimg.com/vid/avc1/720x1280/a.mp4",
							},
						},
					}},
				},
			},
		})
	}))
	defer server.Close()

	c := New(Options{}, &XResolver{
		Endpoint: server.URL + "/status/%s",
		Client:   server.Client(),
	})
	post, err := c.Extract(context.Background(), "https://x.com/a/status/2106288781729157547")
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if post.Author != "某偶像" || post.Text != "测试正文 #标签" {
		t.Fatalf("元信息不对: %+v", post)
	}
	if len(post.Items) != 1 || !strings.Contains(post.Items[0].URL, "720x1280") {
		t.Fatalf("应取到 720x1280 视频: %+v", post.Items)
	}
	if post.Items[0].Duration != 33 {
		t.Fatalf("时长应从浮点换算成整秒，实际 %d", post.Items[0].Duration)
	}
}

func TestClientExtractXReportsBusinessError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 404, "message": "Tweet not found"})
	}))
	defer server.Close()
	c := New(Options{}, &XResolver{Endpoint: server.URL + "/%s", Client: server.Client()})
	_, err := c.Extract(context.Background(), "https://x.com/a/status/2106288781729157547")
	if err == nil || !strings.Contains(err.Error(), "Tweet not found") {
		t.Fatalf("应把业务错误透传给用户，实际 %v", err)
	}
}

func TestMaxItemsTruncates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var photos []map[string]any
		for i := 0; i < 9; i++ {
			photos = append(photos, map[string]any{"url": "https://pbs.twimg.com/p.jpg"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"tweet": map[string]any{"text": "多图", "author": map[string]string{"name": "x"},
				"media": map[string]any{"photos": photos}},
		})
	}))
	defer server.Close()
	c := New(Options{MaxItems: 3}, &XResolver{Endpoint: server.URL + "/%s", Client: server.Client()})
	post, err := c.Extract(context.Background(), "https://x.com/a/status/2106288781729157547")
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if len(post.Items) != 3 {
		t.Fatalf("应截断到 3 张，实际 %d 张", len(post.Items))
	}
}

// ---------- GIF 转换 ----------

func TestMakeGIFRejectsMissingFile(t *testing.T) {
	if _, err := MakeGIF(context.Background(), "/tmp/does-not-exist-xyz.mp4", GifOptions{}); err == nil {
		t.Fatal("文件不存在时应报错")
	}
}

func TestMakeGIFRejectsEmptyPath(t *testing.T) {
	if _, err := MakeGIF(context.Background(), "  ", GifOptions{}); err == nil {
		t.Fatal("空路径应报错")
	}
}

func TestRunFFmpegGIFRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.mp4")
	// 写一个不是视频的 mp4
	if err := os.WriteFile(bad, []byte("this is not a video"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runFFmpegGIF(context.Background(), bad, filepath.Join(dir, "o.gif"), 10, 2, 320, 64); err == nil {
		t.Fatal("非视频输入应报错")
	}
}

func TestCleanGIFCacheMissingDirIsNotError(t *testing.T) {
	// 目录不存在时应返回 0 而非报错（定时清理任务不能因此中断）。
	freed, err := CleanGIFCacheDir(filepath.Join(t.TempDir(), "nope"), time.Hour)
	if err != nil || freed != 0 {
		t.Fatalf("目录不存在时应返回 0 且无错误，实际 freed=%d err=%v", freed, err)
	}
}

func TestCleanGIFCacheRemovesOldFiles(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "old.gif")
	fresh := filepath.Join(dir, "fresh.gif")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("xxxx"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// old 的 mtime 设为 2 小时前，fresh 保持现在
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	freed, err := CleanGIFCacheDir(dir, time.Hour)
	if err != nil {
		t.Fatalf("清理出错: %v", err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("过期文件应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("新文件应保留")
	}
	if freed != 4 {
		t.Fatalf("释放字节数应为 4，实际 %d", freed)
	}
}

func TestFormatSeconds(t *testing.T) {
	cases := map[int]string{0: "", 8: "0:08", 84: "1:24", 3600: "1:00:00", 3725: "1:02:05"}
	for in, want := range cases {
		if got := FormatSeconds(in); got != want {
			t.Errorf("FormatSeconds(%d) = %q, want %q", in, got, want)
		}
	}
}

// ---------- MaxVideoSeconds 生效（此前是死配置） ----------

// TestMaxVideoSecondsDropsOverlongVideo 覆盖一个长期存在的漏洞：
// Options.MaxVideoSeconds 被声明、被normalize 赋值，但从来没有
// 任何地方读它 —— 于是「超长视频不发本体」这个约定形同虚设。
func TestMaxVideoSecondsDropsOverlongVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text":   "长视频",
				"author": map[string]string{"name": "x"},
				"media": map[string]any{
					"videos": []map[string]any{{
						"url":      "https://video.twimg.com/a.mp4",
						"duration": 2400.0,
						"formats": []map[string]any{
							{"container": "mp4", "bitrate": 632000,
								"url": "https://video.twimg.com/a.mp4"},
						},
					}},
				},
			},
		})
	}))
	defer server.Close()

	c := New(Options{MaxVideoSeconds: 600}, &XResolver{Endpoint: server.URL + "/%s", Client: server.Client()})
	post, err := c.Extract(context.Background(), "https://x.com/a/status/2106288781729157547")
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if len(post.Items) != 0 {
		t.Fatalf("2400秒 超过 600 秒阈值，视频本体应被丢弃: %+v", post.Items)
	}
	if post.Text == "" {
		t.Fatal("正文必须保留，用户至少还能拿到文字和链接")
	}
}

func TestMaxVideoSecondsKeepsShortVideo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200,
			"tweet": map[string]any{
				"text":   "短视频",
				"author": map[string]string{"name": "x"},
				"media": map[string]any{
					"videos": []map[string]any{{
						"url":      "https://video.twimg.com/a.mp4",
						"duration": 84.0,
						"formats": []map[string]any{
							{"container": "mp4", "bitrate": 632000,
								"url": "https://video.twimg.com/a.mp4"},
						},
					}},
				},
			},
		})
	}))
	defer server.Close()

	c := New(Options{MaxVideoSeconds: 600}, &XResolver{Endpoint: server.URL + "/%s", Client: server.Client()})
	post, err := c.Extract(context.Background(), "https://x.com/a/status/2106288781729157547")
	if err != nil {
		t.Fatalf("提取失败: %v", err)
	}
	if len(post.Items) != 1 {
		t.Fatalf("84 秒远低于阈值，视频应保留: %+v", post.Items)
	}
}

func TestEstimateXBytes(t *testing.T) {
	// 实测：632000 bps × 10.758s ÷ 8 ≈ 0.85MB，实际 0.77MB（偏高，安全）
	if got := estimateXBytes(632000, 10.758); got < 800000 || got > 900000 {
		t.Fatalf("估算体积偏差过大: %d", got)
	}
	if got := estimateXBytes(0, 10); got != 0 {
		t.Fatalf("码率未知时应返回 0，实际 %d", got)
	}
	if got := estimateXBytes(632000, 0); got != 0 {
		t.Fatalf("时长未知时应返回 0，实际 %d", got)
	}
}
