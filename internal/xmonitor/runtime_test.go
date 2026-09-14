package xmonitor

import "testing"

func TestTimelineBaselineAndDelayedOldRecords(t *testing.T) {
	sub := Subscription{ID: "a", Username: "nekomo_st", UserID: "42", Enabled: true, Posts: true}
	event := func(id string) Event {
		return Event{ID: id, Kind: "post", Author: User{ID: "42", Username: "nekomo_st"}}
	}
	first := []Event{event("100"), event("200")}
	if pending := Pending(Cursor{}, sub, first); len(pending) != 0 {
		t.Fatal("initial history must not be sent")
	}
	cursor, err := Advance(Cursor{}, sub, first)
	if err != nil {
		t.Fatal(err)
	}
	next := []Event{event("50"), event("200"), event("300")}
	pending := Pending(cursor, sub, next)
	if len(pending) != 1 || pending[0].ID != "300" {
		t.Fatalf("old context/pinned records must not be notified: %+v", pending)
	}
	cursor, err = Advance(cursor, sub, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(Pending(cursor, sub, next)) != 0 {
		t.Fatal("restart must not resend already processed posts")
	}
	sub.UserID = "99"
	if len(Pending(cursor, sub, next)) != 0 {
		t.Fatal("replacement account needs a new baseline")
	}
}
func TestPinnedOnlyOverlapIsNotEnough(t *testing.T) {
	cursor := Cursor{Ready: true, Seen: []string{"100"}}
	if Overlap(cursor, []Event{{ID: "100"}}, []string{"100"}) {
		t.Fatal("pinned history cannot prove timeline continuity")
	}
	if !Overlap(cursor, []Event{{ID: "100"}}, nil) {
		t.Fatal("known non-pinned record establishes overlap")
	}
}
func TestCannotAdvanceMixedAuthorTimeline(t *testing.T) {
	_, err := Advance(Cursor{}, Subscription{UserID: "42"}, []Event{{ID: "100", Author: User{ID: "99"}}})
	if err == nil {
		t.Fatal("must reject another user's context record")
	}
}
