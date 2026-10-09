package logic

import "testing"

// 线上问题（2026-10-06 20:0x）：飞书转发的口袋48 回复卡片上，
// 被回复行显示成「convolk1：convolk1:fixx好用」（昵称两遍），而回复行又没有昵称。
//
// 根因有两处：
//  1. 口袋48 的被回复原文本身带「昵称:内容」前缀，渲染层又拼了一次「昵称：」；
//  2. 口袋48 从没设置 Document.ReplyAuthorPrefix（此前只有 weverse 设了），
//     于是回复行前面什么都没有。
func TestSpeakerAlreadyInText(t *testing.T) {
	cases := []struct {
		author string
		text   string
		want   bool
		why    string
	}{
		{"convolk1", "convolk1:fixx好用", true, "半角冒号，口袋48 原文形态"},
		{"convolk1", "convolk1：fixx好用", true, "全角冒号"},
		{"哼唧小虎", "  哼唧小虎: 内容", true, "前导空白仍算自带"},
		{"convolk1", "convolk1", false, "只有昵称没有分隔符，不算自带"},
		{"convolk1", "convolk12:fixx好用", false, "前缀不完全匹配（另一个账号名）"},
		{"convolk1", "别人:fixx好用", false, "别的前缀"},
		{"", "convolk1:fixx好用", false, "没有作者就无从判断"},
		{"convolk1", "", false, "空原文"},
	}
	for _, c := range cases {
		if got := speakerAlreadyInText(c.author, c.text); got != c.want {
			t.Errorf("speakerAlreadyInText(%q, %q) = %v，期望 %v（%s）",
				c.author, c.text, got, c.want, c.why)
		}
	}
}
