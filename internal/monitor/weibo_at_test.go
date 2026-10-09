package monitor

import (
	"strings"
	"testing"
)

// TestWeiboAtAllOwnLine 验证 @全体成员 与标题分行。
//
// 背景：此前 formatWeiboCleanText 里 AtSegment("all") 之后直接接标题文本段，
// QQ 会把两者渲染成同一行（"@全体成员【昵称|微博】"）。修复后 @ 之后补一个
// 独立的换行文本段，与小红书/抖音/X 保持一致。
func TestWeiboAtAllOwnLine(t *testing.T) {
	m := &WeiboMonitor{}
	card := WeiboCard{Text: "正文内容"}
	card.User.ScreenName = "未 Injuries"

	segs := m.formatWeiboCleanText(card, "ABC123", true, "123456")

	if len(segs) < 3 {
		t.Fatalf("段数不足：%d", len(segs))
	}
	if segs[0].Type != "at" {
		t.Errorf("第1段应为 at，实际 %q", segs[0].Type)
	}
	if segs[1].Type != "text" || segs[1].Data["text"] != "\n" {
		t.Errorf("第2段应是纯换行文本，实际 type=%q data=%q", segs[1].Type, segs[1].Data["text"])
	}
	if !strings.HasPrefix(segs[2].Data["text"], "【未 Injuries|微博】") {
		t.Errorf("第3段应为标题且不带 @，实际 %q", segs[2].Data["text"])
	}
	// 拼接后的首行不应是 "@...【"，即 @ 必须独占一行
	joined := segs[0].Type + "|" + segs[1].Data["text"] + "|" + segs[2].Data["text"]
	if strings.HasPrefix(segs[2].Data["text"], "@") {
		t.Errorf("标题段不应以 @ 开头：%q", joined)
	}
	t.Logf("段序列（%d 段）:", len(segs))
	for i, s := range segs {
		t.Logf("  [%d] %-5s %q", i, s.Type, truncStr(s.Data["text"], 44))
	}
}

// TestWeiboAtAllDisabled 不开 @ 时不应插入任何 at/换行段。
func TestWeiboAtAllDisabled(t *testing.T) {
	m := &WeiboMonitor{}
	card := WeiboCard{Text: "正文"}
	card.User.ScreenName = "某用户"
	segs := m.formatWeiboCleanText(card, "X1", false, "999")
	for i, s := range segs {
		if s.Type == "at" {
			t.Fatalf("未开启 AtAll 却在第%d段出现 at", i)
		}
	}
	if segs[0].Type != "text" || !strings.HasPrefix(segs[0].Data["text"], "【") {
		t.Errorf("首段应为标题，实际 %q", truncStr(segs[0].Data["text"], 30))
	}
}

func truncStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}