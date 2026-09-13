package weverse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Weverse settings are separate from config.json so refreshed credentials and
// monitoring cursors cannot be overwritten by other platform config saves.
type Subscription struct {
	ID               string   `json:"id"`
	GroupID          int64    `json:"groupId"`
	CommunityID      int64    `json:"communityId"`
	CommunityName    string   `json:"communityName"`
	Slug             string   `json:"slug"`
	MemberIDs        []string `json:"memberIds"`
	MemberNames      []string `json:"memberNames"`
	Posts            bool     `json:"posts"`
	Comments         bool     `json:"comments"`
	Live             bool     `json:"live"`
	Translate        bool     `json:"translate"`
	AtAll            bool     `json:"atAll"`
	AtAllMemberIDs   []string `json:"atAllMemberIds,omitempty"`
	AtAllMemberNames []string `json:"atAllMemberNames,omitempty"`
	Enabled          bool     `json:"enabled"`
}
type Settings struct {
	Enabled       bool           `json:"enabled"`
	PollSeconds   int            `json:"pollSeconds"`
	ProxyURL      string         `json:"proxyUrl,omitempty"`
	Subscriptions []Subscription `json:"subscriptions"`
}
type Session struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	DeviceID     string `json:"deviceId"`
}
type Status struct {
	LastCheck   string `json:"lastCheck,omitempty"`
	LastSuccess string `json:"lastSuccess,omitempty"`
	Error       string `json:"error,omitempty"`
	Events      int    `json:"events"`
}

var fileMu sync.Mutex

func Dir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "storage", "weverse")
}
func Read[T any](dir, name string, v *T) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func Write(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".weverse-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}
func LoadSettings(dir string) (Settings, error) {
	s := Settings{PollSeconds: 60, Subscriptions: []Subscription{}}
	err := Read(dir, "settings.json", &s)
	return s, err
}
func SaveSettings(dir string, s Settings) error {
	if s.PollSeconds < 30 || s.PollSeconds > 3600 {
		return fmt.Errorf("检查间隔需为 30–3600 秒")
	}
	seen := map[string]bool{}
	for _, v := range s.Subscriptions {
		if v.ID == "" || seen[v.ID] || v.GroupID <= 0 || v.CommunityID <= 0 || !slugRE.MatchString(v.Slug) {
			return fmt.Errorf("订阅的团体、群号或标识无效")
		}
		seen[v.ID] = true
		if !v.Posts && !v.Comments && !v.Live {
			return fmt.Errorf("至少选择一种提醒")
		}
		if len(v.MemberIDs) != len(v.MemberNames) {
			return fmt.Errorf("成员配置不匹配")
		}
		if len(v.AtAllMemberIDs) != len(v.AtAllMemberNames) {
			return fmt.Errorf("@全体成员的成员配置不匹配")
		}
		for _, id := range v.AtAllMemberIDs {
			if !idRE.MatchString(id) {
				return fmt.Errorf("@全体成员的成员标识无效")
			}
		}
		for _, id := range v.MemberIDs {
			if !idRE.MatchString(id) {
				return fmt.Errorf("成员标识无效")
			}
		}
	}
	if err := validateProxy(s.ProxyURL); err != nil {
		return err
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	return Write(dir, "settings.json", s)
}
func ImportSession(dir string, s Session) error {
	s.AccessToken = strings.TrimSpace(s.AccessToken)
	s.RefreshToken = strings.TrimSpace(s.RefreshToken)
	if s.AccessToken == "" && s.RefreshToken == "" {
		return fmt.Errorf("尚未获取到 Weverse 登录态，请先在浏览器登录")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	unlock, err := sessionLock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	return Write(dir, "session.json", s)
}

// The admin server and bot are separate processes. Serialize token rotation with
// browser imports so concurrent requests cannot overwrite a newly rotated token.
func sessionLock(ctx context.Context, dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "session.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		locked, err := trySessionLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() { releaseSessionLock(f); f.Close() }, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (s Subscription) MentionsAll(memberID string) bool {
	if !s.AtAll {
		return false
	}
	if len(s.AtAllMemberIDs) == 0 {
		return true
	}
	for _, id := range s.AtAllMemberIDs {
		if id == memberID {
			return true
		}
	}
	return false
}
