package config

import (
	"os"
	"path/filepath"
	"testing"
)

func loadTestConfig(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNIMSafeDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{}`)
	if !cfg.NIMRoomMessagePollFallback || !cfg.NIMLiveDanmakuEnabled {
		t.Fatalf("unexpected NIM defaults: %#v", cfg)
	}
}

func TestNIMExplicitFalseIsPreserved(t *testing.T) {
	cfg := loadTestConfig(t, `{
        "NIM_ROOM_MESSAGE_POLL_FALLBACK": false,
		"NIM_LIVE_DANMAKU_ENABLED": false
    }`)
	if cfg.NIMRoomMessagePollFallback || cfg.NIMLiveDanmakuEnabled {
		t.Fatalf("explicit false was overwritten: %#v", cfg)
	}
}
