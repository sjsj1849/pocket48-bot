package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Config struct {
	Address  string         `json:"address"`
	APIKey   string         `json:"apiKey"`
	QQ       QQConfig       `json:"qq"`
	Feishu   FeishuConfig   `json:"feishu"`
	Telegram TelegramConfig `json:"telegram"`
	Targets  []Target       `json:"targets"`
	Routes   []Route        `json:"routes"`
}

type QQConfig struct {
	Enabled     bool   `json:"enabled"`
	WSURL       string `json:"wsUrl"`
	AccessToken string `json:"accessToken,omitempty"`
}

type FeishuConfig struct {
	Enabled           bool   `json:"enabled"`
	AppID             string `json:"appId"`
	AppSecret         string `json:"appSecret,omitempty"`
	UploadConcurrency int    `json:"uploadConcurrency"`
	SmallVideoBytes   int64  `json:"smallVideoBytes"`
	VideoWaitSeconds  int    `json:"videoWaitSeconds"`
}

type TelegramConfig struct {
	Enabled  bool   `json:"enabled"`
	BotToken string `json:"botToken,omitempty"`
}

type Target struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	Kind        string `json:"kind"`
	Address     string `json:"address"`
	Favorite    bool   `json:"favorite"`
	Description string `json:"description,omitempty"`
}

type Route struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Project        string   `json:"project"`
	Event          string   `json:"event"`
	LegacyPlatform string   `json:"legacyPlatform,omitempty"`
	LegacyKind     string   `json:"legacyKind,omitempty"`
	LegacyAddress  string   `json:"legacyAddress,omitempty"`
	TargetIDs      []string `json:"targetIds"`
	Enabled        bool     `json:"enabled"`
}

func DefaultConfig() Config {
	return Config{
		Address: "127.0.0.1:8790",
		QQ:      QQConfig{Enabled: true, WSURL: "ws://127.0.0.1:3001"},
		Feishu:  FeishuConfig{UploadConcurrency: 3, SmallVideoBytes: 15 << 20, VideoWaitSeconds: 5},
		Targets: []Target{}, Routes: []Route{},
	}
}

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, cfg: DefaultConfig()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.cfg); err != nil {
		return nil, err
	}
	s.applyDefaultsLocked()
	return s, nil
}

func (s *Store) applyDefaultsLocked() {
	if strings.TrimSpace(s.cfg.Address) == "" {
		s.cfg.Address = "127.0.0.1:8790"
	}
	if s.cfg.Feishu.UploadConcurrency <= 0 {
		s.cfg.Feishu.UploadConcurrency = 3
	}
	if s.cfg.Feishu.SmallVideoBytes <= 0 {
		s.cfg.Feishu.SmallVideoBytes = 15 << 20
	}
	if s.cfg.Feishu.VideoWaitSeconds <= 0 {
		s.cfg.Feishu.VideoWaitSeconds = 5
	}
	if s.cfg.Targets == nil {
		s.cfg.Targets = []Target{}
	}
	if s.cfg.Routes == nil {
		s.cfg.Routes = []Route{}
	}
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.cfg)
	var copy Config
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (s *Store) Update(cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.applyDefaultsLocked()
	return s.saveLocked()
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
