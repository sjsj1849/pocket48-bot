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
