package weverse

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestReportMemberCommentsUsesProfileHistoryAcrossFanPosts(t *testing.T) {
	period, err := NewReportPeriod("monthly", 2026, 9)
	if err != nil {
		t.Fatal(err)
	}
	artist := Object{"memberId": "a", "profileType": "ARTIST", "profileName": "A"}
	fan := Object{"memberId": "fan", "profileType": "FAN", "profileName": "Fan"}
	comment := Object{
		"commentId": "reply-1",
		"createdAt": period.Start.Add(time.Hour).UnixMilli(),
		"body":      "reply",
		"author":    artist,
		"parent": Object{"type": "POST", "data": Object{
			"postId": "fan-post", "body": "fan root", "author": fan,
		}},
		"root": Object{"type": "POST", "data": Object{
			"postId": "fan-post", "body": "fan root", "author": fan,
			"community": Object{"communityId": 235},
		}},
	}
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/comment/v1.0/member-a/comments" {
			t.Fatalf("unexpected request path %s", r.URL.Path)
		}
		if r.URL.Query().Get("fieldSet") != "memberCommentsV1" {
			t.Fatal("member comment field set missing")
		}
		respond(w, Object{"data": []any{comment}})
	})
	events, err := client.reportMemberComments(context.Background(), []Member{{ID: "a", Name: "A"}}, 235, "hearts2hearts", period)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "comment:reply-1" || events[0].ParentProfileType != "FAN" || events[0].PostContext == nil || events[0].PostContext.AuthorIsArtist {
		t.Fatalf("fan-post reply was not reconstructed: %+v", events)
	}
}
