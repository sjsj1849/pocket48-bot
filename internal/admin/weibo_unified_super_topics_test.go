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

func callUnifiedSuperTopics(t *testing.T, server *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/api/weibo/super-topics", bytes.NewBufferString(body))
	response := httptest.NewRecorder()
	server.handleWeiboUnifiedSuperTopics(response, request)
	return response
}

func TestUnifiedSuperTopicsMergeAndAtomicEdit(t *testing.T) {
	const oid = "100808aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const reportOnlyOID = "100808bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const newOID = "100808cccccccccccccccccccccccccccccccc"
	path := filepath.Join(t.TempDir(), "config.json")
	seed := `{
		"WEIBO_SUPER_AUTO_ENABLED":false,
		"WEIBO_SUPER_COUNT_ENABLED":false,
		"WEIBO_SUPER_TOPICS":{"9":{"` + oid + `":{"oid":"` + oid + `","name":"旧名称","last_sign_date":"2026-08-29","last_sign_status":"已签到","last_sign_rank":66}}},
		"WEIBO_SUPER_COUNT_TOPICS":{
			"` + oid + `":{"oid":"` + oid + `","name":"旧名称","group_name":"旧组","report_sign":88},
			"` + reportOnlyOID + `":{"oid":"` + reportOnlyOID + `","name":"仅日报","group_name":"旧组","report_sign":22}
		},
		"WEIBO_SUPER_COUNT_GROUPS":{"旧组":{"name":"旧分组"}}
	}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	server := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { reloads++; return nil }}

	listed := callUnifiedSuperTopics(t, server, http.MethodGet, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	var payload struct {
		Topics []weiboUnifiedSuperTopicPanel `json:"topics"`
		Groups []map[string]string           `json:"groups"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Topics) != 2 || len(payload.Groups) != 1 {
		t.Fatalf("payload=%#v", payload)
	}
	var merged *weiboUnifiedSuperTopicPanel
	for index := range payload.Topics {
		if payload.Topics[index].OID == oid {
			merged = &payload.Topics[index]
		}
	}
	if merged == nil || !merged.SignEnabled || !merged.ReportEnabled || merged.GroupName != "旧分组" || merged.LastSignRank != 66 || merged.ReportSign != 88 {
		t.Fatalf("merged=%#v", merged)
	}

	edited := callUnifiedSuperTopics(t, server, http.MethodPut, `{"oldOid":"`+oid+`","oid":"`+oid+`","name":"新名称","signEnabled":false,"reportEnabled":true,"groupName":"新分组"}`)
	if edited.Code != http.StatusOK {
		t.Fatalf("edit status=%d body=%s", edited.Code, edited.Body.String())
	}
	if reloads != 1 {
		t.Fatalf("one unified edit must emit one reload, got %d", reloads)
	}
	raw, _ := os.ReadFile(path)
	var config map[string]json.RawMessage
	_ = json.Unmarshal(raw, &config)
	signSubs := loadSuperSignSubscriptions(config)
	if removeSuperSignTopic(signSubs, oid) != nil {
		t.Fatal("disabled sign-in topic still exists")
	}
	countTopics := map[string]*weiboSuperCountTopicStored{}
	_ = json.Unmarshal(config["WEIBO_SUPER_COUNT_TOPICS"], &countTopics)
	item := countTopics[oid]
	if item == nil || item.Name != "新名称" || item.GroupName != "新分组" || item.ReportSign != 88 {
		t.Fatalf("count item=%#v", item)
	}
	var autoEnabled, countEnabled bool
	_ = json.Unmarshal(config["WEIBO_SUPER_AUTO_ENABLED"], &autoEnabled)
	_ = json.Unmarshal(config["WEIBO_SUPER_COUNT_ENABLED"], &countEnabled)
	if autoEnabled || countEnabled {
		t.Fatal("editing topic membership must not silently change global switches")
	}

	added := callUnifiedSuperTopics(t, server, http.MethodPost, `{"oid":"`+newOID+`","name":"新增超话","signEnabled":true,"reportEnabled":true,"groupName":"旧分组"}`)
	if added.Code != http.StatusOK || reloads != 2 {
		t.Fatalf("add status=%d reloads=%d body=%s", added.Code, reloads, added.Body.String())
	}
	raw, _ = os.ReadFile(path)
	_ = json.Unmarshal(raw, &config)
	signSubs = loadSuperSignSubscriptions(config)
	countTopics = map[string]*weiboSuperCountTopicStored{}
	_ = json.Unmarshal(config["WEIBO_SUPER_COUNT_TOPICS"], &countTopics)
	if signSubs["0"][newOID] == nil || countTopics[newOID] == nil || countTopics[newOID].GroupName != "旧组" {
		t.Fatalf("new sign=%#v count=%#v", signSubs["0"][newOID], countTopics[newOID])
	}

	deleted := callUnifiedSuperTopics(t, server, http.MethodDelete, `{"oid":"`+newOID+`"}`)
	if deleted.Code != http.StatusOK || reloads != 3 {
		t.Fatalf("delete status=%d reloads=%d body=%s", deleted.Code, reloads, deleted.Body.String())
	}
	raw, _ = os.ReadFile(path)
	_ = json.Unmarshal(raw, &config)
	signSubs = loadSuperSignSubscriptions(config)
	countTopics = map[string]*weiboSuperCountTopicStored{}
	_ = json.Unmarshal(config["WEIBO_SUPER_COUNT_TOPICS"], &countTopics)
	if removeSuperSignTopic(signSubs, newOID) != nil || countTopics[newOID] != nil {
		t.Fatal("unified delete did not remove both memberships")
	}
}

func TestUnifiedSuperTopicsRejectInvalidEdit(t *testing.T) {
	const oid = "100808aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"WEIBO_SUPER_TOPICS":{"0":{"`+oid+`":{"oid":"`+oid+`"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{opts: Options{ConfigPath: path}, reloadSignal: func() error { return nil }}

	changedIdentity := callUnifiedSuperTopics(t, server, http.MethodPut, `{"oldOid":"`+oid+`","oid":"100808bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","signEnabled":true}`)
	if changedIdentity.Code != http.StatusBadRequest || !strings.Contains(changedIdentity.Body.String(), "不能在编辑时修改") {
		t.Fatalf("identity status=%d body=%s", changedIdentity.Code, changedIdentity.Body.String())
	}
	allOff := callUnifiedSuperTopics(t, server, http.MethodPut, `{"oldOid":"`+oid+`","oid":"`+oid+`","signEnabled":false,"reportEnabled":false}`)
	if allOff.Code != http.StatusBadRequest || !strings.Contains(allOff.Body.String(), "至少启用一项") {
		t.Fatalf("all-off status=%d body=%s", allOff.Code, allOff.Body.String())
	}
}
