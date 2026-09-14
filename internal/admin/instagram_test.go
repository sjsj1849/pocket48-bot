package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pocket48-bot/internal/instagram"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
	"time"
)

func TestInstagramVisibleAndSessionRedacted(t *testing.T) {
	s := &Server{}
	s.opts.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	dir := instagram.Dir(s.opts.ConfigPath)
	if c := instagramService(s.opts.ConfigPath, time.Now()); c == nil || c.StatusText != "未启用" {
		t.Fatal("service hidden")
	}
	_ = instagram.Write(dir, "session.json", map[string]any{"username": "me", "cookies": map[string]string{"sessionid": "secret-session", "csrftoken": "secret-csrf"}})
	w := httptest.NewRecorder()
	s.handleInstagram(w, httptest.NewRequest(http.MethodGet, "/api/instagram/settings", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-") || !strings.Contains(w.Body.String(), `"sessionConfigured":true`) {
		t.Fatal("session exposed or status absent")
	}
}
func TestWeversePostPasswordsRedacted(t *testing.T) {
	s := &Server{}
	s.opts.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	_ = weverse.Write(weverse.Dir(s.opts.ConfigPath), "post-passwords.json", []weverse.PostPassword{{PostID: "1-123", URL: "https://weverse.io/a/artist/1-123", Password: "secret-password"}})
	w := httptest.NewRecorder()
	s.handleWeversePostPasswords(w, httptest.NewRequest(http.MethodGet, "/api/weverse/post-passwords", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-password") || !strings.Contains(w.Body.String(), "1-123") {
		t.Fatal("password leaked")
	}
}
