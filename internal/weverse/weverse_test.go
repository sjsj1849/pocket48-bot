package weverse

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustWrite(t *testing.T, dir, name string, data any) {
	t.Helper()
	if err := Write(dir, name, data); err != nil {
		t.Fatal(err)
	}
}
func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	c := NewClient(t.TempDir(), "")
	c.APIBase = server.URL
	c.AccountBase = server.URL
	mustWrite(t, c.Dir, "session.json", Session{AccessToken: "test-access", RefreshToken: "test-refresh", DeviceID: "test-device"})
	t.Cleanup(c.HTTP.CloseIdleConnections)
	return c
}
func respond(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func TestRefreshSerializedAcrossClients(t *testing.T) {
	var rotations atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/token/refresh" {
			if rotations.Add(1) != 1 {
				t.Error("refresh token reused")
			}
			time.Sleep(30 * time.Millisecond)
			respond(w, Session{AccessToken: "new-access", RefreshToken: "new-refresh"})
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Error("stale access token")
		}
		respond(w, Object{"ok": true})
	})
	expired := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`)) + ".x"
	mustWrite(t, c.Dir, "session.json", Session{AccessToken: expired, RefreshToken: "test-refresh"})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := NewClient(c.Dir, "")
			defer other.HTTP.CloseIdleConnections()
			other.APIBase = c.APIBase
			other.AccountBase = c.AccountBase
			var out Object
			if err := other.call(context.Background(), "/test", true, &out); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rotations.Load() != 1 {
		t.Fatal(rotations.Load())
	}
	var session Session
	_ = Read(c.Dir, "session.json", &session)
	if session.RefreshToken != "new-refresh" {
		t.Fatal("rotation not persisted")
	}
	info, _ := os.Stat(filepath.Join(c.Dir, "session.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("session permissions")
	}
}
func TestUnauthorizedRetryAndSafeErrors(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/token/refresh" {
			respond(w, Session{AccessToken: "rotated"})
			return
		}
		if r.Header.Get("Authorization") == "Bearer test-access" {
			requests.Add(1)
			w.WriteHeader(401)
			return
		}
		respond(w, Object{"ok": true})
	})
	var out Object
	if err := c.call(context.Background(), "/test", true, &out); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("expected one retry")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.call(ctx, "/test?secret=must-not-leak", false, &out); err == nil || strings.Contains(err.Error(), "must-not-leak") {
		t.Fatal("unsafe network error")
	}
}
func TestSessionLockCancellation(t *testing.T) {
	dir := t.TempDir()
	unlock, err := sessionLock(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = sessionLock(ctx, dir)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestSearchPagesAliasesAndLinks(t *testing.T) {
	var calls int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "" {
			t.Error("public search leaked authorization")
		}
		if r.URL.Query().Get("after") == "" {
			respond(w, Object{"data": []any{}, "paging": Object{"nextParams": Object{"after": "next/235"}}})
			return
		}
		respond(w, Object{"data": []any{Object{"communityId": 235, "communityName": "Hearts2Hearts", "urlPath": "hearts2hearts", "artistOfficialNames": Object{"data": []string{"CARMEN", "JIWOO"}}}}})
	})
	for _, q := range []string{"H2H", "hearts to hearts", "CARMEN", "https://weverse.io/hearts2hearts/artist"} {
		items, err := c.Search(context.Background(), q)
		if err != nil || len(items) != 1 || items[0].ID != 235 {
			t.Fatalf("%s: %v %v", q, items, err)
		}
	}
	if calls != 2 {
		t.Fatalf("catalog not cached: %d", calls)
	}
}
func TestPendingBaselineRestartAndMemberSelection(t *testing.T) {
	now := time.Now()
	sub := Subscription{ID: "s", CommunityID: 235, GroupID: 123, MemberIDs: []string{"carmen"}, Posts: true, Comments: true, Live: true, Enabled: true}
	var state Runtime
	old := Event{ID: "post:old", Kind: "post", CommunityID: 235, MemberID: "carmen", Time: now.Add(-time.Hour).UnixMilli()}
	if p := Pending(&state, sub, []Event{old}, now); len(p) != 0 {
		t.Fatal("history sent")
	}
	fresh := old
	fresh.ID = "comment:new"
	fresh.Kind = "comment"
	fresh.Time = now.Add(time.Second).UnixMilli()
	foreign := fresh
	foreign.ID = "other"
	foreign.MemberID = "jiwoo"
	p := Pending(&state, sub, []Event{old, fresh, foreign}, now.Add(2*time.Second))
	if len(p) != 1 || p[0].ID != fresh.ID {
		t.Fatal(p)
	}
	// An unmarked event is retried. Only enqueue success marks it delivered.
	if len(Pending(&state, sub, []Event{fresh}, now.Add(3*time.Second))) != 1 {
		t.Fatal("failure advanced cursor")
	}
	MarkDelivered(&state, sub, fresh)
	dir := t.TempDir()
	mustWrite(t, dir, "state.json", state)
	var restarted Runtime
	if err := Read(dir, "state.json", &restarted); err != nil {
		t.Fatal(err)
	}
	if len(Pending(&restarted, sub, []Event{old, fresh}, now.Add(4*time.Second))) != 0 {
		t.Fatal("restart duplicate")
	}
	sub.GroupID = 456
	if len(Pending(&restarted, sub, []Event{old, fresh}, now.Add(5*time.Second))) != 0 {
		t.Fatal("destination change flooded history")
	}
	if Matches(sub, Event{Kind: "unknown", CommunityID: 235, MemberID: "carmen"}) {
		t.Fatal("unknown event allowed")
	}
}
func TestEventsChangedNotificationAndNestedReplies(t *testing.T) {
	var revision atomic.Int32
	now := time.Now().UnixMilli()
	artist := Object{"memberId": "carmen", "profileName": "CARMEN"}
	comment := func(id string) Object {
		return Object{"commentId": id, "author": artist, "body": "안녕<br />하트", "createdAt": now, "parent": Object{"type": "COMMENT", "data": Object{"commentId": "fan", "body": "잘 지내?"}}}
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/member/v1.1/community-235/artistMembers":
			respond(w, Object{"data": []any{artist}})
		case "/noti/feed/v2.0/activities":
			if r.URL.Query().Get("seen") != "false" {
				t.Error("marked notifications read")
			}
			respond(w, Object{"data": []any{Object{"activityId": "same", "activityType": "ARTIST_COMMENT", "webUrl": "https://weverse.io/hearts2hearts/fanpost/1-2/comment/fan", "time": now}, Object{"activityType": "ARTIST_COMMENT", "webUrl": "https://weverse.io/hearts2hearts/fanpost/deleted"}}})
		case "/post/v1.0/post-deleted":
			w.WriteHeader(404)
		case "/post/v1.0/post-1-2":
			respond(w, Object{"body": "fan post"})
		case "/comment/v1.0/post-1-2/artistComments":
			respond(w, Object{"data": []any{
				comment("first"),
				Object{"commentId": "peer", "author": artist, "body": "reply to another member", "parent": Object{"type": "POST", "data": Object{"author": Object{"memberId": "jiwoo", "profileName": "JIWOO", "profileType": "ARTIST"}}}},
				Object{"commentId": "self", "author": artist, "body": "member supplement", "parent": Object{"type": "POST", "data": Object{"author": artist}}},
				Object{"commentId": "fan-reply", "author": Object{"memberId": "fan", "profileType": "FAN"}, "body": "fan reply", "parent": Object{"type": "COMMENT", "data": Object{"commentId": "first", "author": artist}}},
			}})
		case "/comment/v1.0/comment-fan":
			respond(w, Object{"commentId": "fan", "author": Object{"memberId": "fan", "profileType": "FAN", "profileName": "粉丝昵称"}, "body": "잘 지내?", "createdAt": now})
		case "/comment/v1.0/comment-fan/artistComments":
			items := []any{comment("first")}
			if revision.Load() > 0 {
				items = append(items, comment("second"))
			}
			respond(w, Object{"data": items})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	mustWrite(t, c.Dir, "settings.json", Settings{Subscriptions: []Subscription{{ID: "s", CommunityID: 235, Slug: "hearts2hearts", Comments: true, Enabled: true}}})
	first, err := c.Events(context.Background())
	if err != nil || len(first) != 3 {
		t.Fatalf("%v %v", first, err)
	}
	foundFanReply, foundSelf, foundPeer := false, false, false
	for _, e := range first {
		if e.CommentID == "first" {
			foundFanReply = e.ParentCommentID == "fan" && e.ParentBody == "잘 지내?" && e.Body == "안녕\n하트"
		}
		if e.CommentID == "peer" {
			foundPeer = e.ParentAuthor == "JIWOO"
		}
		if e.CommentID == "self" {
			foundSelf = true
		}
		if e.MemberID != "carmen" {
			t.Fatal("fan-authored reply forwarded", e)
		}
	}
	if !foundFanReply || !foundSelf || !foundPeer {
		t.Fatal(first)
	}
	revision.Store(1)
	second, err := c.Events(context.Background())
	if err != nil || len(second) != 4 {
		t.Fatalf("new reply in same notification missed: %v %v", second, err)
	}
}
func TestPagingAndTranslation(t *testing.T) {
	now := time.Now()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pages" {
			if r.URL.Query().Get("next") == "" {
				respond(w, Object{"data": []any{Object{"time": now.UnixMilli()}}, "paging": Object{"nextParams": Object{"after": "page2"}}})
				return
			}
			respond(w, Object{"data": []any{Object{"time": now.Add(-time.Hour).UnixMilli()}}})
			return
		}
		if r.URL.Query().Get("fields") != "translated.type(manual).fieldSet(translatedForComment)" {
			t.Error("wrong comment translation fields")
		}
		respond(w, Object{"translated": Object{"body": "你好", "userLanguage": "zh_CN"}})
	})
	items, err := c.pages(context.Background(), "/pages?count=100", "next", "time", now.Add(-time.Minute))
	if err != nil || len(items) != 2 {
		t.Fatalf("%v %v", items, err)
	}
	event := Event{Kind: "comment", CommentID: "1", Body: "안녕"}
	c.TranslateEvent(context.Background(), &event)
	if event.Body != "안녕" || event.Translation != "你好" || event.TranslationError != "" {
		t.Fatal(event)
	}
}
func TestLivePublicSearch(t *testing.T) {
	if os.Getenv("WEVERSE_LIVE_TEST") != "1" {
		t.Skip("explicit network integration test")
	}
	c := NewClient(t.TempDir(), "")
	defer c.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	items, err := c.Search(ctx, "H2H")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != 235 || len(items[0].Members) != 8 {
		t.Fatalf("unexpected live directory: %+v", items)
	}
	t.Logf("Hearts2Hearts: community=%d, members=%v", items[0].ID, items[0].Members)
}

func TestOfficialArtistMemberProfiles(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, []any{Object{"memberId": "artist-id", "artistOfficialProfile": Object{"officialName": "CARMEN", "officialImageUrl": "https://example.com/artist.jpg"}}})
	})
	members, err := c.Members(context.Background(), 235)
	if err != nil || len(members) != 1 || members[0].Name != "CARMEN" || members[0].ID != "artist-id" {
		t.Fatalf("%v %v", members, err)
	}
}
