package bilibili

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// 动态流 (feed/space) 是新投稿最早的可见入口，用来抵消投稿列表的索引延迟。
// 这里的测试锁住两件事：
//  1. 真实响应能解析出视频投稿（字段映射不出错）；
//  2. 二态字段（pub_ts / stat.play 时而数字时而字符串）不会让整条响应解析失败。

func loadFeedSample(t *testing.T) []spaceFeedItem {
	t.Helper()
	raw, err := os.ReadFile("testdata/feed_space_items.json")
	if err != nil {
		t.Skipf("读不到动态流样本：%v", err)
	}
	var items []spaceFeedItem
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("样本无法解析：%v", err)
	}
	return items
}

func TestFeedDynamicsParsesRealSample(t *testing.T) {
	items := loadFeedSample(t)
	got := feedDynamics(items)
	if len(got) == 0 {
		t.Fatal("动态流样本里一条视频都没解析出来")
	}
	for _, d := range got {
		if d.Kind != "video" {
			t.Errorf("kind=%q 应为 video", d.Kind)
		}
		if !strings.HasPrefix(d.ID, "av:") {
			t.Errorf("ID=%q 应以 av: 开头（与投稿列表/合集对齐，才能互相去重）", d.ID)
		}
		bvid := strings.TrimPrefix(d.ID, "av:")
		if d.URL != "https://www.bilibili.com/video/"+bvid {
			t.Errorf("URL=%q 与 bvid 不匹配", d.URL)
		}
		if d.Title == "" {
			t.Errorf("%s 缺标题", d.ID)
		}
		if d.Time <= 0 {
			t.Errorf("%s 缺发布时间（动态流的 pub_ts 没解析出来）", d.ID)
		}
		if d.Seconds <= 0 {
			t.Errorf("%s 时长解析失败：Length=%q Seconds=%d", d.ID, d.Length, d.Seconds)
		}
		if d.Cover != "" && !strings.HasPrefix(d.Cover, "https://") {
			t.Errorf("%s 封面不是 https：%q", d.ID, d.Cover)
		}
		t.Logf("%s %s | %s | %ds | view=%d | %s", d.ID, d.Length, d.Title, d.Seconds, d.View,
			time.UnixMilli(d.Time).Format("2006-01-02 15:04"))
	}
}

func TestFeedDynamicsSkipsNonVideo(t *testing.T) {
	const body = `[
		{"type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":"1700000000"},
			"module_dynamic":{"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"bvid":"BV1aa1111111","title":"真视频","duration_text":"03:21","cover":"http://i0.hdslb.com/x.jpg","stat":{"play":"123"}}}}}},
		{"type":"DYNAMIC_TYPE_DRAW","modules":{"module_author":{"pub_ts":"1700000001"},
			"module_dynamic":{"major":{"type":"MAJOR_TYPE_DRAW","draw":{"id":1}}}}},
		{"type":"DYNAMIC_TYPE_FORWARD","modules":{"module_author":{"pub_ts":"1700000002"},
			"module_dynamic":{"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"bvid":"BV2bb2222222","title":"转发来的","duration_text":"01:00"}}}}},
		{"type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":"1700000003"},
			"module_dynamic":{"major":{"type":"MAJOR_TYPE_OPUS","opus":{"title":"图文"}}}}},
		{"type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":"1700000004"},
			"module_dynamic":{"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"bvid":"","title":"没 bvid","duration_text":"02:00"}}}}},
		{"type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"pub_ts":"1700000005"},
			"module_dynamic":{"desc":{"text":"文案正文"},"major":{"type":"MAJOR_TYPE_ARCHIVE","archive":{"bvid":"BV3cc3333333","title":"带文案","duration_text":"20:15","desc":"-"}}}}}
	]`
	var items []spaceFeedItem
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		t.Fatalf("解析失败（二态字段没兜住）：%v", err)
	}
	got := feedDynamics(items)
	if len(got) != 2 {
		t.Fatalf("应只保留 2 条自己的投稿，实际 %d 条：%+v", len(got), got)
	}
	if got[0].ID != "av:BV1aa1111111" || got[0].Seconds != 201 {
		t.Errorf("第一条解析错误：%+v", got[0])
	}
	if got[1].ID != "av:BV3cc3333333" || got[1].Seconds != 20*60+15 {
		t.Errorf("长视频时长应按 mm:ss 解析：%+v", got[1])
	}
	if got[1].Text != "文案正文" {
		t.Errorf("投稿文案没取到：%q", got[1].Text)
	}
	if got[0].View != 123 {
		t.Errorf("播放量（字符串）没解析：%d", got[0].View)
	}
}

// 二态字段是这套接口的真实坑：pub_ts / stat.play 都见过字符串形态，
// 用 int64 直解会让整条响应 Unmarshal 失败，等于整个来源静默失效。
func TestFlexibleIntSurvivesMixedTypes(t *testing.T) {
	var probe struct {
		Num    flexibleInt `json:"num"`
		Str    flexibleInt `json:"str"`
		Null   flexibleInt `json:"null"`
		Empty  flexibleInt `json:"empty"`
		Float  flexibleInt `json:"float"`
		Garbage flexibleInt `json:"garbage"`
	}
	body := `{"num":1791203401,"str":"1791203401","null":null,"empty":"","float":"12.9","garbage":"N/A"}`
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		t.Fatalf("二态字段让解析失败了：%v", err)
	}
	if probe.Num != 1791203401 || probe.Str != 1791203401 {
		t.Errorf("数字/字符串应解析成同一个值：%d / %d", probe.Num, probe.Str)
	}
	if probe.Null != 0 || probe.Empty != 0 || probe.Garbage != 0 {
		t.Errorf("异常值应归零：%d %d %d", probe.Null, probe.Empty, probe.Garbage)
	}
	if probe.Float != 12 {
		t.Errorf("浮点字符串应取整：%d", probe.Float)
	}
}

// 真实接口回归：登录态下动态流必须能拿到视频，且带发布时间。
//
// 跑法：POCKET48_BILI_TEST_UID=xxx go test ./internal/bilibili/ -run TestSpaceFeedVideosLive -v
func TestSpaceFeedVideosLive(t *testing.T) {
	uid := os.Getenv("POCKET48_BILI_TEST_UID")
	if uid == "" {
		t.Skip("未设置 POCKET48_BILI_TEST_UID")
	}
	raw, err := os.ReadFile("../../storage/bilibili/settings.json")
	if err != nil {
		t.Skip("读不到 settings.json")
	}
	var cfg struct {
		Cookie string `json:"cookie"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Cookie == "" {
		t.Skip("未配置 Cookie")
	}

	c := &Client{Dir: t.TempDir(), Cookie: cfg.Cookie}
	start := time.Now()
	got, err := c.SpaceFeedVideos(context.Background(), uid)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("动态流请求失败（匿名会 412，必须带 Cookie）：%v", err)
	}
	if len(got) == 0 {
		t.Fatal("登录态下动态流一条视频都没返回")
	}
	t.Logf("%.2fs 拿到 %d 条", elapsed.Seconds(), len(got))
	// 注意：列表第一条可能是**置顶动态**（常驻的旧投稿，时间可能几个月前），
	// 所以「最新」要按发布时间取，不能取 got[0]。
	newest := got[0]
	for _, d := range got {
		if d.Time > newest.Time {
			newest = d
		}
	}
	if newest.Time <= 0 {
		t.Errorf("最新一条缺发布时间：%+v", newest)
	}
	if age := time.Since(time.UnixMilli(newest.Time)); age > 30*24*time.Hour {
		t.Errorf("最新一条时间异常：%v 前", age)
	}
	t.Logf("最新：%s %s %s (%s)", newest.ID, newest.Length, newest.Title,
		time.UnixMilli(newest.Time).Format("2006-01-02 15:04:05"))
}
