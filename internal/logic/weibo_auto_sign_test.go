package logic

import (
	"os"
	"path/filepath"
	"testing"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
)

func newWeiboAutoSignTestBot(t *testing.T) *Bot {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"WEIBO_SUPER_LAST_RUN_DATE":"2026-07-16"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return &Bot{
		cfg:          cfg,
		weiboMonitor: monitor.NewWeiboMonitor(nil),
	}
}

func TestWeiboCredentialUpdatesPreserveSuccessfulAutoSignDate(t *testing.T) {
	tests := []struct {
		name   string
		update func(*Bot) error
	}{
		{
			name: "app auth",
			update: func(bot *Bot) error {
				return bot.updateWeiboAppAuth(&config.WeiboAppConfig{GSID: "new-gsid"})
			},
		},
		{
			name: "manual cookie",
			update: func(bot *Bot) error {
				_, err := bot.updateWeiboCookie(1, "SUB=new-cookie")
				return err
			},
		},
		{
			name: "browser cookie refresh",
			update: func(bot *Bot) error {
				bot.handleWeiboAuthCookies("SUB=browser-cookie", "SUB=mobile-cookie", "scheduled")
				return nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bot := newWeiboAutoSignTestBot(t)
			if err := test.update(bot); err != nil {
				t.Fatal(err)
			}
			if got, want := bot.cfg.WeiboSuperLastRunDate, "2026-07-16"; got != want {
				t.Fatalf("last auto-sign date = %q, want %q", got, want)
			}
		})
	}
}
