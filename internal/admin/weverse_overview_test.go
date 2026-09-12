package admin

import (
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"testing"
	"time"
)

func TestWeverseOverviewHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	dir := weverse.Dir(path)
	now := time.Now().Truncate(time.Second)
	cfg := weverse.Settings{Enabled: true, PollSeconds: 60, Subscriptions: []weverse.Subscription{{Enabled: true}}}
	if err := weverse.Write(dir, "settings.json", cfg); err != nil {
		t.Fatal(err)
	}
	check := now.Format(time.RFC3339)
	status := weverse.Status{LastCheck: check, LastSuccess: check, Events: 112}
	if err := weverse.Write(dir, "status.json", status); err != nil {
		t.Fatal(err)
	}
	card := weverseService(path, now)
	if card == nil || card.Status != "healthy" {
		t.Fatalf("healthy scan: %+v", card)
	}
	activity := overviewActivity([]string{"2026/09/13 02:00:00 [NIM] status=connected"}, []serviceState{*card}, 12)
	if len(activity) != 2 || activity[0].Source != "Weverse" {
		t.Fatalf("activity: %+v", activity)
	}
	if card := weverseService(path, now.Add(6*time.Minute)); card.Status != "down" {
		t.Fatalf("stale: %+v", card)
	}
	status.Error = "登录态失效"
	if err := weverse.Write(dir, "status.json", status); err != nil {
		t.Fatal(err)
	}
	if card := weverseService(path, now); card.Status != "down" || card.LastEvent != status.Error {
		t.Fatalf("failed scan: %+v", card)
	}
	cfg.Subscriptions = nil
	if err := weverse.Write(dir, "settings.json", cfg); err != nil {
		t.Fatal(err)
	}
	if card := weverseService(path, now); card.StatusText != "待配置" {
		t.Fatalf("no subscriptions: %+v", card)
	}
	cfg.Enabled = false
	if err := weverse.Write(dir, "settings.json", cfg); err != nil {
		t.Fatal(err)
	}
	if card := weverseService(path, now); card != nil {
		t.Fatalf("disabled: %+v", card)
	}
}

func TestOneBotURLValidation(t *testing.T) {
	for _, v := range []string{"napcat-webui", "http://127.0.0.1:3001", "ws://", "ws://user:secret@localhost:3001", "ws://localhost/#bad"} {
		if validateOneBotURL(v) == nil {
			t.Errorf("accepted invalid URL %q", v)
		}
	}
	for _, v := range []string{"ws://127.0.0.1:3001", "wss://example.com/onebot"} {
		if err := validateOneBotURL(v); err != nil {
			t.Errorf("rejected valid URL %q: %v", v, err)
		}
	}
}
