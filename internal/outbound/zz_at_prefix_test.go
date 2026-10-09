package outbound

import "testing"

// ★ @全体成员 在飞书上必须**独占正文第一行**。
//
// documentContent 会把 `<at id=all></at>` 加在 text 开头，而 prefixSpeaker
// 又要在正文前加「说话人：」。两者叠加会得到「昵称：<at id=all></at>」，
// at 标记被夹到行中 —— 飞书不认，@ 就等于没发（2026-10-06 用户反馈
// 「飞书基本没有 @全体成员」）。
func TestPrefixSpeakerKeepsAtAllOnFirstLine(t *testing.T) {
	text, original := prefixSpeaker("<at id=all></at>\n我就是想尝试一下别的啦", "", "STELLA")
	want := "<at id=all></at>\nSTELLA：我就是想尝试一下别的啦"
	if text != want {
		t.Errorf("at 标记必须留在第一行且在说话人前缀之前：\n实际 %q\n期望 %q", text, want)
	}
	if original != "" {
		t.Errorf("不应额外产生原文块：%q", original)
	}
	if idx := indexOfAt(text); idx > 0 {
		t.Errorf("at 标记出现在正文中间（位置 %d）：%q", idx, text)
	}
}

// 没有译文、at + 原文的组合。
func TestPrefixSpeakerKeepsAtAllWithOriginalOnly(t *testing.T) {
	text, original := prefixSpeaker("<at id=all></at>", "언니가 더 예뿌", "STELLA")
	if text != "<at id=all></at>\nSTELLA：언니가 더 예뿌" {
		t.Errorf("实际 %q", text)
	}
	if original != "" {
		t.Errorf("原文应并入正文，实际 %q", original)
	}
}

// 只有 at、没有正文与原文：不能把 at 丢掉（否则 @ 彻底消失），也不能留悬空冒号。
func TestPrefixSpeakerKeepsAtAllWhenBodyEmpty(t *testing.T) {
	text, original := prefixSpeaker("<at id=all></at>", "", "STELLA")
	if text != "<at id=all></at>" {
		t.Errorf("应只保留 at 标记，实际 %q", text)
	}
	if original != "" {
		t.Errorf("不应产生原文块：%q", original)
	}
}

// 回归：没有 at 时行为不变。
func TestPrefixSpeakerWithoutAtUnchanged(t *testing.T) {
	text, _ := prefixSpeaker("正文内容", "", "STELLA")
	if text != "STELLA：正文内容" {
		t.Errorf("实际 %q", text)
	}
}

func indexOfAt(s string) int {
	for i := 0; i+3 < len(s); i++ {
		if s[i:i+3] == "<at" {
			return i
		}
	}
	return -1
}
