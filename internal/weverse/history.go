package weverse

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// History retains originals independently of the realtime deduplication cursor.
// Forwarded records provide context across sessions; observed records support
// reports, including the first scan which is not sent to QQ.
type History struct{ db *sql.DB }

func OpenHistory(dir string) (*History, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS events (community_id INTEGER NOT NULL, event_id TEXT NOT NULL, post_id TEXT NOT NULL, kind TEXT NOT NULL, member_id TEXT NOT NULL, event_time INTEGER NOT NULL, original TEXT NOT NULL, PRIMARY KEY(community_id,event_id));
CREATE INDEX IF NOT EXISTS events_period ON events(community_id,event_time);
CREATE INDEX IF NOT EXISTS events_thread ON events(community_id,post_id,event_time);
CREATE TABLE IF NOT EXISTS forwarded (subscription_id TEXT NOT NULL, community_id INTEGER NOT NULL,event_id TEXT NOT NULL,PRIMARY KEY(subscription_id,community_id,event_id));
CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS members (community_id INTEGER NOT NULL,member_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(community_id,member_id));`); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec(`INSERT OR IGNORE INTO metadata(key,value) VALUES('recordingStarted',?)`, time.Now().Format(time.RFC3339)); err != nil {
		db.Close()
		return nil, err
	}
	return &History{db: db}, nil
}
func (h *History) Close() error { return h.db.Close() }

func (h *History) Record(events []Event, forwardedSubscription string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if e.ID == "" || e.PostID == "" || e.MemberID == "" || e.Time <= 0 {
			continue
		}
		// Playback URLs expire and can contain signatures; context/reporting need
		// attachment metadata rather than temporary download credentials.
		e.Videos = append([]VideoAttachment(nil), e.Videos...)
		for i := range e.Videos {
			e.Videos[i].URL = ""
			e.Videos[i].Error = ""
		}
		// Partial API payloads must not erase previously observed engagement counts.
		var oldJSON string
		if err := tx.QueryRow("SELECT original FROM events WHERE community_id=? AND event_id=?", e.CommunityID, e.ID).Scan(&oldJSON); err == nil {
			var old Event
			if json.Unmarshal([]byte(oldJSON), &old) == nil {
				if e.PostComments == nil {
					e.PostComments = old.PostComments
					e.CommentsAt = old.CommentsAt
				}
				if e.PostLikes == nil {
					e.PostLikes = old.PostLikes
					e.LikesAt = old.LikesAt
				}
				if e.MetricsAt == 0 {
					e.MetricsAt = old.MetricsAt
				}
				if e.LiveDuration == 0 {
					e.LiveDuration = old.LiveDuration
				}
				if len(e.LiveParticipants) == 0 {
					e.LiveParticipants = old.LiveParticipants
				}
			}
		}
		original, err := json.Marshal(e)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO events(community_id,event_id,post_id,kind,member_id,event_time,original) VALUES(?,?,?,?,?,?,?) ON CONFLICT(community_id,event_id) DO UPDATE SET original=excluded.original`, e.CommunityID, e.ID, e.PostID, e.Kind, e.MemberID, e.Time, string(original))
		if err != nil {
			return err
		}
		// An ending/replay enriches the original start, including cross-month lives.
		if (e.Kind == "live_end" || e.Kind == "live_replay") && e.LiveDuration > 0 {
			var original string
			if err := tx.QueryRow("SELECT original FROM events WHERE community_id=? AND event_id=?", e.CommunityID, "live:"+e.PostID).Scan(&original); err == nil {
				var start Event
				if json.Unmarshal([]byte(original), &start) == nil {
					start.LiveDuration = e.LiveDuration
					start.LiveEndedAt = e.LiveEndedAt
					if len(e.LiveParticipants) > 0 {
						start.LiveParticipants = e.LiveParticipants
					}
					b, err := json.Marshal(start)
					if err != nil {
						return err
					}
					if _, err = tx.Exec("UPDATE events SET original=? WHERE community_id=? AND event_id=?", string(b), e.CommunityID, start.ID); err != nil {
						return err
					}
				}
			}
		}
		if e.Author != "" {
			if _, err = tx.Exec(`INSERT INTO members VALUES(?,?,?) ON CONFLICT(community_id,member_id) DO UPDATE SET name=excluded.name`, e.CommunityID, e.MemberID, e.Author); err != nil {
				return err
			}
		}
		if forwardedSubscription != "" {
			if _, err = tx.Exec(`INSERT OR IGNORE INTO forwarded VALUES(?,?,?)`, forwardedSubscription, e.CommunityID, e.ID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (h *History) SaveMembers(cid int64, members []Member) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, m := range members {
		if _, err = tx.Exec(`INSERT INTO members VALUES(?,?,?) ON CONFLICT(community_id,member_id) DO UPDATE SET name=excluded.name`, cid, m.ID, m.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (h *History) ForwardedPost(sub string, cid int64, post string) ([]Event, error) {
	rows, err := h.db.Query(`SELECT e.original FROM events e JOIN forwarded f ON f.community_id=e.community_id AND f.event_id=e.event_id WHERE f.subscription_id=? AND e.community_id=? AND e.post_id=? ORDER BY e.event_time,e.event_id`, sub, cid, post)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readHistoryRows(rows)
}
func (h *History) Period(cid int64, start, end time.Time) ([]Event, error) {
	rows, err := h.db.Query(`SELECT original FROM events WHERE community_id=? AND event_time>=? AND event_time<? ORDER BY event_time,event_id`, cid, start.UnixMilli(), end.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readHistoryRows(rows)
}
func readHistoryRows(rows *sql.Rows) ([]Event, error) {
	events := []Event{}
	for rows.Next() {
		var raw string
		var e Event
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, fmt.Errorf("历史原文记录损坏")
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
func (h *History) RecordingStarted() (time.Time, error) {
	var raw string
	err := h.db.QueryRow(`SELECT value FROM metadata WHERE key='recordingStarted'`).Scan(&raw)
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, raw)
}
func (h *History) Members(cid int64) ([]Member, error) {
	rows, err := h.db.Query(`SELECT member_id,name FROM members WHERE community_id=? ORDER BY name`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Name); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func AIHistory(events []Event, current []AIEntry) []AIEntry {
	newIDs := map[string]bool{}
	for _, entry := range current {
		newIDs[entry.ID] = true
	}
	entries := []AIEntry{}
	for _, e := range events {
		if e.Kind != "comment" || newIDs[e.ID] {
			continue
		}
		entries = append(entries, aiEntry(e))
	}
	return entries
}
func aiEntry(e Event) AIEntry {
	return AIEntry{ID: e.ID, Author: e.Author, AuthorMemberID: e.MemberID, ParentAuthor: e.ParentAuthor, ParentMemberID: e.ParentMemberID, ParentProfileType: e.ParentProfileType, ParentBody: e.ParentBody, Body: e.Body, ImageCount: len(e.Images), PostID: e.PostID, ParentCommentID: e.ParentCommentID, Time: e.Time}
}
