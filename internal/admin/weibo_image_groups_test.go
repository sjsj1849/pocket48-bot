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

func callSuperImageGroups(t *testing.T, server *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/weibo/super-count/image-groups", bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	server.handleWeiboSuperCountImageGroups(response, request)
	return response
}

func TestSuperImageGroupsCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	seed := `{"WEIBO_SUPER_COUNT_GROUPS":{"eight":{"name":"八小妹"},"two":{"name":"哈two哈"},"other":{"name":"另外两组"}},"UNRELATED":"keep"}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	server := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { reloads++; return nil }}

	created := callSuperImageGroups(t, server, http.MethodPost, `{"name":"第一张","groupKeys":["eight","two","eight"]}`)
	if created.Code != http.StatusOK || reloads != 1 {
		t.Fatalf("create status=%d reloads=%d body=%s", created.Code, reloads, created.Body.String())
	}

	listed := callSuperImageGroups(t, server, http.MethodGet, "")
	var payload struct {
		ImageGroups []weiboSuperCountImageGroupPanel `json:"imageGroups"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ImageGroups) != 1 || payload.ImageGroups[0].Key != "image-1" || len(payload.ImageGroups[0].GroupKeys) != 2 {
		t.Fatalf("list payload=%#v", payload)
	}

	updated := callSuperImageGroups(t, server, http.MethodPut, `{"key":"image-1","name":"合并图","groupKeys":["other"]}`)
	if updated.Code != http.StatusOK || reloads != 2 {
		t.Fatalf("update status=%d reloads=%d body=%s", updated.Code, reloads, updated.Body.String())
	}

	invalid := callSuperImageGroups(t, server, http.MethodPost, `{"name":"坏方案","groupKeys":["missing"]}`)
	if invalid.Code != http.StatusBadRequest || reloads != 2 {
		t.Fatalf("invalid status=%d reloads=%d body=%s", invalid.Code, reloads, invalid.Body.String())
	}

	deleted := callSuperImageGroups(t, server, http.MethodDelete, `{"key":"image-1"}`)
	if deleted.Code != http.StatusOK || reloads != 3 {
		t.Fatalf("delete status=%d reloads=%d body=%s", deleted.Code, reloads, deleted.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	var unrelated string
	_ = json.Unmarshal(saved["UNRELATED"], &unrelated)
	if unrelated != "keep" {
		t.Fatalf("unrelated config was changed: %q", unrelated)
	}
}
