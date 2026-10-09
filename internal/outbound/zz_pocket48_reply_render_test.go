package outbound

import "testing"

// 口袋48 回复在飞书卡片上的两行渲染（2026-10-06 线上错位回归）。
//
// 线上看到的是：
//
//	被回复行：convolk1：convolk1:fixx有用     ← 昵称两遍
//	回复行：  我就是想尝试一下别的啦…          ← 一个昵称都没有
//
// 正确的形态是引用块保留原文自带的「昵称:内容」，回复行补上回复者昵称，
// 两行读起来才是「谁说了什么 / 谁回了什么」。

func TestQuoteLineKeepsOriginalSpeakerOnce(t *testing.T) {
	// 引用块：author 传空（数据源判定原文自带前缀），于是不重复署名。
	head, grey := turnParts("", "convolk1:fixx好用", "")
	if head != "convolk1:fixx好用" {
		t.Errorf("引用行应保留原文自带的昵称，实际 %q", head)
	}
	if grey != "" {
		t.Errorf("无翻译时不应有灰色原文块，实际 %q", grey)
	}

	// 反证：author 非空（旧行为）就会重复 —— 这个断言是为了让 bug 不会被"顺手改回去"。
	head2, _ := turnParts("convolk1", "convolk1:fixx好用", "")
	if head2 == head {
		t.Fatal("author 非空时 turnParts 应当再拼一次昵称（说明重复来自这一层）")
	}
	if head2 != "convolk1：convolk1:fixx好用" {
		t.Errorf("重复形态应正是线上那串，实际 %q", head2)
	}
}

func TestReplyLineGetsSpeakerPrefix(t *testing.T) {
	// 回复行：没有翻译时前缀落在原文上。
	text, original := prefixSpeaker("", "我就是想尝试一下别的啦，之前用的是这个hhh", "哼唧小虎(胡晓慧)")
	if text != "哼唧小虎(胡晓慧)：我就是想尝试一下别的啦，之前用的是这个hhh" {
		t.Errorf("回复行应带回复者昵称，实际 %q", text)
	}
	if original != "" {
		t.Errorf("前缀已落在正文上，不应再留一份原文，实际 %q", original)
	}

	// 有译文时前缀落在译文上，原文保持灰色无前缀。
	text2, original2 := prefixSpeaker("我就是想尝试一下别的啦", "내가 다른 걸 해보고 싶었어", "哼唧小虎(胡晓慧)")
	if text2 != "哼唧小虎(胡晓慧)：我就是想尝试一下别的啦" {
		t.Errorf("译文行应带前缀，实际 %q", text2)
	}
	if original2 != "내가 다른 걸 해보고 싶었어" {
		t.Errorf("原文行不应加前缀，实际 %q", original2)
	}

	// 没有回复者昵称时不得留下悬空的「：」。
	if got, _ := prefixSpeaker("正文", "", "  "); got != "正文" {
		t.Errorf("空昵称不应改变正文，实际 %q", got)
	}
}
