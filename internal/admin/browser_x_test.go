package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestBrowserXSyncPrivateSessionAndPublicStatus(t *testing.T) {
	var requested string
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]string
		if conn.ReadJSON(&request) != nil {
			return
		}
		requested = request["cmd"]
		_ = conn.WriteJSON(map[string]any{"type": "unrelated"})
		_ = conn.WriteJSON(map[string]any{"type": "x_panel_result", "requestId": request["requestId"], "session": xBrowserSession{Cookies: []xBrowserCookie{{Name: "auth_token", Value: "private-auth", Domain: ".x.com", Expires: -1}, {Name: "ct0", Value: "private-csrf", Domain: ".x.com", Expires: -1}}}})
	}))
	defer sidecar.Close()
	endpoint, _ := url.Parse(sidecar.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	dir := t.TempDir()
	if err := weverse.Write(filepath.Join(dir, "storage"), "browser-sidecar.json", map[string]int{"port": port}); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: filepath.Join(dir, "config.json")}}
	rr := httptest.NewRecorder()
	s.handleBrowserX(rr, httptest.NewRequest(http.MethodPost, "/api/browser/x", strings.NewReader(`{"action":"sync"}`)))
	if rr.Code != http.StatusOK || requested != "x_panel_sync" {
		t.Fatalf("sync: %d %s %s", rr.Code, requested, rr.Body.String())
	}
	path := filepath.Join(dir, "storage", "x", "session.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private session permissions: %v", err)
	}
	var saved xBrowserSession
	if err := weverse.Read(filepath.Dir(path), "session.json", &saved); err != nil || !validXBrowserSession(saved) || saved.UpdatedAt == 0 {
		t.Fatalf("invalid saved session: %v", err)
	}
	rr = httptest.NewRecorder()
	s.handleBrowserX(rr, httptest.NewRequest(http.MethodGet, "/api/browser/x", nil))
	var status map[string]any
	if json.Unmarshal(rr.Body.Bytes(), &status) != nil || status["sessionConfigured"] != true {
		t.Fatal(rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "private-") || strings.Contains(rr.Body.String(), "cookies") {
		t.Fatal("status leaked credentials")
	}
	rr = httptest.NewRecorder()
	s.handleBrowserX(rr, httptest.NewRequest(http.MethodPost, "/api/browser/x", strings.NewReader(`{"action":"shutdown"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatal("arbitrary sidecar command was accepted")
	}
}

func TestBrowserXRejectsInvalidSession(t *testing.T) {
	valid := xBrowserSession{Cookies: []xBrowserCookie{{Name: "auth_token", Value: "a", Domain: ".x.com", Expires: -1}, {Name: "ct0", Value: "b", Domain: ".x.com", Expires: -1}}}
	if !validXBrowserSession(valid) {
		t.Fatal("valid session rejected")
	}
	for _, bad := range []xBrowserCookie{
		{Name: "auth_token", Value: "a", Domain: "evilx.com", Expires: -1},
		{Name: "auth_token", Value: "a; injected=b", Domain: "x.com", Expires: -1},
		{Name: "auth_token", Value: "a", Domain: "x.com", Expires: 1},
		{Name: "ct0", Value: "a", Domain: "x.com", Expires: -1},
	} {
		candidate := xBrowserSession{Cookies: []xBrowserCookie{bad, valid.Cookies[1]}}
		if validXBrowserSession(candidate) {
			t.Fatal("unsafe or incomplete session accepted")
		}
	}
}
