package logic

import (
	"strings"
	"testing"
)

// TestStripRepeatedReplyName 覆盖线上问题（2026-10-03 00:21 / 00:37）：
// Pocket48 的 replyInfo.replyText 本身已经是「昵称:内容」，我们再拼一次
// replyName 就变成「昵称：昵称:内容」，同一个昵称连着出现两遍。
//
// 真实样本：
//
//	replyName="小朱同学🍋" replyText="小朱同学🍋:天呐 你会查阅每一条抖音分享吗"
//	replyName="面包兔"     replyText="面包兔:万一就是精心挑选了100条呢。"
func TestStripRepeatedReplyName(t *testing.T) {
	cases := []struct {
		name, replyName, replyText, want string
	}{
		{"半角冒号重复", "小朱同学🍋", "小朱同学🍋:天呐 你会查阅每一条抖音分享吗", "天呐 你会查阅每一条抖音分享吗"},
		{"另一条真实样本", "面包兔", "面包兔:万一就是精心挑选了100条呢。", "万一就是精心挑选了100条呢。"},
		{"全角冒号重复", "哼唧小虎", "哼唧小虎：天呐", "天呐"},
		{"昵称后有空格", "A", "A: 内容", "内容"},
		{"前缀不匹配则原样", "小朱", "路人甲:内容", "路人甲:内容"},
		{"昵称在正文中出现不动", "小朱", "内容里提到小朱:很棒", "内容里提到小朱:很棒"},
		{"空 replyText", "小朱", "", ""},
		{"空 replyName", "", "小朱:内容", "小朱:内容"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripRepeatedReplyName(c.replyText, c.replyName); got != c.want {
				t.Errorf("stripRepeatedReplyName(%q, %q) = %q，期望 %q", c.replyText, c.replyName, got, c.want)
			}
		})
	}
}

// TestParseEmbeddedReplyNoDuplicateName 端到端验证：解析出来的 quoted 里
// 昵称只能出现一次。
func TestParseEmbeddedReplyNoDuplicateName(t *testing.T) {
	body := `{"messageType":"REPLY","replyInfo":{"replyName":"小朱同学🍋","replyText":"小朱同学🍋:天呐 你会查阅每一条抖音分享吗","text":"不会"}}`
	quoted, _, replyName, ok := parseEmbeddedReplyDetail(body)
	if !ok {
		t.Fatal("应解析成功")
	}
	if strings.Count(quoted, replyName) != 1 {
		t.Errorf("昵称应只出现一次，实际 %d 次：%q", strings.Count(quoted, replyName), quoted)
	}
	if !strings.HasPrefix(quoted, replyName+":") {
		t.Errorf("引用应以「昵称:」开头，实际 %q", quoted)
	}
	if !strings.Contains(quoted, "天呐") {
		t.Errorf("引用应保留正文，实际 %q", quoted)
	}
	t.Logf("quoted=%q", quoted)
}

// TestParseEmbeddedReplyMessageNoDuplicateName 旧函数（MsgText 内嵌回复）同样修好。
func TestParseEmbeddedReplyMessageNoDuplicateName(t *testing.T) {
	body := `{"messageType":"REPLY","replyInfo":{"replyName":"面包兔","replyText":"面包兔:万一就是精心挑选了100条呢。","text":"你有过吗？一天100条。"}}`
	quoted, answer, ok := parseEmbeddedReplyMessage(body)
	if !ok {
		t.Fatal("应解析成功")
	}
	if strings.Count(quoted, "面包兔") != 1 {
		t.Errorf("昵称应只出现一次，实际：%q", quoted)
	}
	if !strings.Contains(answer, "100条") {
		t.Errorf("回复正文不对：%q", answer)
	}
}
