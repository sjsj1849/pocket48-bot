package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func callSuperCountGroups(t *testing.T, server *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/weibo/super-count/groups", bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	server.handleWeiboSuperCountGroups(response, request)
	return response
}

func TestSuperCountGroupManagementOnlyDeletesUnusedGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	seed := `{
		"WEIBO_SUPER_COUNT_GROUPS":{"topic":{"name":"超话使用组"},"image":{"name":"图片使用组"},"old":{"name":"旧组"}},
		"WEIBO_SUPER_COUNT_TOPICS":{"oid":{"oid":"oid","group_name":"topic"}},
		"WEIBO_SUPER_COUNT_IMAGE_GROUPS":{"image-1":{"name":"日报总图","group_keys":["image"]}},
		"UNRELATED":"keep"
	}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	server := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { reloads++; return nil }}

	listed := callSuperCountGroups(t, server, http.MethodGet, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var payload struct {
		Groups []weiboSuperCountGroupPanel `json:"groups"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Groups) != 3 {
		t.Fatalf("groups=%#v", payload.Groups)
	}
	usage := map[string]weiboSuperCountGroupPanel{}
	for _, group := range payload.Groups {
		usage[group.Key] = group
	}
	if usage["topic"].TopicCount != 1 || len(usage["image"].ImageGroupNames) != 1 || usage["old"].TopicCount != 0 || len(usage["old"].ImageGroupNames) != 0 {
		t.Fatalf("incorrect usage=%#v", usage)
	}

	usedTopic := callSuperCountGroups(t, server, http.MethodDelete, `{"key":"topic"}`)
	if usedTopic.Code != http.StatusConflict || !strings.Contains(usedTopic.Body.String(), "1 个超话") {
		t.Fatalf("topic delete status=%d body=%s", usedTopic.Code, usedTopic.Body.String())
	}
	usedImage := callSuperCountGroups(t, server, http.MethodDelete, `{"key":"image"}`)
	if usedImage.Code != http.StatusConflict || !strings.Contains(usedImage.Body.String(), "日报总图") {
		t.Fatalf("image delete status=%d body=%s", usedImage.Code, usedImage.Body.String())
	}
	deleted := callSuperCountGroups(t, server, http.MethodDelete, `{"key":"old"}`)
	if deleted.Code != http.StatusOK || reloads != 1 {
		t.Fatalf("unused delete status=%d reloads=%d body=%s", deleted.Code, reloads, deleted.Body.String())
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	groups := loadCountGroups(saved)
	if groups["old"] != nil || groups["topic"] == nil || groups["image"] == nil {
		t.Fatalf("unexpected saved groups=%#v", groups)
	}
	var unrelated string
	_ = json.Unmarshal(saved["UNRELATED"], &unrelated)
	if unrelated != "keep" {
		t.Fatalf("unrelated config changed: %q", unrelated)
	}
}
