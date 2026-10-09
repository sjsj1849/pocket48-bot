package weverse

import (
	"testing"
	"time"
)

// 真实样本（2026-10-09 00:36 YUHA 双份）。
//
// memberId / 正文 / 图片文件名全同，publishedAt 差 610ms，postId 不同。
// 其中 4-242329429 至今仍能用 /post/v1.0/post-<id> 查到完整内容，
// 3-242338419 返回「内容已删除或不可见」⇒ 平台上确实是两条独立记录。
const (
	dupMember = "82a461dde77ad00207f10074461d628f"
	dupBody   = "이아니 생일 축하해💕\n( 정상적인 사진이 없어서 연습생때 우리의 추억을 올려요...💋)"
	dupImgA   = "https://phinf.wevpstatic.net/MjAyNjEwMDlfMjA2/MDAxNzkxNDc3NDAwNjQ1.cT7GWLy2hgSRdIzNe6v0NF1JDzPBb9-7cH3uNWU5qcQg.sVXM6x3uY2d5AtYp4BxX2DuiLkqCNHunzQpDEnxwbnEg.JPEG/Weverse_a4279.jpg"
	dupImgB   = "https://phinf.wevpstatic.net/MjAyNjEwMDlfNzMg/MDAxNzkxNDc3NDAyNjMw.gam4N31yfex09aN-brZX7QvSyGgvjRB1w921et-C69Ig.1PKXpLYkPgDGqoFG2fmagp2CcZKNNh9TwsICCZe8II8g.JPEG/Weverse_15227.jpg"
	// 第二份：同样的图，但 CDN 重新签过名 —— 路径段与哈希全变，只有文件名不变。
	dupImgA2 = "https://phinf.wevpstatic.net/MjAyNjEwMDlfOTUv/MDAxNzkxNDc3NDAxNjgy.mJ9fqR2Pk0KavZDfvTlhsF8Aj-ZYuVsTYxIgSbx4sbIg.afaMkoZCYxlXPylXVjD-7hDUFniHpY_5H5MxZ2rVb8Ug.JPEG/Weverse_a4279.jpg"
	dupImgB2 = "https://phinf.wevpstatic.net/MjAyNjEwMDlfMjgx/MDAxNzkxNDc3NDAzMjgy.mIw6OKYj2nkk1yVHLv4avFrO8Smk6lAUDcAiv2meatkg.N59LQKSW7sudZVGheqFevOaI6xkiN3Qcgos6DDn9Wiog.JPEG/Weverse_15227.jpg"

	dupAtA = int64(1791477403745)
	dupAtB = int64(1791477404355) // 晚 610ms
)

func dupEvents() []Event {
	return []Event{
		{ID: "post:3-242338419", Kind: "post", CommunityID: 235, MemberID: dupMember, Author: "YUHA",
			Body: dupBody, PostID: "3-242338419", Time: dupAtA, Images: []string{dupImgA, dupImgB}},
		{ID: "post:4-242329429", Kind: "post", CommunityID: 235, MemberID: dupMember, Author: "YUHA",
			Body: dupBody, PostID: "4-242329429", Time: dupAtB, Images: []string{dupImgA2, dupImgB2}},
	}
}

func newDupState() *Runtime {
	return &Runtime{Subscriptions: map[string]*Cursor{}}
}

func dupSub() Subscription {
	return Subscription{ID: "h2h", Enabled: true, CommunityID: 235, Posts: true}
}

// 基线要先初始化，否则 Pending 会把所有事件当成历史。
func primeDupState(state *Runtime, s Subscription) {
	Pending(state, s, nil, time.UnixMilli(dupAtA-1000))
}

// ★ 核心回归：真实双份（差 610ms，图片重新签名）只能推一条。
func TestPendingDedupesRealDoublePost(t *testing.T) {
	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, dupEvents(), time.UnixMilli(dupAtB+1000))
	if len(got) != 1 {
		t.Fatalf("同内容双份应只推 1 条，实际 %d 条：%v", len(got), eventIDs(got))
	}
	if got[0].ID != "post:3-242338419" {
		t.Errorf("保留的应是先到的那条，实际 %s", got[0].ID)
	}
	// 被去重的那条仍要记进 Seen，否则下一轮它又会冒出来（且永远推不掉）。
	if _, ok := state.Subscriptions[s.ID].Seen["post:4-242329429"]; !ok {
		t.Error("被去重的 postId 没有记进 Seen —— 下一轮会重复出现在待发列表")
	}
}

// 时间窗必须真的起作用：超过 5 秒就是两条独立帖子，都要推。
func TestPendingKeepsSameContentBeyondWindow(t *testing.T) {
	later := dupEvents()
	later[1].Time = dupAtA + int64(6*time.Second/time.Millisecond)

	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, later, time.UnixMilli(dupAtA+10_000))
	if len(got) != 2 {
		t.Fatalf("超过 5 秒应推 2 条，实际 %d：%v", len(got), eventIDs(got))
	}
}

// 隔几周重发同一张生日贴是常态，绝不能被合并（实测历史里差几百万毫秒）。
func TestPendingKeepsLongAgoResend(t *testing.T) {
	resend := dupEvents()
	resend[1].Time = dupAtA + int64(7*24*time.Hour/time.Millisecond)

	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, resend, time.UnixMilli(resend[1].Time+1000))
	if len(got) != 2 {
		t.Fatalf("跨周重发应推 2 条，实际 %d", len(got))
	}
}

// 评论内容重复是正常对话，绝不能合并。
func TestPendingNeverDedupesComments(t *testing.T) {
	comments := []Event{
		{ID: "comment:1", Kind: "comment", CommunityID: 235, MemberID: "fanA", Body: "好棒", Time: dupAtA},
		{ID: "comment:2", Kind: "comment", CommunityID: 235, MemberID: "fanB", Body: "好棒", Time: dupAtA + 300},
	}
	state := newDupState()
	s := Subscription{ID: "h2h", Enabled: true, CommunityID: 235, Comments: true}
	primeDupState(state, s)

	got := Pending(state, s, comments, time.UnixMilli(dupAtA+1000))
	if len(got) != 2 {
		t.Fatalf("不同粉丝的同内容评论都该推，实际 %d", len(got))
	}
}

// 同一人两条纯表情帖（正文与图片都空）不能被误合并。
func TestPendingNeverMergesEmptyFingerprint(t *testing.T) {
	empties := []Event{
		{ID: "post:a", Kind: "post", CommunityID: 235, MemberID: dupMember, Time: dupAtA},
		{ID: "post:b", Kind: "post", CommunityID: 235, MemberID: dupMember, Time: dupAtA + 200},
	}
	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, empties, time.UnixMilli(dupAtA+1000))
	if len(got) != 2 {
		t.Fatalf("空内容不应产生指纹，实际只推了 %d 条", len(got))
	}
}

// 不同作者的同内容（转发/生日统一文案）不能合并。
func TestPendingNeverMergesDifferentAuthors(t *testing.T) {
	two := dupEvents()
	two[1].MemberID = "someone-else"

	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, two, time.UnixMilli(dupAtB+1000))
	if len(got) != 2 {
		t.Fatalf("不同作者应各推一条，实际 %d", len(got))
	}
}

// 图片顺序不同但集合相同 ⇒ 仍是同一份内容。
func TestPendingFingerprintIgnoresImageOrder(t *testing.T) {
	swapped := dupEvents()
	swapped[0].Images = []string{dupImgB, dupImgA}

	state := newDupState()
	s := dupSub()
	primeDupState(state, s)

	got := Pending(state, s, swapped, time.UnixMilli(dupAtB+1000))
	if len(got) != 1 {
		t.Fatalf("图片顺序不同不应算不同内容，实际推了 %d 条", len(got))
	}
}

func TestEventFingerprintEmptyForNonPost(t *testing.T) {
	if got := eventFingerprint(Event{Kind: "comment", MemberID: "a", Body: "x"}); got != "" {
		t.Errorf("评论不应产生指纹：%q", got)
	}
	if got := eventFingerprint(Event{Kind: "post", MemberID: "a"}); got != "" {
		t.Errorf("空内容不应产生指纹：%q", got)
	}
	if got := eventFingerprint(Event{Kind: "post", MemberID: "a", Body: "x"}); got == "" {
		t.Error("有正文的帖子应有指纹")
	}
}

func eventIDs(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}
