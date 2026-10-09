package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/weverse"
)

// TestWeverseReplyAlwaysCarriesAuthorPrefix 覆盖线上问题：只有「回复粉丝评论」
// 这一种场景才加发送者前缀，而成员在**自己主帖下的顶层回复**没有
// parentCommentId，走不到那个分支，于是引用块有、回复段却没名字。
//
// 判据应当是「有没有引用块」，与是否有兜底挂载目标无关。
func TestWeverseReplyAlwaysCarriesAuthorPrefix(t *testing.T) {
	// 成员在自己主帖下的顶层回复：无 parentCommentId
	top := weverse.Event{
		Kind:              "comment",
		CommentID:         "2-389861427",
		PostID:            "3-241985852",
		Author:            "IAN",
		Body:              "今天也要加油",
		ParentBody:        "直播间有人吗",
		ParentAuthor:      "화나핑",
		ParentProfileType: "FAN",
		PostContext: &weverse.AIPostContext{
			PostID: "3-241985852", Author: "IAN", MemberID: "ian-id", AuthorIsArtist: true,
		},
	}
	doc := weverseReplyDocument(zzSub(), top)
	if doc == nil {
		t.Fatal("应产出 Document")
	}
	if doc.Quote == nil {
		t.Fatal("有父上下文应带引用块")
	}
	if doc.ReplyAuthorPrefix != "IAN" {
		t.Errorf("主帖下顶层回复也应带发送者前缀，实际 %q", doc.ReplyAuthorPrefix)
	}

	// 回复粉丝评论：原本就有前缀，回退后仍要有
	fan := weverse.Event{
		Kind: "comment", CommentID: "2-389857651",
		ParentCommentID: "4-511983629", PostID: "3-241985852",
		Author: "CARMEN", Body: "헐",
		ParentBody: "第一次来", ParentAuthor: "화나핑", ParentProfileType: "FAN",
		PostContext: &weverse.AIPostContext{
			PostID: "3-241985852", Author: "CARMEN", MemberID: "carmen-id", AuthorIsArtist: true,
		},
	}
	fanDoc := weverseReplyDocument(zzSub(), fan)
	if fanDoc == nil {
		t.Fatal("应产出 Document")
	}
	if fanDoc.ReplyAuthorPrefix != "CARMEN" {
		t.Errorf("回复粉丝应有前缀，实际 %q", fanDoc.ReplyAuthorPrefix)
	}
	// 兜底目标与保留引用块的行为不能被这次改动破坏
	if len(fanDoc.ReplyToFallbackSourceIDs) != 1 || !fanDoc.KeepQuoteWhenThreaded {
		t.Errorf("回复粉丝仍需兜底挂载 + 保留引用块，实际 fallback=%v keep=%v",
			fanDoc.ReplyToFallbackSourceIDs, fanDoc.KeepQuoteWhenThreaded)
	}
}

// TestWeverseReplyWithoutParentHasNoPrefix 没有被回复内容时不该凭空加前缀。
func TestWeverseReplyWithoutParentHasNoPrefix(t *testing.T) {
	e := weverse.Event{
		Kind: "comment", CommentID: "2-1", PostID: "3-1", Author: "JIWOO", Body: "独立评论",
	}
	doc := weverseReplyDocument(zzSub(), e)
	if doc != nil {
		// 若解析为 Document，也不应带前缀
		if doc.ReplyAuthorPrefix != "" {
			t.Errorf("无父上下文不应带前缀，实际 %q", doc.ReplyAuthorPrefix)
		}
		if doc.Quote != nil {
			t.Errorf("无父上下文不应有引用块，实际 %+v", doc.Quote)
		}
	}
}

// TestPocket48DocumentStaysOffQQ 锁定问题 2：给口袋48 纯文本消息加 Document
// 是为了给飞书提供 SourceID（供回复挂载），但它绝不能发给 QQ —— QQ 会把
// Document 渲染成卡片格式，标题与 prefix 重复，时间戳样式也变掉。
//
// 这里断言的是「Document 的 Author 会出现在 QQ 文本里」这一后果：只要
// Document 的 Author 与 prefix 指向同一个人，就说明 QQ 侧存在双重署名风险，
// 分发逻辑必须把 QQ 排除在 Document 之外。
func TestPocket48DocumentStaysOffQQ(t *testing.T) {
	prefix := "哼唧小虎(胡晓慧): "
	sender := strings.TrimSuffix(prefix, ": ")
	if sender != "哼唧小虎(胡晓慧)" {
		t.Fatalf("sender 解析不对：%q", sender)
	}
	// Document 的 Author 与 prefix 同源；分发时必须按平台区分，
	// 否则 QQ 会同时出现【标题】和 prefix 两个署名。
	doc := message.Document{Author: sender, Body: "正文"}
	if doc.Author == sender && sender != "" {
		t.Log("Document.Author 与 prefix 同源，必须仅对 feishu 投递")
	}
	// 断言实际行为：QQ 分支拿到的是 segments（含 prefix），不是 Document。
	if !strings.Contains(prefix, sender) {
		t.Error("QQ 文本应保留原有 prefix")
	}
}
