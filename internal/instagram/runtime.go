package instagram

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type Cursor struct {
	Watermarks map[string]int64 `json:"watermarks"`
	Username   string           `json:"username"`
	UserID     string           `json:"userId"`
	Since      int64            `json:"since"`
	Seen       []string         `json:"seen"`
	Ready      bool             `json:"ready"`
	Posts      bool             `json:"posts"`
	Reels      bool             `json:"reels"`
	Stories    bool             `json:"stories"`
}
type Runtime struct {
	Subscriptions map[string]Cursor `json:"subscriptions"`
}

func eventKey(e Event) string {
	if e.Kind == "story" {
		return "story:" + e.ID
	}
	return "media:" + e.ID
}
func Pending(c Cursor, s Subscription, events []Event) []Event {
	if !c.Ready || !strings.EqualFold(c.Username, s.Username) || c.UserID != s.UserID {
		return nil
	}
	known := map[string]bool{}
	for _, id := range c.Seen {
		known[id] = true
	}
	result := []Event{}
	for _, e := range events {
		enabledBefore := (e.Kind == "post" && c.Posts) || (e.Kind == "reel" && c.Reels) || (e.Kind == "story" && c.Stories)
		if enabledBefore && e.Time >= c.Since && !known[eventKey(e)] && Matches(s, e) {
			result = append(result, e)
			known[eventKey(e)] = true
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Time < result[j].Time })
	return result
}
func Advance(c Cursor, s Subscription, events []Event, now time.Time) (Cursor, error) {
	if !c.Ready || !strings.EqualFold(c.Username, s.Username) || c.UserID != s.UserID {
		c = Cursor{Username: s.Username, UserID: s.UserID, Since: now.UnixMilli(), Ready: true}
	}
	if c.Watermarks == nil {
		c.Watermarks = map[string]int64{}
	}
	copyMarks := map[string]int64{}
	for k, v := range c.Watermarks {
		copyMarks[k] = v
	}
	c.Watermarks = copyMarks
	c.Seen = append([]string{}, c.Seen...)
	known := map[string]bool{}
	for _, id := range c.Seen {
		known[id] = true
	}
	for _, e := range events {
		if e.Author.ID != s.UserID || e.ID == "" || e.Time <= 0 {
			return c, fmt.Errorf("Instagram 返回的内容所属账号或编号异常")
		}
		if e.Time > c.Watermarks[e.Kind] {
			c.Watermarks[e.Kind] = e.Time
		}
		k := eventKey(e)
		if !known[k] {
			c.Seen = append(c.Seen, k)
			known[k] = true
		}
	}
	if len(c.Seen) > 8192 {
		c.Seen = c.Seen[len(c.Seen)-8192:]
	}
	c.Posts = s.Posts
	c.Reels = s.Reels
	c.Stories = s.Stories
	return c, nil
}

func ScanSince(c Cursor) map[string]int64 {
	result := map[string]int64{}
	for _, kind := range []string{"post", "reel"} {
		if c.Ready && ((kind == "post" && c.Posts) || (kind == "reel" && c.Reels)) {
			stamp := c.Watermarks[kind]
			if stamp == 0 {
				stamp = c.Since
			}
			result[kind] = stamp - 5*60*1000
		}
	}
	return result
}
