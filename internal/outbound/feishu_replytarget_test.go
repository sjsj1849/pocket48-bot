package outbound

import "testing"

// TestResolveReplyTargetFallsBackToPost 覆盖线上真实故障：成员回复粉丝评论时，
// 父评论从未被转发（ReplyMap 无记录），必须回退到所属主帖才能挂载成功。
func TestResolveReplyTargetFallsBackToPost(t *testing.T) {
	f := &Feishu{replyMap: NewReplyMap(100)}
	f.replyMap.Record("4-241976860", "om_post_card") // 主帖：转发过

	// 首选粉丝评论查不到 → 应回退到主帖
	if got := f.resolveReplyTarget("4-511983629", []string{"4-241976860"}); got != "om_post_card" {
		t.Fatalf("resolveReplyTarget = %q, want om_post_card", got)
	}
	// 父评论也在（例如我们开始转发粉丝评论后）→ 应优先用父评论
	f.replyMap.Record("4-511983629", "om_fan_comment")
	if got := f.resolveReplyTarget("4-511983629", []string{"4-241976860"}); got != "om_fan_comment" {
		t.Fatalf("首选目标应优先于兜底，实际 %q", got)
	}
	// 全部查不到 → 不挂载，发送方保留引用块
	if got := f.resolveReplyTarget("missing", []string{"also-missing"}); got != "" {
		t.Fatalf("全部未命中应返回空，实际 %q", got)
	}
	// 空候选不应 panic
	if got := f.resolveReplyTarget("", nil); got != "" {
		t.Fatalf("空候选应返回空，实际 %q", got)
	}
}

// TestPrefixSpeakerAlignsWithQuote 引用块以「昵称：」开头，回复侧也要同样形状，
// 两行才对齐成并列的说话人。译文是白色主行，前缀只加在译文上，灰色原文不变。
func TestPrefixSpeakerAlignsWithQuote(t *testing.T) {
	// 有译文：前缀加在译文行
	text, original := prefixSpeaker("넘넘넘 재밌었어", "너무 좋아", "CARMEN")
	if text != "CARMEN：넘넘 넘 재밌었어" && text != "CARMEN：넘넘넘 재밌었어" {
		t.Errorf("译文应带成员名前缀，实际 %q", text)
	}
	if original != "너무 좋아" {
		t.Errorf("灰色原文不应被改写，实际 %q", original)
	}

	// 无译文：原文成为主行，前缀加在它上面
	text, original = prefixSpeaker("", "运动핑", "CARMEN")
	if text != "CARMEN：运动핑" || original != "" {
		t.Errorf("无译文时应把原文提为主行并加前缀，实际 %q / %q", text, original)
	}

	// 双方都空 → 不产生 "作者：" 孤儿行
	text, original = prefixSpeaker("", "", "CARMEN")
	if text != "" || original != "" {
		t.Errorf("空内容不应产生前缀，实际 %q / %q", text, original)
	}
	// 空作者 → 原样返回
	text, _ = prefixSpeaker("内容", "", "")
	if text != "内容" {
		t.Errorf("空作者不应加前缀，实际 %q", text)
	}
}

// TestQuoteDroppedOnlyWhenThreadedToExactParent 验证三种组合下引用块的取舍：
// 挂到父评论本身 → 重复，丢弃；挂到帖子（兜底）→ 粉丝评论没别处出现，保留；
// 没挂上 → 保留（否则上下文全丢）。
func TestQuoteDroppedOnlyWhenThreadedToExactParent(t *testing.T) {
	cases := []struct {
		name                 string
		replyTarget          string
		keepQuoteWhenThreaded bool
		wantDrop             bool
	}{
		{"挂到父评论本身，引用块重复", "om_fan_comment", false, true},
		{"兜底挂到帖子，需保留被回复的评论", "om_post_card", true, false},
		{"没挂上，保留引用块", "", false, false},
		{"没挂上但要求保留", "", true, false},
	}
	for _, c := range cases {
		if got := shouldDropQuote(c.replyTarget, c.keepQuoteWhenThreaded); got != c.wantDrop {
			t.Errorf("%s: shouldDropQuote = %v, want %v", c.name, got, c.wantDrop)
		}
	}
}