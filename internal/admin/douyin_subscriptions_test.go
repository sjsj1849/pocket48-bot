package admin

import (
	"net/http"
	"net/http/httptest"
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
