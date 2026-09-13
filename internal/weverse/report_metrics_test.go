package weverse

import (
	"testing"
	"time"
)

func TestReportDirectTargetsMonthlyBreakdownAndPostOnlyPhotos(t *testing.T) {
	p, _ := NewReportPeriod("annual", 2026, 1)
	zero, likes := int64(0), int64(42)
	members := []Member{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	base := Event{CommunityID: 235, MemberID: "a", Author: "A", PostID: "root", Time: p.Start.UnixMilli(), Kind: "post", ID: "post:root", Images: []string{"p1", "p2"}, PostComments: &zero, PostLikes: &likes}
	fan := Event{CommunityID: 235, MemberID: "a", Author: "A", PostID: "root", Time: p.Start.AddDate(0, 1, 0).UnixMilli(), Kind: "comment", ID: "comment:fan", ParentMemberID: "fan", ParentProfileType: "FAN", Images: []string{"reply-photo"}, PostContext: &AIPostContext{MemberID: "b"}}
	teammate := fan
	teammate.ID = "comment:b"
	teammate.ParentMemberID = "b"
	self := fan
	self.ID = "comment:self"
	self.ParentMemberID = "a"
	unknown := fan
	unknown.ID = "comment:unknown"
	unknown.ParentMemberID = ""
	unknown.ParentProfileType = ""
	unknown.ParentContextUnavailable = true
	moment := base
	moment.ID = "moment:m"
	moment.Kind = "moment"
	moment.PostID = "m"
	moment.Videos = []VideoAttachment{{ID: "v1"}, {ID: "v2"}}
	live := base
	live.ID = "live:l"
	live.Kind = "live"
	live.PostID = "l"
	live.LiveDuration = 120
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{base, fan, teammate, self, unknown, moment, live, fan})
	m := r.Members[0]
	if m.Posts != 1 || m.PostPhotos != 2 || m.PostComments != 0 || m.PostLikes != 42 || m.CommentPosts != 1 || m.Replies != 4 || m.FanReplies != 1 || m.MemberReplies != 1 || m.SelfReplies != 1 || m.UnknownReplies != 1 || m.ReplyMembers["b"] != 1 || m.Videos != 2 || m.Moments != 1 || m.LiveSeconds != 120 || m.SoloLives != 0 {
		t.Fatalf("incorrect distinct statistics: %+v", m)
	}
	if len(r.Months) != 12 || r.Months[0].Members[0].Posts != 1 || r.Months[1].Members[0].Replies != 4 {
		t.Fatal("missing monthly comparison", r.Months)
	}
	if EngagementText(0, 0, 1) != "缺失" || EngagementText(0, 1, 1) != "0" {
		t.Fatal("missing engagement represented as zero")
	}
}
func TestHistoryPreservesSnapshotAndEnrichesCrossMonthLive(t *testing.T) {
	h, e := OpenHistory(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close()
	comments, likes := int64(17), int64(50)
	start := time.Date(2026, 8, 31, 23, 50, 0, 0, ReportLocation)
	post := Event{ID: "post:p", PostID: "p", Kind: "post", CommunityID: 235, MemberID: "a", Time: start.UnixMilli(), PostComments: &comments, PostLikes: &likes, MetricsAt: start.UnixMilli()}
	live := Event{ID: "live:l", PostID: "l", Kind: "live", CommunityID: 235, MemberID: "a", Time: start.UnixMilli()}
	if e = h.Record([]Event{post, live}, ""); e != nil {
		t.Fatal(e)
	}
	post.PostComments = nil
	post.PostLikes = nil
	post.MetricsAt = 0
	end := live
	end.ID = "live_end:l"
	end.Kind = "live_end"
	end.Time = start.Add(time.Hour).UnixMilli()
	end.LiveDuration = 3600
	if e = h.Record([]Event{post, end}, ""); e != nil {
		t.Fatal(e)
	}
	events, e := h.Period(235, start.Add(-time.Minute), start.Add(10*time.Minute))
	if e != nil {
		t.Fatal(e)
	}
	for _, event := range events {
		if event.Kind == "post" && (event.PostLikes == nil || *event.PostLikes != 50 || event.MetricsAt == 0) {
			t.Fatal("lost known snapshot", event)
		}
		if event.Kind == "live" && event.LiveDuration != 3600 {
			t.Fatal("ending outside cohort month lost", event)
		}
	}
}
func TestMomentAndZeroEngagementSourceParsing(t *testing.T) {
	p := Object{"postId": "1-123", "author": Object{"memberId": "a"}, "postType": "MOMENT", "publishedAt": float64(1000), "commentCount": float64(0), "emotionCount": float64(3), "extension": Object{"moment": Object{"video": Object{"videoId": "3-123"}}}}
	e, err := eventFromPost(p, "hearts2hearts", 235)
	if err != nil || e.Kind != "moment" || len(e.Videos) != 1 || e.PostComments == nil || *e.PostComments != 0 || e.PostLikes == nil || *e.PostLikes != 3 {
		t.Fatal("Moment or known zero lost", e, err)
	}
	if !Matches(Subscription{Enabled: true, CommunityID: 235, Posts: true}, e) {
		t.Fatal("Moment is not enabled with posts")
	}
}
