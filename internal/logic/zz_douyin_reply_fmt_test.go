package logic

import (
	"strings"
	"testing"
)

// 用户的原话场景：葡萄吞十七回复了我的文本消息。
func TestZZReplyBothLinesHaveColon(t *testing.T) {
	sender := "葡萄吞十七(唐欣怡)"
	quotedName := "我"
	quotedText := "真的存在这样的时期吗"
	replyText := "那得最最最最开始的时候了吧"

	got := formatDouyinReplyText(sender, replyText, quotedName, quotedText)
	t.Logf("格式化结果 = %q", got)

	lines := strings.Split(got, "\n")
	if len(lines) != 2 {
		t.Fatalf("应两行，实际 %d 行: %q", len(lines), got)
	}
	// 上面那行：我：xxx
	if !strings.HasPrefix(lines[0], quotedName+"：") {
		t.Errorf("第一行缺前缀，应为 %q：...，实际 %q", quotedName, lines[0])
	}
	// ★ 下面那行：也必须有「昵称：」前缀（用户 10-05 明确要求）
	if !strings.HasPrefix(lines[1], sender+"：") {
		t.Errorf("★ 第二行缺「%s：」前缀，实际 %q", sender, lines[1])
	}
}

// sender 为空时应降级，但不能把回复文本也丢掉。
func TestZZReplyEmptySenderFallback(t *testing.T) {
	got := formatDouyinReplyText("", "回复内容", "我", "被回复内容")
	t.Logf("sender 为空时 = %q", got)
	if !strings.Contains(got, "被回复内容") || !strings.Contains(got, "回复内容") {
		t.Errorf("两段内容都该保留，实际 %q", got)
	}
}

// 已带前缀时不能重复加。
func TestZZReplyNoDoublePrefix(t *testing.T) {
	sender := "葡萄吞十七(唐欣怡)"
	got := formatDouyinReplyText(sender, sender+"：已经带前缀了", "我", "被回复内容")
	if strings.Count(got, sender+"：") != 1 {
		t.Errorf("不应重复加前缀，实际 %q", got)
	}
}

// 真实日志样本：12:33:20 那条（用户看到的疑似症状）。
func TestZZReplyRealLogSample(t *testing.T) {
	// 这条日志里 text 没有「我：」前缀 —— 说明是**无引用**的普通消息。
	got := formatDouyinReplyText("葡萄吞十七(唐欣怡)", "每个地方都这样哈哈哈哈哈", "", "")
	t.Logf("无引用场景 = %q", got)
	if strings.Contains(got, "：") {
		t.Errorf("无引用时不该造出前缀，实际 %q", got)
	}
}
