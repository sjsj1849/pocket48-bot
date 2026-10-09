package weverse

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
CREATE TABLE IF NOT EXISTS members (community_id INTEGER NOT NULL,member_id TEXT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(community_id,member_id));
CREATE TABLE IF NOT EXISTS live_assignments (community_id INTEGER NOT NULL,post_id TEXT NOT NULL,participants TEXT NOT NULL,confirmed_at TEXT NOT NULL,PRIMARY KEY(community_id,post_id));`); err != nil {
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

type LiveAssignment struct {
	CommunityID  int64    `json:"communityId"`
	PostID       string   `json:"postId"`
	Title        string   `json:"title"`
	CoverURL     string   `json:"coverUrl,omitempty"`
	HostID       string   `json:"hostId"`
	HostName     string   `json:"hostName"`
	StartedAt    int64    `json:"startedAt"`
	Duration     int64    `json:"duration"`
	Participants []string `json:"participants"`
	Confirmed    bool     `json:"confirmed"`
	ConfirmedAt  string   `json:"confirmedAt,omitempty"`
}

type LockedPost struct {
	PostID string `json:"postId"`
	URL    string `json:"url"`
	Author string `json:"author,omitempty"`
	Time   int64  `json:"time"`
}

func (h *History) PendingLockedPosts(cid int64, known []PostPassword) ([]LockedPost, error) {
	resolved := map[string]bool{}
	for _, row := range known {
		if row.Password != "" {
			resolved[row.PostID] = true
		}
	}
	rows, err := h.db.Query(`SELECT original FROM events WHERE community_id=? AND kind IN ('post','moment') ORDER BY event_time DESC`, cid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LockedPost{}
	seen := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var event Event
		if json.Unmarshal([]byte(raw), &event) != nil || !event.PasswordProtected || resolved[event.PostID] || seen[event.PostID] {
			continue
		}
		seen[event.PostID] = true
		out = append(out, LockedPost{PostID: event.PostID, URL: event.URL, Author: event.Author, Time: event.Time})
	}
	return out, rows.Err()
}

// LiveAssignments returns one row per observed live. Unconfirmed rows default
// to the publishing account in the editor, but do not count as confirmed solo
// lives in reports until an operator saves them.
func (h *History) LiveAssignments(cid int64, limit int) ([]LiveAssignment, error) {
	return h.liveAssignments(cid, limit, nil, 0, 0)
}

func (h *History) PendingLiveAssignments(cid int64, limit int) ([]LiveAssignment, error) {
	confirmed := false
	return h.liveAssignments(cid, limit, &confirmed, 0, 0)
}

func (h *History) LiveAssignmentsBetween(cid int64, start, end int64, limit int) ([]LiveAssignment, error) {
	return h.liveAssignments(cid, limit, nil, start, end)
}

func (h *History) HasLive(cid int64, postID string) bool {
	var found int
	return h.db.QueryRow(`SELECT 1 FROM events WHERE community_id=? AND post_id=? AND kind='live' LIMIT 1`, cid, postID).Scan(&found) == nil
}

func (h *History) liveAssignments(cid int64, limit int, confirmed *bool, start, end int64) ([]LiveAssignment, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	knownMembers := map[string]bool{}
	memberRows, err := h.db.Query(`SELECT member_id FROM members WHERE community_id=?`, cid)
	if err != nil {
		return nil, err
	}
	for memberRows.Next() {
		var id string
		if err := memberRows.Scan(&id); err != nil {
			memberRows.Close()
			return nil, err
		}
		knownMembers[id] = true
	}
	if err := memberRows.Close(); err != nil {
		return nil, err
	}
	query := `SELECT e.original,COALESCE(a.participants,''),COALESCE(a.confirmed_at,'')
FROM events e LEFT JOIN live_assignments a ON a.community_id=e.community_id AND a.post_id=e.post_id
WHERE e.community_id=? AND e.kind='live'`
	args := []any{cid}
	if confirmed != nil {
		if *confirmed {
			query += ` AND a.confirmed_at IS NOT NULL`
		} else {
			query += ` AND a.confirmed_at IS NULL`
		}
	}
	if start > 0 && end > start {
		query += ` AND e.event_time>=? AND e.event_time<?`
		args = append(args, start, end)
	}
	query += ` ORDER BY (a.confirmed_at IS NOT NULL),e.event_time DESC LIMIT ?`
	args = append(args, limit)
	rows, err := h.db.Query(strings.TrimSpace(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LiveAssignment{}
	for rows.Next() {
		var raw, participantsJSON, confirmedAt string
		if err := rows.Scan(&raw, &participantsJSON, &confirmedAt); err != nil {
			return nil, err
		}
		var event Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("历史直播记录损坏")
		}
		participants := []string{}
		if knownMembers[event.MemberID] {
			participants = []string{event.MemberID}
		}
		if confirmedAt != "" {
			participants = nil
			if err := json.Unmarshal([]byte(participantsJSON), &participants); err != nil {
				return nil, fmt.Errorf("直播人员配置损坏")
			}
		}
		out = append(out, LiveAssignment{CommunityID: cid, PostID: event.PostID, Title: event.Body,
			CoverURL: event.CoverURL, HostID: event.MemberID, HostName: event.Author,
			StartedAt: event.LiveStartedAt, Duration: event.LiveDuration, Participants: participants,
			Confirmed: confirmedAt != "", ConfirmedAt: confirmedAt})
	}
	return out, rows.Err()
}

func (h *History) SaveLiveAssignment(cid int64, postID string, participants []string) error {
	encoded, err := json.Marshal(participants)
	if err != nil {
		return err
	}
	result, err := h.db.Exec(`INSERT INTO live_assignments(community_id,post_id,participants,confirmed_at) VALUES(?,?,?,?)
ON CONFLICT(community_id,post_id) DO UPDATE SET participants=excluded.participants,confirmed_at=excluded.confirmed_at`, cid, postID, string(encoded), time.Now().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("直播人员配置未保存")
	}
	return nil
}

func (h *History) applyLiveAssignments(cid int64, events []Event) error {
	rows, err := h.db.Query(`SELECT post_id,participants FROM live_assignments WHERE community_id=?`, cid)
	if err != nil {
		return err
	}
	defer rows.Close()
	assignments := map[string][]string{}
	for rows.Next() {
		var postID, raw string
		if err := rows.Scan(&postID, &raw); err != nil {
			return err
		}
		var ids []string
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return fmt.Errorf("直播人员配置损坏")
		}
		assignments[postID] = ids
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range events {
		if ids, ok := assignments[events[i].PostID]; ok {
			events[i].LiveParticipants = append([]string(nil), ids...)
			events[i].LiveParticipantsConfirmed = true
		}
	}
	return nil
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
