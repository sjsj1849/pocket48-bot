package weverse

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Cursor struct {
	Initialized bool             `json:"initialized"`
	Seen        map[string]int64 `json:"seen"`
	Since       int64            `json:"since"`
	Signature   string           `json:"signature"`
}
type Runtime struct {
	Subscriptions map[string]*Cursor      `json:"subscriptions"`
	Lives         map[string]*TrackedLive `json:"lives,omitempty"`
}

// duplicateFingerprintWindow 是同内容双份的时间窗。
//
// 取 5s 的依据：实测两份的间隔是 610ms 与 853ms（同一分钟内的两次），
// 而「同一张生日贴隔几周再发」是几百万毫秒级 —— 中间差着三个数量级，
// 5s 落在中间，怎么取都不会误伤正常重发。
const duplicateFingerprintWindow = 5 * time.Second

// eventFingerprint 返回一条帖子的内容指纹，空串表示「不参与指纹去重」。
//
// ★ 为什么图片只能取**文件名**：Weverse 的图片 CDN 每次请求都会重新签名，同一个文件
// 在两次抓取里 URL 完全不同 —— 路径段从 `MjAyNjEwMDlfMjA2` 变成 `MjAyNjEwMDlfOTUv`，
// ? 后的哈希也全变。但**文件名稳定**（实测两条双份都是 `Weverse_a4279.jpg` 和
// `Weverse_15227.jpg`，下载后字节数也一致）。用整条 URL 做指纹永远匹配不上。
//
// ★ 为什么只对 post / moment 生效：评论内容重复是常态（两个粉丝各发一句「好棒」
// 属于正常对话），合并它们会丢掉真实互动。
//
// ★ 为什么空内容返回空串：纯表情帖（正文和图片都为空）指纹会退化成「同作者 + 空」，
// 那样同一个人的两条纯表情帖会被误合并。
func eventFingerprint(e Event) string {
	if e.Kind != "post" && e.Kind != "moment" {
		return ""
	}
	body := strings.TrimSpace(e.Body)
	names := make([]string, 0, len(e.Images))
	for _, img := range e.Images {
		img = strings.TrimSpace(img)
		if img == "" {
			continue
		}
		// 只保留最后一段路径（文件名），query 里的签名参数一律丢掉。
		if idx := strings.LastIndexByte(img, '/'); idx >= 0 && idx+1 < len(img) {
			names = append(names, img[idx+1:])
		} else {
			names = append(names, img)
		}
	}
	if body == "" && len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return e.MemberID + "\x00" + body + "\x00" + strings.Join(names, "\x00")
}

// absDuration 把**毫秒**差值转成 time.Duration。
//
// ★ Event.Time 是毫秒（UnixMilli），直接 time.Duration(ms) 会当成纳秒 —— 那样
// 6000（=6 秒）会被算成 6 微秒，从而被 5s 窗口误判成「同一份」。这个错误是反向
// 测试（跨 6 秒 / 跨周重发必须保留两条）抓出来的。
func absDuration(ms int64) time.Duration {
	if ms < 0 {
		return -time.Duration(ms) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
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
	// 同内容指纹：作者 + 正文 + 图片文件名（见 eventFingerprint 的注释）。
	fingerprints := map[string]int64{}
	for _, e := range events {
		if !Matches(s, e) {
			continue
		}
		if _, ok := cur.Seen[e.ID]; ok {
			// Seen is a retention clock, not the content's publish time. An old
			// post may remain (or reappear) in Weverse's current feed long after
			// publication; refreshing it here prevents pruning the dedupe key and
			// sending the same still-visible post again on the next poll.
			cur.Seen[e.ID] = now.UnixMilli()
			continue
		}
		if !cur.Initialized || e.Time < cur.Since {
			cur.Seen[e.ID] = now.UnixMilli()
			continue
		}
		// ★ 同内容指纹去重（2026-10-09 用户要求）。
		//
		// 现象：Weverse 对同一条内容会写出**两个不同 postId**（实测 YUHA
		// 2026-10-09 00:36，3-242338419 与 4-242329429，memberId / 正文 /
		// 图片文件名全同，时间差 610ms）。上面的 Seen 按 event.ID 去重，
		// 两个 id 不同 ⇒ 两条都推，用户收到两份一模一样的内容。
		//
		// 用「同指纹 + 时间差 < 5s」判定：跨周重发同一张生日贴
		// （实测有几百万毫秒的间隔）绝不会被误伤。
		if fp := eventFingerprint(e); fp != "" {
			if prevAt, ok := fingerprints[fp]; ok && absDuration(e.Time-prevAt) < duplicateFingerprintWindow {
				// 记进 Seen：它确实是一条独立帖子（原文仍会入库），
				// 只是不再推送，否则下一轮它会再次冒出来。
				cur.Seen[e.ID] = now.UnixMilli()
				continue
			}
			fingerprints[fp] = e.Time
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
		c.Seen[e.ID] = time.Now().UnixMilli()
	}
}
func (c *Client) TranslateEvent(ctx context.Context, e *Event) {
	// Live chat carries Weverse's own translations in the chat payload; it does
	// not have a post/comment translation endpoint of its own.
	if e.Kind == "live_chat" {
		return
	}
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
		lang := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(text(t, "userLanguage", "language")), "_", "-"))
		if lang != "zh-cn" && lang != "zh-tw" && lang != "zh" {
			return "", fmt.Errorf("请在 Weverse 设置中将翻译语言设为中文")
		}
		result := plain(text(t, "plainBody", "body", "title"))
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
