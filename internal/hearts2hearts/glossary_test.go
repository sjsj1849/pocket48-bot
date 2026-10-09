package hearts2hearts

import (
	"path/filepath"
	"testing"
)

func TestGlossaryDefaultsAndLongestTermWins(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	g, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := g.Apply("태자님과 태자비들이 하츄들을 만났어")
	if got != "太子殿下과 太子妃们이 哈啾们을 만났어" {
		t.Fatalf("locked terms = %q", got)
	}
	if err := Save(configPath, g); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(configPath)
	if err != nil || loaded.Instructions() == "" || len(loaded.Terms) != len(g.Terms) {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
}

func TestGlossaryRejectsDuplicateTerms(t *testing.T) {
	g := Glossary{Terms: []Term{{Source: "하츄", Target: "哈啾"}, {Source: "하츄", Target: "S2U"}}}
	if err := Validate(g); err == nil {
		t.Fatal("duplicate term accepted")
	}
}
