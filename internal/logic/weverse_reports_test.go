package logic

import (
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/weverse"
)

func TestWeverseReportHTMLUsesClearDurationAndInteractionLabels(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, weverse.ReportLocation)
	report := weverse.Report{
		Community:        "Hearts2Hearts",
		AsOf:             start.AddDate(0, 1, 0),
		RecordingStarted: start.Add(13*24*time.Hour + 27*time.Minute),
		Period:           weverse.ReportPeriod{Title: "2026年9月月报", Start: start, End: start.AddDate(0, 1, 0)},
		Members: []weverse.MemberCount{
			{ID: "a", Name: "CARMEN", Teammates: map[string]int{"b": 2}, ReplyMembers: map[string]int{"b": 4}, LiveChatHosts: map[string]int{"b": 3}, LiveChatHostLives: map[string]int{"b": 1}},
			{ID: "b", Name: "JIWOO", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{}},
		},
		LiveChatTargets: []weverse.LiveChatTarget{{ID: "a", Name: "CARMEN", IsMember: true}, {ID: "b", Name: "JIWOO", IsMember: true}},
	}
	body := WeverseReportHTML(report)
	for _, want := range []string{"00:00:00", "被回复", "发弹幕直播场次", "直播成员", "总和", `<ul class="method-list">`, "此前可见的帖子、回复、直播及直播弹幕已做历史回采"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report HTML missing %q", want)
		}
	}
	for _, unwanted := range []string{"Moment*", "参与直播", "直播发起成员", "未确认单人", ">直播时长</h2>"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("report HTML still contains %q", unwanted)
		}
	}
	for _, want := range []string{"回复数", "被回复数", "发送弹幕数", "收到弹幕数"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report HTML missing directional total label %q", want)
		}
	}
	for _, want := range []string{"matrix-detail-highest", "matrix-zero", "matrix-total-highest", "matrix-total-lowest", "matrix-grand"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report HTML missing matrix emphasis class %q", want)
		}
	}
	if strings.Contains(body, `<span>01</span>`) {
		t.Fatal("single section number should not be rendered")
	}
}

func TestReportMetricRankUsesFourDistinctBands(t *testing.T) {
	rows := [][]reportMetricCell{{metric(10)}, {metric(20)}, {metric(30)}, {metric(40)}, {metric(50)}}
	want := []string{"metric-lowest", "metric-low", "", "metric-high", "metric-highest"}
	for i, expected := range want {
		class, _ := reportMetricRank(rows, 0, rows[i][0])
		if class != expected {
			t.Fatalf("rank %d class=%q, want %q", i, class, expected)
		}
	}
}

func TestReplyTrendUsesDailyBucketsForMonthlyReports(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, weverse.ReportLocation)
	report := weverse.Report{
		AsOf:   start.AddDate(0, 1, 0),
		Period: weverse.ReportPeriod{Kind: "monthly", Start: start, End: start.AddDate(0, 1, 0)},
		Members: []weverse.MemberCount{
			{ID: "a", Name: "A", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{}},
			{ID: "b", Name: "B", Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{}},
		},
		Events: []weverse.Event{
			{ID: "1", Kind: "comment", MemberID: "a", Time: start.Add(time.Hour).UnixMilli()},
			{ID: "2", Kind: "comment", MemberID: "a", Time: start.Add(2 * time.Hour).UnixMilli()},
			{ID: "3", Kind: "comment", MemberID: "b", Time: start.Add(3 * time.Hour).UnixMilli()},
		},
	}
	body := WeverseReportHTML(report)
	for _, want := range []string{"成员回复趋势", "9月1日", "9月30日", "reply-highest", "reply-lowest", "期间总回复", `matrix-total reply-highest`, `matrix-total reply-lowest`} {
		if !strings.Contains(body, want) {
			t.Fatalf("daily reply trend missing %q", want)
		}
	}
	if !strings.Contains(body, `<tfoot><tr><th>期间总回复</th><td class="reply-highest">2</td><td class="reply-lowest">1</td><td class="matrix-grand">3</td>`) {
		t.Fatal("period reply totals should highlight member maximum and minimum while preserving the grand total")
	}
}

func TestReplyTrendUsesCalendarWeeksForAnnualReports(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, weverse.ReportLocation)
	report := weverse.Report{AsOf: start.AddDate(1, 0, 0), Period: weverse.ReportPeriod{Kind: "annual", Start: start, End: start.AddDate(1, 0, 0)}}
	buckets := reportReplyBuckets(report)
	if len(buckets) != 53 || buckets[0].Label != "1月1日–4日" || buckets[1].Label != "1月5日–11日" {
		t.Fatalf("unexpected annual reply buckets: count=%d first=%+v second=%+v", len(buckets), buckets[0], buckets[1])
	}
}
