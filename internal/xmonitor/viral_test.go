package xmonitor

import (
	"reflect"
	"testing"
	"time"
)

func TestSuggestViralMembers(t *testing.T) {
	got := SuggestViralMembers("260930 #CARMEN #카르멘 with STELLA and #이안")
	want := []string{"carmen", "stella", "ian"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("members=%v want=%v", got, want)
	}
	if got := SuggestViralMembers("brilliant performance"); len(got) != 0 {
		t.Fatalf("IAN matched inside an English word: %v", got)
	}
}

func TestViralStoreKeepsManualConfirmationAcrossMetricRefresh(t *testing.T) {
	store, err := OpenViralStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now()
	event := Event{ID: "123", Kind: "post", Author: User{Username: "fan", Name: "Fan"}, Body: "#CARMEN", URL: "https://x.com/fan/status/123", Time: now.UnixMilli(), LikeCount: 12000}
	if _, err := store.Upsert([]Event{event}, 10000, 10000, now); err != nil {
		t.Fatal(err)
	}
	if err := store.Confirm("123", []string{"carmen"}, false, now); err != nil {
		t.Fatal(err)
	}
	event.Body, event.LikeCount, event.RepostCount = "#STELLA", 13000, 11000
	if _, err := store.Upsert([]Event{event}, 10000, 10000, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	posts, err := store.List(ViralFilter{Status: "confirmed"})
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%v err=%v", posts, err)
	}
	if !reflect.DeepEqual(posts[0].ConfirmedMembers, []string{"carmen"}) || posts[0].LikeCount != 13000 || !posts[0].QualifiesReposts {
		t.Fatalf("post=%+v", posts[0])
	}
}

func TestViralPostScope(t *testing.T) {
	for _, test := range []struct {
		members []string
		want    string
	}{{nil, "待确认"}, {[]string{"ian"}, "单人"}, {[]string{"ian", "stella"}, "非单人"}, {[]string{"group"}, "非单人"}} {
		if got := (ViralPost{ConfirmedMembers: test.members}).Scope(); got != test.want {
			t.Fatalf("scope(%v)=%s", test.members, got)
		}
	}
}
