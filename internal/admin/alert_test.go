package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAlertConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "ALERT_EMAIL_ENABLED": true,
  "ALERT_EMAIL_TO": "owner@example.com",
  "ALERT_EMAIL_FROM": "pocket48@jiufeng.cloud",
  "ALERT_EMAIL_COOLDOWN_MINUTES": 90
}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadAlertConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.To != "owner@example.com" || cfg.CooldownMinutes != 90 {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestAlertStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	server := &Server{opts: Options{AlertStatePath: path}}
	want := alertStateFile{Services: map[string]serviceAlertState{
		"qchat": {Failures: 2, Alerted: true},
	}}
	if err := server.saveAlertState(want); err != nil {
		t.Fatal(err)
	}
	got := server.loadAlertState()
	if got.Services["qchat"].Failures != 2 || !got.Services["qchat"].Alerted {
		t.Fatalf("state = %#v", got)
	}
}
