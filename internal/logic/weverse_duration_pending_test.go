package logic

import (
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/weverse"
)

// TestPendingDurationRendersPending 验证规则 2：
// 只有「直播没完成面板二次确认」导致单人时长为空时，才显示「待确认」。
//
// 关键在于「总时长」列永远不该跟着变成「待确认」——
// 总时长只要该场直播有时长就累加，与确认状态无关。
func TestPendingDurationRendersPending(t *testing.T) {
	build := func(unconfirmed int) string {
		start := time.Date(2026, 9, 1, 0, 0, 0, 0, weverse.ReportLocation)
		r := weverse.Report{
			Community:        "Hearts2Hearts",
			AsOf:             start.AddDate(0, 1, 0),
			RecordingStarted: start,
			Period:           weverse.ReportPeriod{Title: "2026年9月月报", Start: start, End: start.AddDate(0, 1, 0)},
			Members: []weverse.MemberCount{
				{ID: "a", Name: "CARMEN", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{},
					LiveSeconds: 1860, TimedLives: 2, SoloLiveSeconds: 1800, SoloLives: 1},
				// 这个人没有任何已确认的单人直播。
				{ID: "b", Name: "JIWOO", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{},
					LiveSeconds: 3723, TimedLives: 1},
			},
			LiveChatTargets: []weverse.LiveChatTarget{
				{ID: "a", Name: "CARMEN", IsMember: true},
				{ID: "b", Name: "JIWOO", IsMember: true},
			},
			UnconfirmedLives: unconfirmed,
		}
		return WeverseReportHTML(r)
	}

	// ---- 场景 A：有未确认直播 ----
	pending := build(3)
	table := overviewTable(t, pending)
	if !strings.Contains(table, "待确认") {
		t.Fatalf("有未确认直播时单人时长列应为「待确认」:\n%s", table)
	}
	// 但总时长列仍是确定的数值，且必须出现「待确认」以外的时间。
	if strings.Count(table, "待确认") != 1 {
		t.Fatalf("「待确认」只应出现在单人时长列的 JIWOO 行，实际出现 %d 次", strings.Count(table, "待确认"))
	}
	for _, want := range []string{"00:31:00", "01:02:03"} {
		if !strings.Contains(table, want) {
			t.Fatalf("总时长列不应受待确认影响，缺少 %q:\n%s", want, table)
		}
	}
	// 「待确认」不是最终值，表尾总和必须标注这是已知部分的合计。
	if !strings.Contains(table, "（已知）") {
		t.Fatalf("存在待确认时总和行应标注「（已知）」:\n%s", table)
	}

	// ---- 场景 B：没有未确认直播 ----
	clean := build(0)
	cleanTable := overviewTable(t, clean)
	if strings.Contains(cleanTable, "待确认") {
		t.Fatalf("没有未确认直播时不该出现「待确认」:\n%s", cleanTable)
	}
	if strings.Contains(cleanTable, "缺失") {
		t.Fatalf("没有未确认直播时不该出现「缺失」:\n%s", cleanTable)
	}
	// 规则 1：全列都该是确定的数字，没有数据就写 0。
	if !strings.Contains(cleanTable, "00:00:00") {
		t.Fatalf("无数据成员应写成 00:00:00:\n%s", cleanTable)
	}
	// 没有待确认时总和是完整口径，不该再挂「（已知）」。
	if strings.Contains(cleanTable, "（已知）") {
		t.Fatalf("无待确认时总和行不该挂「（已知）」:\n%s", cleanTable)
	}
	// 总和 = 1860 + 3723 = 5583 = 01:33:03
	if !strings.Contains(cleanTable, "01:33:03") {
		t.Fatalf("总和应为 01:33:03:\n%s", cleanTable)
	}
}
