package weverse

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestParseH2HPasswordBody(t *testing.T) {
	entries, err := parseH2HPasswordBody("260908 🌴: one two\n260909 🧁：three")
	if err != nil || len(entries) != 2 {
		t.Fatalf("parse: %+v %v", entries, err)
	}
	if entries[0].MemberName != "CARMEN" || entries[0].Password != "one two" || entries[1].MemberName != "STELLA" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if _, err = parseH2HPasswordBody("260908 ❓: secret"); err == nil {
		t.Fatal("unknown member emoji accepted")
	}
}

func TestSyncH2HPasswordsMatchesAndVerifiesBeforeSaving(t *testing.T) {
	published := time.Date(2026, 9, 8, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60)).UnixMilli()
	const secret = "published-secret"
	const secondSecret = "second-published-secret"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/source":
			respond(w, Object{"documents": []any{Object{
				"name": "projects/test/documents/stock_articles/source",
				"fields": Object{
					"groupId":   Object{"stringValue": "hearts2hearts"},
					"category":  Object{"stringValue": "weverse_password"},
					"title":     Object{"stringValue": "Weverseシークレット投稿パスワード"},
					"body":      Object{"stringValue": "260908 🌴: " + secret + "\n260908 🌴: " + secondSecret},
					"updatedAt": Object{"timestampValue": "2026-09-09T03:16:56Z"},
				},
			}}})
		case "/member/v1.1/community-235/artistMembers":
			respond(w, []any{Object{"memberId": "carmen-id", "profileName": "CARMEN"}})
		case "/post/v1.0/community-235/artistTabPosts":
			respond(w, Object{"data": []any{
				Object{"postId": "3-secret-a", "publishedAt": published, "locked": true, "author": Object{"memberId": "carmen-id", "profileName": "CARMEN"}},
				Object{"postId": "3-secret-b", "publishedAt": published, "locked": true, "author": Object{"memberId": "carmen-id", "profileName": "CARMEN"}},
			}, "paging": Object{}})
		case "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{}, "paging": Object{}})
		case "/post/v1.0/post-3-secret-a", "/post/v1.0/post-3-secret-b":
			want := secondSecret
			id := "3-secret-a"
			if r.URL.Path == "/post/v1.0/post-3-secret-b" {
				want = secret
				id = "3-secret-b"
			}
			if r.URL.Query().Get("lockPassword") != want {
				w.WriteHeader(http.StatusForbidden)
				respond(w, Object{"errorCode": "post_700"})
				return
			}
			respond(w, Object{"postId": id, "postType": "POST", "plainBody": "unlocked", "publishedAt": published, "locked": true, "author": Object{"memberId": "carmen-id", "profileName": "CARMEN"}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	status, err := c.syncH2HPasswords(context.Background(), nil, 235, "hearts2hearts", c.APIBase+"/source")
	if err != nil {
		t.Fatal(err)
	}
	if status.Entries != 2 || status.Candidates != 2 || status.Imported != 2 || status.Unmatched != 0 {
		t.Fatalf("status: %+v", status)
	}
	rows, err := LoadPostPasswords(c.Dir)
	if err != nil || len(rows) != 2 || rows[0].PostID != "3-secret-a" || rows[0].Password != secondSecret || rows[0].Source != h2hPasswordSource || rows[1].PostID != "3-secret-b" || rows[1].Password != secret {
		t.Fatalf("saved rows: %+v %v", rows, err)
	}
}

func TestPasswordCandidatesIncludesLatestLockedMoment(t *testing.T) {
	published := time.Date(2026, 9, 9, 10, 0, 0, 0, time.FixedZone("KST", 9*60*60)).UnixMilli()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/post/v1.0/community-235/artistTabPosts", "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{}, "paging": Object{}})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	})
	members := []Member{{ID: "stella-id", Name: "STELLA", latestMomentPostID: "3-moment", latestMomentAt: published, latestMomentLocked: true}}
	entries := []h2hPasswordEntry{{Date: "260909", Emoji: "🧁", MemberName: "STELLA", Password: "secret"}}
	candidates, err := c.passwordCandidates(context.Background(), 235, "hearts2hearts", members, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Kind != "moment" || candidates[0].PostID != "3-moment" || candidates[0].MemberID != "stella-id" {
		t.Fatalf("candidates: %+v", candidates)
	}
}
