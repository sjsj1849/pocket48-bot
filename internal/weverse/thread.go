package weverse

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ThreadEvents reads the root and all visible artist replies, without changing
// realtime cursors or marking the user's notifications as read.
func (c *Client) ThreadEvents(ctx context.Context, cid int64, slug, postID string) ([]Event, error) {
	return c.threadEvents(ctx, cid, slug, postID, nil, time.UnixMilli(1), time.Time{})
}
func (c *Client) threadEvents(ctx context.Context, cid int64, slug, postID string, post Object, since, end time.Time) ([]Event, error) {
	if !idRE.MatchString(postID) || !slugRE.MatchString(slug) {
		return nil, fmt.Errorf("帖子标识无效")
	}
	members := c.Artists[cid]
	var err error
	if len(members) == 0 {
		members, err = c.Members(ctx, cid)
		if err != nil {
			return nil, err
		}
		if c.Artists == nil {
			c.Artists = map[int64][]Member{}
		}
		c.Artists[cid] = members
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.ID] = m.Name
	}
	if post == nil {
		if err = c.call(ctx, "/post/v1.0/post-"+postID+"?fieldSet=postV1", true, &post); err != nil {
			return nil, err
		}
	}
	// Verify the fetched root belongs to the requested community when supplied.
	if actual := num(post["communityId"]); actual != 0 && actual != cid {
		return nil, fmt.Errorf("帖子不属于所选社区")
	}
	all := map[string]Event{}
	root, err := eventFromPost(post, slug, cid)
	if err != nil {
		return nil, err
	}
	if names[root.MemberID] != "" && root.ID != "" {
		root.Author = names[root.MemberID]
		root.PostContext = aiPostContext(post, postID, slug, names)
		all[root.ID] = root
	}
	items, err := c.pages(ctx, "/comment/v1.0/post-"+postID+"/artistComments?fieldSet=postArtistCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
	if err != nil {
		return nil, err
	}
	filtered := []any{}
	for _, raw := range items {
		ts := num(obj(raw)["createdAt"])
		if ts >= since.UnixMilli() && (end.IsZero() || ts < end.UnixMilli()) {
			filtered = append(filtered, raw)
		}
	}
	if err = c.addThreadComments(ctx, filtered, postID, slug, cid, post, names, map[string]Object{}, all); err != nil {
		return nil, err
	}
	result := []Event{}
	for _, e := range all {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Time == result[j].Time {
			return result[i].ID < result[j].ID
		}
		return result[i].Time < result[j].Time
	})
	return result, nil
}
