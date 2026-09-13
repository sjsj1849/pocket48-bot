package weverse

import (
	"testing"
	"time"
)

func TestHistoryKeepsOnlySamePostForwardedOriginalsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := Event{ID: "old", PostID: "p", Kind: "comment", CommunityID: 235, MemberID: "yeon", Author: "YE-ON", Body: "original Korean", Translation: "bad native translation", Time: 1000, Videos: []VideoAttachment{{ID: "v", URL: "https://example.com/video?private-signature"}}}
	newReply := old
	newReply.ID = "new"
	newReply.Time = 2000
	other := old
	other.ID = "other"
	other.PostID = "q"
	observed := old
	observed.ID = "not-forwarded"
	if err = h.Record([]Event{old, newReply, other}, "sub"); err != nil {
		t.Fatal(err)
	}
	if err = h.Record([]Event{observed}, ""); err != nil {
		t.Fatal(err)
	}
	h.Close()
	h, err = OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	events, err := h.ForwardedPost("sub", 235, "p")
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	past := AIHistory(events, []AIEntry{{ID: "new"}})
	if len(past) != 1 || past[0].ID != "old" || past[0].Body != "original Korean" {
		t.Fatal("context contaminated by native translation", past)
	}
	if events[0].Videos[0].URL != "" {
		t.Fatal("temporary playback credential retained")
	}
	if events, err := h.ForwardedPost("different-sub", 235, "p"); err != nil || len(events) != 0 {
		t.Fatal("cross-subscription context", events, err)
	}
	all, err := h.Period(235, time.UnixMilli(1000), time.UnixMilli(2000))
	if err != nil || len(all) != 3 {
		t.Fatal("exclusive report end boundary", all, err)
	}
}

func TestReportCountsOwnMediaAndTeammateRootNotReplyTarget(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	t0 := p.Start.UnixMilli()
	members := []Member{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}, {ID: "d", Name: "D"}, {ID: "e", Name: "E"}, {ID: "f", Name: "F"}, {ID: "g", Name: "G"}, {ID: "h", Name: "H"}}
	own := Event{ID: "post:own", CommunityID: 235, PostID: "own", Kind: "post", MemberID: "a", Author: "A", Time: t0, Images: []string{"one", "two"}, Videos: []VideoAttachment{{ID: "v"}}}
	teammate := Event{ID: "comment:teammate", CommunityID: 235, PostID: "older-b", Kind: "comment", MemberID: "a", Author: "A", Time: t0 + 1, ParentMemberID: "fan", ParentProfileType: "FAN", Images: []string{"one"}, PostContext: &AIPostContext{MemberID: "b", ImageCount: 8}}
	replyOwn := teammate
	replyOwn.ID = "comment:own"
	replyOwn.PostID = "own"
	replyOwn.ParentMemberID = "b"
	replyOwn.ParentProfileType = "ARTIST"
	replyOwn.Images = nil
	replyOwn.PostContext = &AIPostContext{MemberID: "a"}
	live := Event{ID: "live:l", CommunityID: 235, PostID: "l", Kind: "live", MemberID: "a", Author: "A", Time: t0 + 3}
	ended := live
	ended.ID = "live_end:l"
	ended.Kind = "live_end"
	outside := own
	outside.ID = "post:outside"
	outside.Time = p.End.UnixMilli()
	report := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{own, teammate, teammate, replyOwn, live, ended, outside})
	if len(report.Members) != 8 {
		t.Fatal("zero-activity members omitted")
	}
	a := report.Members[0]
	if a.Posts != 1 || a.Replies != 2 || a.ImageMessages != 2 || a.Images != 3 || a.VideoMessages != 1 || a.Lives != 1 || a.TeammateReplies != 1 || a.Teammates["b"] != 1 {
		t.Fatal("incorrect report counts", a)
	}
	if len(report.TeammateReplies) != 1 || report.TeammateReplies[0].Owner != "B" {
		t.Fatal("direct fan target incorrectly used as root owner")
	}
}

func TestReportScheduleBeijingHalfYearAnnualAndNoInitialOldMail(t *testing.T) {
	settings := ReportSettings{Enabled: true, Monthly: true, FirstHalf: true, Annual: true, SendTime: "09:00", EnabledAt: "2026-09-14T00:00:00+08:00"}
	if periods := DueReportPeriods(settings, time.Date(2026, 9, 14, 10, 0, 0, 0, ReportLocation)); len(periods) != 0 {
		t.Fatal("old report sent immediately", periods)
	}
	if periods := DueReportPeriods(settings, time.Date(2026, 10, 1, 8, 59, 0, 0, ReportLocation)); len(periods) != 0 {
		t.Fatal("report sent before scheduled time")
	}
	periods := DueReportPeriods(settings, time.Date(2027, 1, 1, 9, 0, 0, 0, ReportLocation))
	if len(periods) != 2 || periods[0].Key != "monthly-2026-12" || periods[1].Key != "annual-2026" {
		t.Fatal(periods)
	}
	periods = DueReportPeriods(settings, time.Date(2027, 7, 1, 9, 0, 0, 0, ReportLocation))
	if len(periods) != 3 || periods[1].Start.Month() != 1 || periods[1].End.Month() != 7 {
		t.Fatal("half-year period incorrect", periods)
	}
}
