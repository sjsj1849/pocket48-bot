package xmonitor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type Cursor struct {
	Username    string   `json:"username"`
	UserID      string   `json:"userId"`
	HighWaterID string   `json:"highWaterId"`
	Seen        []string `json:"seen"`
	Ready       bool     `json:"ready"`
}
type Runtime struct {
	Subscriptions map[string]Cursor `json:"subscriptions"`
}

// A known pinned post alone cannot establish overlap with the previous scan.
func Overlap(cursor Cursor, events []Event, pinned []string) bool {
	if cursor.Ready && len(cursor.Seen) == 0 {
		return true
	}
	pins := map[string]bool{}
	for _, id := range pinned {
		pins[id] = true
	}
	known := map[string]bool{}
	for _, id := range cursor.Seen {
		known[id] = true
	}
	for _, e := range events {
		if known[e.ID] && !pins[e.ID] {
			return true
		}
	}
	return false
}
func Pending(cursor Cursor, s Subscription, events []Event) []Event {
	if !cursor.Ready || !strings.EqualFold(cursor.Username, s.Username) || cursor.UserID != s.UserID {
		return nil
	}
	known := map[string]bool{}
	for _, id := range cursor.Seen {
		known[id] = true
	}
	result := []Event{}
	for _, e := range events {
		floor, _ := strconv.ParseUint(cursor.HighWaterID, 10, 64)
		id, _ := strconv.ParseUint(e.ID, 10, 64)
		if !known[e.ID] && id > floor && Matches(s, e) {
			result = append(result, e)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		a, _ := strconv.ParseUint(result[i].ID, 10, 64)
		b, _ := strconv.ParseUint(result[j].ID, 10, 64)
		return a < b
	})
	return result
}
func Advance(cursor Cursor, s Subscription, events []Event) (Cursor, error) {
	known := map[string]bool{}
	if strings.EqualFold(cursor.Username, s.Username) && cursor.UserID == s.UserID {
		for _, id := range cursor.Seen {
			known[id] = true
		}
	}
	for _, e := range events {
		if _, err := strconv.ParseUint(e.ID, 10, 64); err != nil {
			return cursor, fmt.Errorf("X 帖子编号格式异常")
		}
		if e.Author.ID != s.UserID {
			return cursor, fmt.Errorf("X 时间线混入了其他用户内容")
		}
		known[e.ID] = true
	}
	ids := make([]string, 0, len(known))
	for id := range known {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.ParseUint(ids[i], 10, 64)
		b, _ := strconv.ParseUint(ids[j], 10, 64)
		return a > b
	})
	if len(ids) > 4096 {
		ids = ids[:4096]
	}
	high := ""
	if len(ids) > 0 {
		high = ids[0]
	}
	return Cursor{Username: s.Username, UserID: s.UserID, HighWaterID: high, Seen: ids, Ready: true}, nil
}
