package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Config describes the local archive rotation defaults shown by the bot's
// status command. The current public repository does not contain the former
// private COS writer, so the implementation intentionally remains local-only.
type Config struct {
	MaxLines      int
	MaxBytes      int64
	FlushInterval int
}

type Cursor struct {
	LastMsgID   string `json:"last_msg_id,omitempty"`
	LastMsgTime int64  `json:"last_msg_time"`
}

type QChatIdentity struct {
	Account   string `json:"account"`
	UserID    int64  `json:"user_id"`
	Nickname  string `json:"nickname,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

type PocketActivity struct {
	Type         string `json:"type"`
	MemberRoomID int64  `json:"member_room_id"`
	MemberUserID int64  `json:"member_user_id"`
	MemberName   string `json:"member_name"`
	TargetRoomID int64  `json:"target_room_id,omitempty"`
	TargetName   string `json:"target_name,omitempty"`
	At           int64  `json:"at"`
	Duration     int64  `json:"duration,omitempty"`
}

type Storage struct {
	dir    string
	cosDir string
	cfg    Config
	mu     sync.Mutex
}

func NewStorage(dir, cosDir string) *Storage {
	return &Storage{
		dir:    dir,
		cosDir: cosDir,
		cfg: Config{
			MaxLines:      1000,
			MaxBytes:      5 * 1024 * 1024,
			FlushInterval: 60,
		},
	}
}

func (s *Storage) cursorPath(roomID int64) string {
	return filepath.Join(s.dir, "cursors", fmt.Sprintf("%d.json", roomID))
}

func (s *Storage) GetCursor(roomID int64) (*Cursor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.cursorPath(roomID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func (s *Storage) SaveCursor(roomID int64, messageID string, messageTime int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.cursorPath(roomID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Never regress: concurrent REST/QChat writers must only move forward.
	if existing, err := os.ReadFile(path); err == nil {
		var prev Cursor
		if json.Unmarshal(existing, &prev) == nil && messageTime > 0 && prev.LastMsgTime > messageTime {
			return nil
		}
		// Keep previous message id when caller only supplies a newer time with empty id.
		if strings.TrimSpace(messageID) == "" && strings.TrimSpace(prev.LastMsgID) != "" && messageTime >= prev.LastMsgTime {
			messageID = prev.LastMsgID
		}
	}
	data, err := json.Marshal(Cursor{LastMsgID: messageID, LastMsgTime: messageTime})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Storage) qchatIdentityPath(roomID int64) string {
	return filepath.Join(s.dir, "qchat-identities", fmt.Sprintf("%d.json", roomID))
}

func (s *Storage) GetQChatIdentity(roomID int64) (*QChatIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.qchatIdentityPath(roomID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var identity QChatIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return nil, err
	}
	return &identity, nil
}

func (s *Storage) SaveQChatIdentity(roomID int64, identity QChatIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.qchatIdentityPath(roomID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Storage) AppendPocketActivity(activity PocketActivity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if activity.At == 0 {
		activity.At = time.Now().UnixMilli()
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, "pocket-activity.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	encoded, err := json.Marshal(activity)
	if err != nil {
		return err
	}
	_, err = file.Write(append(encoded, '\n'))
	return err
}

func (s *Storage) PocketActivities(memberRoomID int64, limit int) ([]PocketActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	file, err := os.Open(filepath.Join(s.dir, "pocket-activity.jsonl"))
	if os.IsNotExist(err) {
		return []PocketActivity{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	items := make([]PocketActivity, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var item PocketActivity
		if json.Unmarshal(scanner.Bytes(), &item) != nil || (memberRoomID != 0 && item.MemberRoomID != memberRoomID) {
			continue
		}
		items = append(items, item)
		if len(items) > limit {
			items = items[1:]
		}
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	return items, scanner.Err()
}

func (s *Storage) IsCOSAvailable() bool {
	if s.cosDir == "" {
		return false
	}
	info, err := os.Stat(s.cosDir)
	return err == nil && info.IsDir()
}

func (s *Storage) GetConfig() Config { return s.cfg }

func (s *Storage) GetArchiveDir() string {
	if s.IsCOSAvailable() {
		return s.cosDir
	}
	return s.dir
}

func (s *Storage) QueueLen() int { return 0 }

func (s *Storage) RetryQueuedMessages() error { return nil }
