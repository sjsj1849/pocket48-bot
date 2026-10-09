package bilibili

import "sort"

// Cursor is the per-subscription dedupe state, persisted in state.json so a bot
// restart never re-notifies old dynamics or re-fires a live transition.
type Cursor struct {
	Ready      bool     `json:"ready"`
	UID        string   `json:"uid"`
	Seen       []string `json:"seen"`
	Watermark  int64    `json:"watermark"`
	LiveStatus int      `json:"liveStatus"`
	LiveReady  bool     `json:"liveReady"`
	// BaselineAt 是「只推增量」的时间基线（unix ms）。
	//
	// 用途：放开短视频过滤后，历史上被过滤掉的投稿会突然变成未见过的新内容，
	// 首轮轮询就会把它们一次性全推出去。写入 BaselineAt = 首次运行时刻，
	// 之后只推送发布时间晚于它的投稿，等价于「不回溯，从现在开始记」。
	// 0 表示尚未建立基线。
	BaselineAt int64 `json:"baselineAt,omitempty"`
}

type Runtime struct {
	Subscriptions map[string]Cursor `json:"subscriptions"`
}

const maxSeenDynamics = 512

// PendingDynamics returns unseen dynamics, oldest first. Before the baseline is
// taken it returns nothing, so a brand-new subscription never replays history.
func PendingDynamics(c Cursor, items []Dynamic) []Dynamic {
	if !c.Ready {
		return nil
	}
	known := make(map[string]bool, len(c.Seen))
	for _, id := range c.Seen {
		known[id] = true
	}
	result := make([]Dynamic, 0, len(items))
	for _, d := range items {
		if d.ID == "" || known[d.ID] {
			continue
		}
		result = append(result, d)
		known[d.ID] = true
	}
	// 基线之前的投稿只登记不推送（建立基线那一轮）。
	if c.BaselineAt > 0 {
		fresh := result[:0]
		for _, d := range result {
			if d.Time > 0 && d.Time < c.BaselineAt {
				continue
			}
			fresh = append(fresh, d)
		}
		result = fresh
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Time < result[j].Time })
	return result
}

// Advance records every fetched id and moves the watermark.
func Advance(c Cursor, uid string, items []Dynamic) Cursor {
	if c.UID != uid {
		c = Cursor{UID: uid}
	}
	known := make(map[string]bool, len(c.Seen))
	for _, id := range c.Seen {
		known[id] = true
	}
	seen := append([]string{}, c.Seen...)
	for _, d := range items {
		if d.ID == "" {
			continue
		}
		if d.Time > c.Watermark {
			c.Watermark = d.Time
		}
		if !known[d.ID] {
			seen = append(seen, d.ID)
			known[d.ID] = true
		}
	}
	if len(seen) > maxSeenDynamics {
		seen = seen[len(seen)-maxSeenDynamics:]
	}
	c.Seen = seen
	c.Ready = true
	return c
}

// ObserveLive records the newest live_status and reports a notifiable transition
// ("online" / "offline"). The first observation only establishes the baseline, and
// 轮播(2)/未开播(0) 之间不互相切换通知。
func ObserveLive(c Cursor, status int) (Cursor, string) {
	if !c.LiveReady {
		c.LiveReady = true
		c.LiveStatus = status
		return c, ""
	}
	if status == c.LiveStatus {
		return c, ""
	}
	previous := c.LiveStatus
	c.LiveStatus = status
	if status == 1 {
		return c, "online"
	}
	if previous == 1 {
		return c, "offline"
	}
	return c, ""
}
