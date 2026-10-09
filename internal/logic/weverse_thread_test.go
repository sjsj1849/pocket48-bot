package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/weverse"
)

// zzSub 是给只需要 Document 构造、不关心订阅设置的用例用的空订阅。
// 2026-10-06 起 weverseReplyDocument 需要 Subscription 才能判断 @全体成员。
func zzSub() weverse.Subscription { return weverse.Subscription{ID: "sub-zz"} }

// TestWeverseReplyUnderMemberPostThreads 验证「成员主帖下的顶层回复」改为原生挂载。
//
// 真实场景（JIWOO 2026-10-02）：她发主帖 "Spotify CLOSER!!!"，随后在自己帖子下
// 回复一条。该回复的原始数据里有 parentBody/parentAuthor，但**没有 parentCommentId**
// （顶层回复本就没有父评论），于是早期只认 parentCommentId 的逻辑退化成
// 「被回复/回复」分隔线排版。现在应挂到postId 上。
func TestWeverseReplyUnderMemberPostThreads(t *testing.T) {
	e := weverse.Event{
		Kind:              "comment",
		CommentID:         "2-389849916",
		PostID:            "4-241974648",
		MemberID:          "jiwoo-id",
		Author:            "JIWOO",
		Body:              "한정선을 선물로 주신 스포티파이",
		ParentBody:        "Spotify CLOSER!!!",
		ParentAuthor:      "JIWOO",
		ParentProfileType: "ARTIST",
		PostContext: &weverse.AIPostContext{
			PostID:         "4-241974648",
			Author:         "JIWOO",
			MemberID:       "jiwoo-id",
			AuthorIsArtist: true,
		},
	}

	if !isWeversePostAuthoredByMember(e) {
		t.Fatal("应判定为主帖由成员发布")
	}

	doc := weverseReplyDocument(zzSub(), e)
	if doc == nil {
		t.Fatal("不应返回 nil（这是有父上下文的回复）")
	}
	if doc.ReplyToSourceID != "4-241974648" {
		t.Errorf("应挂到主帖 postId，实际 %q", doc.ReplyToSourceID)
	}
	// 引用块始终保留：能否真正挂载要由适配器在发送时查 ReplyMap 才知道，
	// 命中后适配器会自己丢掉它；未命中时它就是唯一的上下文。
	if doc.Quote == nil {
		t.Error("应保留引用块作为挂载失败时的兜底上下文")
	}
	if len(doc.ReplyToFallbackSourceIDs) != 0 {
		t.Errorf("主帖已是首选目标，不应再重复列为兜底：%v", doc.ReplyToFallbackSourceIDs)
	}
	// 译文/正文都不应带"回复者："前缀
	if strings.HasPrefix(doc.Translation, "JIWOO：") {
		t.Errorf("译文不应带回复者前缀：%q", doc.Translation)
	}
	if strings.HasPrefix(doc.Body, "JIWOO：") {
		t.Errorf("正文不应带回复者前缀：%q", doc.Body)
	}
	t.Logf("ReplyToSourceID=%q Quote=%v Translation=%q Body=%q",
		doc.ReplyToSourceID, doc.Quote, doc.Translation, doc.Body)
}

// TestWeverseReplyToFanCommentFallsBackToPost 覆盖线上真实故障：
// 成员回复粉丝评论时，父评论我们从未转发（ReplyMap 里没有它的消息 id），
// 挂载会静默失效。此时必须把所属主帖列为兜底目标，让回复仍挂在帖子下面。
//
// 真实数据（CARMEN 2026-10-02 21:35，主帖 4-241976860）：
//
//	cid=2-389857651 pcid=4-511983629 ptype=FAN "헐 ㅠㅠㅠ"
//
// 该父评论是粉丝 쮸바이아니안 的评论，events/forwarded 表中均无记录。
func TestWeverseReplyToFanCommentFallsBackToPost(t *testing.T) {
	e := weverse.Event{
		Kind:              "comment",
		CommentID:         "2-389857651",
		ParentCommentID:   "4-511983629",
		PostID:            "4-241976860",
		MemberID:          "carmen-id",
		Author:            "CARMEN",
		Body:              "헐 ㅠㅠㅠ",
		ParentBody:        "오늘 처음으로 보러갔는데 진짜 너무 좋았어",
		ParentAuthor:      "쮸바이아니안",
		ParentProfileType: "FAN",
		PostContext: &weverse.AIPostContext{
			PostID:         "4-241976860",
			Author:         "CARMEN",
			MemberID:       "carmen-id",
			AuthorIsArtist: true,
		},
	}

	doc := weverseReplyDocument(zzSub(), e)
	if doc == nil {
		t.Fatal("不应返回 nil")
	}
	// 首选仍是粉丝评论（若哪天我们开始转发粉丝评论，语义不变）
	if doc.ReplyToSourceID != "4-511983629" {
		t.Errorf("首选目标应为父评论，实际 %q", doc.ReplyToSourceID)
	}
	// 关键：主帖必须作为兜底，否则这条回复会散落在聊天流里
	if len(doc.ReplyToFallbackSourceIDs) != 1 || doc.ReplyToFallbackSourceIDs[0] != "4-241976860" {
		t.Errorf("主帖应作为兜底挂载目标，实际 %v", doc.ReplyToFallbackSourceIDs)
	}
	// 兜底命中时原生 reply 上方是帖子而不是粉丝评论，引用块必须保留
	if !doc.KeepQuoteWhenThreaded {
		t.Error("兜底挂载到帖子时应保留引用块")
	}
	if doc.Quote == nil || doc.Quote.Text == "" {
		t.Error("应保留被回复评论的引用块")
	}
	t.Logf("primary=%q fallback=%v keepQuote=%v", doc.ReplyToSourceID, doc.ReplyToFallbackSourceIDs, doc.KeepQuoteWhenThreaded)
}

// TestWeverseNestedReplyDropsQuoteOnThreading 父评论挂载成功时，引用块是重复的
// （原生 reply 上方已经是被回复内容），此时不应保留。
//
// 真实场景（CARMEN 2026-10-02，回复另一成员 JUUN 的评论 cid=3-512000193）：
// 父评论是成员评论、我们转发过，能直接挂上，所以不需要兜底也不需要引用块。
func TestWeverseNestedReplyDropsQuoteOnThreading(t *testing.T) {
	e := weverse.Event{
		Kind:              "comment",
		CommentID:         "3-512000193",
		ParentCommentID:   "3-512014949",
		PostID:            "4-241976860",
		Author:            "CARMEN",
		Body:              "넘넘넘 재밌었어",
		ParentBody:        "르멘아 오늘 스포티 재밌었어요핑~?",
		ParentAuthor:      "숭편드세숭",
		ParentProfileType: "FAN",
		PostContext: &weverse.AIPostContext{
			PostID:         "4-241976860",
			Author:         "CARMEN",
			MemberID:       "carmen-id",
			AuthorIsArtist: true,
		},
	}
	// 父评论是粉丝评论 → 需要兜底 + 保留引用块
	fan := weverseReplyDocument(zzSub(), e)
	if fan == nil {
		t.Fatal("不应返回 nil")
	}
	if !fan.KeepQuoteWhenThreaded || len(fan.ReplyToFallbackSourceIDs) != 1 {
		t.Errorf("粉丝评论应兜底到主帖并保留引用块，实际 keep=%v fallback=%v",
			fan.KeepQuoteWhenThreaded, fan.ReplyToFallbackSourceIDs)
	}

	// 换成回复成员评论：能直接挂上，不需要兜底，引用块也不必保留
	e.ParentProfileType = "ARTIST"
	e.ParentAuthor = "JUUN"
	e.ParentMemberID = "juun-id"
	artist := weverseReplyDocument(zzSub(), e)
	if artist == nil {
		t.Fatal("不应返回 nil")
	}
	if artist.ReplyToSourceID != "3-512014949" {
		t.Errorf("应挂到父评论，实际 %q", artist.ReplyToSourceID)
	}
	if artist.KeepQuoteWhenThreaded {
		t.Error("父评论即挂载目标，不应要求保留引用块")
	}
	if len(artist.ReplyToFallbackSourceIDs) != 0 {
		t.Errorf("成员评论可直接挂载，不应加兜底目标：%v", artist.ReplyToFallbackSourceIDs)
	}
}

// TestWeversePostDocumentCarriesVideo 帖子必须带上视频：此前只处理 Images，
// 导致帖子视频在飞书上整段丢失，连封面都不显示。
func TestWeversePostDocumentCarriesVideo(t *testing.T) {
	s := weverse.Subscription{}
	e := weverse.Event{
		Kind:   "post",
		PostID: "4-241976860",
		Author: "CARMEN",
		Body:   "SPOTIFY CLOSER",
		Images: []string{"https://img/1.jpg"},
		Videos: []weverse.VideoAttachment{{
			ID:       "vid-1",
			URL:      "https://video/1.mp4",
			CoverURL: "https://img/cover.jpg",
		}},
	}
	doc := weversePostDocument(s, e)
	if doc == nil {
		t.Fatal("帖子应产出 Document")
	}
	var video *message.Media
	for i := range doc.Media {
		if doc.Media[i].Kind == "video" {
			video = &doc.Media[i]
		}
	}
	if video == nil {
		t.Fatalf("帖子应带上视频，实际 Media=%+v", doc.Media)
	}
	if video.Source != "https://video/1.mp4" || video.Cover != "https://img/cover.jpg" {
		t.Errorf("视频地址/封面不对：%+v", *video)
	}
}

// TestWeversePostDocumentVideoCoverOnly 无直链时至少把封面作为图片发出去，
// 避免「正文配图都发了，唯独视频什么都没发」。
func TestWeversePostDocumentVideoCoverOnly(t *testing.T) {
	s := weverse.Subscription{}
	e := weverse.Event{
		Kind:   "post",
		PostID: "4-1",
		Author: "CARMEN",
		Videos: []weverse.VideoAttachment{{ID: "vid-1", CoverURL: "https://img/cover.jpg"}},
	}
	doc := weversePostDocument(s, e)
	if doc == nil {
		t.Fatal("帖子应产出 Document")
	}
	if len(doc.Media) != 1 || doc.Media[0].Kind != "image" || doc.Media[0].Source != "https://img/cover.jpg" {
		t.Errorf("无直链时应回退为封面图片，实际 %+v", doc.Media)
	}
}

// TestWeverseReplyUnderFanPostKeepsQuote 粉丝帖下的回复不该被当成成员主帖挂载。
func TestWeverseReplyUnderFanPostKeepsQuote(t *testing.T) {
	e := weverse.Event{
		Kind:         "comment",
		CommentID:    "2-1",
		PostID:       "4-2",
		Author:       "JIWOO",
		Body:         "谢谢",
		ParentBody:   "生日快乐",
		ParentAuthor: "粉丝昵称",
		PostContext: &weverse.AIPostContext{
			PostID:         "4-2",
			Author:         "粉丝昵称",
			AuthorIsArtist: false,
		},
	}
	if isWeversePostAuthoredByMember(e) {
		t.Fatal("粉丝帖不应判定为成员主帖")
	}
}

// TestWeverseNestedReplyStillThreadsToComment 有 parentCommentId 时仍挂评论。
func TestWeverseNestedReplyStillThreadsToComment(t *testing.T) {
	e := weverse.Event{
		Kind:              "comment",
		CommentID:         "3-1",
		ParentCommentID:   "2-389849916",
		PostID:            "4-241974648",
		Author:            "ELLA",
		Body:              "同感",
		ParentBody:        "限定版",
		ParentAuthor:      "JIWOO",
		ParentProfileType: "ARTIST",
		PostContext: &weverse.AIPostContext{
			PostID:         "4-241974648",
			Author:         "JIWOO",
			AuthorIsArtist: true,
		},
	}
	doc := weverseReplyDocument(zzSub(), e)
	if doc == nil {
		t.Fatal("不应返回 nil")
	}
	if doc.ReplyToSourceID != "2-389849916" {
		t.Errorf("应挂到父评论，实际 %q", doc.ReplyToSourceID)
	}
}

// TestWeversePostDocumentCarriesSourceID 帖子必须带 SourceID 才能作为挂载目标。
func TestWeversePostDocumentCarriesSourceID(t *testing.T) {
	s := weverse.Subscription{}
	e := weverse.Event{
		Kind:   "post",
		PostID: "4-241974648",
		Author: "JIWOO",
		Body:   "Spotify CLOSER!!!",
		Time:   1790943470766,
		Images: []string{"https://img/1.jpg"},
	}
	doc := weversePostDocument(s, e)
	if doc == nil {
		t.Fatal("帖子应产出 Document")
	}
	if doc.SourceID != "4-241974648" {
		t.Errorf("帖子 SourceID 应为 postId，实际 %q", doc.SourceID)
	}
	if doc.Source != "Weverse" || doc.Author != "JIWOO" {
		t.Errorf("来源/作者不对：%q %q", doc.Source, doc.Author)
	}
	if len(doc.Media) != 1 {
		t.Errorf("图片应挂到 Media，实际 %d", len(doc.Media))
	}
}

// TestWeversePlainCommentNoThread 无父上下文的顶层评论不应挂载。
func TestWeversePlainCommentNoThread(t *testing.T) {
	e := weverse.Event{Kind: "comment", CommentID: "2-9", PostID: "4-3", Author: "JIWOO", Body: "独立评论"}
	if doc := weverseReplyDocument(zzSub(), e); doc != nil {
		t.Errorf("无父上下文的独立评论不应走回复文档，实际 %+v", doc)
	}
}
