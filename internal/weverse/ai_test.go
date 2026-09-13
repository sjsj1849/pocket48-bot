package weverse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAIConversationLifecycle(t *testing.T) {
	dir := t.TempDir()
	ai := AISettings{Enabled: true, BaseURL: "https://example.com/v1", Model: "test", IdleSeconds: 300, APIKey: "secret"}
	if err := SaveAISettings(dir, ai); err != nil {
		t.Fatal(err)
	}
	sub := Subscription{ID: "s", GroupID: 1, CommunityID: 235, Comments: true, Enabled: true}
	cfg := Settings{Enabled: true, Subscriptions: []Subscription{sub}}
	now := time.Now().Add(-6 * time.Minute)
	for i, id := range []string{"one", "two", "two"} {
		e := Event{ID: id, PostID: "first", Kind: "comment", MemberID: "member", CommunityID: 235, Author: "IAN", Body: "reply", ParentBody: "context", ParentAuthor: "fan", Time: now.Add(time.Duration(i) * time.Second).UnixMilli()}
		if err := CollectAI(dir, sub, e, now); err != nil {
			t.Fatal(err)
		}
	}
	_ = CollectAI(dir, sub, Event{ID: "other", PostID: "other", Kind: "comment", MemberID: "other", CommunityID: 235, Time: now.UnixMilli()}, now.Add(4*time.Minute))
	if job, err := ClaimAIJob(dir, cfg, ai, now.Add(6*time.Minute), false); err != nil || job != nil {
		t.Fatal("outage incorrectly closed conversation", err)
	}
	job, err := ClaimAIJob(dir, cfg, ai, now.Add(6*time.Minute), true)
	if err != nil || job == nil || len(job.Entries) != 2 || job.Entries[0].ParentBody != "context" {
		t.Fatalf("bad conversation: %+v %v", job, err)
	}
	if err := SaveAIResult(dir, job.ID, "summary"); err != nil {
		t.Fatal(err)
	}
	if err := FinishAIJob(dir, job.ID, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	if retry, _ := ClaimAIJob(dir, cfg, ai, now.Add(6*time.Minute+time.Second), true); retry != nil {
		t.Fatal("retried too early")
	}
	retry, err := ClaimAIJob(dir, cfg, ai, now.Add(7*time.Minute), true)
	if err != nil || retry == nil || retry.ID != job.ID || retry.Result != "summary" {
		t.Fatal("restart lost pending result", err)
	}
	cfg.Subscriptions[0].GroupID = 2
	if retry, _ := ClaimAIJob(dir, cfg, ai, now.Add(20*time.Minute), true); retry != nil {
		t.Fatal("sent to removed destination")
	}
	active, pending, _, _ := AIStatus(dir)
	if active != 0 || pending != 0 {
		t.Fatal("removed subscription retained", active, pending)
	}
}

func TestAIUsesWholeOriginalConversationAndSanitizesErrors(t *testing.T) {
	code := 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer confidential" {
			t.Error("invalid request")
		}
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "grok-4.5" || len(payload.Messages) != 2 || !strings.Contains(payload.Messages[1].Content, "parent-original") || !strings.Contains(payload.Messages[1].Content, "second-reply") || !strings.Contains(payload.Messages[1].Content, "artist") || !strings.Contains(payload.Messages[1].Content, "完整通顺") || !strings.Contains(payload.Messages[1].Content, "root-original") || !strings.Contains(payload.Messages[1].Content, `"imageCount":5`) {
			t.Error("missing context")
		}
		w.WriteHeader(code)
		if code == 200 {
			content, _ := json.Marshal(map[string]any{"postChinese": "头发漂亮吧😎", "translations": []map[string]string{{"id": "r1", "parentChinese": "上下文", "replyChinese": "第一条"}, {"id": "r2", "parentChinese": "", "replyChinese": "第二条"}}, "summary": "完整上下文翻译"})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}, "finish_reason": "stop"}}})
		} else {
			w.Write([]byte("confidential"))
		}
	}))
	defer server.Close()
	cfg := AISettings{BaseURL: server.URL + "/v1", Model: "grok-4.5", APIKey: "confidential"}
	b := AIBatch{Author: "IAN", PostContext: &AIPostContext{PostID: "p", Author: "IAN", MemberID: "artist", Body: "root-original", ImageCount: 5}, Entries: []AIEntry{{ID: "one", Author: "IAN", ParentAuthor: "粉丝", ParentBody: "parent-original", Body: "first-reply"}, {ID: "two", Author: "IAN", Body: "second-reply"}}}
	result, err := SummarizeAI(context.Background(), cfg, b)
	if err != nil || !strings.Contains(result, "IAN：第一条\nIAN：第二条\n\n这段在聊什么：\n完整上下文翻译") {
		t.Fatal(result, err)
	}
	code = 401
	_, err = SummarizeAI(context.Background(), cfg, b)
	if err == nil || strings.Contains(err.Error(), "confidential") {
		t.Fatal("unsafe API error", err)
	}
}

func TestAIStreamRejectsPartialOutput(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"complete", "data: {\"choices\":[{\"delta\":{\"content\":\"上下文\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"翻译\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n", true},
		{"disconnected", "data: {\"choices\":[{\"delta\":{\"content\":\"部分翻译\"}}]}\n", false},
		{"truncated", "data: {\"choices\":[{\"delta\":{\"content\":\"部分翻译\"},\"finish_reason\":\"length\"}]}\n", false},
		{"error", "data: {\"error\":{\"message\":\"private-key\"}}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := readAIStream(strings.NewReader(tc.body))
			if tc.valid {
				if err != nil || result != "上下文翻译" {
					t.Fatal(result, err)
				}
			} else if err == nil || result != "" || strings.Contains(err.Error(), "private-key") {
				t.Fatal("partial or unsafe output", result, err)
			}
		})
	}
}

func TestAIRetryUsesServerDelay(t *testing.T) {
	dir := t.TempDir()
	b := &AIBatch{ID: "job", Attempts: 1}
	if err := Write(dir, "ai-state.json", AIState{Jobs: []*AIBatch{b}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := FinishAIJob(dir, b.ID, &AIRequestError{StatusCode: 429, RetryAfter: 5 * time.Minute}); err != nil {
		t.Fatal(err)
	}
	var state AIState
	_ = Read(dir, "ai-state.json", &state)
	if state.Jobs[0].RetryAfter < start.Add(5*time.Minute).UnixMilli() || !strings.Contains(state.Error, "429") {
		t.Fatal("rate limit delay ignored")
	}
	if aiRetryAfter("120") != 2*time.Minute || aiRetryAfter("invalid") != 0 {
		t.Fatal("invalid retry delay")
	}
}

func TestAIGroupsAllArtistRepliesByPostAndWaitsForActors(t *testing.T) {
	dir := t.TempDir()
	ai := AISettings{Enabled: true, BaseURL: "https://example.com/v1", Model: "test", IdleSeconds: 300, APIKey: "key"}
	_ = SaveAISettings(dir, ai)
	sub := Subscription{ID: "sub", GroupID: 1, CommunityID: 235, Enabled: true, Comments: true}
	cfg := Settings{Enabled: true, Subscriptions: []Subscription{sub}}
	start := time.Now()
	collect := func(id, member, parentType, parentMember, post string, minute int) {
		t.Helper()
		e := Event{ID: id, MemberID: member, Author: member, Kind: "comment", CommunityID: 235, ParentAuthor: parentMember, ParentMemberID: parentMember, ParentProfileType: parentType, PostID: post, Body: "reply", ParentBody: "context", Time: start.Add(time.Duration(minute) * time.Minute).UnixMilli()}
		if err := CollectAI(dir, sub, e, start.Add(time.Duration(minute)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	collect("a-fan", "a", "FAN", "fan-a", "p", 0)
	collect("b-fan", "b", "FAN", "fan-b", "p", 0)
	collect("a-to-b", "a", "ARTIST", "b", "p", 1)
	collect("b-to-a", "b", "ARTIST", "a", "p", 2)
	collect("a-new-fan", "a", "FAN", "fan-c", "different-post", 4)
	var state AIState
	_ = Read(dir, "ai-state.json", &state)
	if len(state.Batches) != 2 {
		t.Fatal("incorrect post groups", len(state.Batches))
	}
	pair := state.Batches["sub/post:p"]
	if pair == nil || len(pair.Entries) != 4 || len(pair.ActorIDs()) != 2 || pair.Entries[1].AuthorMemberID != "b" {
		t.Fatal("same-post fans and artists not merged", pair)
	}
	if len(state.Batches["sub/post:different-post"].Entries) != 1 {
		t.Fatal("different posts merged")
	}
	if job, _ := ClaimAIJob(dir, cfg, ai, start.Add(7*time.Minute), true); job != nil {
		t.Fatal("member still replying on another post but summary sent")
	}
	job, err := ClaimAIJob(dir, cfg, ai, start.Add(9*time.Minute), true)
	if err != nil || job == nil {
		t.Fatal("idle post was not completed", err)
	}
	var queued AIState
	_ = Read(dir, "ai-state.json", &queued)
	if len(queued.Batches) != 0 || len(queued.Jobs) != 2 {
		t.Fatal("idle post summaries not queued", queued)
	}
	found := false
	for _, pending := range queued.Jobs {
		if pending.PostID == "p" {
			found = true
			if len(pending.Entries) != 4 {
				t.Fatal("lost fan/member replies")
			}
		}
	}
	if !found {
		t.Fatal("missing original post")
	}
	sub.MemberIDs = []string{"a"}
	if pair.MatchesSubscription(sub) {
		t.Fatal("changed member selection retained unselected actor")
	}
}

func TestAITranslationFormattingKeepsEveryReplyBeforeSummary(t *testing.T) {
	entries := []AIEntry{{ID: "one", Author: "IAN", ParentAuthor: "粉丝", ParentBody: "팬", Body: "안녕", PostID: "post", ParentCommentID: "parent"}, {ID: "two", Author: "STELLA", ParentAuthor: "粉丝", ParentBody: "팬", Body: "좋아", PostID: "post", ParentCommentID: "parent"}}
	raw := `{"translations":[{"id":"two","parentChinese":"粉丝的话","replyChinese":"好呀"},{"id":"one","parentChinese":"粉丝的话","replyChinese":"你好"}],"summary":"两位成员互动。"}`
	result, err := formatAISummary(raw, entries)
	if err != nil || result != "粉丝：粉丝的话\nIAN：你好\nSTELLA：好呀\n\n这段在聊什么：\n两位成员互动。" {
		t.Fatal(result, err)
	}
	captioned := strings.Replace(raw, "粉丝的话", "粉丝：粉丝的话", -1)
	captioned = strings.Replace(captioned, "你好", "IAN：你好", 1)
	if result, err := formatAISummary(captioned, entries); err != nil || strings.Contains(result, "粉丝：粉丝：") || strings.Contains(result, "IAN：IAN：") {
		t.Fatal("duplicated speaker", result, err)
	}
	for _, bad := range []string{`{"translations":[{"id":"one","replyChinese":"你好"}],"summary":"missing"}`, strings.Replace(raw, "你好", "안녕", 1), strings.Replace(raw, "你好", "《안녕》", 1), strings.Replace(raw, `"two"`, `"other"`, 1)} {
		if result, err := formatAISummary(bad, entries); err == nil || result != "" {
			t.Fatal("incomplete translation accepted", result)
		}
	}
}

func TestAIPostMigrationKeepsFansMembersAndDifferentPosts(t *testing.T) {
	entry := func(id, post, artist, parentType string, when int64) AIEntry {
		return AIEntry{ID: id, PostID: post, Author: artist, AuthorMemberID: artist, ParentAuthor: "target", ParentProfileType: parentType, Time: when}
	}
	state := AIState{Batches: map[string]*AIBatch{
		"sub/a":        {SubscriptionID: "sub", GroupID: 1, CommunityID: 235, MemberID: "a", LastReceived: 10, Entries: []AIEntry{entry("one", "p", "a", "FAN", 1), entry("other", "q", "a", "FAN", 3)}},
		"sub/thread:p": {SubscriptionID: "sub", GroupID: 1, CommunityID: 235, MemberID: "b", LastReceived: 20, Entries: []AIEntry{entry("two", "p", "b", "ARTIST", 2)}},
	}, Jobs: []*AIBatch{{ID: "legacy-job", SubscriptionID: "sub", GroupID: 1, CommunityID: 235, MemberID: "a", LastReceived: 30, Result: "obsolete member-only summary", Entries: []AIEntry{entry("one", "p", "a", "FAN", 1), entry("three", "p", "a", "FAN", 4)}}}}
	migrateAIPostGroups(&state)
	if len(state.Jobs) != 0 || len(state.Batches) != 2 {
		t.Fatal("legacy groups not migrated")
	}
	p := state.Batches["sub/post:p"]
	if p == nil || len(p.Entries) != 3 || p.Result != "" || p.LastReceived != 30 || len(p.ActorIDs()) != 2 {
		t.Fatal("migration lost or replayed content", p)
	}
	if p.Entries[0].ID != "one" || p.Entries[1].ID != "two" || p.Entries[2].ID != "three" {
		t.Fatal("wrong chronological order")
	}
	migrateAIPostGroups(&state)
	if len(p.Entries) != 3 {
		t.Fatal("migration repeated entries")
	}
}

func TestAISummaryIncludesRootAndRejectsMissingRootTranslation(t *testing.T) {
	post := &AIPostContext{PostID: "p", Author: "IAN", MemberID: "ian", Body: "머리이뿌죠😎", ImageCount: 5}
	entries := []AIEntry{{ID: "one", PostID: "p", Author: "IAN", AuthorMemberID: "ian", Body: "요즘 좀 데뷔 초 같지", ParentBody: "데뷔 때의 이안이랑 좀 비슷하네~ 정말 그립다.", ParentAuthor: "粉丝", ParentProfileType: "FAN"}}
	raw := `{"postChinese":"头发漂亮吧😎","translations":[{"id":"one","parentChinese":"这组照片里的伊安有点像出道时呢～真的好怀念。","replyChinese":"最近有点出道初期的感觉，对吧？"}],"summary":"粉丝说照片让人想起伊安刚出道时，伊安也有相同感受。"}`
	result, err := formatAISummary(raw, entries, post)
	if err != nil || !strings.HasPrefix(result, "IAN（主帖）：头发漂亮吧😎\n（主帖含5 张图片，未分析画面）") || !strings.Contains(result, "粉丝：这组照片里的伊安") {
		t.Fatal(result, err)
	}
	for _, bad := range []string{strings.Replace(raw, "头发漂亮吧😎", "", 1), strings.Replace(raw, "头发漂亮吧😎", post.Body, 1)} {
		if result, err := formatAISummary(bad, entries, post); err == nil || result != "" {
			t.Fatal("invalid root translation accepted")
		}
	}
}

func TestAISummaryReviewsDraftAgainstOriginalBeforeSending(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var p struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Fatal(err)
		}
		translation := "头发有点乱"
		if requests == 2 {
			if !strings.Contains(p.Messages[1].Content, "历史原文") || !strings.Contains(p.Messages[1].Content, "头发有点乱") || !strings.Contains(p.Messages[1].Content, "머리이뿌죠") || !strings.Contains(p.Messages[1].Content, "contextNotes") {
				t.Error("review omitted original or draft")
			}
			translation = "头发漂亮吧"
		}
		content, _ := json.Marshal(map[string]any{"postChinese": translation, "translations": []map[string]string{{"id": "r1", "replyChinese": "历史对话"}, {"id": "r2", "replyChinese": "好呀"}}, "summary": "成员分享自己的发型，回应粉丝。"})
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": string(content)}, "finish_reason": "stop"}}})
	}))
	defer server.Close()
	batch := AIBatch{History: []AIEntry{{ID: "historic", PostID: "p", Author: "IAN", Body: "历史原文"}}, PostID: "p", CommunityID: 235, PostContext: &AIPostContext{PostID: "p", Author: "IAN", Body: "머리이뿌죠"}, Entries: []AIEntry{{ID: "one", PostID: "p", Author: "IAN", Body: "ㄱㄱ"}}}
	result, err := SummarizeAI(context.Background(), AISettings{BaseURL: server.URL, Model: "test"}, batch)
	if err != nil || requests != 2 || strings.Contains(result, "有点乱") || !strings.Contains(result, "头发漂亮吧") || !strings.Contains(result, "IAN：历史对话") {
		t.Fatal("unreviewed draft sent", result, err, requests)
	}
	batch.Entries[0].PostID = "different"
	if _, err := SummarizeAI(context.Background(), AISettings{BaseURL: server.URL}, batch); err == nil || requests != 2 {
		t.Fatal("mixed posts sent to AI")
	}
}

func TestAIRejectsKnownWrongInterjectionAndPartialKorean(t *testing.T) {
	entry := AIEntry{ID: "one", Author: "CARMEN", Body: "어머핑~~~🥹"}
	for _, body := range []string{"妈妈~~~🥹", "哎呀 어머핑~~~🥹"} {
		raw := fmt.Sprintf(`{"translations":[{"id":"one","replyChinese":%q}],"summary":"成员感叹。"}`, body)
		if result, e := formatAISummary(raw, []AIEntry{entry}); e == nil || result != "" {
			t.Fatal("known mistranslation sent", result)
		}
	}
	raw := `{"translations":[{"id":"one","replyChinese":"哎呀~~~🥹"}],"summary":"成员表达惊喜。"}`
	if _, e := formatAISummary(raw, []AIEntry{entry}); e != nil {
		t.Fatal(e)
	}
}
