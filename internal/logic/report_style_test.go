package logic

import (
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/weverse"
)

// TestWeiboDailyReportUsesSingleInkGreenPalette 覆盖用户反馈的"又绿又蓝"。
// 此前顶条/kicker 用墨绿 #2f6657，而分组徽章却用蓝 #2466b3/#edf4ff，
// 两种色系混在一起很杂乱。现在统一到 Weverse 报表的墨绿色板。
func TestWeiboDailyReportUsesSingleInkGreenPalette(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 5, 0, time.FixedZone("CST", 8*3600))
	sections := []weiboSuperCountHTMLSection{{
		Title: " Hearts2Hearts ",
		Results: []monitor.WeiboSuperCountResult{
			{OID: "1022:1", Name: "超话A", SignCount: 120, SignText: "签到120人"},
			{OID: "1022:2", Name: "超话B", SignCount: 80, SignText: "签到80人"},
		},
	}}

	out := formatWeiboSuperCountDualRankingHTML(sections, nil, "微博超话日报", now,
		nil, nil, nil, nil, nil, true)

	// 分组徽章的蓝必须消失
	for _, blue := range []string{"#2466b3", "#edf4ff", "#f0f4fa", "#f5f7fb", "#e5eaf2", "#e7ebf1"} {
		if strings.Contains(strings.ToLower(out), blue) {
			t.Errorf("不应再出现旧蓝色 %s", blue)
		}
	}
	// 墨绿主色必须在
	if !strings.Contains(out, reportInkGreen) {
		t.Errorf("应使用墨绿主色 %s", reportInkGreen)
	}
	// 顶条渐变两端都应是同色系（不再出现 #4a9a85 那支偏青的绿）
	if strings.Contains(out, "#4a9a85") {
		t.Error("顶条渐变应改为同色系深绿")
	}
	// 页面底色与 Weverse 报表一致
	if !strings.Contains(out, reportPageBg) {
		t.Errorf("页面底色应统一为 %s", reportPageBg)
	}
	t.Log("palette unified to ink green")
}

// TestWeiboDailyReportKeepsSemanticDeltaColors 涨跌是语义色（绿=涨、红=跌），
// 不属于"配色不统一"的范畴，不能被一起改成墨绿。
func TestWeiboDailyReportKeepsSemanticDeltaColors(t *testing.T) {
	// 两个超话：当前值都低于基线 → 渲染出下降（红）；再用一个高于基线的造出上涨（绿）。
	out := formatWeiboSuperCountDualRankingHTML(
		[]weiboSuperCountHTMLSection{{Title: "g", Results: []monitor.WeiboSuperCountResult{
			{OID: "1022:1", Name: "跌", SignCount: 10, SignText: "签到10人"},
			{OID: "1022:2", Name: "涨", SignCount: 999, SignText: "签到999人"},
		}}},
		nil, "t", time.Now(),
		map[string]int{"1022:1": 100, "1022:2": 100}, nil, nil, nil, nil, true)
	if !strings.Contains(out, "#0f9d58") {
		t.Error("增长用的绿色应保留（它是语义色，不是主题色）")
	}
	if !strings.Contains(out, "#d93025") {
		t.Error("下降用的红色应保留（它是语义色，不是主题色）")
	}
}

// TestReportCardTitle 飞书卡片顶栏文案：去掉邮件用的方括号，空标题回落默认值。
func TestReportCardTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "微博超话日报"},
		{"   ", "微博超话日报"},
		{"[超话签到人数日报]", "超话签到人数日报"},
		{"[heart to heart 的超话日报]", "heart to heart 的超话日报"},
		{"分组A", "分组A 的超话日报"},
	}
	for _, c := range cases {
		if got := reportCardTitle(c.in); got != c.want {
			t.Errorf("reportCardTitle(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestWeverseReportCardTitleAndTime Weverse 报表用同一套卡片外壳，
// 标题要带团体名与报表类型，时间锚在统计区间结束时刻。
func TestWeverseReportCardTitleAndTime(t *testing.T) {
	r := weverse.Report{
		Community: "Hearts2Hearts",
		Period: weverse.ReportPeriod{
			Kind:  "week",
			Title: "周报",
			Start: time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
			End:   time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		},
	}
	title := weverseReportCardTitle(r)
	if !strings.Contains(title, "Hearts2Hearts") {
		t.Errorf("标题应含团体名，实际 %q", title)
	}
	if !strings.Contains(title, "周报") {
		t.Errorf("标题应含报表类型，实际 %q", title)
	}
	if got := weverseReportTime(r); !got.Equal(r.Period.End) {
		t.Errorf("时间应锚在统计结束时刻，实际 %v", got)
	}
	// 零值报表不能 panic
	if weverseReportCardTitle(weverse.Report{}) == "" {
		t.Error("空报表也应给出标题")
	}
	_ = weverseReportTime(weverse.Report{})
}
