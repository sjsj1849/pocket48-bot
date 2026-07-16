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

func TestWeiboBrowserAuthDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{}`)
	if !cfg.WeiboBrowserHeadless {
		t.Fatal("weibo browser should default to headless mode")
	}
	if cfg.WeiboBrowserAuthCmd != "node ./sidecar/weibo-auth/index.mjs" {
		t.Fatalf("unexpected browser auth command: %q", cfg.WeiboBrowserAuthCmd)
	}
	if cfg.WeiboBrowserProfileDir != "./storage/weibo-browser-profile" {
		t.Fatalf("unexpected browser profile dir: %q", cfg.WeiboBrowserProfileDir)
	}
	if cfg.WeiboBrowserRefreshMinutes != 30 {
		t.Fatalf("unexpected browser refresh interval: %d", cfg.WeiboBrowserRefreshMinutes)
	}
}

func TestWeiboBrowserAuthExplicitValuesArePreserved(t *testing.T) {
	cfg := loadTestConfig(t, `{
        "WEIBO_BROWSER_HEADLESS": false,
        "WEIBO_BROWSER_AUTH_CMD": "node custom.mjs",
        "WEIBO_BROWSER_PROFILE_DIR": "/private/weibo-profile",
        "WEIBO_BROWSER_REFRESH_MINUTES": 60
    }`)
	if cfg.WeiboBrowserHeadless {
		t.Fatal("explicit non-headless mode was overwritten")
	}
	if cfg.WeiboBrowserAuthCmd != "node custom.mjs" || cfg.WeiboBrowserProfileDir != "/private/weibo-profile" || cfg.WeiboBrowserRefreshMinutes != 60 {
		t.Fatalf("explicit browser settings were overwritten: %#v", cfg)
	}
}
