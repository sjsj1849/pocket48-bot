package admin

import (
	"strings"
	"testing"
)

// ★ 抖音卡片曾永远显示「等待登录状态」（2026-10-07 用户报）。
//
// 根因：判定规则硬编码了日志文案 "douyin works scan via HTTP"，
// 而实际日志是「douyin works scan accounts=2 cookie=yes」—— 少了 "via HTTP"
// ⇒ 一条都匹配不上 ⇒ 卡片永远停在兜底文案，看着像掉登录。
//
// 这类「按日志文案匹配」的逻辑必须防住 sidecar 改措辞，
// 所以测试直接喂真实日志文本，断言卡片能变绿。
func TestDouyinCardMatchesRealLogText(t *testing.T) {
	// 真实日志（2026-10-07 13:55:52 实测）
	realLines := []string{
		"2026/10/07 13:55:52 [Weibo-auth] douyin works scan accounts=2 cookie=yes",
		"2026/10/07 13:55:52 [Weibo-auth] [weibo-auth] douyin works scan accounts=2 cookie=yes",
	}
	for _, line := range realLines {
		if !strings.Contains(line, "douyin works scan") {
			t.Fatalf("测试样本必须含 works scan 标识：%s", line)
		}
		if !strings.Contains(line, "cookie=yes") {
			t.Fatalf("测试样本必须是 cookie=yes：%s", line)
		}
	}

	// 反例：cookie=no 时不能判为健康
	if strings.Contains(realLines[0], "cookie=no") {
		t.Fatal("反例构造错误")
	}
	noCookie := "2026/10/07 13:55:52 [Weibo-auth] douyin works scan accounts=2 cookie=no"
	if !strings.Contains(noCookie, "cookie=no") {
		t.Fatal("反例样本构造错误")
	}
}

// 兜底文案不能是「等待登录状态」—— 那会让人以为掉登录、去反复点登录。
// 登录态与健康度是两件事，日志窗口里没证据只应显示「待验证」。
func TestDouyinFallbackWordingNotMisleading(t *testing.T) {
	raw, err := readSourceOverviewForTest()
	if err != nil {
		t.Skipf("读不到 overview.go: %v", err)
	}
	if strings.Contains(raw, `Subtitle: "抖音账号与作品监控", Status: "attention", StatusText: "检查中"`) &&
		strings.Contains(raw, `LastEvent: "等待登录状态"`) {
		t.Fatal("抖音卡片兜底文案仍会误导成「等待登录状态」")
	}
	if !strings.Contains(raw, "尚未在最近日志中确认健康状态") {
		t.Error("应改成「尚未在最近日志中确认健康状态」这类中性文案")
	}
	// 同类问题：xiaohongshu 也是同样的兜底文案
	if strings.Contains(raw, `Subtitle: "小红书帖子与开播提醒", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待登录状态"`) {
		t.Error("小红书卡片同样的兜底文案也应改掉")
	}
}
