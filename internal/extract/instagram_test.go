package extract

import (
	"testing"

	"pocket48-bot/internal/instagram"
)

// igMediaFixture 造一条 10 秒视频：低码率档 1Mbps、高码率档 20Mbps。
//
// 10 秒 × 20Mbps ÷ 8 = 25MB ⇒ 30MB 预算内选高码率；
// 100KB 预算下两档都超 ⇒ 退回低码率。
func igMediaFixture() instagram.Media {
	return instagram.Media{
		Kind:       "video",
		Cover:      "https://cover",
		DurationMS: 10000,
		Variants: []instagram.Variant{
			{URL: "https://low", Bitrate: 1000000},
			{URL: "https://high", Bitrate: 20000000},
		},
	}
}

func igVariantsFixture(urls ...string) []instagram.Variant {
	out := make([]instagram.Variant, 0, len(urls))
	for _, u := range urls {
		out = append(out, instagram.Variant{URL: u, Bitrate: 1000000})
	}
	return out
}

func TestInstagramResolverMatchesKnownPaths(t *testing.T) {
	r := &InstagramResolver{}
	cases := []struct {
		target string
		want   bool
		why    string
	}{
		{"instagram.com/p/DeQtXnACYJZ/", true, "普通帖子"},
		{"www.instagram.com/p/abc", true, "带 www"},
		{"instagram.com/reel/DeQtXnACYJZ/", true, "Reels 单数拼写"},
		{"instagram.com/reels/DeQtXnACYJZ/", true, "Reels 复数拼写也要认，否则这类分享链接直接报不支持"},
		{"instagram.com/tv/abc/", true, "长视频形态"},
		{"instagram.com/stories/hearts2hearts/123/", false, "Story 链接内容会过期，不支持提取"},
		{"instagram.com/hearts2hearts/", false, "主页不是单条内容"},
		{"tiktok.com/p/abc/", false, "别的平台"},
		{"instagram.com.evil.com/p/abc/", false, "域名后缀攻击"},
		{"instagram.com/p/abc/extra/", false, "多一层路径不是帖子页"},
	}
	for _, tc := range cases {
		if got := r.Matches(tc.target); got != tc.want {
			t.Errorf("%s: Matches(%q) = %v, want %v", tc.why, tc.target, got, tc.want)
		}
	}
}

func TestPickInstagramVideoPrefersHighestBitrateWithinBudget(t *testing.T) {
	url, ok := pickInstagramVideo(igMediaFixture(), 30<<20)
	if !ok || url != "https://high" {
		t.Fatalf("预算内应选最高码率档: url=%q ok=%v", url, ok)
	}
}

func TestPickInstagramVideoFallsBackToLowestWhenAllOversize(t *testing.T) {
	// 预算只有 100KB：两档都超，最小的那档是 https://low。
	// 返回它而不是失败 —— 宁可糊也不能让用户什么都收不到。
	url, ok := pickInstagramVideo(igMediaFixture(), 100<<10)
	if !ok || url != "https://low" {
		t.Fatalf("全都超预算时应退回最小档: url=%q ok=%v", url, ok)
	}
}

func TestPickInstagramVideoFalseWhenNoUsableVariant(t *testing.T) {
	if _, ok := pickInstagramVideo(instagram.Media{Kind: "video"}, 30<<20); ok {
		t.Fatal("没有任何可用档位时必须返回 false，不能返回空地址冒充成功")
	}
	// 空白地址等同于不可用。
	if _, ok := pickInstagramVideo(instagram.Media{
		Kind:     "video",
		Variants: igVariantsFixture("  ", " "),
	}, 30<<20); ok {
		t.Fatal("全空白地址必须视为不可用")
	}
}

func TestInstagramVideoFirstKeepsRelativeOrder(t *testing.T) {
	in := []Item{
		{Kind: "image", URL: "a"},
		{Kind: "image", URL: "b"},
		{Kind: "video", URL: "v"},
		{Kind: "image", URL: "c"},
	}
	out := instagramVideoFirst(in)
	if out[0].Kind != "video" {
		t.Fatalf("视频必须排到最前，实际 %q", out[0].Kind)
	}
	// 图片相对顺序不能被打乱，否则和原帖对不上。
	if out[1].URL != "a" || out[2].URL != "b" || out[3].URL != "c" {
		t.Fatalf("图片顺序必须保持原样: %+v", out)
	}
	// 没有视频时不应额外分配，原样返回。
	plain := []Item{{Kind: "image", URL: "x"}}
	if got := instagramVideoFirst(plain); len(got) != 1 || got[0].URL != "x" {
		t.Fatalf("无视频时应原样返回: %+v", got)
	}
}

func TestEstimateInstagramVideoSizeUsesHighestVariant(t *testing.T) {
	// 10 秒 × 20,000,000 bps ÷ 8 = 25,000,000 字节。
	size := estimateInstagramVideoSize(igMediaFixture())
	if size != 25_000_000 {
		t.Fatalf("体积估算应取码率最高的档, got %d", size)
	}
	// 时长未知但有码率时，仍要给保守估算（按 30 秒兜底），上层靠它判断能不能发得出去。
	unknownLen := instagram.Media{Kind: "video", DurationMS: 0,
		Variants: []instagram.Variant{{URL: "https://x", Bitrate: 1000000}}}
	if estimateInstagramVideoSize(unknownLen) <= 0 {
		t.Fatal("时长未知时也要给出保守估算，不能是 0")
	}
	// 什么都不知道（无档位）时返回 0 表示「无法估算」，这是有意义的信号。
	if got := estimateInstagramVideoSize(instagram.Media{Kind: "video"}); got != 0 {
		t.Fatalf("无档位时应返回 0 表示无法估算, got %d", got)
	}
}

func TestInstagramSplitHostPath(t *testing.T) {
	host, path, ok := splitHostPath("www.instagram.com/p/abc/")
	if !ok || host != "instagram.com" || path != "/p/abc/" {
		t.Fatalf("拆分结果不对: host=%q path=%q ok=%v", host, path, ok)
	}
	// 无路径时必须补成 "/"，否则 Matches 拿到空串。
	host, path, ok = splitHostPath("instagram.com")
	if !ok || host != "instagram.com" || path != "/" {
		t.Fatalf("无路径时应补成 /: host=%q path=%q ok=%v", host, path, ok)
	}
}
