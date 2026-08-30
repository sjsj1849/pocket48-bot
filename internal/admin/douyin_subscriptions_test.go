package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDouyinSubscriptionsRequireAdminSession(t *testing.T) {
	s := &Server{sessions: make(map[string]session)}
	handler := s.requireSession(http.HandlerFunc(s.handleDouyinSubscriptions))
	req := httptest.NewRequest(http.MethodGet, "/api/douyin/subscriptions", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newDouyinAdminTestServer(t *testing.T, reload func() error) (*Server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"DOUYIN_ENABLED":false,"DOUYIN_SUBSCRIPTIONS":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Server{opts: Options{ConfigPath: path}, reloadSignal: reload}, path
}

func callDouyinSubscriptions(t *testing.T, s *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/douyin/subscriptions", bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	s.handleDouyinSubscriptions(response, req)
	return response
}

func TestDouyinSubscriptionsCRUDAndHotReload(t *testing.T) {
	reloads := 0
	s, path := newDouyinAdminTestServer(t, func() error { reloads++; return nil })
	sec := "MS4wLjABAAAA_panel-user"
	added := callDouyinSubscriptions(t, s, http.MethodPost, `{"groupId":123456,"target":"`+sec+`","name":"新人昵称","atAll":true,"worksEnabled":true,"liveEnabled":false}`)
	if added.Code != http.StatusOK {
		t.Fatalf("add status=%d body=%s", added.Code, added.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(raw, &config) != nil {
		t.Fatal("saved config is invalid")
	}
	var enabled bool
	_ = json.Unmarshal(config["DOUYIN_ENABLED"], &enabled)
	if enabled {
		t.Fatal("adding a subscription must not silently enable/restart the Douyin service")
	}
	subs := loadDouyinSubs(config["DOUYIN_SUBSCRIPTIONS"])
	item := subs["123456"][sec]
	if item == nil || item.Name != "新人昵称" || !item.NameManual || !item.AtAll || item.WorksDisabled || !item.LiveDisabled {
		t.Fatalf("saved item=%#v", item)
	}

	edited := callDouyinSubscriptions(t, s, http.MethodPut, `{"oldGroupId":123456,"oldSecUserId":"`+sec+`","groupId":654321,"secUserId":"`+sec+`","name":"新备注","atAll":false,"enabled":false,"worksEnabled":false,"liveEnabled":true}`)
	if edited.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", edited.Code, edited.Body.String())
	}
	raw, _ = os.ReadFile(path)
	_ = json.Unmarshal(raw, &config)
	subs = loadDouyinSubs(config["DOUYIN_SUBSCRIPTIONS"])
	item = subs["654321"][sec]
	if item == nil || item.Name != "新备注" || !item.Disabled || !item.WorksDisabled || item.LiveDisabled {
		t.Fatalf("edited item=%#v", item)
	}

	bothOff := callDouyinSubscriptions(t, s, http.MethodPut, `{"oldGroupId":654321,"oldSecUserId":"`+sec+`","groupId":654321,"secUserId":"`+sec+`","enabled":true,"worksEnabled":false,"liveEnabled":false}`)
	if bothOff.Code != http.StatusOK {
		t.Fatalf("both-off status=%d body=%s", bothOff.Code, bothOff.Body.String())
	}
	raw, _ = os.ReadFile(path)
	_ = json.Unmarshal(raw, &config)
	subs = loadDouyinSubs(config["DOUYIN_SUBSCRIPTIONS"])
	item = subs["654321"][sec]
	if item == nil || item.Disabled || !item.WorksDisabled || !item.LiveDisabled {
		t.Fatalf("both-off item=%#v", item)
	}

	deleted := callDouyinSubscriptions(t, s, http.MethodDelete, `{"groupId":654321,"secUserId":"`+sec+`"}`)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if reloads != 4 {
		t.Fatalf("reload count=%d", reloads)
	}
}

func TestDouyinSubscriptionReportsHotReloadFailure(t *testing.T) {
	s, _ := newDouyinAdminTestServer(t, func() error { return errors.New("signal unavailable") })
	response := callDouyinSubscriptions(t, s, http.MethodPost, `{"groupId":123,"target":"MS4wLjABAAAA_reload-failure"}`)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "热重载失败") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDouyinSubscriptionUsesCachedNickname(t *testing.T) {
	s, path := newDouyinAdminTestServer(t, func() error { return nil })
	profileDir := filepath.Join(filepath.Dir(path), "browser-profile")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sec := "MS4wLjABAAAA_cached-name"
	cache := `{"contacts":[{"secUid":"` + sec + `","nickname":"自动解析昵称","remarkName":""}]}`
	if err := os.WriteFile(filepath.Join(profileDir, "douyin-contact-cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	configBody := `{"DOUYIN_ENABLED":true,"BROWSER_PROFILE_DIR":"` + profileDir + `","DOUYIN_SUBSCRIPTIONS":{"123":{"` + sec + `":{"sec_user_id":"` + sec + `"}}}}`
	if err := os.WriteFile(path, []byte(configBody), 0o600); err != nil {
		t.Fatal(err)
	}
	response := callDouyinSubscriptions(t, s, http.MethodGet, "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "自动解析昵称") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDouyinStoredSubscriptionEnabledStatus(t *testing.T) {
	disabled := &douyinStoredSub{SecUserID: "sec", Disabled: true}
	if got := douyinSubscriptionStatus("/tmp/config.json", disabled); got != "已停用" {
		t.Fatalf("status=%q", got)
	}
	enabled := &douyinStoredSub{SecUserID: "sec", Name: "主播", LiveID: "123"}
	if got := douyinSubscriptionStatus("/tmp/config.json", enabled); got != "已解析直播间，等待/正在监控" {
		t.Fatalf("status=%q", got)
	}
}
