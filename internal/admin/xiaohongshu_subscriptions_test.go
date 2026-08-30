package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXiaohongshuSubscriptionRejectsCreatorIdentityChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := "5f1234567890abcdef123456"
	seed := `{"XIAOHONGSHU_SUBSCRIPTIONS":{"100":{"` + original + `":{"user_id":"` + original + `"}}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { return nil }}
	req := httptest.NewRequest(http.MethodPut, "/api/xiaohongshu/subscriptions", bytes.NewBufferString(
		`{"oldGroupId":100,"oldUserId":"`+original+`","groupId":100,"userId":"5f0000000000000000000000"}`,
	))
	response := httptest.NewRecorder()
	s.handleXiaohongshuSubscriptions(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不能在编辑时修改") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWeiboSubscriptionRejectsCreatorIdentityChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	seed := `{"WEIBO_SUBSCRIPTIONS":{"100":{"123456789":{"uid":"123456789","name":"原账号"}}}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { return nil }}
	req := httptest.NewRequest(http.MethodPut, "/api/weibo/subscriptions", bytes.NewBufferString(
		`{"oldGroupId":100,"oldUid":"123456789","groupId":100,"uid":"987654321"}`,
	))
	response := httptest.NewRecorder()
	s.handleWeiboSubscriptions(response, req)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "不能在编辑时修改") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
