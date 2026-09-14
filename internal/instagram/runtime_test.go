package instagram

import (
	"testing"
	"time"
)

func TestBaselineDedupAndNewStream(t *testing.T) {
	now := time.Unix(10000, 0)
	s := Subscription{ID: "a", Username: "artist", UserID: "42", Posts: true}
	event := func(id, kind string, stamp int64) Event {
		return Event{ID: id, Kind: kind, Time: stamp, Author: User{ID: "42"}}
	}
	old := []Event{event("100", "post", now.Add(-time.Hour).UnixMilli())}
	c, e := Advance(Cursor{}, s, old, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(Pending(c, s, old)) != 0 {
		t.Fatal("baseline spam")
	}
	fresh := event("101", "post", now.Add(time.Minute).UnixMilli())
	if len(Pending(c, s, []Event{fresh, fresh})) != 1 {
		t.Fatal("new event should occur once")
	}
	c, e = Advance(c, s, []Event{fresh}, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(Pending(c, s, []Event{fresh})) != 0 {
		t.Fatal("repeat spam")
	}
	s.Reels = true
	reel := event("102", "reel", now.Add(2*time.Minute).UnixMilli())
	if len(Pending(c, s, []Event{reel})) != 0 {
		t.Fatal("newly enabled stream must establish baseline")
	}
	c, e = Advance(c, s, []Event{reel}, now)
	if e != nil {
		t.Fatal(e)
	}
	if len(Pending(c, s, []Event{event("103", "reel", now.Add(3*time.Minute).UnixMilli())})) != 1 {
		t.Fatal("new Reel omitted")
	}
	changed := s
	changed.UserID = "99"
	if len(Pending(c, changed, []Event{fresh})) != 0 {
		t.Fatal("different account needs new baseline")
	}
	if _, e = Advance(c, s, []Event{{ID: "x", Time: 1, Author: User{ID: "99"}}}, now); e == nil {
		t.Fatal("mixed owner accepted")
	}
	if ScanSince(c)["post"] != fresh.Time-300000 {
		t.Fatal("per-stream watermark not advanced")
	}
}
func TestInstagramUsername(t *testing.T) {
	for _, raw := range []string{"@hearts2hearts", "https://www.instagram.com/hearts2hearts/?igsh=x"} {
		if n, e := Username(raw); e != nil || n != "hearts2hearts" {
			t.Fatalf("%s: %s %v", raw, n, e)
		}
	}
	for _, raw := range []string{"https://evil.test/a", "https://instagram.com/p/abc", "https://instagram.com/accounts/login", "a..b"} {
		if _, e := Username(raw); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
