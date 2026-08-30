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

func TestLoadConfigInitializesDouyinDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"NAPCAT_WS_URL":"ws://localhost"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BrowserSidecarCmd != "node ./sidecar/weibo-auth/index.mjs" {
		t.Fatalf("unexpected browser command: %q", cfg.BrowserSidecarCmd)
	}
	if cfg.BrowserProfileDir != "./storage/weibo-browser-profile" || cfg.DouyinPollSeconds != 60 {
		t.Fatalf("unexpected douyin defaults: profile=%q poll=%d", cfg.BrowserProfileDir, cfg.DouyinPollSeconds)
	}
	if cfg.DouyinLiveWSURL != "ws://127.0.0.1:1088/ws" || cfg.DouyinSubscriptions == nil {
		t.Fatalf("unexpected douyin live/subscription defaults")
	}
	if !cfg.DouyinLiveSummaryEnabled || !cfg.DouyinLiveSoundWaveEnabled || cfg.DouyinLiveRawStatsDebug || cfg.DouyinLiveRawGiftDebug {
		t.Fatalf("unexpected douyin live summary defaults: %#v", cfg)
	}
	if cfg.DouyinIMGroupName != "" || cfg.DouyinIMGroupNumber != "" {
		t.Fatalf("unexpected douyin IM defaults: %#v", cfg)
	}
	if cfg.DouyinIMEnabled {
		t.Fatal("privacy-sensitive Douyin account features must default to disabled")
	}
}

func TestNIMSafeDefaults(t *testing.T) {
	cfg := loadTestConfig(t, `{}`)
	if !cfg.NIMRoomMessagePollFallback || !cfg.NIMLiveDanmakuEnabled {
		t.Fatalf("unexpected NIM defaults: %#v", cfg)
	}
}

func TestDouyinLiveExplicitFalseIsPreserved(t *testing.T) {
	cfg := loadTestConfig(t, `{"DOUYIN_LIVE_SUMMARY_ENABLED":false,"DOUYIN_LIVE_SOUND_WAVE_ENABLED":false}`)
	if cfg.DouyinLiveSummaryEnabled || cfg.DouyinLiveSoundWaveEnabled {
		t.Fatalf("explicit douyin live false was overwritten: %#v", cfg)
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
