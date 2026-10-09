package monitor

import (
	"encoding/json"
	"testing"
	"time"
)

// TestWeiboSecondsTwoStates 锁住实测到的二态字段。
//
// 2026-10-04 抓 UID 7971304015 的真实时间线：
//   短视频 id=5350346069115769 -> media_info.duration = 21.479 (float)
//   长视频 id=5330863962202902 -> media_info.duration = "232"  (string)
//
// 如果把 Duration 声明成 float64，那条长视频会让**整条响应解析失败**，
// 于是整条微博变成空（页面完全推不出去），而不是只丢一个时长。
func TestWeiboSecondsTwoStates(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want float64
	}{
		{"float", `21.479`, 21.479},
		{"string", `"232"`, 232},
		{"int_string", `"21"`, 21},
		{"null", `null`, 0},
		{"empty_string", `""`, 0},
		{"garbage", `"N/A"`, 0},
		{"negative", `-5`, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s weiboSeconds
			if err := json.Unmarshal([]byte(c.raw), &s); err != nil {
				t.Fatalf("Unmarshal(%s) 报错 = %v（必须容错，不能让整条响应失败）", c.raw, err)
			}
			if float64(s) != c.want {
				t.Errorf("weiboSeconds(%s) = %v，期望 %v", c.raw, float64(s), c.want)
			}
		})
	}
}

// TestWeiboCardParseRealPayload 用真实响应片段验证整条卡片能解析出来，
// 且时长落在 page_info.media_info.duration（**不是** page_info.duration）。
//
// 这是最容易搞错的地方：page_info 顶层根本没有 duration 键，
// 键集合实测为 content1/content2/media_info/object_id/object_type/
// page_pic/page_title/page_url/play_count/title/type/url_ori/urls/
// video_orientation。
func TestWeiboCardParseRealPayload(t *testing.T) {
	// 取自 2026-10-04 实抓的 id=5350346069115769
	raw := []byte(`{
		"id": 5350346069115769,
		"bid": "Rl7JkCfqx",
		"mblogid": "Rl7JkCfqx",
		"created_at": "Sun Oct 04 17:22:31 +0800 2026",
		"text": "现在让大家和IAN的ChatGPT通话ᯓ★｜Hearts2Hearts 2026 TIMA BH2ND",
		"user": {"id": 7971304015, "screen_name": "Hearts2Hearts"},
		"page_info": {
			"type": "video",
			"object_type": 11,
			"video_orientation": "horizontal",
			"page_title": "正文",
			"page_pic": {"url": "https://wx1.sinaimg.cn/large/abc.jpg"},
			"media_info": {
				"duration": 21.479,
				"stream_url": "https://video.example.com/ld.mp4",
				"stream_url_hd": "https://video.example.com/hd.mp4"
			},
			"urls": {"mp4_720p_mp4": "https://video.example.com/720.mp4"}
		}
	}`)

	var card WeiboCard
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatalf("整条卡片解析失败：%v", err)
	}
	if card.User.ScreenName != "Hearts2Hearts" {
		t.Errorf("screen_name = %q，期望 Hearts2Hearts", card.User.ScreenName)
	}
	if got := weiboCardSeconds(card); got != 21 {
		t.Errorf("weiboCardSeconds = %d，期望 21（21.479 四舍五入）", got)
	}
	ms := weiboCardCreatedAtMS(card)
	if ms == 0 {
		t.Fatal("created_at 解析失败，返回 0")
	}
	// 2026-10-04 17:22:31 +0800 == 2026-10-04 09:22:31 UTC
	want := time.Date(2026, 10, 4, 9, 22, 31, 0, time.UTC).UnixMilli()
	if ms != want {
		t.Errorf("created_at ms = %d，期望 %d", ms, want)
	}
}

// TestWeiboCardParseStringDuration 确认长视频（字符串时长）不会让整条解析失败。
func TestWeiboCardParseStringDuration(t *testing.T) {
	// 取自 2026-10-04 实抓的 id=5330863962202902
	raw := []byte(`{
		"created_at": "Tue Aug 11 23:01:14 +0800 2026",
		"user": {"screen_name": "Hearts2Hearts"},
		"page_info": {
			"type": "video",
			"media_info": {"duration": "232", "stream_url": "https://v.example/x.mp4"}
		}
	}`)

	var card WeiboCard
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatalf("字符串时长导致整条解析失败：%v", err)
	}
	if got := weiboCardSeconds(card); got != 232 {
		t.Errorf("weiboCardSeconds = %d，期望 232", got)
	}
	if card.Text == "" {
		t.Log("（正文为空属正常，此用例只关心解析不炸）")
	}
}

// TestWeiboCardSecondsZeroForImage 确认图文微博取不到时长（返回 0），
// 从而在 logic 层被排除在登记之外（图文不会同时出现在 B站/抖音）。
func TestWeiboCardSecondsZeroForImage(t *testing.T) {
	raw := []byte(`{
		"created_at": "Sun Aug 09 23:03:20 +0800 2026",
		"text": "音源上架",
		"user": {"screen_name": "Hearts2Hearts"},
		"page_info": {"type": "video", "page_pic": {"url": "https://x/y.jpg"}}
	}`)

	var card WeiboCard
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if got := weiboCardSeconds(card); got != 0 {
		t.Errorf("图文微博时长 = %d，期望 0（没有 media_info 就是没有时长）", got)
	}
}

// TestCrossDecideWeiboPassthroughWithoutGate 确认未注入钩子时一律放行。
//
// 这是刻意的兜底方向：任何装配遗漏的最坏后果只能是「多推」，
// 绝不能静默变成「漏推」。
func TestCrossDecideWeiboPassthroughWithoutGate(t *testing.T) {
	m := &WeiboMonitor{}
	card := WeiboCard{
		CreatedAt: "Sun Oct 04 17:22:31 +0800 2026",
		Text:      "现在让大家和IAN的ChatGPT通话",
	}
	card.User.ScreenName = "Hearts2Hearts"

	if _, skip := m.crossDecideWeibo(card); skip {
		t.Error("未注入 gate 时应放行，却返回 skip=true（会导致漏推）")
	}
}

// TestCrossDecideWeiboPassesCleanTitle 确认传给 gate 的标题是**清洗后**的正文。
//
// 跨平台比对的是归一化指纹，各平台原始标题都混着「【团名】」前缀、
// 话题标签与统计后缀。不清洗的话同一句话在两个平台会算出两个指纹，
// 永远匹配不上 —— 这正是当初 B站【Hearts2Hearts】前缀导致漏判的同类问题。
func TestCrossDecideWeiboPassesCleanTitle(t *testing.T) {
	var gotTitle, gotAuthor string
	var gotSeconds int
	var gotMS int64

	m := &WeiboMonitor{}
	m.SetCrossDedupe(
		func(title, author string, seconds int, publishedAtMS int64) bool {
			gotTitle, gotAuthor, gotSeconds, gotMS = title, author, seconds, publishedAtMS
			return false
		},
		nil,
	)

	card := WeiboCard{
		CreatedAt: "Sun Oct 04 17:22:31 +0800 2026",
		Text:      `现在让大家和IAN的ChatGPT通话ᯓ★｜<a href="https://weibo.cn/sinaurl?u=x">全文链接</a>`,
	}
	card.User.ScreenName = "Hearts2Hearts"
	pi := card.PageInfo
	_ = pi

	title, skip := m.crossDecideWeibo(card)
	if skip {
		t.Error("gate 返回 false 时不应 skip")
	}
	if title == "" {
		t.Fatal("标题为空")
	}
	if gotTitle != title {
		t.Errorf("gate 收到的标题 %q 与返回值 %q 不一致", gotTitle, title)
	}
	// HTML 标签必须已被剥掉
	if gotTitle != "" && containsAngle(gotTitle) {
		t.Errorf("标题仍含 HTML：%q", gotTitle)
	}
	if gotAuthor != "Hearts2Hearts" {
		t.Errorf("author = %q，期望 Hearts2Hearts", gotAuthor)
	}
	if gotMS == 0 {
		t.Error("publishedAtMS 应解析出非 0 值")
	}
	_ = gotSeconds // 图文微博为 0，属正常
}

func containsAngle(s string) bool {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '<' {
			return true
		}
	}
	return false
}

// TestCrossDecideWeiboSkipSuppressesEverything 确认命中去重时返回 skip=true，
// 由 DispatchPerfectWeibo 负责「一条都不发」。
func TestCrossDecideWeiboSkipSuppressesEverything(t *testing.T) {
	m := &WeiboMonitor{}
	m.SetCrossDedupe(
		func(title, author string, seconds int, publishedAtMS int64) bool { return true },
		func(title, author string, seconds int, publishedAtMS int64) {
			t.Error("跳过的内容不应被登记")
		},
	)

	card := WeiboCard{Text: "重复内容", CreatedAt: "Sun Oct 04 17:22:31 +0800 2026"}
	card.User.ScreenName = "Hearts2Hearts"

	if _, skip := m.crossDecideWeibo(card); !skip {
		t.Error("gate 返回 true 时应 skip")
	}
}

// TestCrossRecordWeiboNilRec 安全：未注入 recorder 时不应 panic。
func TestCrossRecordWeiboNilRec(t *testing.T) {
	m := &WeiboMonitor{}
	card := WeiboCard{}
	card.User.ScreenName = "X"
	m.crossRecordWeibo("标题", card) // 不应 panic
}
