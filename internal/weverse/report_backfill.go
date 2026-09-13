package weverse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type BackfillProgress struct {
	Running   bool   `json:"running"`
	Period    string `json:"period"`
	Total     int    `json:"total"`
	Done      int    `json:"done"`
	Records   int    `json:"records"`
	UpdatedAt string `json:"updatedAt"`
	Error     string `json:"error,omitempty"`
}

func (c *Client) BackfillReport(ctx context.Context, h *History, s ReportSettings, p ReportPeriod, slug string, progress func(BackfillProgress)) error {
	state := BackfillProgress{Running: true, Period: p.Key}
	update := func() {
		state.UpdatedAt = time.Now().Format(time.RFC3339)
		if progress != nil {
			progress(state)
		}
	}
	update()
	members, e := c.Members(ctx, s.CommunityID)
	if e != nil {
		return e
	}
	if c.Artists == nil {
		c.Artists = map[int64][]Member{}
	}
	c.Artists[s.CommunityID] = members
	if e = h.SaveMembers(s.CommunityID, members); e != nil {
		return e
	}
	// Include old roots: a reply this month can be under a much older post.
	posts, e := c.pagesLimit(ctx, fmt.Sprintf("/post/v1.0/community-%d/artistTabPosts?fieldSet=postsV1&pagingType=CURSOR&limit=100", s.CommunityID), "after", "publishedAt", time.UnixMilli(1), 200)
	if e != nil {
		return e
	}
	state.Total = len(posts)
	update()
	unavailable := 0
	for _, raw := range posts {
		root := str(obj(raw)["postId"])
		if !idRE.MatchString(root) {
			return fmt.Errorf("历史帖子缺少编号")
		}
		// Compact postsV1 lists omit engagement fields. Fetch full postV1
		// for the period's post cohort before counting cumulative interactions.
		source := obj(raw)
		if stamp := num(source["publishedAt"]); stamp >= p.Start.UnixMilli() && stamp < p.End.UnixMilli() {
			var full Object
			err := c.call(ctx, "/post/v1.0/post-"+root+"?fieldSet=postV1", true, &full)
			if err == nil {
				source = full
			} else if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		var events []Event
		var e error
		for attempt := 0; attempt < 3; attempt++ {
			events, e = c.threadEvents(ctx, s.CommunityID, slug, root, source, p.Start, p.End)
			if e == nil || errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) || errors.Is(e, ErrLogin) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrForbidden) {
			unavailable++
			// A stale list entry may still provide the root metadata.
			if rootEvent, parseErr := eventFromPost(obj(raw), slug, s.CommunityID); parseErr == nil && rootEvent.ID != "" {
				events = []Event{rootEvent}
			}
		} else if e != nil {
			return fmt.Errorf("历史帖子 %s 未能完整回采：%w", root, e)
		}
		selected := []Event{}
		for _, event := range events {
			if event.Time >= p.Start.UnixMilli() && event.Time < p.End.UnixMilli() {
				selected = append(selected, event)
			}
		}
		if e = h.Record(selected, ""); e != nil {
			return e
		}
		state.Records += len(selected)
		state.Done++
		update()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	lives, e := c.pages(ctx, fmt.Sprintf("/post/v1.0/community-%d/liveTabPosts?fieldSet=postsV1&pagingType=CURSOR&limit=100", s.CommunityID), "after", "publishedAt", time.UnixMilli(1))
	if e != nil {
		return e
	}
	allowed := map[string]bool{}
	names := map[string]string{}
	for _, m := range members {
		allowed[m.ID] = true
		names[m.ID] = m.Name
	}
	selected := []Event{}
	for _, raw := range lives {
		event, e := historicalLive(obj(raw), slug, s.CommunityID)
		if e != nil {
			return e
		}
		if allowed[event.MemberID] && event.Time >= p.Start.UnixMilli() && event.Time < p.End.UnixMilli() {
			event.Author = names[event.MemberID]
			selected = append(selected, event)
		}
	}
	if e = h.Record(selected, ""); e != nil {
		return e
	}
	state.Records += len(selected)
	update()
	// Moments are independently published posts; artist-tab history need not contain them.
	notifications, err := c.pagesLimit(ctx, fmt.Sprintf("/noti/feed/v2.0/activities?communityId=%d&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", s.CommunityID), "next", "time", p.Start, 200)
	if err != nil {
		return err
	}
	moments := []Event{}
	for _, raw := range notifications {
		n := obj(raw)
		typ := strings.ToUpper(text(n, "activityType", "type"))
		if !strings.Contains(typ, "MOMENT") || strings.Contains(typ, "COMMENT") {
			continue
		}
		targetSlug, id, _ := notificationTarget(n)
		if id == "" || targetSlug != slug {
			continue
		}
		var post Object
		err := c.call(ctx, "/post/v1.0/post-"+id+"?fieldSet=postV1", true, &post)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		event, err := eventFromPost(post, slug, s.CommunityID)
		if err != nil {
			return err
		}
		if event.Kind == "moment" && allowed[event.MemberID] && event.Time >= p.Start.UnixMilli() && event.Time < p.End.UnixMilli() {
			event.Author = names[event.MemberID]
			moments = append(moments, event)
		}
	}
	if err := h.Record(moments, ""); err != nil {
		return err
	}
	state.Records += len(moments)
	update()
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfillUnavailable:"+p.Key, fmt.Sprint(unavailable))
	if e != nil {
		return e
	}
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfill:"+p.Key, time.Now().Format(time.RFC3339))
	return e
}
func historicalLive(p Object, slug string, cid int64) (Event, error) {
	v := obj(obj(p["extension"])["video"])
	vod, _ := v["liveToVod"].(bool)
	if str(v["type"]) != "LIVE" && !vod {
		return Event{}, nil
	}
	// Convert an actual live replay to the same stable start event ID used online.
	copyPost := Object{}
	for k, x := range p {
		copyPost[k] = x
	}
	ext := Object{}
	for k, x := range obj(p["extension"]) {
		ext[k] = x
	}
	video := Object{}
	for k, x := range v {
		video[k] = x
	}
	video["type"] = "LIVE"
	video["status"] = "ONAIR"
	ext["video"] = video
	copyPost["extension"] = ext
	return eventFromPost(copyPost, slug, cid)
}

// RefreshReportPosts updates only sampled post metadata, without replaying old
// messages or advancing realtime cursors. Unavailable records retain last values.
func (c *Client) RefreshReportPosts(ctx context.Context, h *History, s ReportSettings, p ReportPeriod, slug string) error {
	events, err := h.Period(s.CommunityID, p.Start, p.End)
	if err != nil {
		return err
	}
	members, err := h.Members(s.CommunityID)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.ID] = m.Name
	}
	seen := map[string]bool{}
	for _, previous := range events {
		if (previous.Kind != "post" && previous.Kind != "moment") || seen[previous.PostID] {
			continue
		}
		seen[previous.PostID] = true
		var post Object
		err := c.call(ctx, "/post/v1.0/post-"+previous.PostID+"?fieldSet=postV1", true, &post)
		if errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		event, err := eventFromPost(post, slug, s.CommunityID)
		if err != nil {
			return err
		}
		if names[event.MemberID] == "" {
			return fmt.Errorf("帖子作者不在已记录的成员列表")
		}
		event.Author = names[event.MemberID]
		event.PostContext = aiPostContext(post, event.PostID, slug, names)
		if err = h.Record([]Event{event}, ""); err != nil {
			return err
		}
	}
	return nil
}
