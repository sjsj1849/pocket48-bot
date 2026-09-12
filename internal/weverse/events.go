package weverse

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Event struct {
	ID                string   `json:"id"`
	Kind              string   `json:"kind"`
	CommunityID       int64    `json:"communityId"`
	MemberID          string   `json:"memberId"`
	Author            string   `json:"author"`
	Body              string   `json:"body"`
	ParentCommentID   string   `json:"parentCommentId,omitempty"`
	ParentBody        string   `json:"parentBody,omitempty"`
	Translation       string   `json:"translation,omitempty"`
	ParentTranslation string   `json:"parentTranslation,omitempty"`
	TranslationError  string   `json:"translationError,omitempty"`
	URL               string   `json:"url"`
	Images            []string `json:"images,omitempty"`
	Time              int64    `json:"time"`
	PostID            string   `json:"postId"`
	CommentID         string   `json:"commentId,omitempty"`
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func plain(s string) string {
	s = strings.ReplaceAll(s, "<br>", "\n")
	s = strings.ReplaceAll(s, "<br/>", "\n")
	s = strings.ReplaceAll(s, "<br />", "\n")
	s = strings.ReplaceAll(s, "</p>", "\n")
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}
func eventFromPost(p Object, slug string, cid int64) (Event, error) {
	id := str(p["postId"])
	author := obj(p["author"])
	if id == "" || text(author, "memberId", "id") == "" {
		return Event{}, fmt.Errorf("动态缺少内容或作者标识")
	}
	ext := obj(p["extension"])
	video := obj(ext["video"])
	kind := "post"
	section := "artist"
	if str(video["type"]) == "LIVE" {
		if str(video["status"]) != "ONAIR" {
			return Event{}, nil
		}
		kind = "live"
		section = "live"
	}
	body := plain(text(p, "plainBody", "body", "title"))
	if body == "" {
		body = plain(text(obj(ext["mediaInfo"]), "title", "body"))
	}
	timestamp := num(p["publishedAt"])
	if timestamp == 0 {
		timestamp = num(p["createdAt"])
	}
	if kind == "live" && num(video["onAirStartAt"]) > 0 {
		timestamp = num(video["onAirStartAt"])
	}
	e := Event{ID: kind + ":" + id, Kind: kind, CommunityID: cid, MemberID: text(author, "memberId", "id"), Author: str(author["profileName"]), Body: body, PostID: id, Time: timestamp, URL: "https://weverse.io/" + slug + "/" + section + "/" + id}
	for _, v := range list(obj(ext["image"])["photos"]) {
		x := obj(v)
		if u := text(x, "url", "originalUrl"); strings.HasPrefix(u, "https://") {
			e.Images = append(e.Images, u)
		}
	}
	return e, nil
}
func eventFromComment(p Object, postID, slug string, cid int64, parentBody string) (Event, error) {
	id := str(p["commentId"])
	author := obj(p["author"])
	if id == "" || text(author, "memberId", "id") == "" {
		return Event{}, fmt.Errorf("评论缺少内容或作者标识")
	}
	if body := plain(text(obj(obj(p["parent"])["data"]), "plainBody", "body")); body != "" {
		parentBody = body
	}
	event := Event{ID: "comment:" + id, CommentID: id, Kind: "comment", CommunityID: cid, MemberID: text(author, "memberId", "id"), Author: str(author["profileName"]), Body: plain(text(p, "plainBody", "body")), ParentBody: parentBody, PostID: postID, Time: num(p["createdAt"]), URL: "https://weverse.io/" + slug + "/fanpost/" + postID + "/comment/" + id}
	if parent := obj(p["parent"]); str(parent["type"]) == "COMMENT" {
		event.ParentCommentID = str(obj(parent["data"])["commentId"])
	}
	return event, nil
}

// notificationTarget uses the official v2 notification URL rather than translated
// notification text. The same post can appear repeatedly as replies accumulate.
func notificationTarget(n Object) (slug, postID, commentID string) {
	raw := text(n, "webUrl", "url")
	u, e := url.Parse(raw)
	if e != nil {
		return
	}
	if u.Hostname() != "" && u.Hostname() != "weverse.io" && u.Hostname() != "www.weverse.io" {
		return
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 3 {
		return
	}
	slug = p[0]
	if !slugRE.MatchString(slug) {
		return "", "", ""
	}
	if p[1] == "artist" || p[1] == "fanpost" || p[1] == "feed" || p[1] == "live" || p[1] == "media" {
		postID = p[2]
	}
	if p[1] == "moment" {
		for i := 2; i+1 < len(p); i++ {
			if p[i] == "post" {
				postID = p[i+1]
			}
		}
	}
	for i := 3; i+1 < len(p); i++ {
		if p[i] == "comment" || p[i] == "comments" {
			commentID = p[i+1]
		}
	}
	if !idRE.MatchString(postID) {
		postID = ""
	}
	if commentID != "" && !idRE.MatchString(commentID) {
		commentID = ""
	}
	return
}
func (c *Client) Events(ctx context.Context) ([]Event, error) {
	cfg, e := LoadSettings(c.Dir)
	if e != nil {
		return nil, e
	}
	targets := map[int64]Subscription{}
	for _, s := range cfg.Subscriptions {
		if s.Enabled {
			targets[s.CommunityID] = s
		}
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("请先添加至少一个启用的订阅")
	}
	var status Status
	_ = Read(c.Dir, "status.json", &status)
	since, _ := time.Parse(time.RFC3339, status.LastSuccess)
	if !since.IsZero() {
		since = since.Add(-5 * time.Minute)
	}
	all := map[string]Event{}
	for cid, s := range targets {
		members, err := c.Members(ctx, cid)
		if err != nil {
			return nil, err
		}
		artist := map[string]bool{}
		for _, m := range members {
			artist[m.ID] = true
		}
		wantPosts, wantComments, wantLive := false, false, false
		for _, sub := range cfg.Subscriptions {
			if sub.Enabled && sub.CommunityID == cid {
				wantPosts = wantPosts || sub.Posts
				wantComments = wantComments || sub.Comments
				wantLive = wantLive || sub.Live
			}
		}
		addPost := func(p any) error {
			event, err := eventFromPost(obj(p), s.Slug, cid)
			if err != nil {
				return err
			}
			if event.ID != "" && artist[event.MemberID] {
				all[event.ID] = event
			}
			return nil
		}
		if wantPosts {
			posts, err := c.pages(ctx, fmt.Sprintf("/post/v1.0/community-%d/artistTabPosts?fieldSet=postsV1&pagingType=CURSOR&limit=100", cid), "after", "publishedAt", since)
			if err != nil {
				return nil, err
			}
			for _, p := range posts {
				if err := addPost(p); err != nil {
					return nil, err
				}
			}
		}
		if wantLive {
			var live Object
			ep := fmt.Sprintf("/post/v1.0/community-%d/liveTab?fields=", cid) + url.QueryEscape("onAirLivePosts.fieldSet(postsV1).limit(10)")
			if err := c.call(ctx, ep, true, &live); err != nil {
				return nil, err
			}
			onAir, ok := obj(live["onAirLivePosts"])["data"].([]any)
			if !ok {
				return nil, fmt.Errorf("直播列表格式已变更")
			}
			for _, p := range onAir {
				if err := addPost(p); err != nil {
					return nil, err
				}
			}
		}
		if !wantComments {
			continue
		}
		// seen=false avoids marking the user's notifications as read.
		notifications, err := c.pages(ctx, fmt.Sprintf("/noti/feed/v2.0/activities?communityId=%d&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", cid), "next", "time", since)
		if err != nil {
			return nil, err
		}
		seenPosts, seenComments := map[string]bool{}, map[string]bool{}
		parents := map[string]Object{}
		addComments := func(items []any, postID, slug string, parent Object) error {
			for _, x := range items {
				event, err := eventFromComment(obj(x), postID, slug, cid, plain(text(parent, "plainBody", "body")))
				if err != nil {
					return err
				}
				if artist[event.MemberID] {
					if share := str(obj(x)["shareUrl"]); validShareURL(share, slug) {
						event.URL = share
					}
					all[event.ID] = event
				}
			}
			return nil
		}
		for _, v := range notifications {
			n := obj(v)
			slug, postID, commentID := notificationTarget(n)
			if postID == "" || slug != s.Slug {
				continue
			}
			typ := strings.ToUpper(text(n, "activityType", "type"))
			if !strings.Contains(typ, "COMMENT") && !strings.Contains(typ, "REPLY") && commentID == "" {
				continue
			}
			parent, ok := parents[postID]
			if !ok {
				if err := c.call(ctx, "/post/v1.0/post-"+postID+"?fieldSet=postV1", true, &parent); err != nil {
					if errors.Is(err, ErrNotFound) {
						continue
					}
					return nil, err
				}
				parents[postID] = parent
			}
			if !seenPosts[postID] {
				seenPosts[postID] = true
				comments, err := c.pages(ctx, "/comment/v1.0/post-"+postID+"/artistComments?fieldSet=postArtistCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := addComments(comments, postID, slug, parent); err != nil {
					return nil, err
				}
			}
			// A notification may point directly to an artist reply, or to the fan
			// comment under which artist replies have accumulated. Resolve both forms.
			if commentID != "" && !seenComments[commentID] {
				seenComments[commentID] = true
				var direct Object
				err := c.call(ctx, "/comment/v1.0/comment-"+commentID+"?fieldSet=commentV1", true, &direct)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := addComments([]any{direct}, postID, slug, parent); err != nil {
					return nil, err
				}
				replies, err := c.pages(ctx, "/comment/v1.0/comment-"+commentID+"/artistComments?fieldSet=commentArtistCommentsV1", "after", "createdAt", since)
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := addComments(replies, postID, slug, parent); err != nil {
					return nil, err
				}
			}
		}
	}
	results := make([]Event, 0, len(all))
	for _, e := range all {
		results = append(results, e)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Time == results[j].Time {
			return results[i].ID < results[j].ID
		}
		return results[i].Time < results[j].Time
	})
	return results, nil
}
func Matches(s Subscription, e Event) bool {
	if !s.Enabled || s.CommunityID != e.CommunityID {
		return false
	}
	if e.Kind != "post" && e.Kind != "comment" && e.Kind != "live" {
		return false
	}
	if (e.Kind == "post" && !s.Posts) || (e.Kind == "comment" && !s.Comments) || (e.Kind == "live" && !s.Live) {
		return false
	}
	if len(s.MemberIDs) == 0 {
		return true
	}
	for _, id := range s.MemberIDs {
		if id == e.MemberID {
			return true
		}
	}
	return false
}

func validShareURL(raw, slug string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() == "weverse.io" && strings.HasPrefix(u.Path, "/"+slug+"/")
}

// Follow the public web client's cursor format. On a first scan one page is
// sufficient to establish a baseline. Later scans cover the previous success
// (with overlap). Fail visibly on an unbounded backlog rather than silently skip.
func (c *Client) pages(ctx context.Context, ep, param, timeKey string, since time.Time) ([]any, error) {
	result := []any{}
	next := ""
	for page := 0; page < 20; page++ {
		request := ep
		if next != "" {
			request += "&" + param + "=" + url.QueryEscape(next)
		}
		var data Object
		if err := c.call(ctx, request, true, &data); err != nil {
			return nil, err
		}
		items, ok := data["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("Weverse 列表格式已变更")
		}
		result = append(result, items...)
		cursor := str(obj(obj(data["paging"])["nextParams"])["after"])
		reached := false
		for _, v := range items {
			if ts := num(obj(v)[timeKey]); ts > 0 && !since.IsZero() && ts < since.UnixMilli() {
				reached = true
			}
		}
		if cursor == "" || len(items) == 0 || reached || since.IsZero() {
			return result, nil
		}
		if cursor == next {
			return nil, fmt.Errorf("Weverse 分页游标未前进")
		}
		next = cursor
	}
	return nil, fmt.Errorf("Weverse 积压内容超过单次检查上限，未推进检查记录")
}
