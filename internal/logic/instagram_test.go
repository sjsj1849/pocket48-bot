package logic

import (
	"path/filepath"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/napcat"
	"strings"
	"testing"
	"time"
)

// newTestIndex 建一个落在临时目录的去重索引，测试之间互不干扰。
func newTestIndex(t *testing.T) *dedupe.Index {
	t.Helper()
	return dedupe.NewIndex(filepath.Join(t.TempDir(), "idx.json"), dedupeTTL, dedupeMaxSize)
}

// igTestNow 是去重测试的时间基准。
//
// ★ 必须用「当前时刻」而不是固定历史时刻：dedupe.sanitizePublishedAt
// 会把超出 TTL(14 天) 的时间戳静默替换成 now，所以任何写死的历史毫秒
// 在测试里都会被换成「现在」，导致「对方比我早」永远不成立。
// 这个坑很隐蔽 —— 表现是判定恒为放行，看起来像去重没生效。
func igTestNow() int64 { return time.Now().UnixMilli() }

func TestInstagramMediaCardFooterAndSeparateVideo(t *testing.T) {
	e := instagram.Event{ID: "123", Body: "hello", URL: "https://www.instagram.com/p/abc/", Time: 1789137110000, Author: instagram.User{Name: "nekomo"}, Media: []instagram.Media{{Kind: "photo", URL: "https://image"}, {Kind: "video", Cover: "https://cover", Variants: []instagram.Variant{{URL: "https://4k", Bitrate: 25000000}, {URL: "https://720", Bitrate: 2176000}}}}}
	groups := instagramMessageGroups(instagram.Subscription{AtAll: true}, e, false)
	if len(groups) != 2 {
		t.Fatalf("one card plus one standalone video expected: %d", len(groups))
	}
	if groups[0][0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("at-all must be a real OneBot segment")
	}
	if groups[0][1].(napcat.MessageSegment).Data["text"] != "\n" {
		t.Fatal("at-all must be on its own line")
	}
	last := groups[0][len(groups[0])-1].(napcat.MessageSegment)
	if !strings.HasPrefix(last.Data["text"], "\n\n"+e.URL+"\n\n") {
		t.Fatal("footer must be below media, with a blank line between URL and timestamp")
	}
	for _, segment := range groups[0] {
		if segment.(napcat.MessageSegment).Type == "video" {
			t.Fatal("video must not be mixed with text/images")
		}
	}
	video := groups[1][0].(napcat.MessageSegment)
	if len(groups[1]) != 1 || video.Type != "video" || video.Data["file"] != "https://720" {
		t.Fatalf("QQ receives a separate practical-resolution video: %+v", video)
	}
}

// igReelEvent 造一条带视频的 Reels，时长 18 秒。
func igReelEvent(id, body string) instagram.Event {
	return instagram.Event{
		ID:   id,
		Kind: "reel",
		Body: body,
		URL:  "https://www.instagram.com/reel/" + id + "/",
		Time: 1789137110000,
		Author: instagram.User{
			Username: "hearts2hearts",
			Name:     "Hearts2Hearts",
		},
		Media: []instagram.Media{{
			Kind:       "video",
			Cover:      "https://cover",
			DurationMS: 18000,
			Variants:   []instagram.Variant{{URL: "https://720", Bitrate: 2176000}},
		}},
	}
}

func TestInstagramSkipVideoWhenOtherPlatformSentFirst(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 抖音在 4 秒前先发了同名同长的一条。
	ix.Record("同一条舞蹈翻跳", base, "douyin", 18)

	e := igReelEvent("a1", "同一条舞蹈翻跳")
	if !instagramShouldSkipVideo(ix, e, base+4000) {
		t.Fatal("抖音已先发同一条视频本体，Instagram 必须跳过，否则两边都发")
	}
}

func TestInstagramKeepsVideoWhenItIsFirst(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 对方登记在我方采集时刻**之后**（523 秒），即我方才是首发。
	ix.Record("同一条舞蹈翻跳", base+6000, "tiktok", 18)

	e := igReelEvent("a2", "同一条舞蹈翻跳")
	if instagramShouldSkipVideo(ix, e, base+5477) {
		t.Fatal("我方是首发，绝不能被吞掉 —— 首发被吞这条片子就永远发不出去")
	}
}

func TestInstagramSkipVideoIgnoresSamePlatformHistory(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 同平台更早发过：Instagram 自己的补发窗口管这个，不该由跨平台去重吞掉。
	ix.Record("同一条舞蹈翻跳", base, "instagram", 18)

	e := igReelEvent("a3", "同一条舞蹈翻跳")
	if instagramShouldSkipVideo(ix, e, base+4000) {
		t.Fatal("跨平台去重只能拦别的平台，同平台历史由 cursor 管")
	}
}

func TestInstagramSkipVideoFallsBackToIDWhenBodyEmpty(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	// 对方发的是完全无关的内容。
	ix.Record("另一支完全不同的编舞", base, "douyin", 18)

	// 正文为空的 Reels 必须用 id 当键，绝不能用空标题去撞。
	e := igReelEvent("a4", "")
	if instagramShouldSkipVideo(ix, e, base+4000) {
		t.Fatal("空正文的 Reels 不能被别的平台同名内容误判")
	}
}

func TestInstagramSkipVideoFalseForPhotoOnly(t *testing.T) {
	ix := newTestIndex(t)
	base := igTestNow()
	ix.Record("同一条舞蹈翻跳", base, "douyin", 18)

	e := instagram.Event{
		ID:     "a5",
		Kind:   "post",
		Body:   "同一条舞蹈翻跳",
		Author: instagram.User{Username: "hearts2hearts"},
		Media:  []instagram.Media{{Kind: "photo", URL: "https://image"}},
	}
	if instagramShouldSkipVideo(ix, e, base+4000) {
		t.Fatal("纯图文没有视频本体可跳过，必须照常推送")
	}
}

func TestInstagramMessageGroupsSkipVideoKeepsCardOnly(t *testing.T) {
	e := igReelEvent("a6", "翻跳")
	groups := instagramMessageGroups(instagram.Subscription{}, e, true)

	if len(groups) != 1 {
		t.Fatalf("跳过视频本体时只应剩一张图文卡片，实际 %d 段", len(groups))
	}
	text := ""
	for _, segment := range groups[0] {
		s := segment.(napcat.MessageSegment)
		if s.Type == "video" {
			t.Fatal("去重命中后不得再出现视频段")
		}
		if s.Type == "text" {
			text += s.Data["text"]
		}
	}
	// ★ 2026-10-09（用户要求）：正文不再写「为什么没有视频」。
	//   来源与内容类型改到飞书卡片底栏，视频占位说明一律去掉。
	//   原来这里断言必须出现「已在其它平台发布」，现在反过来断言它不该出现。
	for _, banned := range []string{"已在其它平台发布", "[视频]", "视频单独发送"} {
		if strings.Contains(text, banned) {
			t.Fatalf("正文不该再包含 %q: %q", banned, text)
		}
	}
}

func TestInstagramVideoSecondsRoundsUp(t *testing.T) {
	// 9.6 秒必须记成 10 秒，否则与其它平台的 10 秒对不上而漏判。
	if got := instagramVideoSeconds(instagram.Media{DurationMS: 9600}); got != 10 {
		t.Fatalf("9600ms should round up to 10s, got %d", got)
	}
	if got := instagramVideoSeconds(instagram.Media{}); got != 0 {
		t.Fatalf("unknown duration must stay 0, got %d", got)
	}
}
