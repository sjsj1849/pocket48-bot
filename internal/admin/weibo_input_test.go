package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeWeiboUID(t *testing.T) {
	tests := map[string]string{
		"1234567890":                                       "1234567890",
		"https://weibo.com/u/1234567890":                   "1234567890",
		"weibo.com/u/1234567890":                           "1234567890",
		"https://m.weibo.cn/profile/1234567890":            "1234567890",
		"https://weibo.com/1234567890/AbCdEf?refer_flag=1": "1234567890",
		"https://m.weibo.cn/api/container/getIndex?type=uid&value=1234567890&containerid=1005051234567890":    "1234567890",
		"https://m.weibo.cn/api/container/getIndex?containerid=1076031234567890_-_WEIBO_SECOND_PROFILE_WEIBO": "1234567890",
		"https://weibo.com/p/1005051234567890/home":                                                           "1234567890",
		"复制链接 https://weibo.com/u/1234567890 查看微博":                                                            "1234567890",
		"https://example.com/u/1234567890":                                                                    "",
		"https://m.weibo.cn/detail/1234567890123456":                                                          "",
	}
	for input, want := range tests {
		if got := normalizeWeiboUID(input); got != want {
			t.Errorf("normalizeWeiboUID(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestNormalizeSuperOIDFromLink(t *testing.T) {
	const oid = "100808aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []string{
		oid,
		"https://weibo.com/p/" + oid + "/super_index",
		"https://m.weibo.cn/p/index?containerid=" + oid + "_-_feed",
		"https://m.weibo.cn/p/index?containerid=1022%3A" + oid,
	}
	for _, input := range tests {
		if got := normalizeSuperOID(input); got != oid {
			t.Errorf("normalizeSuperOID(%q)=%q, want %q", input, got, oid)
		}
	}
}

func TestWeiboSubscriptionPostNormalizesLinkBeforePersisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"WEIBO_SUBSCRIPTIONS":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	server := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { reloads++; return nil }}
	request := httptest.NewRequest(http.MethodPost, "/api/weibo/subscriptions", bytes.NewBufferString(
		`{"groupId":123456,"uid":"https://weibo.com/u/1234567890?tabtype=feed","name":"测试账号"}`,
	))
	response := httptest.NewRecorder()
	server.handleWeiboSubscriptions(response, request)
	if response.Code != http.StatusOK || reloads != 1 {
		t.Fatalf("status=%d reloads=%d body=%s", response.Code, reloads, response.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	stored := map[string]map[string]*weiboStoredSub{}
	if err := json.Unmarshal(config["WEIBO_SUBSCRIPTIONS"], &stored); err != nil {
		t.Fatal(err)
	}
	if item := stored["123456"]["1234567890"]; item == nil || item.UID != "1234567890" || item.Name != "测试账号" {
		t.Fatalf("normalized subscription not persisted: %#v", stored)
	}
}
