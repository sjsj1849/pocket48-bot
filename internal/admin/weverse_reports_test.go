package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strings"
	"testing"
)

func TestWeverseReportPanelDoesNotExposeSMTPSecretsAndExportsWorkbook(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if e := os.WriteFile(cfg, []byte(`{"ALERT_EMAIL_ENABLED":true,"ALERT_EMAIL_TO":"report@example.com","ALERT_EMAIL_SMTP_PASSWORD":"private-password"}`), 0600); e != nil {
		t.Fatal(e)
	}
	s := &Server{opts: Options{ConfigPath: cfg}}
	history, e := weverse.OpenHistory(weverse.Dir(cfg))
	if e != nil {
		t.Fatal(e)
	}
	if e = history.SaveMembers(235, []weverse.Member{{ID: "a", Name: "A"}}); e != nil {
		t.Fatal(e)
	}
	history.Close()
	response := httptest.NewRecorder()
	s.handleWeverseReports(response, httptest.NewRequest(http.MethodGet, "/api/weverse/reports", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-password") {
		t.Fatal("report configuration leaked private mail settings", response.Code)
	}
	var body struct {
		EmailTo string `json:"emailTo"`
	}
	if e = json.Unmarshal(response.Body.Bytes(), &body); e != nil || body.EmailTo != "report@example.com" {
		t.Fatal(body, e)
	}
	response = httptest.NewRecorder()
	s.handleWeverseReports(response, httptest.NewRequest(http.MethodGet, "/api/weverse/reports/download?kind=monthly&year=2026&month=9", nil))
	if response.Code != 200 || !strings.HasPrefix(response.Body.String(), "PK") || !strings.Contains(response.Header().Get("Content-Disposition"), "monthly-2026-09.xlsx") {
		t.Fatal("workbook download failed", response.Code)
	}
	response = httptest.NewRecorder()
	s.handleWeverseReports(response, httptest.NewRequest(http.MethodGet, "/api/weverse/reports/download?kind=monthly&year=2026&month=13", nil))
	if response.Code != 400 {
		t.Fatal("invalid period accepted")
	}
}

func TestWeverseWeeklyPreviewAndDownload(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{ConfigPath: cfg}}
	h, err := weverse.OpenHistory(weverse.Dir(cfg))
	if err != nil {
		t.Fatal(err)
	}
	_ = h.SaveMembers(235, []weverse.Member{{ID: "a", Name: "A"}})
	h.Close()
	for _, path := range []string{"preview", "download"} {
		response := httptest.NewRecorder()
		s.handleWeverseReports(response, httptest.NewRequest(http.MethodGet, "/api/weverse/reports/"+path+"?kind=weekly&date=2027-01-01", nil))
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
		if path == "preview" && !strings.Contains(response.Body.String(), "weekly-2026-12-28") {
			t.Fatal(response.Body.String())
		}
		if path == "download" && !strings.Contains(response.Header().Get("Content-Disposition"), "weekly-2026-12-28.xlsx") {
			t.Fatal(response.Header())
		}
	}
	response := httptest.NewRecorder()
	s.handleWeverseReports(response, httptest.NewRequest(http.MethodGet, "/api/weverse/reports/preview?kind=weekly&date=2026-02-30", nil))
	if response.Code != 400 {
		t.Fatal(response.Code)
	}
}
