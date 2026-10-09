package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/napcat"
)

// ★ 2026-10-09 用户反馈「顶栏还是有大写的 H2H」。
//
// 根因：H2H 不只在 caption 里，**账号全名本身就是 "Hearts2Hearts H2H"**
// （实测 sidecar 返回的 author.name）。上一轮只清了 caption，顶栏漏了。
func TestInstagramCleanAuthorNameDropsH2H(t *testing.T) {
	cases := []struct{ in, fallback, want string }{
		{"Hearts2Hearts H2H", "", "Hearts2Hearts"},
		{"Hearts2Hearts", "", "Hearts2Hearts"},
		{"Hearts2Hearts H2H H2H", "", "Hearts2Hearts"}, // 重复出现要剥净
		{"Hearts2Hearts h2h", "", "Hearts2Hearts"},     // 大小写不敏感
		{"Hearts2Hearts | H2H", "", "Hearts2Hearts"},   // 常见分隔符
		{"H2H", "hearts2hearts", "hearts2hearts"},      // 剥空后回退
		{"", "hearts2hearts", "hearts2hearts"},
		{"", "", "Instagram"}, // 绝不返回空串
	}
	for _, tc := range cases {
		got := instagramCleanAuthorName(tc.in, tc.fallback)
		if got != tc.want {
			t.Fatalf("输入 %q (fallback=%q) 期望 %q，实际 %q", tc.in, tc.fallback, tc.want, got)
		}
		if strings.Contains(strings.ToLower(got), "h2h") {
			t.Fatalf("结果不该残留 H2H: %q", got)
		}
	}
}

// 顶栏最终渲染出来的字符串里不该有 H2H。
func TestInstagramHeaderHasNoH2H(t *testing.T) {
	e := igReelEvent("ig-h2h-1", "内容")
	e.Author.Name = "Hearts2Hearts H2H"
	e.Author.Username = "hearts2hearts"

	groups := instagramMessageGroups(instagram.Subscription{Username: "hearts2hearts"}, e, false)
	joined := ""
	for _, g := range groups {
		for _, item := range g {
			if seg, ok := item.(napcat.MessageSegment); ok && seg.Type == "text" {
				joined += seg.Data["text"]
			}
		}
	}
	if strings.Contains(joined, "H2H") {
		t.Fatalf("整条消息不该出现 H2H: %q", joined)
	}
	if !strings.Contains(joined, "【Hearts2Hearts|Instagram】") {
		t.Fatalf("顶栏应为「【Hearts2Hearts|Instagram】」: %q", joined)
	}
}

// ★ 2026-10-09 用户反馈「Reels 没发视频」。
// 根因：我改造格式时写了 `_ = groups`，把视频段整段丢掉了。
// 这条守住「groups[1:] 里的视频组必须被识别出来」。
func TestInstagramGroupHasVideoDetectsVideoGroups(t *testing.T) {
	withVideo := []interface{}{
		napcat.TextSegment("封面"),
		napcat.VideoSegment("https://v.example/a.mp4", "https://c.example/a.jpg"),
	}
	if !instagramGroupHasVideo(withVideo) {
		t.Fatal("含 video 段的消息组必须被识别为视频组")
	}

	textOnly := []interface{}{
		napcat.TextSegment("正文"),
		napcat.ImageSegment("https://c.example/a.jpg"),
	}
	if instagramGroupHasVideo(textOnly) {
		t.Fatal("只有文本和图片的消息组不该被判为视频组")
	}
}

// 完整的 groups 结构：groups[0] 文字封面、groups[1] 视频本体。
func TestInstagramGroupsContainVideoBody(t *testing.T) {
	e := igReelEvent("ig-video-1", "内容")
	e.Author.Name = "Hearts2Hearts"

	groups := instagramMessageGroups(instagram.Subscription{Username: "hearts2hearts"}, e, false)
	if len(groups) < 2 {
		t.Fatalf("Reels 应有文字组 + 视频组，实际 %d 组", len(groups))
	}
	videoGroups := 0
	for _, g := range groups[1:] {
		if instagramGroupHasVideo(g) {
			videoGroups++
		}
	}
	if videoGroups == 0 {
		t.Fatalf("groups[1:] 里必须有视频组，实际=%d 组", len(groups)-1)
	}
}

// 去重命中时应只剩文字封面（没有视频组）。
func TestInstagramGroupsSkipVideoRemovesVideoBody(t *testing.T) {
	e := igReelEvent("ig-video-2", "内容")
	e.Author.Name = "Hearts2Hearts"

	groups := instagramMessageGroups(instagram.Subscription{Username: "hearts2hearts"}, e, true)
	for _, g := range groups[1:] {
		if instagramGroupHasVideo(g) {
			t.Fatal("去重命中时不该出现视频组")
		}
	}
}
