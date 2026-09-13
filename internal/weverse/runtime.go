package weverse

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

type Cursor struct {
	Initialized bool             `json:"initialized"`
	Seen        map[string]int64 `json:"seen"`
	Since       int64            `json:"since"`
	Signature   string           `json:"signature"`
}
type Runtime struct {
	Subscriptions map[string]*Cursor `json:"subscriptions"`
}

// Pending establishes a per-subscription baseline. Re-enabling or changing a
// member/event selection establishes a fresh baseline rather than flooding history.
func Pending(state *Runtime, s Subscription, events []Event, now time.Time) []Event {
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]*Cursor{}
	}
	signature := fmt.Sprintf("%d/%d/%v/%t/%t/%t", s.GroupID, s.CommunityID, s.MemberIDs, s.Posts, s.Comments, s.Live)
	cur := state.Subscriptions[s.ID]
	if cur == nil || cur.Signature != signature {
		cur = &Cursor{Seen: map[string]int64{}, Signature: signature}
		state.Subscriptions[s.ID] = cur
	}
	if cur.Seen == nil {
		cur.Seen = map[string]int64{}
	}
	out := []Event{}
	for _, e := range events {
		if !Matches(s, e) {
			continue
		}
		if _, ok := cur.Seen[e.ID]; ok {
			continue
		}
		if !cur.Initialized || e.Time < cur.Since {
			cur.Seen[e.ID] = e.Time
			continue
		}
		out = append(out, e)
	}
	if !cur.Initialized {
		cur.Since = now.UnixMilli()
		cur.Initialized = true
	}
	for id, t := range cur.Seen {
		if t < now.Add(-7*24*time.Hour).UnixMilli() {
			delete(cur.Seen, id)
		}
	}
	return out
}
func MarkDelivered(state *Runtime, s Subscription, e Event) {
	if c := state.Subscriptions[s.ID]; c != nil {
		c.Seen[e.ID] = e.Time
	}
}
func (c *Client) TranslateEvent(ctx context.Context, e *Event) {
	// Translation is fetched on demand, preserving the source text. It follows the
	// logged-in account's translation language; accept only Chinese responses.
	translate := func(kind, id string) (string, error) {
		var d Object
		field := "translated.type(manual).fieldSet(translatedForPost)"
		if kind == "comment" {
			field = "translated.type(manual).fieldSet(translatedForComment)"
		}
		ep := "/" + kind + "/v1.0/" + kind + "-" + id + "?fields=" + url.QueryEscape(field)
		if err := c.call(ctx, ep, true, &d); err != nil {
			return "", err
		}
		t := obj(d["translated"])
		lang := text(t, "userLanguage", "language")
		if lang != "zh-cn" && lang != "zh_CN" && lang != "zh-tw" && lang != "zh_TW" && lang != "zh" {
			return "", fmt.Errorf("请在 Weverse 设置中将翻译语言设为中文")
		}
		result := plain(text(t, "plainBody", "body"))
		if result == "" {
			return "", fmt.Errorf("暂未返回译文")
		}
		return result, nil
	}
	var err error
	e.TranslationError = ""
	if e.Body == "" {
		e.Translation = ""
	} else if e.Kind == "comment" {
		e.Translation, err = translate("comment", e.CommentID)
	} else {
		e.Translation, err = translate("post", e.PostID)
	}
	if err != nil {
		e.TranslationError = err.Error()
	}
	if e.Kind == "comment" && e.ParentBody != "" {
		if e.ParentCommentID != "" {
			e.ParentTranslation, _ = translate("comment", e.ParentCommentID)
		} else {
			e.ParentTranslation, _ = translate("post", e.PostID)
		}
	}
}
