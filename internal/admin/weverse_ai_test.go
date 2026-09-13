package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestWeverseAISettingsKeepSecrets(t *testing.T) {
	s := &Server{opts: Options{ConfigPath: filepath.Join(t.TempDir(), "config.json")}}
	for _, body := range []string{`{"enabled":true,"baseUrl":"https://example.com/v1","model":"grok-4.5","idleSeconds":300,"apiKey":"private-api-key"}`, `{"enabled":true,"baseUrl":"https://example.com/v1","model":"grok-4.5","idleSeconds":600}`} {
		rr := httptest.NewRecorder()
		s.handleWeverseAI(rr, httptest.NewRequest(http.MethodPut, "/api/weverse/ai", strings.NewReader(body)))
		if rr.Code != 200 || strings.Contains(rr.Body.String(), "private-api-key") || !strings.Contains(rr.Body.String(), `"keyConfigured":true`) {
			t.Fatal(rr.Body.String())
		}
	}
	cfg, err := weverse.LoadAISettings(weverse.Dir(s.opts.ConfigPath))
	if err != nil || cfg.APIKey != "private-api-key" || cfg.IdleSeconds != 600 {
		t.Fatal("key not preserved", err)
	}
	rr := httptest.NewRecorder()
	s.handleWeverseAI(rr, httptest.NewRequest(http.MethodGet, "/api/weverse/ai", nil))
	if strings.Contains(rr.Body.String(), "private-api-key") {
		t.Fatal("key exposed")
	}
	rr = httptest.NewRecorder()
	s.handleWeverseAI(rr, httptest.NewRequest(http.MethodPut, "/api/weverse/ai", strings.NewReader(`{"baseUrl":"http://example.com","model":"test","idleSeconds":300}`)))
	if rr.Code != 400 {
		t.Fatal("insecure URL accepted")
	}
}
