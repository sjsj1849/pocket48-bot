package logic

import (
	"testing"

	"pocket48-bot/internal/weverse"
)

// 线上问题（2026-10-06 22:0x）：weverse 订阅里配了 atAll=true + atAllMemberIds=[STELLA]，
// QQ 上能看到 @全体成员，飞书上几乎从不出现。
//
// 根因：**两条投递路径互不相通**
//   - QQ   走 weverseMessageSegments → napcat.AtSegment("all")
//   - 飞书 走 Document → 只认 Document.MentionAll，而 weverse 的两个 Document
//     构造（主帖 / 回复）都没设这个字段 ⇒ 飞书永远没有 @。
// 全项目此前只有 melon 设了 Document.MentionAll。

func zzStellaEvent() weverse.Event {
	return weverse.Event{
		Kind:      "comment",
		CommentID: "3-999000111",
		PostID:    "2-180661201",
		Author:    "STELLA",
		MemberID:  "0691cff43923e824d411f2d5f22d692e",
		Body:      "今天也很幸福",
		Time:      1791300000000,
	}
}

func zzAtAllSub() weverse.Subscription {
	return weverse.Subscription{
		ID:               "sub-stella",
		AtAll:            true,
		AtAllMemberIDs:   []string{"0691cff43923e824d411f2d5f22d692e"},
		AtAllMemberNames: []string{"STELLA"},
		TargetIDs:        []string{"feishu:group:oc_x"},
		CommunityID:      235,
		CommunityName:    "Hearts2Hearts",
		Slug:             "hearts2hearts",
		Posts:            true,
		Comments:         true,
		Translate:        true,
		Enabled:          true,
	}
}

func TestWeverseReplyDocumentCarriesMentionAll(t *testing.T) {
	e := zzStellaEvent()
	e.ParentBody = " fans"
	e.ParentCommentID = "0-390419624"
	e.ParentProfileType = "FAN"

	doc := weverseReplyDocument(zzAtAllSub(), e)
	if doc == nil {
		t.Fatal("回复 Document 不应为 nil")
	}
	if !doc.MentionAll {
		t.Error("STELLA 在 @全体成员 名单里，Document.MentionAll 必须为 true（否则飞书不 @）")
	}
}

func TestWeversePostDocumentCarriesMentionAll(t *testing.T) {
	e := zzStellaEvent()
	e.Kind = "post"
	e.PostContext = &weverse.AIPostContext{
		PostID: "2-180661201", Author: "STELLA",
		MemberID: "0691cff43923e824d411f2d5f22d692e", AuthorIsArtist: true,
	}
	doc := weversePostDocument(zzAtAllSub(), e)
	if doc == nil {
		t.Fatal("主帖 Document 不应为 nil")
	}
	if !doc.MentionAll {
		t.Error("STELLA 的主帖也应当 @全体成员")
	}
}

// 名单外的人不该被 @（atAllMemberIds 是白名单语义）。
func TestWeverseDocumentSkipsMentionForOtherMembers(t *testing.T) {
	e := zzStellaEvent()
	e.MemberID = "ffffffffffffffffffffffffffffffff" // 不在名单里
	e.ParentBody = "fans"
	e.ParentCommentID = "0-1"
	e.ParentProfileType = "FAN"

	if doc := weverseReplyDocument(zzAtAllSub(), e); doc != nil && doc.MentionAll {
		t.Error("名单外的成员不应触发 @全体成员")
	}
}

// 没开 atAll 的订阅一律不 @。
func TestWeverseDocumentRespectsAtAllOff(t *testing.T) {
	e := zzStellaEvent()
	e.ParentBody = "fans"
	e.ParentCommentID = "0-1"
	e.ParentProfileType = "FAN"

	sub := zzAtAllSub()
	sub.AtAll = false
	if doc := weverseReplyDocument(sub, e); doc != nil && doc.MentionAll {
		t.Error("AtAll=false 时不得 @")
	}
}
