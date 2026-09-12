package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestWeverseSettingsValidationAndSecrets(t *testing.T) {
	s := &Server{opts: Options{ConfigPath: filepath.Join(t.TempDir(), "config.json")}}
	dir := weverse.Dir(s.opts.ConfigPath)
	if err := weverse.ImportSession(dir, weverse.Session{AccessToken: "do-not-expose", RefreshToken: "also-secret"}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.handleWeverse(rr, httptest.NewRequest(http.MethodGet, "/api/weverse/settings", nil))
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "do-not-expose") || strings.Contains(rr.Body.String(), "also-secret") || !strings.Contains(rr.Body.String(), `"sessionConfigured":true`) {
		t.Fatal(rr.Body.String())
	}
	for _, body := range []string{`{"pollSeconds":1}`, `{"pollSeconds":60,"proxyUrl":"file:///etc/passwd"}`, `{"pollSeconds":60,"subscriptions":[{"id":"1","groupId":123,"communityId":235,"slug":"../bad","posts":true}]}`} {
		rr = httptest.NewRecorder()
		s.handleWeverse(rr, httptest.NewRequest(http.MethodPut, "/api/weverse/settings", strings.NewReader(body)))
		if rr.Code != 400 {
			t.Fatal("invalid settings accepted")
		}
	}
	rr = httptest.NewRecorder()
	s.handleWeverse(rr, httptest.NewRequest(http.MethodPut, "/api/weverse/settings", strings.NewReader(`{"enabled":false,"pollSeconds":60,"subscriptions":[{"id":"1","groupId":123,"communityId":235,"slug":"hearts2hearts","memberIds":[],"memberNames":[],"posts":true,"enabled":true}]}`)))
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	cfg, err := weverse.LoadSettings(dir)
	if err != nil || len(cfg.Subscriptions) != 1 {
		t.Fatal(cfg, err)
	}
}
