package weverse

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Event struct {
	CommentsAt                int64    `json:"commentsAt,omitempty"`
	LikesAt                   int64    `json:"likesAt,omitempty"`
	PostComments              *int64   `json:"postComments,omitempty"`
	PostLikes                 *int64   `json:"postLikes,omitempty"`
	MetricsAt                 int64    `json:"metricsAt,omitempty"`
	LiveParticipants          []string `json:"liveParticipants,omitempty"`
	LiveParticipantsConfirmed bool     `json:"liveParticipantsConfirmed,omitempty"`

	ID                       string            `json:"id"`
	Kind                     string            `json:"kind"`
	CommunityID              int64             `json:"communityId"`
	MemberID                 string            `json:"memberId"`
	Author                   string            `json:"author"`
	Body                     string            `json:"body"`
	ParentCommentID          string            `json:"parentCommentId,omitempty"`
	ParentBody               string            `json:"parentBody,omitempty"`
	ParentAuthor             string            `json:"parentAuthor,omitempty"`
	ParentMemberID           string            `json:"parentMemberId,omitempty"`
	ParentContextUnavailable bool              `json:"parentContextUnavailable,omitempty"`
	ParentProfileType        string            `json:"parentProfileType,omitempty"`
	MembershipOnly           bool              `json:"membershipOnly,omitempty"`
	PasswordProtected        bool              `json:"passwordProtected,omitempty"`
	PostContext              *AIPostContext    `json:"postContext,omitempty"`
	Translation              string            `json:"translation,omitempty"`
	ParentTranslation        string            `json:"parentTranslation,omitempty"`
	TranslationError         string            `json:"translationError,omitempty"`
	URL                      string            `json:"url"`
	Images                   []string          `json:"images,omitempty"`
	Videos                   []VideoAttachment `json:"videos,omitempty"`
	CoverURL                 string            `json:"coverUrl,omitempty"`
	LiveStartedAt            int64             `json:"liveStartedAt,omitempty"`
	LiveEndedAt              int64             `json:"liveEndedAt,omitempty"`
	LiveDuration             int64             `json:"liveDuration,omitempty"`
	LiveChatID               string            `json:"liveChatId,omitempty"`
	LiveHostMemberID         string            `json:"liveHostMemberId,omitempty"`
	LiveHostAuthor           string            `json:"liveHostAuthor,omitempty"`
	Time                     int64             `json:"time"`
	PostID                   string            `json:"postId"`
	CommentID                string            `json:"commentId,omitempty"`
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
	liveToVod, _ := video["liveToVod"].(bool)
	if liveToVod && str(video["type"]) == "VOD" {
		return Event{}, nil
	}
	kind := "post"
	section := "artist"
	if str(video["type"]) == "LIVE" {
		if str(video["status"]) != "ONAIR" {
			return Event{}, nil
		}
		kind = "live"
		section = "live"
	}
	if strings.HasPrefix(str(p["postType"]), "MOMENT") || ext["moment"] != nil || ext["momentW1"] != nil {
		kind = "moment"
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
	membershipOnly, _ := p["membershipOnly"].(bool)
	locked, _ := p["locked"].(bool)
	e := Event{ID: kind + ":" + id, Kind: kind, CommunityID: cid, MemberID: text(author, "memberId", "id"), Author: str(author["profileName"]), Body: body, PostID: id, Time: timestamp, URL: "https://weverse.io/" + slug + "/" + section + "/" + id, MembershipOnly: membershipOnly, PasswordProtected: locked}
	if kind == "moment" {
		e.URL = "https://weverse.io/" + slug + "/moment/" + e.MemberID + "/post/" + id
	}
	e.Images = eventImages(p)
	if kind == "post" || kind == "moment" {
		e.PostComments = optionalCount(p, "commentCount")
		e.PostLikes = optionalCount(p, "emotionCount")
		if e.PostComments != nil || e.PostLikes != nil {
			e.MetricsAt = time.Now().UnixMilli()
		}
		if e.PostComments != nil {
			e.CommentsAt = e.MetricsAt
		}
		if e.PostLikes != nil {
			e.LikesAt = e.MetricsAt
		}
		e.Videos = eventVideos(p)
	}
	if kind == "live" {
		e.LiveDuration = num(video["playTime"])
		for _, raw := range list(p["liveParticipants"]) {
			if id := text(obj(raw), "memberId", "id"); id != "" {
				e.LiveParticipants = append(e.LiveParticipants, id)
			}
		}
		e.LiveStartedAt = timestamp
		if title := plain(text(obj(ext["mediaInfo"]), "title")); title != "" {
			e.Body = title
		}
		if title := plain(text(p, "title")); title != "" {
			e.Body = title
		}
		e.CoverURL = text(obj(obj(ext["mediaInfo"])["thumbnail"]), "url")
		if e.CoverURL == "" {
			e.CoverURL = text(video, "thumb", "thumbnailUrl", "coverUrl")
		}
		if strings.HasPrefix(e.CoverURL, "https://") {
			e.Images = []string{e.CoverURL}
		} else {
			e.CoverURL = ""
		}
	}
	// Membership-only content must never be redistributed. Keep only enough
	// metadata to notify that it exists and to deduplicate the notification.
	if e.MembershipOnly || e.PasswordProtected {
		e.Body = ""
		e.Images = nil
		e.Videos = nil
		e.CoverURL = ""
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
	event.Images = eventImages(p)
	event.Videos = eventVideos(p)
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
		if c.Artists == nil {
			c.Artists = map[int64][]Member{}
		}
		c.Artists[cid] = members
		artist := map[string]bool{}
		artistNames := map[string]string{}
		for _, m := range members {
			artist[m.ID] = true
			artistNames[m.ID] = m.Name
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
			id := text(obj(p), "postId", "id")
			if idRE.MatchString(id) {
				ep, e := c.withPostPassword("/post/v1.0/post-" + id + "?fieldSet=postV1")
				if e != nil {
					return e
				}
				if strings.Contains(ep, "lockPassword=") {
					var full Object
					if e = c.call(ctx, ep, true, &full); e != nil {
						return e
					}
					// Weverse may retain locked=true in an otherwise successfully
					// unlocked response. The explicit verified password means its body
					// and attachments are safe to parse as ordinary password content.
					full["locked"] = false
					p = full
				}
			}
			event, err := eventFromPost(obj(p), s.Slug, cid)
			if err != nil {
				return err
			}
			if event.ID != "" && artist[event.MemberID] {
				event.Author = artistNames[event.MemberID]
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
			// Moment 走活动流：活动流自带 ARTIST_MOMENT 类型（实测覆盖），
			// 不再遍历成员去取 artistLatestMoment —— 那要求每轮都拉一次完整成员
			// 名单（响应体大、实测 3.8s），而名单本身几乎不变。
			// 若活动流漏推，成员名单缓存 10 分钟 TTL 过期后的那次刷新会补上。
			// The official artist directory exposes current Moments independently
			// of the notification feed, which may omit an artist's notification.
			for _, member := range members {
				id := member.latestMomentPostID
				// A restricted Moment may have been skipped by an older bot version.
				// Keep exposing the current restricted item until the runtime cursor has
				// recorded it; Pending still establishes a baseline on first setup.
				if !idRE.MatchString(id) || (!(member.latestMomentMembershipOnly || member.latestMomentLocked) && !since.IsZero() && member.latestMomentAt > 0 && member.latestMomentAt < since.UnixMilli()) {
					continue
				}
				if _, ok := all["moment:"+id]; ok {
					continue
				}
				if member.latestMomentMembershipOnly {
					all["moment:"+id] = Event{
						ID:             "moment:" + id,
						Kind:           "moment",
						CommunityID:    cid,
						MemberID:       member.ID,
						Author:         member.Name,
						PostID:         id,
						Time:           member.latestMomentAt,
						URL:            "https://weverse.io/" + s.Slug + "/moment/" + member.ID + "/post/" + id,
						MembershipOnly: true,
					}
					continue
				}
				if member.latestMomentLocked {
					ep, passwordErr := c.withPostPassword("/post/v1.0/post-" + id + "?fieldSet=postV1")
					if passwordErr != nil {
						return nil, passwordErr
					}
					if !strings.Contains(ep, "lockPassword=") {
						all["moment:"+id] = Event{ID: "moment:" + id, Kind: "moment", CommunityID: cid, MemberID: member.ID, Author: member.Name, PostID: id, Time: member.latestMomentAt, URL: "https://weverse.io/" + s.Slug + "/moment/" + member.ID + "/post/" + id, PasswordProtected: true}
						continue
					}
				}
				var post Object
				if err := c.call(ctx, "/post/v1.0/post-"+id+"?fieldSet=postV1", true, &post); err != nil {
					if errors.Is(err, ErrPostPassword) && member.latestMomentLocked {
						all["moment:"+id] = Event{ID: "moment:" + id, Kind: "moment", CommunityID: cid, MemberID: member.ID, Author: member.Name, PostID: id, Time: member.latestMomentAt, URL: "https://weverse.io/" + s.Slug + "/moment/" + member.ID + "/post/" + id, PasswordProtected: true}
						continue
					}
					if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
						continue
					}
					return nil, err
				}
				if err := addPost(post); err != nil {
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
				id := text(obj(p), "postId", "id")
				if !idRE.MatchString(id) {
					return nil, fmt.Errorf("直播缺少内容编号")
				}
				var full Object
				if err := c.call(ctx, "/post/v1.0/post-"+id+"?fieldSet=postV1", true, &full); err != nil {
					return nil, err
				}
				if err := addPost(full); err != nil {
					return nil, err
				}
				live, err := eventFromPost(full, s.Slug, cid)
				if err != nil {
					return nil, err
				}
				if live.ID == "" {
					continue
				}
				if name := artistNames[live.MemberID]; name != "" {
					live.Author = name
				}
				chats, err := c.liveChatEvents(ctx, full, live, s.Slug, cid, artistNames, since, 20)
				if err != nil {
					return nil, err
				}
				for _, chat := range chats {
					all[chat.ID] = chat
				}
			}
		}
		if !wantComments && !wantPosts {
			continue
		}
		// seen=false avoids marking the user's notifications as read.
		notifications, err := c.pages(ctx, fmt.Sprintf("/noti/feed/v2.0/activities?communityId=%d&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", cid), "next", "time", since)
		if err != nil {
			return nil, err
		}
		seenPosts, seenComments := map[string]bool{}, map[string]bool{}
		parents := map[string]Object{}
		commentParents := map[string]Object{}
		// 回复主路径：按成员维度拉取（每成员 1 个请求）。这是月报回填已在用、
		// 验证过的接口，能直接拿到全员回复，不必为每个帖子回帖拉详情。
		// 月报与实时用同一口径，两边统计数字保持一致。
		if wantComments {
			memberCommentEvents(ctx, c, s, cid, members, artistNames, since, all)
		}
		addComments := func(items []any, postID, slug string, parent Object) error {
			return c.addThreadComments(ctx, items, postID, slug, cid, parent, artistNames, commentParents, all)
		}
		// 通知流仍用于兜底：member 接口只覆盖「成员发过的评论」，
		// 而 ARTIST_MOMENT_COMMENT / 成员在 moment 下的回复等类型可能不在其中。
		// 这里只对尚未命中 member 结果的帖子回帖拉取，请求量比原来低一个数量级。
		cutMS := int64(0)
		if !since.IsZero() {
			cutMS = since.UnixMilli()
		}
		for _, v := range notifications {
			n := obj(v)
			// pagesLimit 只用时间判断"是否继续翻页"，从不丢弃早于 since 的条目，
			// 于是每轮都会收到整页历史活动（实测第一页可横跨数天）。这里按活动
			// 自身的时间再过滤一次，否则会为早已处理过的活动反复回帖拉详情。
			if cutMS > 0 {
				if ts := num(n["time"]); ts > 0 && ts < cutMS {
					continue
				}
			}
			slug, postID, commentID := notificationTarget(n)
			if postID == "" || slug != s.Slug {
				continue
			}
			if wantComments && threadCoveredByMember(postID, all) {
				continue
			}
			typ := strings.ToUpper(text(n, "activityType", "type"))
			if wantPosts && strings.Contains(typ, "MOMENT") && !strings.Contains(typ, "COMMENT") {
				post, err := c.postDetail(ctx, postID)
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := addPost(post); err != nil {
					return nil, err
				}
				continue
			}
			if !wantComments {
				continue
			}
			if !strings.Contains(typ, "COMMENT") && !strings.Contains(typ, "REPLY") && commentID == "" {
				continue
			}
			parent, ok := parents[postID]
			if !ok {
				var err error
				if parent, err = c.postDetail(ctx, postID); err != nil {
					if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
						continue
					}
					return nil, err
				}
				parents[postID] = parent
			}
			if !seenPosts[postID] {
				seenPosts[postID] = true
				comments, err := c.pages(ctx, "/comment/v1.0/post-"+postID+"/artistComments?fieldSet=postArtistCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
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
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if err := addComments([]any{direct}, postID, slug, parent); err != nil {
					return nil, err
				}
				replies, err := c.pages(ctx, "/comment/v1.0/comment-"+commentID+"/artistComments?fieldSet=commentArtistCommentsV1", "after", "createdAt", since)
				if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
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
	if e.Kind != "post" && e.Kind != "comment" && e.Kind != "live" && e.Kind != "live_end" && e.Kind != "live_replay" && e.Kind != "live_chat" && e.Kind != "moment" {
		return false
	}
	if ((e.Kind == "post" || e.Kind == "moment") && !s.Posts) || (e.Kind == "comment" && !s.Comments) || ((e.Kind == "live" || e.Kind == "live_end" || e.Kind == "live_replay" || e.Kind == "live_chat") && !s.Live) {
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
	return c.pagesLimit(ctx, ep, param, timeKey, since, 20)
}
func (c *Client) pagesLimit(ctx context.Context, ep, param, timeKey string, since time.Time, maxPages int) ([]any, error) {
	result := []any{}
	next := ""
	for page := 0; page < maxPages; page++ {
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

// Current web responses place photos in orderedAttachments; retain the legacy
// extension.image.photos fallback without sending the same photo twice.
func eventImages(p Object) []string {
	var images []string
	seen := map[string]bool{}
	add := func(photo Object) {
		u := text(photo, "url", "originalUrl")
		if strings.HasPrefix(u, "https://") && !seen[u] {
			images = append(images, u)
			seen[u] = true
		}
	}
	for _, v := range list(p["orderedAttachments"]) {
		attachment := obj(v)
		if strings.EqualFold(str(attachment["type"]), "photo") {
			add(obj(attachment["data"]))
		}
	}
	for _, v := range list(obj(obj(p["extension"])["image"])["photos"]) {
		add(obj(v))
	}
	add(obj(obj(obj(p["extension"])["momentW1"])["photo"]))
	return images
}

// PostContext fetches the original post for pending batches written by older versions.
func (c *Client) PostContext(ctx context.Context, postID, slug string, cid int64) (*AIPostContext, error) {
	if !idRE.MatchString(postID) || !slugRE.MatchString(slug) {
		return nil, fmt.Errorf("主帖标识无效")
	}
	members, err := c.Members(ctx, cid)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.ID] = m.Name
	}
	var p Object
	if err := c.call(ctx, "/post/v1.0/post-"+postID+"?fieldSet=postV1", true, &p); err != nil {
		return nil, err
	}
	return aiPostContext(p, postID, slug, names), nil
}

// threadCoveredByMember 判断该帖子的爱豆回复是否已经由 member 维度接口覆盖。
// 注意 Event.ID 形如 "comment:<commentId>"，**不含 postId**，所以必须用 PostID
// 字段判断；早先按 ID 前缀反查永远不成立，会让兜底逻辑照旧逐帖拉取。
func threadCoveredByMember(postID string, all map[string]Event) bool {
	for _, e := range all {
		if e.PostID == postID {
			return true
		}
	}
	return false
}

// memberCommentEvents 用「按成员维度」的评论接口收集全员最新回复。
//
// 为什么不用回帖拉取：活动流里带着大量历史帖子上下文（实测一次 5 分钟窗口就有
// 241 个唯一 postId），逐帖串行拉详情+评论约需 482 个请求，是单轮 19~29s 的主因。
// 而 /comment/v1.0/member-<id>/comments 直接按评论作者返回该成员的评论，
// 8 位成员只需 8 个请求，月报回填已在用并验证过这个口径。
func memberCommentEvents(ctx context.Context, c *Client, sub Subscription, cid int64, members []Member, artistNames map[string]string, since time.Time, all map[string]Event) {
	for _, member := range members {
		comments, err := c.MemberComments(ctx, member.ID, since)
		if err != nil {
			// 单个成员失败（限流/网络）不应中断整轮，继续下一位。
			log.Printf("[Weverse] 读取 %s 的回复失败: %v", member.Name, err)
			continue
		}
		commentParents := map[string]Object{}
		for _, comment := range comments {
			createdAt := num(comment["createdAt"])
			if since.IsZero() || (createdAt > 0 && createdAt < since.UnixMilli()) {
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
			// addThreadComments 会自行按 artistNames 过滤作者并解析父评论上下文。
			if err := c.addThreadComments(ctx, []any{comment}, postID, sub.Slug, cid, root, artistNames, commentParents, all); err != nil {
				log.Printf("[Weverse] 解析 %s 的回复失败: %v", member.Name, err)
			}
		}
	}
}

func (c *Client) addThreadComments(ctx context.Context, items []any, postID, slug string, cid int64, parent Object, artistNames map[string]string, commentParents map[string]Object, all map[string]Event) error {
	for _, x := range items {
		item := obj(x)
		// Check the actual author, not the notification type or text.
		if artistNames[text(obj(item["author"]), "memberId", "id")] == "" {
			continue
		}
		relation := obj(item["parent"])
		target := obj(relation["data"])
		switch str(relation["type"]) {
		case "POST":
			if text(obj(target["author"]), "memberId", "id") == "" {
				target = parent
			}
		case "COMMENT":
			id := str(target["commentId"])
			if text(obj(target["author"]), "memberId", "id") == "" || str(obj(target["author"])["profileType"]) == "" {
				if idRE.MatchString(id) {
					cached, ok := commentParents[id]
					if !ok {
						// 父评论正文不可变，走缓存；失败（如已删除）保持零值，
						// 与原有"忽略 NotFound/Forbidden"的行为一致。
						cached, _ = c.commentDetail(ctx, id)
						commentParents[id] = cached
					}
					target = cached
				}
			}
		default:
			target = nil // Keep the member reply without guessing its context.
		}
		event, err := eventFromComment(item, postID, slug, cid, plain(text(target, "plainBody", "body")))
		if err != nil {
			return err
		}
		targetAuthor := obj(target["author"])
		event.ParentBody = plain(text(target, "plainBody", "body"))
		event.ParentMemberID = text(targetAuthor, "memberId", "id")
		event.ParentAuthor = text(obj(targetAuthor["artistOfficialProfile"]), "officialName")
		if event.ParentAuthor == "" {
			event.ParentAuthor = str(targetAuthor["profileName"])
		}
		event.ParentProfileType = str(targetAuthor["profileType"])
		event.ParentContextUnavailable = str(relation["type"]) == "COMMENT" && event.ParentMemberID == "" && event.ParentBody == ""
		event.PostContext = aiPostContext(parent, postID, slug, artistNames)
		if name := artistNames[event.ParentMemberID]; name != "" {
			event.ParentAuthor = name
			event.ParentProfileType = "ARTIST"
		}
		if artistNames[event.MemberID] != "" {
			if share := str(obj(x)["shareUrl"]); validShareURL(share, slug) {
				event.URL = share
			}
			event.Author = artistNames[event.MemberID]
			all[event.ID] = event
		}
	}
	return nil
}

func optionalCount(o Object, key string) *int64 {
	if value, ok := o[key]; ok && value != nil {
		n := num(value)
		if n >= 0 {
			return &n
		}
	}
	return nil
}
