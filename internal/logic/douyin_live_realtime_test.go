package logic

import (
	"bytes"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

func TestParseDouyinDisplayCountUnits(t *testing.T) {
	cases := map[string]int64{
		"1.2万":    12_000,
		"1.5亿":    150_000_000,
		"100,001": 100_001,
		"120000":  120_000,
		"":        0,
	}
	for input, want := range cases {
		if got := parseDouyinDisplayCount(input); got != want {
			t.Fatalf("parse %q=%d, want %d", input, got, want)
		}
	}
}

func TestResolveDouyinOnlineExactAndFormatted(t *testing.T) {
	metrics := resolveDouyinMetrics("HTTP", map[string]interface{}{
		"user_count": 1, "user_count_str": "1.2万",
	})
	if metrics.OnlineCount != 12_000 || metrics.OnlineSource != "user_count_str" {
		t.Fatalf("sentinel resolution=%+v", metrics)
	}
	metrics = resolveDouyinMetrics("HTTP", map[string]interface{}{
		"user_count": 22_140, "user_count_str": "2.2万",
	})
	if metrics.OnlineCount != 22_140 || metrics.OnlineSource != "user_count" {
		t.Fatalf("exact resolution=%+v", metrics)
	}
}

func TestRoomUserSeqKeepsExactCountOverFormattedCount(t *testing.T) {
	metrics := resolveDouyinMetrics("WebcastRoomUserSeqMessage", map[string]interface{}{
		"payload": map[string]interface{}{
			"total": 22_140, "onlineUserForAnchor": "2.2万", "totalUser": "10.5万",
		},
	})
	if metrics.OnlineCount != 22_140 || metrics.OnlineSource != "RoomUserSeqMessage.payload.total" {
		t.Fatalf("RoomUserSeq metrics=%+v", metrics)
	}
	if metrics.TotalViewers != 105_000 {
		t.Fatalf("total viewers=%d", metrics.TotalViewers)
	}
}

func TestExtractDouyinHTTPMetricsEscapedJSON(t *testing.T) {
	raw := []byte(`window.__DATA__={\"user_count\":1,\"user_count_str\":\"1.2万\",\"room_user_count\":0}`)
	metrics := extractDouyinHTTPMetrics(raw)
	if metrics.OnlineCount != 12_000 {
		t.Fatalf("HTTP metrics=%+v", metrics)
	}
}

func TestDouyinLiveStoreMigrationPreservesExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE existing_user_data(value TEXT); INSERT INTO existing_user_data VALUES('keep-me')`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := openDouyinLiveStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var value string
	if err := store.db.QueryRow(`SELECT value FROM existing_user_data`).Scan(&value); err != nil || value != "keep-me" {
		t.Fatalf("old data value=%q err=%v", value, err)
	}
	var migrations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=1`).Scan(&migrations); err != nil || migrations != 1 {
		t.Fatalf("migration row=%d err=%v", migrations, err)
	}
}

func TestDouyinEventKeyIsIdempotentAndCookieIsRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "live.db")
	store, err := openDouyinLiveStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	secret := "sessionid=do-not-persist"
	payload := map[string]interface{}{"cookie": secret, "value": 1}
	inserted, err := store.recordEvent("session", "event-1", "GiftMessage", time.Now(), payload)
	if err != nil || !inserted {
		t.Fatalf("first insert=%v err=%v", inserted, err)
	}
	inserted, err = store.recordEvent("session", "event-1", "GiftMessage", time.Now(), payload)
	if err != nil || inserted {
		t.Fatalf("duplicate insert=%v err=%v", inserted, err)
	}
	var count int
	var saved string
	if err := store.db.QueryRow(`SELECT COUNT(*), MAX(payload_json) FROM live_realtime_event`).Scan(&count, &saved); err != nil {
		t.Fatal(err)
	}
	if count != 1 || strings.Contains(saved, secret) || !strings.Contains(saved, "[redacted]") {
		t.Fatalf("count=%d saved=%s", count, saved)
	}
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	log.Printf("payload=%v", sanitizeDouyinSample(payload))
	log.SetOutput(previous)
	if strings.Contains(logs.String(), secret) {
		t.Fatalf("cookie leaked in log: %s", logs.String())
	}
}

func TestDouyinRealtimeRevenueEventsAreIgnored(t *testing.T) {
	m := NewDouyinMonitor(&config.Config{DouyinLiveSoundWaveEnabled: true}, nil, nil)
	m.liveSessionDir = t.TempDir()
	m.liveStates["room"] = &douyinLiveState{SessionID: "session", LiveID: "room", Online: true, ComboGiftCounts: map[string]int64{}}
	m.handleLiveMessage("room", []byte(`{"method":"WebcastGiftMessage","msgId":"gift1","payload":{"gift":{"id":"1","diamondCount":100},"repeatCount":3}}`))
	m.handleLiveMessage("room", []byte(`{"method":"LightGiftMessage","msgId":"light1","payload":{"giftInfo":{"giftId":"7","diamondCount":3},"repeatCount":2}}`))
	m.handleLiveMessage("room", []byte(`{"method":"UpdateFanTicketMessage","msgId":"ticket1","payload":{"roomFanTicketCount":"12,345"}}`))
	m.handleLiveMessage("room", []byte(`{"method":"MatchAgainstScoreMessage","msgId":"pk1","against":{"leftGoalInt":123,"rightGoalInt":456}}`))
	state := m.liveStates["room"]
	if state.GiftCount != 0 || state.DiamondTotal != 0 || state.FanTicketTotal != 0 || state.PKLeftScore != 0 || state.PKRightScore != 0 {
		t.Fatalf("revenue event mutated alert-only state: %+v", state)
	}
}
