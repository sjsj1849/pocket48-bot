package admin

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBilibiliGroupPresent(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"BILIBILI_ENABLED":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: cfgPath}}
	rec := httptest.NewRecorder()
	s.handleConfig(rec, httptest.NewRequest("GET", "/api/config", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Groups     map[string][]configField `json:"groups"`
		GroupOrder []string                 `json:"groupOrder"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range out.GroupOrder {
		if g == "Bilibili" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Bilibili missing from groupOrder: %v", out.GroupOrder)
	}
	fields := out.Groups["Bilibili"]
	if len(fields) == 0 {
		keys := make([]string, 0, len(out.Groups))
		for k := range out.Groups {
			keys = append(keys, k)
		}
		t.Fatalf("groups[Bilibili] is empty; all groups=%v", keys)
	}
	if fields[0].Key != "BILIBILI_ENABLED" {
		t.Fatalf("unexpected key %q", fields[0].Key)
	}
	// 主开关保存后应热生效，不需要重启
	if configFieldNeedsRestart("BILIBILI_ENABLED") {
		t.Fatal("BILIBILI_ENABLED should hot-reload, not require restart")
	}
	t.Logf("OK Bilibili group has %d field(s), first=%s", len(fields), fields[0].Key)
}
