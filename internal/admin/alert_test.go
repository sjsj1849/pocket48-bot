package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAlertConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	data := []byte(`{
  "ALERT_EMAIL_ENABLED": true,
  "ALERT_EMAIL_TO": "owner@example.com",
  "ALERT_EMAIL_FROM": "bot@example.com",
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

func TestBuildServiceEmailIncludesStyledAndPlainVersions(t *testing.T) {
	cfg := alertConfig{From: "bot@example.com", To: "owner@example.com", PanelURL: "https://panel.example.com"}
	service := serviceState{Name: "QChat <实时>", StatusText: "重连中", LastEvent: "连接 <异常>"}
	message := string(buildServiceEmail(cfg, service, false, time.Date(2026, 7, 17, 10, 0, 0, 0, time.Local)))
	for _, want := range []string{"multipart/alternative", "Content-Type: text/plain", "Content-Type: text/html", "Pocket48 Console", "打开管理面板", "panel.example.com", "QChat &lt;实时&gt;", "连接 &lt;异常&gt;"} {
		if !strings.Contains(message, want) {
			t.Fatalf("email does not contain %q", want)
		}
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

func TestBuildServiceEmailOfflineNeedsManualCopy(t *testing.T) {
	cfg := alertConfig{From: "bot@example.com", To: "owner@example.com"}
	service := serviceState{Name: "Bot", StatusText: "已停止", LastEvent: "pocket48-bot.service"}
	message := string(buildServiceEmail(cfg, service, false, time.Date(2026, 7, 19, 2, 40, 0, 0, time.Local)))
	for _, want := range []string{"服务持续异常，需人工处理", "需要处理", "Bot", "请人工检查"} {
		if !strings.Contains(message, want) {
			t.Fatalf("offline email missing %q", want)
		}
	}
	// recovery 文案不应出现在 offline 邮件里
	if strings.Contains(message, "服务已经恢复") {
		t.Fatal("offline email should not claim recovered")
	}
}
