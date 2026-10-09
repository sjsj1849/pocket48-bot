package logic

import (
	"pocket48-bot/internal/xmonitor"
	"testing"
	"time"
)

func TestFilterXViralEventsUsesExactMetricsAndExcludesOfficial(t *testing.T) {
	now := time.Now()
	cfg := xmonitor.Settings{ViralMinLikes: 10000, ViralMinReposts: 10000}
	event := func(id, author string, likes, reposts int64) xmonitor.Event {
		return xmonitor.Event{ID: id, Kind: "post", Author: xmonitor.User{Username: author}, Body: "#CARMEN #Hearts2Hearts", URL: "https://x.com/x/status/" + id, Time: now.UnixMilli(), LikeCount: likes, RepostCount: reposts}
	}
	got := filterXViralEvents([]xmonitor.Event{event("1", "fan", 9999, 9999), event("2", "Hearts2Hearts", 50000, 20000), event("3", "fan", 10000, 20), event("3", "fan", 11000, 30)}, cfg, now.Add(-time.Hour), now.Add(time.Hour), true)
	if len(got) != 1 || got[0].ID != "3" || got[0].LikeCount != 10000 {
		t.Fatalf("filtered=%+v", got)
	}
}
