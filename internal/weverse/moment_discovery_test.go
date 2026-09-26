package weverse

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMomentDiscoveredWithoutNotificationAndForwardsOnce(t *testing.T) {
	now := time.Now()
	artist := Object{"memberId": "stella", "profileName": "STELLA", "artistLatestMoment": Object{"postId": "1-2", "publishedAt": now.UnixMilli()}}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/member/v1.1/community-235/artistMembers":
			respond(w, []any{artist})
		case "/post/v1.0/community-235/artistTabPosts", "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{}})
		case "/post/v1.0/post-1-2":
			respond(w, Object{"postId": "1-2", "postType": "MOMENT_W1", "author": artist, "body": "안녕", "publishedAt": now.UnixMilli(), "extension": Object{"momentW1": Object{"photo": Object{"url": "https://example.test/photo.jpg"}}}})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	s := Subscription{ID: "s", CommunityID: 235, Slug: "hearts2hearts", Posts: true, Enabled: true}
	mustWrite(t, c.Dir, "settings.json", Settings{Subscriptions: []Subscription{s}})
	events, err := c.Events(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	e := events[0]
	if e.Kind != "moment" || len(e.Images) != 1 || e.URL != "https://weverse.io/hearts2hearts/moment/stella/post/1-2" {
		t.Fatal(e)
	}
	state := Runtime{}
	Pending(&state, s, nil, now.Add(-time.Minute))
	if len(Pending(&state, s, events, now)) != 1 {
		t.Fatal("moment not forwarded")
	}
	MarkDelivered(&state, s, e)
	if len(Pending(&state, s, events, now)) != 0 {
		t.Fatal("duplicate moment")
	}
}

func TestMembershipOnlyMomentIsAnnouncedWithoutFetchingContent(t *testing.T) {
	now := time.Now()
	artist := Object{
		"memberId":    "stella",
		"profileName": "STELLA",
		"artistLatestMoment": Object{
			"postId":         "3-membership",
			"publishedAt":    now.UnixMilli(),
			"membershipOnly": true,
			"body":           "restricted body",
			"summary":        Object{"thumbnails": []any{Object{"url": "https://example.test/restricted.jpg"}}},
		},
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/member/v1.1/community-235/artistMembers":
			respond(w, []any{artist})
		case "/post/v1.0/community-235/artistTabPosts", "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{}})
		case "/post/v1.0/post-3-membership":
			t.Fatal("membership-only content endpoint must not be requested")
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	s := Subscription{ID: "s", CommunityID: 235, Slug: "hearts2hearts", Posts: true, Enabled: true}
	mustWrite(t, c.Dir, "settings.json", Settings{Subscriptions: []Subscription{s}})
	// Simulate an older bot having completed scans after this restricted Moment
	// was published without recording it.
	mustWrite(t, c.Dir, "status.json", Status{LastSuccess: now.Add(time.Hour).Format(time.RFC3339)})
	events, err := c.Events(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatal(events, err)
	}
	e := events[0]
	if !e.MembershipOnly || e.Kind != "moment" || e.MemberID != "stella" || e.Body != "" || len(e.Images) != 0 || len(e.Videos) != 0 {
		t.Fatal(e)
	}
	if e.URL != "https://weverse.io/hearts2hearts/moment/stella/post/3-membership" {
		t.Fatal(e.URL)
	}
}

func TestPasswordProtectedMomentIsAnnouncedWithoutPassword(t *testing.T) {
	now := time.Now()
	artist := Object{"memberId": "stella", "profileName": "STELLA", "artistLatestMoment": Object{"postId": "3-secret", "publishedAt": now.UnixMilli(), "locked": true}}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/member/v1.1/community-235/artistMembers":
			respond(w, []any{artist})
		case "/post/v1.0/community-235/artistTabPosts", "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{}})
		case "/post/v1.0/post-3-secret":
			t.Fatal("locked Moment must not be fetched without a configured password")
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	s := Subscription{ID: "s", CommunityID: 235, Slug: "hearts2hearts", Posts: true, Enabled: true}
	mustWrite(t, c.Dir, "settings.json", Settings{Subscriptions: []Subscription{s}})
	mustWrite(t, c.Dir, "status.json", Status{LastSuccess: now.Add(time.Hour).Format(time.RFC3339)})
	events, err := c.Events(context.Background())
	if err != nil || len(events) != 1 || !events[0].PasswordProtected || events[0].Body != "" || len(events[0].Images) != 0 {
		t.Fatal(events, err)
	}
}

func TestPasswordProtectedPostMetadataIsSanitized(t *testing.T) {
	e, err := eventFromPost(Object{
		"postId":             "1-secret",
		"postType":           "NORMAL",
		"publishedAt":        time.Now().UnixMilli(),
		"locked":             true,
		"plainBody":          "restricted body",
		"author":             Object{"memberId": "stella", "profileName": "STELLA"},
		"orderedAttachments": []any{Object{"type": "photo", "data": Object{"url": "https://example.test/restricted.jpg"}}},
	}, "hearts2hearts", 235)
	if err != nil || !e.PasswordProtected || e.Body != "" || len(e.Images) != 0 || len(e.Videos) != 0 {
		t.Fatal(e, err)
	}
}
