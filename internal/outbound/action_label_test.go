package outbound

import (
	"strings"
	"testing"
)

// TestActionLabelIsUniform 覆盖用户反馈：按钮文案不统一
// （「查看原微博」/「在 X 中查看」/「查看小红书」vs「打开抖音」/「打开 Weverse」），
// 看起来像一堆不相干的按钮。统一成「打开 + 平台名」。
func TestActionLabelIsUniform(t *testing.T) {
	cases := []struct {
		title string
		want  string
	}{
		{"【JAMBO|抖音】", "打开抖音"},
		{"【葡萄吞十七|抖音】", "打开抖音"},
		{"【 hearts to heart |Weverse】", "打开 Weverse"},
		{"【胡晓慧|Weverse】", "打开 Weverse"},
		{"微博", "打开微博"},
		{"【林珍娜|微博】", "打开微博"},
		{"小红书", "打开小红书"},
		{"Instagram", "打开 Instagram"},
		{"B站", "打开 B站"},
		{"【某UP|bilibili】", "打开 B站"},
		{"哔哩哔哩", "打开 B站"},
		{"X", "打开 X"},
		{"【某人|X】", "打开 X"},
		{"Melon", "打开 Melon"},
		{"Pocket48", "查看原文"},
	}
	for _, c := range cases {
		if got := actionLabel(c.title); got != c.want {
			t.Errorf("actionLabel(%q) = %q，期望 %q", c.title, got, c.want)
		}
	}
}

// TestActionLabelPlatformNamesOnly 除了兜底文案，所有按钮都应是「打开+平台」。
func TestActionLabelPlatformNamesOnly(t *testing.T) {
	titles := []string{"【JAMBO|抖音】", "【Weverse】", "微博", "小红书", "Instagram", "B站", "X", "Melon"}
	for _, title := range titles {
		got := actionLabel(title)
		if got == "查看原文" {
			t.Errorf("actionLabel(%q) 不应落到兜底文案", title)
		}
		if !strings.HasPrefix(got, "打开") {
			t.Errorf("actionLabel(%q) = %q，应统一以「打开」开头", title, got)
		}
	}
}
