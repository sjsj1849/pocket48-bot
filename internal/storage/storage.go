package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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
