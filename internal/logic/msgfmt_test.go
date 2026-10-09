package logic

import (
	"strings"
	"testing"
)

// 用码点构造方括号，避免源文件在不同编码环境下传���时字符被破坏。
// U+3010 = 【, U+3011 = 】
func bracket(s string) string {
	return "\u3010" + s + "\u3011"
}

// TestBilibiliCleanTitle 锁住标题清洗行为，直接对应用户的三条要求：
//  1. 去掉「【UP主名】」前缀（UP 主名已在消息头上，重复是噪音）。
//  2. 竖线 → 换行。
//  3. 不要「发布了新视频」这类套话（由 bilibiliKindLabel 返回空串保证）。
func TestBilibiliCleanTitle(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "去掉方框前缀",
			in:   bracket("Hearts2Hearts") + "我们来到东京巨蛋参加开球仪式舞台!",
			want: "我们来到东京巨蛋参加开球仪式舞台!",
		},
		{
			name: "全角竖线换行",
			in:   bracket("Hearts2Hearts") + "我们来到东京巨蛋参加开球仪式舞台!(ᵔᗜᵔ)✧* \uff5c《ICONIC HEART》日本宣传活动 BH2ND #2",
			want: "我们来到东京巨蛋参加开球仪式舞台!(ᵔᗜᵔ)✧*\n《ICONIC HEART》日本宣传活动 BH2ND #2",
		},
		{
			name: "半角竖线换行",
			in:   "标题A | 标题B",
			want: "标题A\n标题B",
		},
		{
			name: "多个竖线产生多行且不留空行",
			in:   "甲 \uff5c 乙 \uff5c 丙",
			want: "甲\n乙\n丙",
		},
		{
			name: "多层方框只去到第一个右括号之后",
			in:   bracket("A") + bracket("B") + "真正的标题",
			want: "真正的标题",
		},
		{
			name: "无前缀无竖线时原样返回",
			in:   "普通标题",
			want: "普通标题",
		},
		{
			name: "前缀与竖线同时存在",
			in:   bracket("UP") + "团综第一期 \uff5c 下期预告",
			want: "团综第一期\n下期预告",
		},
		{
			name: "竖线在开头或结尾不产生空行",
			in:   "\uff5c前导\uff5c \uff5c尾部\uff5c",
			want: "前导\n尾部",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bilibiliCleanTitle(c.in, "Hearts2Hearts")
			if got != c.want {
				t.Errorf("bilibiliCleanTitle(%q)\n  得到 %q\n  期望 %q", c.in, got, c.want)
			}
		})
	}
}

// TestBilibiliCleanTitleKeepsContent 确认清洗不会误删正文内容。
func TestBilibiliCleanTitleKeepsContent(t *testing.T) {
	in := bracket("UP") + "标题｜后面还有内容"
	got := bilibiliCleanTitle(in, "UP")
	if !strings.Contains(got, "后面还有内容") {
		t.Errorf("竖线后的内容被误删: %q", got)
	}
}

// TestBilibiliKindLabelIsQuiet 确认不再输出「发布了新视频」套话。
func TestBilibiliKindLabelIsQuiet(t *testing.T) {
	for _, kind := range []string{"video", "article", "text", "draw"} {
		if got := bilibiliKindLabel(kind); got != "" {
			t.Errorf("kind=%q 不应产生标签文案，实际得到 %q", kind, got)
		}
	}
	// 转发是唯一保留短标签的类型。
	if got := bilibiliKindLabel("forward"); got != "转发" {
		t.Errorf("forward 标签应为「转发」，实际 %q", got)
	}
}
