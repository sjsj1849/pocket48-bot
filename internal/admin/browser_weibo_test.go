package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestBrowserWeiboAction(t *testing.T) {
	commands := make(chan string, 1)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var cmd map[string]string
		if conn.ReadJSON(&cmd) != nil {
			return
		}
		commands <- cmd["cmd"]
		_ = conn.WriteJSON(map[string]string{"type": "log", "message": "unrelated event"})
		_ = conn.WriteJSON(map[string]string{"type": "panel_ack", "requestId": cmd["requestId"]})
	}))
	defer sidecar.Close()
	endpoint, _ := url.Parse(sidecar.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "storage"), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]int{"port": port})
	if err := os.WriteFile(filepath.Join(dir, "storage", "browser-sidecar.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: filepath.Join(dir, "config.json")}}
	for _, action := range []string{"open", "sync"} {
		rr := httptest.NewRecorder()
		s.handleBrowserWeibo(rr, httptest.NewRequest(http.MethodPost, "/api/browser/weibo", strings.NewReader(`{"action":"`+action+`"}`)))
		if rr.Code != http.StatusAccepted {
			t.Fatalf("%s: %d %s", action, rr.Code, rr.Body.String())
		}
		if got := <-commands; got != "weibo_panel_"+action {
			t.Fatal(got)
		}
	}
	rr := httptest.NewRecorder()
	s.handleBrowserWeibo(rr, httptest.NewRequest(http.MethodPost, "/api/browser/weibo", strings.NewReader(`{"action":"shutdown"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary command accepted: %d", rr.Code)
	}
}
