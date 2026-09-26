package logic

import (
	"testing"
	"time"
)

func TestWeiboDailyCollectionStartsOnlyDuring2359(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	for _, tc := range []struct {
		time string
		want bool
	}{
		{"2026-09-15 23:58:59", false},
		{"2026-09-15 23:59:00", false},
		{"2026-09-15 23:59:14", false},
		{"2026-09-15 23:59:15", true},
		{"2026-09-15 23:59:59", true},
		{"2026-09-16 00:00:00", false},
	} {
		now, err := time.ParseInLocation("2006-01-02 15:04:05", tc.time, loc)
		if err != nil {
			t.Fatal(err)
		}
		if got := withinWeiboDailyCollectionWindow(now); got != tc.want {
			t.Fatalf("withinWeiboDailyCollectionWindow(%s)=%v want %v", tc.time, got, tc.want)
		}
	}
}
