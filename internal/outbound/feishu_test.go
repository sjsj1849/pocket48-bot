package outbound

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pocket48-bot/internal/message"
)

func TestFeishuSendsNativeCardAndUploadsImage(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/v3/tenant_access_token/internal"):
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"token","expire":7200}`)
		case strings.HasSuffix(r.URL.Path, "/im/v1/images"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("image_type") != "message" {
				t.Errorf("image_type = %q", r.FormValue("image_type"))
			}
			_, _ = io.WriteString(w, `{"code":0,"data":{"image_key":"img-key"}}`)
		case strings.HasSuffix(r.URL.Path, "/im/v1/messages"):
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			mu.Lock()
			sent = append(sent, payload)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	f := NewFeishu(FeishuOptions{AppID: "app", AppSecret: "secret"})
	f.apiBase = server.URL
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("image"))
	}))
	defer imageServer.Close()

	err := f.deliver(context.Background(), feishuDelivery{
		target: Target{Platform: "feishu", Kind: GroupChat, Address: "oc-chat"},
		content: []message.Segment{
			message.Text("【韩家乐|Pocket48】\n今天晚上见"),
			message.Image(imageServer.URL + "/photo.jpg"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0]["msg_type"] != "interactive" || sent[0]["receive_id"] != "oc-chat" {
		t.Fatalf("sent = %#v", sent)
	}
	if !strings.Contains(sent[0]["content"], "img-key") || !strings.Contains(sent[0]["content"], "韩家乐") || !strings.Contains(sent[0]["content"], "Pocket48") {
		t.Fatalf("card content = %s", sent[0]["content"])
	}
}

func TestDocumentHasPortableLinearFallback(t *testing.T) {
	doc := message.Document{
		Source: "Weverse", Author: "STELLA", Body: "晚上好",
		Quote: &message.Quote{Author: "粉丝", Text: "吃饭了吗"},
		Link:  "https://example.com", Media: []message.Media{{Kind: "image", Source: "photo.jpg"}},
	}
	segments, ok := message.ToSegments(doc).([]message.Segment)
	if !ok || len(segments) != 2 {
		t.Fatalf("segments = %#v", message.ToSegments(doc))
	}
	text := segments[0].Data["text"]
	if !strings.Contains(text, "粉丝：") || !strings.Contains(text, "吃饭了吗") || !strings.Contains(text, "晚上好") || segments[1].Type != "image" {
		t.Fatalf("segments = %#v", segments)
	}
}

// TestFeishuListChatsParsesDataEnvelope guards the "data" nesting in Feishu
// list responses. Reading items from the top level silently returns nothing.
func TestFeishuListChatsParsesDataEnvelope(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/auth/v3/tenant_access_token/internal") {
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-1","expire":7200}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"has_more":false,"items":[
			{"chat_id":"oc_1","name":"运营通知群","chat_mode":"group"},
			{"chat_id":"oc_2","name":"单聊","chat_mode":"p2p"}]}}`))
	}))
	defer server.Close()

	f := NewFeishuPanel(FeishuOptions{AppID: "cli_test", AppSecret: "secret"})
	f.apiBase = server.URL
	chats, err := f.ListChats(context.Background())
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats, got %#v", chats)
	}
	if chats[0].ChatID != "oc_1" || chats[0].Name != "运营通知群" || chats[0].Kind != "group" {
		t.Fatalf("unexpected first chat: %#v", chats[0])
	}
	if chats[1].Kind != "private" {
		t.Fatalf("p2p chat should map to private, got %q", chats[1].Kind)
	}
	if len(gotPaths) != 2 || !strings.HasSuffix(gotPaths[1], "/im/v1/chats") {
		t.Fatalf("unexpected requests: %v", gotPaths)
	}
}
