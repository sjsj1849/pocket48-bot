package logic

import (
	"errors"
	"pocket48-bot/internal/monitor"
	"testing"
	"time"
)

func TestDailyFetchRejectsResponseAfterDeadline(t *testing.T) {
	calls := 0
	deadline := time.Now().Add(10 * time.Millisecond)
	r, e := fetchWeiboDailyCount(deadline, func() (*monitor.WeiboSuperCountResult, error) {
		calls++
		time.Sleep(20 * time.Millisecond)
		return &monitor.WeiboSuperCountResult{SignCount: 123}, nil
	})
	if e == nil || r != nil || calls != 1 {
		t.Fatal("late response entered previous day's snapshot", r, e, calls)
	}
	calls = 0
	r, e = fetchWeiboDailyCount(time.Now().Add(-time.Second), func() (*monitor.WeiboSuperCountResult, error) { calls++; return nil, nil })
	if e == nil || r != nil || calls != 0 {
		t.Fatal("request began after cutoff")
	}
}
func TestManualCountDoesNotRetry(t *testing.T) {
	calls := 0
	_, e := fetchWeiboDailyCount(time.Time{}, func() (*monitor.WeiboSuperCountResult, error) { calls++; return nil, errors.New("unavailable") })
	if e == nil || calls != 1 {
		t.Fatal(e, calls)
	}
}
