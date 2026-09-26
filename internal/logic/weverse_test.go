package logic

import (
	"fmt"
	"pocket48-bot/internal/melon"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestFormatWeverseOriginalAndChinese(t *testing.T) {
	e := weverse.Event{Kind: "comment", Author: "CARMEN", ParentAuthor: "粉丝昵称", Body: "안녕", Translation: "你好", ParentBody: "잘 지내?", ParentTranslation: "最近好吗？", URL: "https://weverse.io/hearts2hearts/fanpost/1/comment/2"}
	got := formatWeverseEvent(e)
	want := "【CARMEN|Weverse】\n粉丝昵称：最近好吗？\nCARMEN：你好\n\n粉丝昵称（原文）：잘 지내?\nCARMEN（原文）：안녕\n\n" + e.URL
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	e.ParentTranslation = ""
	got = formatWeverseEvent(e)
	if !strings.Contains(got, "粉丝昵称：（译文暂不可用，原文见下）") || !strings.Contains(got, "粉丝昵称（原文）：잘 지내?") {
		t.Fatal(got)
	}
	e.Translation = ""
	e.TranslationError = "failed"
	if !strings.Contains(formatWeverseEvent(e), "翻译暂不可用，已保留原文") {
		t.Fatal("missing fallback")
	}
	e.Kind = "live"
	if !strings.Contains(formatWeverseEvent(e), "【CARMEN|Weverse】\n已开播") {
		t.Fatal("missing live notice")
	}
}

func TestWeverseReplyShowsDifferentPostAuthor(t *testing.T) {
	e := weverse.Event{
		Kind: "comment", MemberID: "carmen", Author: "CARMEN", Body: "좋아요",
		ParentAuthor: "粉丝昵称", ParentBody: "예쁘다", PostContext: &weverse.AIPostContext{MemberID: "stella", Author: "STELLA", AuthorIsArtist: true},
	}
	got := formatWeverseEvent(e)
	if !strings.HasPrefix(got, "【CARMEN（STELLA）|Weverse】\n粉丝昵称：예쁘다") {
		t.Fatal(got)
	}

	e.PostContext = &weverse.AIPostContext{MemberID: "carmen", Author: "CARMEN", AuthorIsArtist: true}
	if !strings.HasPrefix(formatWeverseEvent(e), "【CARMEN|Weverse】\n") {
		t.Fatal("成员自己帖子下的回复不应额外标注")
	}

	e.PostContext = &weverse.AIPostContext{Author: "STELLA"}
	if !strings.HasPrefix(formatWeverseEvent(e), "【CARMEN|Weverse】\n") {
		t.Fatal("主帖成员 ID 缺失时不应猜测归属")
	}

	e.PostContext = &weverse.AIPostContext{MemberID: "fan", Author: "粉丝昵称"}
	if !strings.HasPrefix(formatWeverseEvent(e), "【CARMEN|Weverse】\n") {
		t.Fatal("粉丝帖下的成员回复不应把粉丝名放进顶栏", formatWeverseEvent(e))
	}
}

func TestWeverseLiveEndNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_end", Author: "JIWOO", Body: "방송", Translation: "直播标题", LiveDuration: 2076, Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【JIWOO|Weverse】", "直播已结束", "直播时长：0小时34分36秒", "JIWOO：直播标题", "JIWOO（原文）：방송", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
	if !strings.HasSuffix(got, "\n\n1970-01-01 08:01:40") || strings.Contains(got, "时间：") || strings.Contains(got, "链接：") {
		t.Fatal("footer labels or timestamp spacing incorrect", got)
	}
}

func TestWeverseLiveChatNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_chat", Author: "YUHA", Body: "웃겨 정말", Translation: "真好笑。", LiveHostAuthor: "JUUN", Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【YUHA|Weverse】", "来自 JUUN 的直播", "YUHA：真好笑。", "YUHA（原文）：웃겨 정말", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
}

func TestWeverseOnlyStellaMentionsAll(t *testing.T) {
	sub := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}, AtAllMemberNames: []string{"STELLA"}, Enabled: true, CommunityID: 235, Posts: true, Comments: true, Live: true}
	for _, kind := range []string{"post", "comment", "live", "live_end", "live_replay", "live_chat"} {
		for _, member := range []string{"stella", "carmen"} {
			e := weverse.Event{Kind: kind, MemberID: member, CommunityID: 235, Images: []string{"https://img.example/a.jpg"}}
			if !weverse.Matches(sub, e) {
				t.Fatal("mention restriction filtered subscription", kind, member)
			}
			segments := weverseMessageSegments(sub, e)
			mentions, images := 0, 0
			for _, v := range segments {
				m := v.(napcat.MessageSegment)
				if m.Type == "at" {
					mentions++
				}
				if m.Type == "image" {
					images++
				}
			}
			if images != 1 || (member == "stella" && mentions != 1) || (member != "stella" && mentions != 0) {
				t.Fatal(kind, member, segments)
			}
		}
	}
	sub.AtAll = false
	if sub.MentionsAll("stella") {
		t.Fatal("disabled mentions still enabled")
	}
	sub.AtAll = true
	sub.AtAllMemberIDs = nil
	if !sub.MentionsAll("carmen") {
		t.Fatal("legacy all-member mentions changed")
	}
}
func TestWeverseReplayNotification(t *testing.T) {
	e := weverse.Event{Kind: "live_replay", Author: "STELLA", Body: "방송", Translation: "直播标题", Time: 100000, URL: "https://weverse.io/hearts2hearts/live/1-2"}
	got := formatWeverseEvent(e)
	for _, want := range []string{"【STELLA|Weverse】", "直播回放已生成", "STELLA：直播标题", e.URL} {
		if !strings.Contains(got, want) {
			t.Fatal(want, got)
		}
	}
}

func TestWeverseMediaBeforeFooter(t *testing.T) {
	for _, images := range [][]string{nil, {"https://img.example/a.jpg"}, {"https://img.example/a.jpg", "https://img.example/b.jpg"}} {
		e := weverse.Event{Kind: "post", Author: "STELLA", Body: "안녕", Translation: "你好", URL: "https://weverse.io/hearts2hearts/artist/1-2", Time: 100000, Images: images}
		segments := weverseMessageSegments(weverse.Subscription{}, e)
		if len(segments) != len(images)+2 {
			t.Fatal(segments)
		}
		body := segments[0].(napcat.MessageSegment).Data["text"]
		if strings.Contains(body, e.URL) || strings.Contains(body, "────────") || !strings.Contains(body, "STELLA（原文）：안녕") {
			t.Fatal(body)
		}
		for i, image := range images {
			m := segments[i+1].(napcat.MessageSegment)
			if m.Type != "image" || m.Data["file"] != image {
				t.Fatal("media reordered", segments)
			}
		}
		footer := segments[len(segments)-1].(napcat.MessageSegment).Data["text"]
		if footer != "\n\n"+e.URL+"\n\n1970-01-01 08:01:40" {
			t.Fatal("ambiguous QQ link/date boundary", footer)
		}
	}
}

func TestAIChunksNeverMentionAll(t *testing.T) {
	s := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}}
	job := &weverse.AIBatch{MemberID: "ian", Author: "IAN", Entries: []weverse.AIEntry{{Body: "reply"}}, Result: strings.Repeat("中文", 2000)}
	chunks := weverseAISegments(s, job)
	if len(chunks) < 2 {
		t.Fatal("long AI message not split")
	}
	for _, chunk := range chunks {
		for _, segment := range chunk {
			if segment.(napcat.MessageSegment).Type == "at" {
				t.Fatal("IAN mentioned all")
			}
		}
	}
	job.MemberID = "stella"
	chunks = weverseAISegments(s, job)
	for _, chunk := range chunks {
		for _, segment := range chunk {
			if segment.(napcat.MessageSegment).Type == "at" {
				t.Fatal("STELLA AI summary mentioned all")
			}
		}
	}
	job.MemberID = "ian"
	job.MemberIDs = []string{"ian", "stella"}
	for _, chunk := range weverseAISegments(s, job) {
		for _, segment := range chunk {
			if segment.(napcat.MessageSegment).Type == "at" {
				t.Fatal("merged Stella AI summary mentioned all")
			}
		}
	}
	job.MemberIDs = nil
	cfg := weverse.Settings{Enabled: true, Subscriptions: []weverse.Subscription{{ID: "s", GroupID: 1, CommunityID: 235, Enabled: true, Comments: true}}}
	job.SubscriptionID = "s"
	job.GroupID = 1
	job.CommunityID = 235
	if _, ok := aiSubscription(cfg, job); !ok {
		t.Fatal("valid subscription rejected")
	}
	cfg.Subscriptions[0].Comments = false
	if _, ok := aiSubscription(cfg, job); ok {
		t.Fatal("disabled replies still forwarded")
	}
}

func TestMelonMusicWaveMessagePreservesDialogueOrderAndFooter(t *testing.T) {
	sub := melon.Subscription{ArtistName: "Hearts2Hearts (하츠투하츠)", AtAll: true, AtAllAuthorNames: []string{"STELLA"}}
	events := []melon.Event{
		{Author: "유하 (YUHA)", Body: "첫 번째", URL: melon.Hearts2HeartsMusicWaveURL, Time: 100000},
		{Author: "스텔라 (STELLA)", Body: "두 번째", URL: melon.Hearts2HeartsMusicWaveURL, Time: 101000},
		{Author: "유하 (YUHA)", Body: "세 번째", URL: melon.Hearts2HeartsMusicWaveURL, Time: 102000},
	}
	messages := melonMusicWaveMessages(sub, events)
	if len(messages) != 1 || len(messages[0]) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	if messages[0][0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("missing at-all")
	}
	body := messages[0][2].(napcat.MessageSegment).Data["text"]
	first, second, third := strings.Index(body, "YUHA)：첫 번째"), strings.Index(body, "STELLA)：두 번째"), strings.Index(body, "YUHA)：세 번째")
	if !(first >= 0 && first < second && second < third) || strings.Contains(body, "试推") || strings.Contains(body, "来源：") {
		t.Fatal(body)
	}
	if !strings.HasSuffix(body, melon.Hearts2HeartsMusicWaveURL+"\n\n1970-01-01 08:01:42") {
		t.Fatal("footer not last", body)
	}
	withoutStella := melonMusicWaveMessages(sub, []melon.Event{{Author: "유하 (YUHA)", Body: "안녕", Time: 103000}})
	if len(withoutStella) != 1 || withoutStella[0][0].(napcat.MessageSegment).Type == "at" {
		t.Fatal("non-STELLA message mentioned all", withoutStella)
	}
}

func TestMelonMusicWaveMessageLimitsEachCardForQQTranslation(t *testing.T) {
	sub := melon.Subscription{ArtistName: "Hearts2Hearts", AtAll: true, AtAllAuthorNames: []string{"STELLA"}}
	events := make([]melon.Event, 0, 9)
	for i := 0; i < 9; i++ {
		author := "유하 (YUHA)"
		if i == 8 {
			author = "스텔라 (STELLA)"
		}
		events = append(events, melon.Event{Author: author, Body: fmt.Sprintf("message-%d", i+1), URL: melon.Hearts2HeartsMusicWaveURL, Time: int64(100000 + i)})
	}
	messages := melonMusicWaveMessages(sub, events)
	if len(messages) != 2 {
		t.Fatalf("expected two short cards, got %d", len(messages))
	}
	if messages[0][0].(napcat.MessageSegment).Type == "at" || messages[1][0].(napcat.MessageSegment).Type != "at" {
		t.Fatal("STELLA mention was not scoped to her card", messages)
	}
}

func TestAIPostSummaryFooterAndContextDoNotMentionQuotedStella(t *testing.T) {
	s := weverse.Subscription{AtAll: true, AtAllMemberIDs: []string{"stella"}}
	job := &weverse.AIBatch{MemberID: "ian", Author: "IAN", PostContext: &weverse.AIPostContext{Author: "STELLA", MemberID: "stella", URL: "https://weverse.io/hearts2hearts/artist/p"}, Entries: []weverse.AIEntry{{Time: 100000}}, Result: "中文对话\n\n这段在聊什么：\n总结"}
	chunks := weverseAISegments(s, job)
	var text strings.Builder
	for _, chunk := range chunks {
		for _, v := range chunk {
			segment := v.(napcat.MessageSegment)
			if segment.Type == "at" {
				t.Fatal("quoted root author caused mention")
			}
			text.WriteString(segment.Data["text"])
		}
	}
	if !strings.HasSuffix(text.String(), "https://weverse.io/hearts2hearts/artist/p\n\n1970-01-01 08:01:40") {
		t.Fatal("ambiguous post footer", text.String())
	}
}

func TestWeverseVideoCardAndStandaloneVideoRespectQQConstraint(t *testing.T) {
	e := weverse.Event{Kind: "post", MemberID: "yeon", Author: "YE-ON", Body: "☘️💌🩷", URL: "https://weverse.io/hearts2hearts/artist/1-180305534", Time: 1789312206000, Videos: []weverse.VideoAttachment{{ID: "3-3005881", CoverURL: "https://example.com/cover.jpg", URL: "https://example.com/video.mp4"}}}
	messages := weverseMessageGroups(weverse.Subscription{}, e)
	if len(messages) != 2 || len(messages[1]) != 1 {
		t.Fatal("video not standalone", messages)
	}
	texts := ""
	cover := false
	for _, raw := range messages[0] {
		segment := raw.(napcat.MessageSegment)
		if segment.Type == "video" {
			t.Fatal("QQ card contains video")
		}
		if segment.Type == "image" {
			cover = true
		}
		if segment.Type == "text" {
			texts += segment.Data["text"]
		}
	}
	if !cover || !strings.Contains(texts, "[视频]") || !strings.Contains(texts, e.URL) {
		t.Fatal("card lost cover, placeholder or URL", texts)
	}
	if messages[1][0].(napcat.MessageSegment).Type != "video" {
		t.Fatal("standalone message is not video")
	}
}
