package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pocket48-bot/internal/xmonitor"
	"strings"
	"testing"
	"time"
)

func TestXServiceReflectsActualScan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	dir := xmonitor.Dir(path)
	now := time.Now().Truncate(time.Second)
	cfg := xmonitor.Settings{Enabled: true, PollSeconds: 120, Subscriptions: []xmonitor.Subscription{{ID: "a", Username: "nekomo_st", UserID: "42", GroupID: 123, Enabled: true, Posts: true}}}
	if err := xmonitor.SaveSettings(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if got := xService(path, now); got.Status == "healthy" {
		t.Fatal("cookie alone must not imply healthy scanning")
	}
	status := xmonitor.Status{LastCheck: now.Format(time.RFC3339), LastSuccess: now.Format(time.RFC3339), Events: 20}
	if err := xmonitor.Write(dir, "status.json", status); err != nil {
		t.Fatal(err)
	}
	if got := xService(path, now); got.Status != "healthy" {
		t.Fatalf("fresh success should be healthy: %+v", got)
	}
	if got := xService(path, now.Add(10*time.Minute)); got.Status != "down" {
		t.Fatal("stale scan must be detected")
	}
	status.Error = "会话失效"
	_ = xmonitor.Write(dir, "status.json", status)
	if got := xService(path, now); got.LastEvent != "会话失效" || got.Status != "down" {
		t.Fatal("collector errors must be visible")
	}
	cfg.Enabled = false
	_ = xmonitor.SaveSettings(dir, cfg)
	if got := xService(path, now); got == nil || got.StatusText != "未启用" {
		t.Fatal("X remains discoverable in overview before enabling")
	}
}
func TestXSettingsNeverExposeCookies(t *testing.T) {
	s := &Server{}
	s.opts.ConfigPath = filepath.Join(t.TempDir(), "config.json")
	dir := xmonitor.Dir(s.opts.ConfigPath)
	_ = xmonitor.Write(dir, "session.json", xBrowserSession{Cookies: []xBrowserCookie{{Name: "auth_token", Value: "secret-auth", Domain: ".x.com", Expires: -1}, {Name: "ct0", Value: "secret-csrf", Domain: ".x.com", Expires: -1}}})
	w := httptest.NewRecorder()
	s.handleX(w, httptest.NewRequest(http.MethodGet, "/api/x/settings", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret-") || !strings.Contains(w.Body.String(), `"sessionConfigured":true`) {
		t.Fatalf("status-only settings response expected: %s", w.Body.String())
	}
}
