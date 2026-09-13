package weverse

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestLiveMetadata(t *testing.T) {
	p := Object{"postId": "1-2", "title": "방송", "author": Object{"memberId": "artist"}, "extension": Object{"video": Object{"type": "LIVE", "status": "ONAIR", "onAirStartAt": float64(1000), "thumb": "https://img.example/fallback.jpg"}, "mediaInfo": Object{"title": "방송", "thumbnail": Object{"url": "https://img.example/live.jpg"}}}}
	e, err := eventFromPost(p, "hearts2hearts", 235)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "live" || e.Body != "방송" || e.LiveStartedAt != 1000 || e.CoverURL != "https://img.example/live.jpg" || len(e.Images) != 1 {
		t.Fatal(e)
	}
	obj(obj(p["extension"])["video"])["status"] = "DONE"
	if e, err := eventFromPost(p, "hearts2hearts", 235); err != nil || e.ID != "" {
		t.Fatal("historical ending emitted", e, err)
	}
}
func TestLiveEndingConfirmationAndRestart(t *testing.T) {
	status, kind := "ONAIR", "LIVE"
	httpStatus := 200
	vod := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if httpStatus != 200 {
			w.WriteHeader(httpStatus)
			return
		}
		respond(w, Object{"extension": Object{"video": Object{"status": status, "type": kind, "liveToVod": vod, "onAirEndAt": 0, "playTime": 2076}}})
	})
	now := time.Now()
	live := Event{ID: "live:1-2", PostID: "1-2", Kind: "live", CommunityID: 235, MemberID: "artist", Author: "JIWOO", Body: "방송", CoverURL: "https://img.example/live.jpg", Images: []string{"https://img.example/live.jpg"}, LiveStartedAt: now.Add(-time.Hour).UnixMilli(), Time: now.Add(-time.Hour).UnixMilli()}
	cfg := Settings{Subscriptions: []Subscription{{ID: "s", CommunityID: 235, Live: true, Enabled: true}}}
	var state Runtime
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, []Event{live}, now); err != nil || len(e) != 0 {
		t.Fatal(e, err)
	}
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err != nil || len(e) != 0 {
		t.Fatal("list absence misreported", e, err)
	}
	httpStatus = 500
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err == nil || len(e) != 0 {
		t.Fatal("HTTP error misreported", e, err)
	}
	httpStatus = 404
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err != nil || len(e) != 0 {
		t.Fatal("deleted live misreported", e, err)
	}
	httpStatus = 200
	status = "UNKNOWN"
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err != nil || len(e) != 0 {
		t.Fatal("unknown status misreported", e, err)
	}
	status = "DONE"
	ends, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now)
	if err != nil || len(ends) != 1 || ends[0].Kind != "live_end" || ends[0].LiveDuration != 2076 || ends[0].CoverURL != live.CoverURL {
		t.Fatal(ends, err)
	}
	sub := cfg.Subscriptions[0]
	Pending(&state, sub, nil, now.Add(-time.Minute))
	if p := Pending(&state, sub, ends, now); len(p) != 1 {
		t.Fatal("ending lost", p)
	}
	MarkDelivered(&state, sub, ends[0])
	mustWrite(t, c.Dir, "state.json", state)
	var restarted Runtime
	if err := Read(c.Dir, "state.json", &restarted); err != nil {
		t.Fatal(err)
	}
	retry, err := c.LiveEndEvents(context.Background(), &restarted, cfg, nil, now)
	if err != nil || len(retry) != 1 || len(Pending(&restarted, sub, retry, now)) != 0 {
		t.Fatal("restart duplicated ending", retry, err)
	}
	// A replay is also an explicit terminal state even when status is absent.
	state.Lives["235/1-2"].End = nil
	kind = "VOD"
	status = ""
	vod = true
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err != nil || len(e) != 1 {
		t.Fatal("live-to-VOD missed", e, err)
	}
	cfg.Subscriptions[0].Live = false
	if e, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now); err != nil || len(e) != 0 || len(state.Lives) != 0 {
		t.Fatal("disabled live retained", e, err)
	}
}

func TestReplayWaitsForEncodingAndSurvivesRestart(t *testing.T) {
	encoding := "PROCESSING"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		respond(w, Object{"extension": Object{"video": Object{"type": "VOD", "liveToVod": true, "encodingStatus": encoding, "infraVideoId": "infra", "videoId": "video"}}})
	})
	now := time.Now()
	live := Event{ID: "live:1", PostID: "1", Kind: "live", MemberID: "stella", CommunityID: 235}
	end := live
	end.ID = "live_end:1"
	end.Kind = "live_end"
	end.Time = now.Add(-time.Minute).UnixMilli()
	state := Runtime{Lives: map[string]*TrackedLive{"235/1": {Event: live, End: &end, LastSeen: now.UnixMilli()}}}
	cfg := Settings{Subscriptions: []Subscription{{ID: "s", Enabled: true, CommunityID: 235, Live: true}}}
	Pending(&state, cfg.Subscriptions[0], nil, now.Add(-time.Hour))
	MarkDelivered(&state, cfg.Subscriptions[0], end)
	events, err := c.LiveEndEvents(context.Background(), &state, cfg, nil, now)
	if err != nil || len(events) != 1 || len(Pending(&state, cfg.Subscriptions[0], events, now)) != 0 {
		t.Fatal("processing replay notified", events, err)
	}
	mustWrite(t, c.Dir, "state.json", state)
	var restarted Runtime
	if err := Read(c.Dir, "state.json", &restarted); err != nil {
		t.Fatal(err)
	}
	encoding = "COMPLETE"
	events, err = c.LiveEndEvents(context.Background(), &restarted, cfg, nil, now)
	pending := Pending(&restarted, cfg.Subscriptions[0], events, now)
	if err != nil || len(pending) != 1 || pending[0].Kind != "live_replay" {
		t.Fatal("new replay missed", pending, err)
	}
	MarkDelivered(&restarted, cfg.Subscriptions[0], pending[0])
	mustWrite(t, c.Dir, "state.json", restarted)
	var again Runtime
	if err := Read(c.Dir, "state.json", &again); err != nil {
		t.Fatal(err)
	}
	events, err = c.LiveEndEvents(context.Background(), &again, cfg, nil, now)
	if err != nil || len(Pending(&again, cfg.Subscriptions[0], events, now)) != 0 {
		t.Fatal("replay duplicated after restart", events, err)
	}
}
