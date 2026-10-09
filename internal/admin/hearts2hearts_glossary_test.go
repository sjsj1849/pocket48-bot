package admin

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHearts2HeartsGlossaryAPI(t *testing.T) {
	s := &Server{opts: Options{ConfigPath: filepath.Join(t.TempDir(), "config.json")}}
	rr := httptest.NewRecorder()
	s.handleHearts2HeartsGlossary(rr, httptest.NewRequest(http.MethodGet, "/api/hearts2hearts/glossary", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "태자님") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	body := `{"context":"H2H","terms":[{"source":"태자님","target":"太子殿下","note":"易安外号"}]}`
	rr = httptest.NewRecorder()
	s.handleHearts2HeartsGlossary(rr, httptest.NewRequest(http.MethodPut, "/api/hearts2hearts/glossary", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	s.handleHearts2HeartsGlossary(rr, httptest.NewRequest(http.MethodGet, "/api/hearts2hearts/glossary", nil))
	if !strings.Contains(rr.Body.String(), "易安外号") {
		t.Fatal(rr.Body.String())
	}
}
