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
	// The member comment-history endpoint below discovers replies on old artist
	// and fan roots. The artist tab only needs to supply posts published in-period.
	posts, e := c.pagesLimit(ctx, fmt.Sprintf("/post/v1.0/community-%d/artistTabPosts?fieldSet=postsV1&pagingType=CURSOR&limit=100", s.CommunityID), "after", "publishedAt", p.Start, 200)
	if e != nil {
		return e
	}
	state.Total = len(posts)
	update()
	unavailablePeriod := 0
	unavailableContext := 0
	restrictedPeriod := 0
	restrictedContext := 0
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
			} else if !reportPostUnavailable(err) {
				return err
			}
		}
		var events []Event
		var e error
		for attempt := 0; attempt < 3; attempt++ {
			events, e = c.threadEvents(ctx, s.CommunityID, slug, root, source, p.Start, p.End)
			if e == nil || reportPostUnavailable(e) || errors.Is(e, ErrLogin) || ctx.Err() != nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if reportPostUnavailable(e) {
			stamp := num(obj(raw)["publishedAt"])
			// A stale list entry may still provide the root metadata.
			rootEvent, parseErr := eventFromPost(obj(raw), slug, s.CommunityID)
			if stamp >= p.Start.UnixMilli() && stamp < p.End.UnixMilli() {
				if parseErr == nil && rootEvent.MembershipOnly {
					restrictedPeriod++
				} else {
					unavailablePeriod++
				}
			} else if parseErr == nil && rootEvent.MembershipOnly {
				restrictedContext++
			} else {
				unavailableContext++
			}
			if parseErr == nil && rootEvent.ID != "" {
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
		if event.ID == "" || event.Time < p.Start.UnixMilli() || event.Time >= p.End.UnixMilli() {
			continue
		}
		var full Object
		err := c.call(ctx, "/post/v1.0/post-"+event.PostID+"?fieldSet=postV1", true, &full)
		if reportPostUnavailable(err) {
			unavailablePeriod++
			if name := names[event.MemberID]; name != "" {
				event.Author = name
			}
			selected = append(selected, event)
			continue
		}
		if err != nil {
			return err
		}
		if complete, parseErr := historicalLive(full, slug, s.CommunityID); parseErr != nil {
			return parseErr
		} else if complete.ID != "" {
			event = complete
		}
		if name := names[event.MemberID]; name != "" {
			event.Author = name
		}
		selected = append(selected, event)
		chats, chatErr := c.liveChatEvents(ctx, full, event, slug, s.CommunityID, names, time.UnixMilli(1), 200)
		if chatErr != nil {
			return chatErr
		}
		for _, chat := range chats {
			if chat.Time >= p.Start.UnixMilli() && chat.Time < p.End.UnixMilli() {
				selected = append(selected, chat)
			}
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
	memberComments, err := c.reportMemberComments(ctx, members, s.CommunityID, slug, p)
	if err != nil {
		return err
	}
	if err := h.Record(memberComments, ""); err != nil {
		return err
	}
	state.Records += len(memberComments)
	update()
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
		if reportPostUnavailable(err) {
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
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfillUnavailable:"+p.Key, fmt.Sprint(unavailablePeriod))
	if e != nil {
		return e
	}
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfillUnavailableContext:"+p.Key, fmt.Sprint(unavailableContext))
	if e != nil {
		return e
	}
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfillRestricted:"+p.Key, fmt.Sprint(restrictedPeriod))
	if e != nil {
		return e
	}
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfillRestrictedContext:"+p.Key, fmt.Sprint(restrictedContext))
	if e != nil {
		return e
	}
	_, e = h.db.Exec("INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "backfill:"+p.Key, time.Now().Format(time.RFC3339))
	return e
}

func (c *Client) reportNotificationComments(ctx context.Context, notifications []any, communityID int64, slug string, since time.Time, artistNames map[string]string) ([]Event, int, error) {
	all := map[string]Event{}
	parents := map[string]Object{}
	commentParents := map[string]Object{}
	seenPosts := map[string]bool{}
	seenComments := map[string]bool{}
	inaccessibleRoots := map[string]bool{}
	addComments := func(items []any, postID string, parent Object) error {
		return c.addThreadComments(ctx, items, postID, slug, communityID, parent, artistNames, commentParents, all)
	}
	for _, raw := range notifications {
		notification := obj(raw)
		typ := strings.ToUpper(text(notification, "activityType", "type"))
		targetSlug, postID, commentID := notificationTarget(notification)
		if postID == "" || targetSlug != slug || (!strings.Contains(typ, "COMMENT") && !strings.Contains(typ, "REPLY") && commentID == "") {
			continue
		}
		parent, ok := parents[postID]
		if !ok {
			if err := c.call(ctx, "/post/v1.0/post-"+postID+"?fieldSet=postV1", true, &parent); err != nil {
				if reportPostUnavailable(err) {
					inaccessibleRoots[postID] = true
					continue
				}
				return nil, len(inaccessibleRoots), err
			}
			parents[postID] = parent
		}
		if !seenPosts[postID] {
			seenPosts[postID] = true
			comments, err := c.pages(ctx, "/comment/v1.0/post-"+postID+"/artistComments?fieldSet=postArtistCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
			if err != nil {
				if reportPostUnavailable(err) {
					inaccessibleRoots[postID] = true
					continue
				}
				return nil, len(inaccessibleRoots), err
			}
			if err := addComments(comments, postID, parent); err != nil {
				return nil, len(inaccessibleRoots), err
			}
		}
		if commentID == "" || seenComments[commentID] {
			continue
		}
		seenComments[commentID] = true
		var direct Object
		if err := c.call(ctx, "/comment/v1.0/comment-"+commentID+"?fieldSet=commentV1", true, &direct); err != nil {
			if reportPostUnavailable(err) {
				continue
			}
			return nil, len(inaccessibleRoots), err
		}
		if err := addComments([]any{direct}, postID, parent); err != nil {
			return nil, len(inaccessibleRoots), err
		}
		replies, err := c.pages(ctx, "/comment/v1.0/comment-"+commentID+"/artistComments?fieldSet=commentArtistCommentsV1", "after", "createdAt", since)
		if err != nil {
			if reportPostUnavailable(err) {
				continue
			}
			return nil, len(inaccessibleRoots), err
		}
		if err := addComments(replies, postID, parent); err != nil {
			return nil, len(inaccessibleRoots), err
		}
	}
	result := make([]Event, 0, len(all))
	for _, event := range all {
		result = append(result, event)
	}
	return result, len(inaccessibleRoots), nil
}

// BackfillReportNotificationComments discovers historical artist replies from
// notification roots without rescanning the full artist-post archive.
func (c *Client) BackfillReportNotificationComments(ctx context.Context, h *History, s ReportSettings, p ReportPeriod, slug string) (int, int, error) {
	members, err := c.Members(ctx, s.CommunityID)
	if err != nil {
		return 0, 0, err
	}
	if c.Artists == nil {
		c.Artists = map[int64][]Member{}
	}
	c.Artists[s.CommunityID] = members
	if err := h.SaveMembers(s.CommunityID, members); err != nil {
		return 0, 0, err
	}
	names := map[string]string{}
	for _, member := range members {
		names[member.ID] = member.Name
	}
	notifications, err := c.pagesLimit(ctx, fmt.Sprintf("/noti/feed/v2.0/activities?communityId=%d&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", s.CommunityID), "next", "time", p.Start, 200)
	if err != nil {
		return 0, 0, err
	}
	events, inaccessibleRoots, err := c.reportNotificationComments(ctx, notifications, s.CommunityID, slug, p.Start, names)
	if err != nil {
		return 0, inaccessibleRoots, err
	}
	selected := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Time >= p.Start.UnixMilli() && event.Time < p.End.UnixMilli() {
			selected = append(selected, event)
		}
	}
	if err := h.Record(selected, ""); err != nil {
		return 0, inaccessibleRoots, err
	}
	return len(selected), inaccessibleRoots, nil
}

// MemberComments reads the same paginated history shown by the member profile's
// comment tab. Unlike notifications, it is keyed by the comment author.
func (c *Client) MemberComments(ctx context.Context, memberID string, since time.Time) ([]Object, error) {
	if !idRE.MatchString(memberID) {
		return nil, fmt.Errorf("成员标识无效")
	}
	raw, err := c.pages(ctx, "/comment/v1.0/member-"+memberID+"/comments?fieldSet=memberCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
	if err != nil {
		return nil, err
	}
	comments := make([]Object, 0, len(raw))
	for _, item := range raw {
		comments = append(comments, obj(item))
	}
	return comments, nil
}

func (c *Client) reportMemberComments(ctx context.Context, members []Member, communityID int64, slug string, period ReportPeriod) ([]Event, error) {
	names := map[string]string{}
	for _, member := range members {
		names[member.ID] = member.Name
	}
	all := map[string]Event{}
	commentParents := map[string]Object{}
	for _, member := range members {
		comments, err := c.MemberComments(ctx, member.ID, period.Start)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 的历史回复失败: %w", member.Name, err)
		}
		for _, comment := range comments {
			createdAt := num(comment["createdAt"])
			if createdAt < period.Start.UnixMilli() || createdAt >= period.End.UnixMilli() {
				continue
			}
			root := obj(obj(comment["root"])["data"])
			postID := text(root, "postId", "id")
			if postID == "" {
				postID = text(obj(obj(comment["parent"])["data"]), "postId")
			}
			if !idRE.MatchString(postID) {
				continue
			}
			if err := c.addThreadComments(ctx, []any{comment}, postID, slug, communityID, root, names, commentParents, all); err != nil {
				return nil, err
			}
		}
	}
	result := make([]Event, 0, len(all))
	for _, event := range all {
		result = append(result, event)
	}
	return result, nil
}

// BackfillReportMemberComments restores every member's replies from the
// profile comment history without scanning any post archive.
func (c *Client) BackfillReportMemberComments(ctx context.Context, h *History, s ReportSettings, p ReportPeriod, slug string) (map[string]MemberCount, error) {
	members, err := c.Members(ctx, s.CommunityID)
	if err != nil {
		return nil, err
	}
	if c.Artists == nil {
		c.Artists = map[int64][]Member{}
	}
	c.Artists[s.CommunityID] = members
	if err := h.SaveMembers(s.CommunityID, members); err != nil {
		return nil, err
	}
	events, err := c.reportMemberComments(ctx, members, s.CommunityID, slug, p)
	if err != nil {
		return nil, err
	}
	if err := h.Record(events, ""); err != nil {
		return nil, err
	}
	counts := map[string]MemberCount{}
	report := AggregateReport(s, p, members, events)
	for _, member := range report.Members {
		counts[member.ID] = member
	}
	return counts, nil
}

// A single deleted, access-restricted, or password-changed post must not make
// an otherwise valid historical report impossible to generate. The report
// already exposes the unavailable-post count so the missing detail stays
// visible instead of being silently treated as complete data.
func reportPostUnavailable(err error) bool {
	return errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrPostPassword)
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
		if reportPostUnavailable(err) {
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
