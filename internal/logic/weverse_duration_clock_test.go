package logic

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/weverse"
)

func TestFormatClockDuration(t *testing.T) {
	cases := []struct {
		seconds int64
		want    string
	}{
		{0, "00:00:00"},
		{31, "00:00:31"},
		{59, "00:00:59"},
		{60, "00:01:00"},
		{1860, "00:31:00"},
		{3600, "01:00:00"},
		{3661, "01:01:01"},
		{86399, "23:59:59"},
		{86400, "24:00:00"},
		// 半年报/年报的总时长会超过一天，不能被截断。
		{360000, "100:00:00"},
		{360000 + 3661, "101:01:01"},
		// 脏数据不能让格式化炸掉，更不能输出负数。
		{-1, "00:00:00"},
		{-98765, "00:00:00"},
	}
	for _, c := range cases {
		if got := formatClockDuration(c.seconds); got != c.want {
			t.Fatalf("formatClockDuration(%d) = %q, want %q", c.seconds, got, c.want)
		}
	}
}

func TestDurationMetricAlwaysNumeric(t *testing.T) {
	// 有时长数据：正常 HH:MM:SS。
	cell := durationMetric(1860, 1)
	if cell.Text != "00:31:00" {
		t.Fatalf("总时长文案 = %q, want %q", cell.Text, "00:31:00")
	}
	if !cell.Known || cell.Value != 1860 {
		t.Fatalf("总时长元数据被破坏: %+v", cell)
	}

	// 没有时长数据：一律写 00:00:00，不允许再出现「缺失」之类的状态文案。
	// 这一列与面板二次确认无关，所以永远算得出来，不该有中间态。
	empty := durationMetric(0, 0)
	if empty.Text != "00:00:00" {
		t.Fatalf("零值总时长文案 = %q, want %q", empty.Text, "00:00:00")
	}
	// Known 必须是 true，否则表尾「总和」行会永久误标「（已知）」。
	if !empty.Known {
		t.Fatalf("零值总时长的 Known 必须为 true")
	}

	// 超出阈值的脏数据同样不能漏成状态文案。
	if got := durationMetric(-5, 0).Text; got != "00:00:00" {
		t.Fatalf("负数秒数渲染成 %q", got)
	}
}

func TestSoloDurationMetricUsesClockFormat(t *testing.T) {
	// 确认过有单人直播。
	if got := soloDurationMetric(3661, 1, true); got.Text != "01:01:01" {
		t.Fatalf("单人时长文案 = %q, want %q", got.Text, "01:01:01")
	}
	// 有确认过的单人直播时，即使还有别的直播待确认，本行也必须是确定的数值，
	// 不能因为 pending 就被写成「待确认」。
	if got := soloDurationMetric(3661, 1, true); got.Known != true {
		t.Fatalf("已确认的单人时长不该降级成待确认: %+v", got)
	}
	// 已有确认值的单人时长时，即使别处还有待确认直播，本行也必须是确定数值。
	if got := soloDurationMetric(3661, 1, true); got.Known != true {
		t.Fatalf("已确认的单人时长不该降级成待确认: %+v", got)
	}
	// 没有单人直播、且本期还有未确认开播：保持「待确认」。
	// 这是唯一允许出现非数值文案的情形。
	pending := soloDurationMetric(0, 0, true)
	if pending.Text != "待确认" {
		t.Fatalf("待确认文案 = %q, want %q", pending.Text, "待确认")
	}
	// 「待确认」不是最终值，不能参与高亮排名，表尾总和要据此标注。
	if pending.Known {
		t.Fatalf("待确认态的 Known 必须为 false，否则会被当成 0 参与排名")
	}
	// 没有单人直播也没有待确认：一律 00:00:00。
	zero := soloDurationMetric(0, 0, false)
	if zero.Text != "00:00:00" || !zero.Known || zero.Value != 0 {
		t.Fatalf("零值文案 = %+v", zero)
	}
	// 零值但存在待确认直播时，仍然是「待确认」而不是 00:00:00 ——
	// 用户规则 1 的前提是「没有待确认的直播」。
	if got := soloDurationMetric(0, 0, true); got.Text == "00:00:00" {
		t.Fatalf("有待确认直播时不能写成 00:00:00，会掩盖待办")
	}
}

// TestWeverseReportHTMLDurationColumnIsUniform 锁住整张表：
// 直播两列（总时长 / 单人时长）和表尾「总和」都不能再出现旧的中文写法，
// 也不能出现宽度不一的字符串。
func TestWeverseReportHTMLDurationColumnIsUniform(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, weverse.ReportLocation)
	report := weverse.Report{
		Community:        "Hearts2Hearts",
		AsOf:             start.AddDate(0, 1, 0),
		RecordingStarted: start.Add(72 * time.Hour),
		Period:           weverse.ReportPeriod{Title: "2026年9月月报", Start: start, End: start.AddDate(0, 1, 0)},
		Members: []weverse.MemberCount{
			{ID: "a", Name: "CARMEN", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{},
				LiveSeconds: 1860, TimedLives: 2, SoloLiveSeconds: 1800, SoloLives: 1},
			{ID: "b", Name: "JIWOO", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{},
				LiveSeconds: 3723, TimedLives: 1, SoloLives: 0},
			{ID: "c", Name: "YOO", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{},
				TimedLives: 0},
		},
		LiveChatTargets: []weverse.LiveChatTarget{
			{ID: "a", Name: "CARMEN", IsMember: true},
			{ID: "b", Name: "JIWOO", IsMember: true},
			{ID: "c", Name: "YOO", IsMember: true},
		},
	}
	body := WeverseReportHTML(report)

	// 表里应出现这几个定宽值。
	for _, want := range []string{"00:31:00", "00:30:00", "01:02:03", "00:00:00"} {
		if !strings.Contains(body, want) {
			t.Fatalf("报告缺少 %q:\n%s", want, body)
		}
	}
	// 总和 = 1860 + 3723 = 5583 秒 = 01:33:03
	if !strings.Contains(body, "01:33:03") {
		t.Fatalf("总和行缺少 01:33:03:\n%s", body)
	}

	// 旧的「N小时M分」「0分」写法必须彻底消失。
	// 注意不能直接搜「小时」二字：报告尾部的口径说明里有「发布后 24 小时内
	// 开放评论」这类文案，跟时长无关。这里只匹配紧跟在数字后面的写法。
	if regexp.MustCompile(`[0-9]+小时`).MatchString(body) {
		t.Fatalf("报告仍残留「N小时」旧写法")
	}
	for _, unwanted := range []string{"0分", ">分<"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("报告仍残留旧时长写法 %q", unwanted)
		}
	}

	// 规则 1：完全没有时长数据的成员（YOO）两列都必须是 00:00:00。
	//
	// 断言必须只扫表格区域：报告里还有两处合法的「缺失」——
	// 顶部采集警告「历史尚未完整回采…缺失不代表未发布」
	// 和统计说明里的「较早 Moment 可能缺失」，都不是时长列。
	table := overviewTable(t, body)
	for _, unwanted := range []string{"缺失", "待确认"} {
		if strings.Contains(table, unwanted) {
			t.Fatalf("成员数据总览表仍残留 %q:\n%s", unwanted, table)
		}
	}
	// 规则：总时长是完整口径，表尾总和不该再挂「（已知）」后缀。
	if strings.Contains(table, "（已知）") {
		t.Fatalf("总时长列被误判为不完整，挂了「（已知）」后缀:\n%s", table)
	}

	// 时长单元格必须严格是 HH:MM:SS：把表体里所有 6 位数字加 2 个冒号的
	// 片段抠出来逐个校验，确保没有半个冒号或缺位的情况混进报告。
	matches := regexp.MustCompile(`[0-9]{1,3}:[0-9]{2}:[0-9]{2}`).FindAllString(body, -1)
	if len(matches) < 5 {
		t.Fatalf("时长片段数量异常: %d 个 (%v)", len(matches), matches)
	}
	for _, m := range matches {
		parts := strings.Split(m, ":")
		if len(parts) != 3 || len(parts[1]) != 2 || len(parts[2]) != 2 {
			t.Fatalf("时长片段 %q 不是 HH:MM:SS 格式", m)
		}
		minutes, _ := strconv.Atoi(parts[1])
		secs, _ := strconv.Atoi(parts[2])
		if minutes > 59 || secs > 59 {
			t.Fatalf("时长片段 %q 的分/秒超过 59", m)
		}
	}
}

// overviewTable 抠出「成员数据总览」那张表的 tbody + tfoot。
// 只检查这块才能避开报告里其他合法的状态文案。
func overviewTable(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "<tbody>")
	end := strings.Index(body, "</tfoot>")
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("报告结构异常，找不到总览表:\n%s", body)
	}
	return body[start:end]
}
