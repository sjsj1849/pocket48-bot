package weverse

import (
	"strings"
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
	unknown.PostContext = nil
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
	chat := Event{CommunityID: 235, MemberID: "a", Author: "A", PostID: "live-b", Time: p.Start.AddDate(0, 2, 0).UnixMilli(), Kind: "live_chat", ID: "live_chat:one", LiveHostMemberID: "b", LiveHostAuthor: "B"}
	chatAgain := chat
	chatAgain.ID = "live_chat:two"
	chatOtherLive := chat
	chatOtherLive.ID = "live_chat:three"
	chatOtherLive.PostID = "live-b-2"
	groupChat := chat
	groupChat.ID = "live_chat:group"
	groupChat.PostID = "live-group"
	groupChat.LiveHostMemberID = "group-account"
	groupChat.LiveHostAuthor = "Official"
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{base, fan, teammate, self, unknown, moment, live, fan, chat, chatAgain, chatOtherLive, groupChat})
	m := r.Members[0]
	if m.Posts != 1 || m.PostPhotos != 2 || m.PostComments != 0 || m.PostLikes != 42 || m.CommentPosts != 1 || m.Replies != 4 || m.FanReplies != 1 || m.MemberReplies != 1 || m.SelfReplies != 1 || m.UnknownReplies != 1 || m.ReplyMembers["b"] != 1 || m.Videos != 2 || m.Moments != 1 || m.LiveSeconds != 120 || m.SoloLives != 0 || m.LiveChats != 4 || m.LiveChatLives != 3 || m.LiveChatHosts["b"] != 3 || m.LiveChatHostLives["b"] != 2 || m.LiveChatHosts["group-account"] != 1 || m.LiveChatHostLives["group-account"] != 1 {
		t.Fatalf("incorrect distinct statistics: %+v", m)
	}
	if len(r.LiveChatTargets) != 3 || r.LiveChatTargets[2].Name != "团体账号：Official" || r.LiveChatTargets[2].IsMember {
		t.Fatalf("special live host missing from matrix: %+v", r.LiveChatTargets)
	}
	if len(r.Months) != 12 || r.Months[0].Members[0].Posts != 1 || r.Months[1].Members[0].Replies != 4 {
		t.Fatal("missing monthly comparison", r.Months)
	}
	if EngagementText(0, 0, 1) != "缺失" || EngagementText(0, 1, 1) != "0" {
		t.Fatal("missing engagement represented as zero")
	}
}

func TestReceivedMemberReplyCountsUsesDirectReplyTargets(t *testing.T) {
	counts := receivedMemberReplyCounts([]MemberCount{
		{ID: "a", ReplyMembers: map[string]int{"b": 2, "c": 1}},
		{ID: "b", ReplyMembers: map[string]int{"a": 4}},
	})
	if counts["a"] != 4 || counts["b"] != 2 || counts["c"] != 1 {
		t.Fatalf("unexpected received reply counts: %#v", counts)
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

func TestConfirmedLiveParticipantsOwnDurationAndChatTargets(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	members := []Member{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}, {ID: "c", Name: "C"}}
	live := Event{ID: "live:shared", PostID: "shared", Kind: "live", CommunityID: 235, MemberID: "group-account", Author: "Official", Time: p.Start.UnixMilli(), LiveDuration: 600, LiveParticipants: []string{"a", "b"}, LiveParticipantsConfirmed: true}
	chat := Event{ID: "live_chat:c", PostID: "shared", Kind: "live_chat", CommunityID: 235, MemberID: "c", Author: "C", Time: p.Start.Add(time.Minute).UnixMilli(), LiveHostMemberID: "a", LiveParticipants: []string{"a", "b"}, LiveParticipantsConfirmed: true}
	hostChat := chat
	hostChat.ID = "live_chat:a"
	hostChat.MemberID = "a"
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{live, chat, hostChat})
	if r.Members[0].LiveSeconds != 600 || r.Members[1].LiveSeconds != 600 || r.Members[0].SoloLives != 0 || r.Members[1].SoloLives != 0 {
		t.Fatalf("shared live duration attributed incorrectly: %+v", r.Members)
	}
	if r.Members[2].LiveChatHosts["a"] != 1 || r.Members[2].LiveChatHosts["b"] != 1 || r.Members[0].LiveChatHosts["a"] != 0 || r.Members[0].LiveChatHosts["b"] != 1 {
		t.Fatalf("shared live chat targets attributed incorrectly: %+v", r.Members)
	}
}

func TestConsecutiveSameOwnerLivesMergeByCountButAddDuration(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	members := []Member{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	start := p.Start.Add(12 * time.Hour)
	first := Event{ID: "live:first", PostID: "first", Kind: "live", CommunityID: 235, MemberID: "a", Time: start.UnixMilli(), LiveStartedAt: start.UnixMilli(), LiveDuration: 600, LiveParticipants: []string{"a"}, LiveParticipantsConfirmed: true}
	restart := first
	restart.ID, restart.PostID = "live:restart", "restart"
	restart.Time = start.Add(15 * time.Minute).UnixMilli()
	restart.LiveStartedAt = restart.Time
	restart.LiveDuration = 900
	later := first
	later.ID, later.PostID = "live:later", "later"
	later.Time = start.Add(2 * time.Hour).UnixMilli()
	later.LiveStartedAt = later.Time
	later.LiveDuration = 300
	differentOwners := restart
	differentOwners.ID, differentOwners.PostID = "live:shared", "shared"
	differentOwners.Time = start.Add(20 * time.Minute).UnixMilli()
	differentOwners.LiveStartedAt = differentOwners.Time
	differentOwners.LiveParticipants = []string{"a", "b"}
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{later, restart, differentOwners, first})
	if r.Members[0].Lives != 3 || r.Members[0].TimedLives != 3 || r.Members[0].LiveSeconds != 2700 || r.Members[0].SoloLives != 2 || r.Members[0].SoloLiveSeconds != 1800 {
		t.Fatalf("consecutive live merge is wrong: %+v", r.Members[0])
	}
	if r.Members[1].Lives != 1 || r.Members[1].LiveSeconds != 900 {
		t.Fatalf("different owner set should remain separate: %+v", r.Members[1])
	}
	if !strings.Contains(r.CoverageNote(), "前后间隔不超过 30 分钟") {
		t.Fatal("coverage note does not document live merge")
	}
}

func TestEveryReportUsesHearts2HeartsAgeOrder(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	names := []string{"YE-ON", "JUUN", "JIWOO", "Hearts2Hearts", "IAN", "STELLA", "CARMEN", "A-NA", "YUHA"}
	members := make([]Member, 0, len(names))
	for _, name := range names {
		members = append(members, Member{ID: name, Name: name})
	}
	r := AggregateReport(ReportSettings{CommunityID: 235, CommunityName: "Hearts2Hearts"}, p, members, nil)
	want := []string{"CARMEN", "JIWOO", "YUHA", "STELLA", "JUUN", "A-NA", "IAN", "YE-ON"}
	if len(r.Members) != len(want) {
		t.Fatalf("report contains non-member accounts: %+v", r.Members)
	}
	for i, name := range want {
		if r.Members[i].Name != name {
			t.Fatalf("age order[%d]=%s, want %s", i, r.Members[i].Name, name)
		}
	}
}

func TestTopLevelCommentsUseRootPostAuthorAsReplyTarget(t *testing.T) {
	p, _ := NewReportPeriod("monthly", 2026, 9)
	members := []Member{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}
	self := Event{ID: "comment:self-root", PostID: "self-root", Kind: "comment", CommunityID: 235, MemberID: "a", Time: p.Start.UnixMilli(), PostContext: &AIPostContext{MemberID: "a", AuthorIsArtist: true}}
	teammate := self
	teammate.ID = "comment:teammate-root"
	teammate.PostContext = &AIPostContext{MemberID: "b", AuthorIsArtist: true}
	fan := self
	fan.ID = "comment:fan-root"
	fan.PostContext = &AIPostContext{MemberID: "fan", AuthorIsArtist: false}
	r := AggregateReport(ReportSettings{CommunityID: 235}, p, members, []Event{self, teammate, fan})
	if r.Members[0].SelfReplies != 1 || r.Members[0].MemberReplies != 1 || r.Members[0].FanReplies != 1 || r.Members[0].UnknownReplies != 0 {
		t.Fatalf("top-level replies classified incorrectly: %+v", r.Members[0])
	}
}
