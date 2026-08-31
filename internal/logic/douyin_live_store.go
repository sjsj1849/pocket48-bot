package logic

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const douyinLiveDatabasePath = "storage/douyin-live.db"

type douyinLiveStore struct {
	db *sql.DB
}

type douyinMetricSnapshot struct {
	SessionID      string
	CapturedAt     time.Time
	OnlineCount    int64
	OnlineSource   string
	TotalViewers   int64
	LikeCount      int64
	RawMetricsJSON string
}

type douyinRevenueSnapshot struct {
	SessionID      string
	CapturedAt     time.Time
	DiamondDelta   int64
	GiftCountDelta int64
	FanTicketTotal int64
	PKLeftScore    int64
	PKRightScore   int64
	SourceEventKey string
}

func openDouyinLiveStore(path string) (*douyinLiveStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &douyinLiveStore{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *douyinLiveStore) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS live_metric_snapshot (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			captured_at INTEGER NOT NULL,
			online_count INTEGER NOT NULL DEFAULT 0,
			online_source TEXT NOT NULL DEFAULT '',
			total_viewers INTEGER NOT NULL DEFAULT 0,
			like_count INTEGER NOT NULL DEFAULT 0,
			raw_metrics_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_live_metric_session_time
			ON live_metric_snapshot(session_id, captured_at)`,
		`CREATE TABLE IF NOT EXISTS live_realtime_event (
			session_id TEXT NOT NULL,
			event_key TEXT NOT NULL,
			event_type TEXT NOT NULL,
			captured_at INTEGER NOT NULL,
			payload_json TEXT NOT NULL,
			PRIMARY KEY(event_key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_live_event_session_time
			ON live_realtime_event(session_id, captured_at)`,
		`CREATE TABLE IF NOT EXISTS live_revenue_snapshot (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			captured_at INTEGER NOT NULL,
			diamond_delta INTEGER NOT NULL DEFAULT 0,
			gift_count_delta INTEGER NOT NULL DEFAULT 0,
			fan_ticket_total INTEGER NOT NULL DEFAULT 0,
			pk_left_score INTEGER NOT NULL DEFAULT 0,
			pk_right_score INTEGER NOT NULL DEFAULT 0,
			source_event_key TEXT NOT NULL UNIQUE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_live_revenue_session_time
			ON live_revenue_snapshot(session_id, captured_at)`,
		`INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, unixepoch('now') * 1000)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("douyin live migration: %w", err)
		}
	}
	return tx.Commit()
}

func (s *douyinLiveStore) recordEvent(sessionID, eventKey, eventType string, capturedAt time.Time, payload interface{}) (bool, error) {
	if s == nil || s.db == nil || sessionID == "" || eventKey == "" {
		return false, nil
	}
	raw, err := json.Marshal(sanitizeDouyinSample(payload))
	if err != nil {
		return false, err
	}
	result, err := s.db.Exec(`INSERT OR IGNORE INTO live_realtime_event
		(session_id, event_key, event_type, captured_at, payload_json) VALUES(?,?,?,?,?)`,
		sessionID, eventKey, eventType, capturedAt.UnixMilli(), string(raw))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *douyinLiveStore) recordMetric(snapshot douyinMetricSnapshot) error {
	if s == nil || s.db == nil || snapshot.SessionID == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO live_metric_snapshot
		(session_id, captured_at, online_count, online_source, total_viewers, like_count, raw_metrics_json)
		VALUES(?,?,?,?,?,?,?)`, snapshot.SessionID, snapshot.CapturedAt.UnixMilli(), snapshot.OnlineCount,
		snapshot.OnlineSource, snapshot.TotalViewers, snapshot.LikeCount, snapshot.RawMetricsJSON)
	return err
}

func (s *douyinLiveStore) recordRevenue(snapshot douyinRevenueSnapshot) error {
	if s == nil || s.db == nil || snapshot.SessionID == "" || snapshot.SourceEventKey == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO live_revenue_snapshot
		(session_id, captured_at, diamond_delta, gift_count_delta, fan_ticket_total, pk_left_score, pk_right_score, source_event_key)
		VALUES(?,?,?,?,?,?,?,?)`, snapshot.SessionID, snapshot.CapturedAt.UnixMilli(), snapshot.DiamondDelta,
		snapshot.GiftCountDelta, snapshot.FanTicketTotal, snapshot.PKLeftScore, snapshot.PKRightScore, snapshot.SourceEventKey)
	return err
}

func (s *douyinLiveStore) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
