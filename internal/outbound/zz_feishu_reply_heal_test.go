package outbound

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 线上故障（2026-10-06）：Weverse 成员回复粉丝评论时，reply 锚点是一条**已失效**
// 的 message_id（换绑前的旧应用 id），飞书对 reply 返回 230002 "Bot/User can NOT be
// out of the chat"。老代码把这个错误直接抛给上层，于是整张卡片被降级成纯文本，
// 且**一条日志都不打** —— 表现就是「某条之后所有消息突然变成纯文本」。

// 换绑后旧 message_id 必须被丢弃：id 只对它签发它的应用有效。
func TestReplyMapDropsEntriesFromOtherApps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")
	old := `{"sourceId":"post:2-180661201","messageId":"om_old","appId":"cli_old"}` + "\n" +
		`{"sourceId":"post:legacy","messageId":"om_legacy"}` + "\n" +
		`{"sourceId":"post:1-180623152","messageId":"om_new","appId":"cli_new"}` + "\n"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewPersistentReplyMap(100, path, "cli_new")
	if _, ok := m.Lookup("post:2-180661201"); ok {
		t.Error("旧应用的映射必须丢弃，否则每次回复都会撞上同一个坏锚点")
	}
	if _, ok := m.Lookup("post:legacy"); ok {
		t.Error("没有 appId 的历史记录同样不可信（换绑前写入），应丢弃")
	}
	if got, ok := m.Lookup("post:1-180623152"); !ok || got != "om_new" {
		t.Errorf("当前应用的映射应保留，实际 %q %v", got, ok)
	}
	if m.Len() != 1 {
		t.Errorf("应只剩 1 条，实际 %d", m.Len())
	}
}

// 坏锚点一旦确认就要剪掉，否则同一线程的每条后续回复都会重复失败。
func TestReplyMapDeleteMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replymap.jsonl")
	m := NewPersistentReplyMap(100, path, "cli")
	m.Record("a", "om_dead")
	m.Record("b", "om_alive")
	m.Record("c", "om_dead") // 同一锚点被两个 source 引用

	if n := m.DeleteMessage("om_dead"); n != 2 {
		t.Fatalf("应删除 2 条，实际 %d", n)
	}
	if _, ok := m.Lookup("a"); ok {
		t.Error("a 仍指向被删锚点")
	}
	if _, ok := m.Lookup("c"); ok {
		t.Error("c 仍指向被删锚点")
	}
	if got, ok := m.Lookup("b"); !ok || got != "om_alive" {
		t.Errorf("无关映射不应受影响，实际 %q %v", got, ok)
	}
	if n := m.DeleteMessage("om_missing"); n != 0 {
		t.Errorf("删除不存在的锚点应返回 0，实际 %d", n)
	}

	// 持久化也要跟着更新，否则重启后坏锚点又回来了。
	reloaded := NewPersistentReplyMap(100, path, "cli")
	if _, ok := reloaded.Lookup("a"); ok {
		t.Error("重启后坏锚点不应复活")
	}
	if got, ok := reloaded.Lookup("b"); !ok || got != "om_alive" {
		t.Errorf("重启后存活映射应仍在，实际 %q %v", got, ok)
	}
}

// 核心回归：reply 失败时卡片必须**仍然以卡片形式送达**，只是不再挂载。
func TestSendCardFallsBackToTopLevelWhenAnchorDead(t *testing.T) {
	var mu sync.Mutex
	var replyHits, topHits int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "tenant_access_token"):
			io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-xxx","expire":7200}`)
		case strings.Contains(r.URL.Path, "/reply"):
			mu.Lock()
			replyHits++
			mu.Unlock()
			// 线上真实返回
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"code":230002,"msg":"Bot/User can NOT be out of the chat."}`)
		case strings.Contains(r.URL.Path, "/im/v1/messages"):
			mu.Lock()
			topHits++
			mu.Unlock()
			io.WriteString(w, `{"code":0,"msg":"success","data":{"message_id":"om_new_top"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"code":404}`)
		}
	}))
	defer server.Close()

	f := newFeishu(FeishuOptions{AppID: "cli", AppSecret: "secret"})
	f.apiBase = server.URL
	rm := NewReplyMap(10)
	rm.Record("post:2-180661201", "om_dead_anchor")
	f.replyMap = rm

	content := feishuContent{source: "Weverse", sender: "STELLA", text: "不对哈哈哈，我看着裤子不知不觉被卷起来了一边。"}
	id, err := f.sendCard(context.Background(), "oc_chat", "chat_id", content, nil, "", "om_dead_anchor")
	if err != nil {
		t.Fatalf("锚点失效不应让整张卡片失败：%v", err)
	}
	if id != "om_new_top" {
		t.Errorf("应回退为顶层发送并拿到新 message_id，实际 %q", id)
	}
	mu.Lock()
	defer mu.Unlock()
	if replyHits != 1 || topHits != 1 {
		t.Errorf("应先试 reply 一次、再顶层发送一次，实际 reply=%d top=%d", replyHits, topHits)
	}
	if _, ok := rm.Lookup("post:2-180661201"); ok {
		t.Error("坏锚点应被自动剪除，避免后续回复重复失败")
	}
}

// 兜底路径也必须留痕：卡片退化成纯文本曾是完全静默的。
func TestSendTextFallbackKeepsCardBody(t *testing.T) {
	var mu sync.Mutex
	var sent []map[string]string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "tenant_access_token") {
			io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var decoded struct {
			MsgType  string            `json:"msg_type"`
			Content  string            `json:"content"`
			Receive  map[string]string `json:"-"`
			rawExtra []string          `json:"-"`
		}
		_ = json.Unmarshal(body, &decoded)
		mu.Lock()
		sent = append(sent, map[string]string{"type": decoded.MsgType, "content": decoded.Content})
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":230002,"msg":"Bot/User can NOT be out of the chat."}`)
	}))
	defer server.Close()

	f := newFeishu(FeishuOptions{AppID: "cli", AppSecret: "secret"})
	f.apiBase = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := f.sendText(ctx, "oc_chat", "chat_id", "Weverse\nSTELLA：不对哈哈哈")
	if err == nil {
		t.Fatal("平台报错时应返回错误")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0]["type"] != "text" {
		t.Fatalf("应发出 text 消息，实际 %+v", sent)
	}
	if !strings.Contains(sent[0]["content"], "不对哈哈哈") {
		t.Errorf("正文丢失：%q", sent[0]["content"])
	}
}
