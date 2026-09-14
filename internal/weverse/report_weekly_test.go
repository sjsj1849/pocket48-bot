package weverse

import (
	"testing"
	"time"
)

func TestWeeklyPeriodNormalizesAcrossMonthAndYear(t *testing.T) {
	for _, tc := range []struct{ date, start, end string }{
		{"2026-09-15", "2026-09-14", "2026-09-21"},
		{"2026-09-20", "2026-09-14", "2026-09-21"},
		{"2027-01-01", "2026-12-28", "2027-01-04"},
	} {
		p, err := NewWeeklyReportPeriod(tc.date)
		if err != nil || p.Start.Format("2006-01-02") != tc.start || p.End.Format("2006-01-02") != tc.end || p.Key != "weekly-"+tc.start {
			t.Fatal(p, err)
		}
	}
	for _, date := range []string{"", "2026-02-30", "2024-09-15"} {
		if _, err := NewWeeklyReportPeriod(date); err == nil {
			t.Fatal("accepted", date)
		}
	}
}
func TestWeeklyScheduleNoOldSendAndMondayClockWithCatchup(t *testing.T) {
	s := ReportSettings{Enabled: true, Weekly: true, SendTime: "09:00", EnabledAt: "2026-09-01T00:00:00+08:00", WeeklyEnabledAt: "2026-09-15T00:00:00+08:00"}
	at := func(date string) time.Time { v, _ := time.Parse(time.RFC3339, date); return v }
	for _, date := range []string{"2026-09-15T10:00:00+08:00", "2026-09-21T08:59:59+08:00"} {
		if p := DueReportPeriods(s, at(date)); len(p) != 0 {
			t.Fatal("early report", p)
		}
	}
	for _, date := range []string{"2026-09-21T09:00:00+08:00", "2026-09-23T10:00:00+08:00"} {
		p := DueReportPeriods(s, at(date).UTC())
		if len(p) != 1 || p[0].Key != "weekly-2026-09-14" {
			t.Fatal(p)
		}
	}
	s.Weekly = false
	if p := DueReportPeriods(s, at("2026-09-21T09:00:00+08:00")); len(p) != 0 {
		t.Fatal(p)
	}
}
func TestWeeklyAggregationExcludesOtherWeeksAndMonthlyBreakdown(t *testing.T) {
	p, _ := NewWeeklyReportPeriod("2026-09-30")
	events := []Event{{ID: "post:a", Kind: "post", MemberID: "a", CommunityID: 235, Time: p.Start.UnixMilli()}, {ID: "post:b", Kind: "post", MemberID: "a", CommunityID: 235, Time: p.End.UnixMilli()}}
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, []Member{{ID: "a", Name: "A"}}, events)
	if r.Members[0].Posts != 1 || len(r.Months) != 0 {
		t.Fatal("week includes other-month activity", r)
	}
	if _, err := r.XLSX(); err != nil {
		t.Fatal(err)
	}
}
func TestWeeklyEnableTimestampSurvivesSaveAndResetsWhenReenabled(t *testing.T) {
	dir := t.TempDir()
	s, _ := LoadReportSettings(dir)
	s.Enabled = true
	s.Weekly = true
	if err := SaveReportSettings(dir, s); err != nil {
		t.Fatal(err)
	}
	saved, _ := LoadReportSettings(dir)
	if saved.WeeklyEnabledAt == "" {
		t.Fatal("missing enable timestamp")
	}
	initial := saved.WeeklyEnabledAt
	saved.WeeklyEnabledAt = "2025-01-01T00:00:00+08:00"
	if err := SaveReportSettings(dir, saved); err != nil {
		t.Fatal(err)
	}
	next, _ := LoadReportSettings(dir)
	if next.WeeklyEnabledAt != initial {
		t.Fatal("client changed enable timestamp")
	}
	next.Weekly = false
	_ = SaveReportSettings(dir, next)
	next.Weekly = true
	_ = SaveReportSettings(dir, next)
	next, _ = LoadReportSettings(dir)
	if next.WeeklyEnabledAt == "" {
		t.Fatal("missing reenable timestamp")
	}
}
