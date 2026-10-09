package extract

// 锁死 2026-10-04 新增的抖音/微博解析器行为。
//
// 最重要的两条：
//  1. 微博的stream_url_hd 名字叫 hd，实际只有 540P；
//     真正的高清是 urls.mp4_720p_mp4（720x1564）。
//  2. 抖音实况图在数据层就是 mp4，要作为视频发出去。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------- 微博 ----------

// TestPickBestWeiboVideoPrefers720OverMisnamedHD 是最关键的一条。
//
// 实测样本（mid=5350064312812042）：
//
//	media_info.stream_url_hd → 540x1172  2.33MB← 名字叫 hd，其实是低清
//	urls.mp4_720p_mp4        → 720x1564  4.00MB  ← 真高清
//
// 现有监控代码优先取 stream_url_hd，所以长期只发 540P。
// 链接提取必须反过来。
func TestPickBestWeiboVideoPrefers720OverMisnamedHD(t *testing.T) {
	urls := weiboURLs{
		MP4720p: "https://f.video.weibocdn.com/o0/REAL720.mp4?label=mp4_720p",
		MP4HD:   "https://f.video.weibocdn.com/o0/FAKEHD.mp4?label=mp4_hd",
		MP4LD:   "https://f.video.weibocdn.com/o0/low.mp4",
	}
	mi := weiboMediaInfo{
		StreamURLHD: "https://f.video.weibocdn.com/o0/STREAM_HD_540.mp4?label=mp4_hd",
		StreamURL:   "https://f.video.weibocdn.com/o0/stream_540.mp4",
	}
	got := pickBestWeiboVideo(urls, mi)
	if !strings.Contains(got, "REAL720") {
		t.Fatalf("必须优先 urls.mp4_720p_mp4（真720P），实际 %q", got)
	}
}

// TestPickBestWeiboVideoFallsBackWhenNo720 确认没有 720p 时能降级。
func TestPickBestWeiboVideoFallsBackWhenNo720(t *testing.T) {
	mi := weiboMediaInfo{
		StreamURLHD: "https://x/hd.mp4",
		StreamURL:   "https://x/sd.mp4",
	}
	if got := pickBestWeiboVideo(weiboURLs{}, mi); got != "https://x/hd.mp4" {
		t.Fatalf("无 720p 时应退回 media_info，实际 %q", got)
	}
	if got := pickBestWeiboVideo(weiboURLs{}, weiboMediaInfo{}); got != "" {
		t.Fatalf("全空应返回空，实际 %q", got)
	}
	// 只有 stream_url 时也要能用。
	if got := pickBestWeiboVideo(weiboURLs{}, weiboMediaInfo{StreamURL: "https://x/s.mp4"}); got != "https://x/s.mp4" {
		t.Fatalf("应退回 stream_url，实际 %q", got)
	}
}

func TestExtractWeiboID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://weibo.com/6694957538/5350064312812042", "5350064312812042"},
		{"https://m.weibo.cn/status/5350064312812042", "5350064312812042"},
		{"https://weibo.com/u/1234/9876543210123456789", "9876543210123456789"},
		{"https://weibo.com/1234/", ""},
		{"完全不是微博", ""},
	}
	for _, c := range cases {
		if got := extractWeiboID(c.in); got != c.want {
			t.Errorf("extractWeiboID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWeiboResolverMatches(t *testing.T) {
	r := &WeiboResolver{}
	for _, host := range []string{"weibo.com/6694957538/535", "m.weibo.cn/status/1", "weibo.cn/xx"} {
		if !r.Matches(host) {
			t.Errorf("应匹配 %q", host)
		}
	}
	for _, host := range []string{"bilibili.com/x", "douyin.com/x", "x.com/y"} {
		if r.Matches(host) {
			t.Errorf("不应匹配 %q", host)
		}
	}
}

func TestCleanWeiboHTML(t *testing.T) {
	in := "前文<br />中段 &amp; 后文\u0006 https://t.cn/xxx"
	got := cleanWeiboHTML(in)
	if strings.Contains(got, "<br") || strings.Contains(got, "&amp;") {
		t.Fatalf("应清掉标签与实体，实际 %q", got)
	}
	if strings.Contains(got, "t.cn") {
		t.Fatalf("应截断不可见控制符之后的内容，实际 %q", got)
	}
	if !strings.Contains(got, "前文") || !strings.Contains(got, "中段") {
		t.Fatalf("正文丢失: %q", got)
	}
}

// TestWeiboResolvePicksVideo 用假服务器验证端到端选档。
func TestWeiboResolvePicksVideo(t *testing.T) {
	payload := map[string]interface{}{
		"ok": 1,
		"data": map[string]interface{}{
			"id":   "5350064312812042",
			"text": "视频正文 &amp; 更多",
			"user": map[string]interface{}{"screen_name": "测试博主"},
			"page_info": map[string]interface{}{
				"page_pic": "https://p1.jpg",
				"media_info": map[string]interface{}{
					"duration":      36,
					"stream_url_hd": "https://f/540.mp4",
				},
				"urls": map[string]interface{}{
					"mp4_720p_mp4": "https://f/720.mp4",
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "5350064312812042" {
			t.Errorf("应带上 mid 查询参数，实际 %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	res := &WeiboResolver{
		Cookie: "SUB=xxx",
		Client: srv.Client(),
	}
	// 把端点换成测试服务器，测完还原。
	origURL := weiboShowURL
	weiboShowURL = srv.URL + "?id="
	defer func() { weiboShowURL = origURL }()

	post, err := res.Resolve(context.Background(), "https://weibo.com/6694957538/5350064312812042")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if post.Author != "测试博主" {
		t.Errorf("作者 = %q", post.Author)
	}
	if len(post.Items) != 1 {
		t.Fatalf("应有 1 条视频，实际 %d", len(post.Items))
	}
	if !strings.Contains(post.Items[0].URL, "720.mp4") {
		t.Errorf("应选 720P，实际 %q", post.Items[0].URL)
	}
	if post.Items[0].Duration != 36 {
		t.Errorf("时长 = %d", post.Items[0].Duration)
	}
	if strings.Contains(post.Text, "&amp;") {
		t.Errorf("正文未清实体: %q", post.Text)
	}
}

// ---------- 抖音 ----------

func TestExtractDouyinID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://www.douyin.com/video/7675209447762131172", "7675209447762131172"},
		{"https://www.iesdouyin.com/share/video/7675209447762131172/?region=SG", "7675209447762131172"},
		{"https://www.douyin.com/note/7675209447762131172", "7675209447762131172"},
		{"随便一个链接", ""},
	}
	for _, c := range cases {
		if got := extractDouyinID(c.in); got != c.want {
			t.Errorf("extractDouyinID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDouyinResolverMatches(t *testing.T) {
	r := &DouyinResolver{}
	for _, host := range []string{"douyin.com/video/1", "v.douyin.com/abc", "iesdouyin.com/share/x"} {
		if !r.Matches(host) {
			t.Errorf("应匹配 %q", host)
		}
	}
	if r.Matches("bilibili.com/x") {
		t.Error("不应匹配 B站")
	}
}

// TestDouyinResolverMissingCookie 没有 cookie 必须给出可读错误，
// 而不是 panic 或空结果。
func TestDouyinResolverMissingCookie(t *testing.T) {
	r := &DouyinResolver{}
	_, err := r.Resolve(context.Background(), "https://www.douyin.com/video/7675209447762131172")
	if err == nil {
		t.Fatal("无 cookie 应报错")
	}
	if !strings.Contains(err.Error(), "Cookie") {
		t.Errorf("错误信息应点明缺 Cookie: %v", err)
	}
}

// TestDouyinLivePhotoAsVideo 确认实况图按视频发出。
// 抖音实况图数据层就是 mp4，是用户要的「Live 图」实现方式。
func TestDouyinLivePhotoAsVideo(t *testing.T) {
	p := &douyinFetchResult{}
	raw := `{"awemeId":"1","http":200,"status_code":0,"post":{
		"id":"1","nickname":"作者","desc":"正文","type":"note",
		"cover":"https://c.jpg","images":["https://i1.jpg"],
		"videoUrl":"","livePhotoVideos":["https://live1.mp4","https://live2.mp4"]}}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("解析样本失败: %v", err)
	}
	if len(p.Post.LivePhotoVideos) != 2 {
		t.Fatalf("实况图应有 2 条，实际 %d", len(p.Post.LivePhotoVideos))
	}
	for _, v := range p.Post.LivePhotoVideos {
		if !strings.HasSuffix(v, ".mp4") {
			t.Errorf("实况图应是 mp4: %q", v)
		}
	}
}

// TestDouyinFetchResultFields 锁定 Python 侧字段名不漂移。
func TestDouyinFetchResultFields(t *testing.T) {
	raw := `{"awemeId":"7675209447762131172","http":200,"status_code":0,"post":{
		"id":"7675209447762131172","nickname":"Hearts2Hearts","desc":"魔法少女",
		"type":"video","cover":"https://c.jpg","videoUrl":"https://v.mp4",
		"videoBitrate":4486238}}`
	var res douyinFetchResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.StatusCode != 0 {
		t.Errorf("status_code = %d", res.StatusCode)
	}
	if res.Post == nil {
		t.Fatal("post 不应为 nil")
	}
	if res.Post.VideoURL != "https://v.mp4" {
		t.Errorf("videoUrl = %q", res.Post.VideoURL)
	}
	// highest_bitrate_video 已取最高码率，4K 样本码率是 400万级。
	if res.Post.VideoBitrate < 1000000 {
		t.Errorf("videoBitrate = %d，应为百万级（最高码率档）", res.Post.VideoBitrate)
	}
}

// ---------- 平台注册 ----------

func TestSupportedPlatformsIncludesNewOnes(t *testing.T) {
	c := New(Options{},
		&DouyinResolver{},
		&WeiboResolver{},
	)
	got := c.SupportedPlatforms()
	want := map[Platform]bool{PlatformDouyin: false, PlatformWeibo: false}
	for _, p := range got {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("平台 %q 未出现在 SupportedPlatforms: %v", p, got)
		}
	}
}

func TestNewPlatformConstants(t *testing.T) {
	if PlatformDouyin != "douyin" || PlatformWeibo != "weibo" {
		t.Fatalf("平台常量不对: %q %q", PlatformDouyin, PlatformWeibo)
	}
}
