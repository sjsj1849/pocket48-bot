package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/extract"
	"pocket48-bot/internal/napcat"
)

// ---------- 链接提取：触发条件 ----------

func TestExtractFirstLink(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://x.com/a/status/123", "https://x.com/a/status/123"},
		{"http://x.com/a/status/123", "http://x.com/a/status/123"},
		{
			"看看这个 https://x.com/a/status/123 谢谢",
			"https://x.com/a/status/123",
		},
		{
			// 链接后跟中文标点，标点不该被吞进链接
			"https://www.bilibili.com/video/BV1UMYx6BEP8，好看",
			"https://www.bilibili.com/video/BV1UMYx6BEP8",
		},
		{"没有链接的普通消息", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := extractFirstLink(c.in); got != c.want {
			t.Errorf("extractFirstLink(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractFirstLinkTakesFirstOfMany(t *testing.T) {
	// 一次贴多个链接时只处理第一个，避免重复发消息。
	msg := "https://x.com/a/status/111 和 https://x.com/b/status/222"
	if got := extractFirstLink(msg); got != "https://x.com/a/status/111" {
		t.Fatalf("应只取第一个链接，实际 %q", got)
	}
}

func TestExtractWantsGIF(t *testing.T) {
	yes := []string{
		"https://x.com/a/status/1 gif",
		"https://x.com/a/status/1 GIF",
		"https://x.com/a/status/1 动图",
		"https://x.com/a/status/1 表情包",
		"gif https://x.com/a/status/1",
	}
	for _, in := range yes {
		if !extractWantsGIF(in) {
			t.Errorf("应识别为要 GIF: %q", in)
		}
	}
	// 纯链接（没说要动图）绝不触发 GIF。
	for _, in := range []string{
		"https://x.com/a/status/1",
		"https://www.bilibili.com/video/BV1UMYx6BEP8",
		"",
	} {
		if extractWantsGIF(in) {
			t.Errorf("纯链接不应触发 GIF: %q", in)
		}
	}
	// 刻意不做词边界的精细匹配：链接消息里出现 "gif" 就转，
	// 比要求用户记住精确指令更友好。多花的代价只是一次 ffmpeg 转换。
	if !extractWantsGIF("https://x.com/a/status/1 这个gif 好笑") {
		t.Error("消息中出现 gif 关键词就应触发")
	}
}

func TestStripAtSegments(t *testing.T) {
	got := stripAtSegments("[CQ:at,qq=3808515247] https://x.com/a/status/1")
	if strings.Contains(got, "CQ:at") {
		t.Fatalf("应去掉 CQ 码，实际 %q", got)
	}
	if !strings.Contains(got, "https://") {
		t.Fatalf("链接应保留，实际 %q", got)
	}
}

func TestTryHandleExtractLinkIgnoresPlainText(t *testing.T) {
	b := &Bot{}
	if b.tryHandleExtractLink(&napcat.Event{MessageType: "private"}, "今天天气不错", true) {
		t.Fatal("不含链接的消息不应被受理")
	}
}

func TestTryHandleExtractLinkRequiresAtInGroup(t *testing.T) {
	b := &Bot{}
	// 群里没 @机器人：即使有链接也不处理，避免机器人对群里每条链接都反应。
	ev := &napcat.Event{MessageType: "group", GroupID: 123, UserID: 1}
	if b.tryHandleExtractLink(ev, "https://x.com/a/status/2106288781729157547", false) {
		t.Fatal("群里未 @机器人 时不应受理")
	}
}

func TestFriendlyExtractError(t *testing.T) {
	cases := []struct {
		in       string
		contains string
	}{
		{"这个链接的平台还不支持", "还不支持"},
		{"内容不存在、已删除，或需要登录才能查看", "已删除"},
		{"X 解析服务返回了无法解析的数据：xxx", "看不懂"},
		{"取这个链接超时了", "超时"},
		{"B 站风控校验失败", "风控"},
	}
	for _, c := range cases {
		got := friendlyExtractError(&staticErr{c.in})
		if !strings.Contains(got, c.contains) {
			t.Errorf("friendlyExtractError(%q) = %q，期望包含 %q", c.in, got, c.contains)
		}
	}
	// 未知错误必须带上原始信息，不能吞掉。
	unknown := friendlyExtractError(&staticErr{"某种没见过的错"})
	if !strings.Contains(unknown, "某种没见过的错") {
		t.Errorf("未知错误应透传原文，实际 %q", unknown)
	}
}

type staticErr struct{ s string }

func (e *staticErr) Error() string { return e.s }

// ---------- 链接提取：正文构造 ----------

func TestFormatExtractedText(t *testing.T) {
	post := &extract.Post{
		Platform: extract.PlatformX,
		Author:   "某偶像",
		Text:     "正文内容",
		URL:      "https://x.com/a/status/123",
	}
	got := formatExtractedText(post)
	for _, want := range []string{"X", "某偶像", "正文内容", "原链接"} {
		if !strings.Contains(got, want) {
			t.Errorf("正文应包含 %q，实际 %q", want, got)
		}
	}
}

func TestFormatExtractedTextHandlesEmptyFields(t *testing.T) {
	// 没有作者和正文时也不能崩，且至少要给出可识别内容。
	got := formatExtractedText(&extract.Post{Platform: extract.PlatformBilibili})
	if !strings.Contains(got, "B 站") {
		t.Fatalf("应至少标出平台，实际 %q", got)
	}
}

func TestHasImageItem(t *testing.T) {
	if hasImageItem(&extract.Post{Items: []extract.Item{{Kind: "video"}}}) {
		t.Fatal("只有视频时不应认为有图片")
	}
	if !hasImageItem(&extract.Post{Items: []extract.Item{{Kind: "image"}}}) {
		t.Fatal("含图片时应返回 true")
	}
}

func TestPlatformLabel(t *testing.T) {
	if got := platformLabel(extract.PlatformBilibili); got != "B 站" {
		t.Errorf("B 站标签不对: %q", got)
	}
	if got := platformLabel(extract.PlatformX); got != "X" {
		t.Errorf("X 标签不对: %q", got)
	}
	if got := platformLabel(extract.PlatformUnknown); got == "" {
		t.Error("未知平台也应给出非空标签")
	}
}

func TestExtractHelpTextMentionsGIF(t *testing.T) {
	if !strings.Contains(extractHelpText(), "gif") {
		t.Error("帮助文案应说明 gif 用法")
	}
}

// 体积上限常量必须与飞书限制相符：30MB 上限留余量。
func TestExtractMaxVideoBytesUnderFeishuLimit(t *testing.T) {
	const feishuLimit = 30 << 20
	if extractMaxVideoBytes >= feishuLimit {
		t.Fatalf("体积上限 %d 应小于飞书限制 %d", extractMaxVideoBytes, feishuLimit)
	}
	if extractMaxVideoBytes <= 0 {
		t.Fatal("体积上限必须为正")
	}
}
