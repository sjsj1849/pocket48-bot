package xmonitor

import (
	"fmt"
	"net/url"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"regexp"
	"strings"
)

func Dir(configPath string) string             { return filepath.Join(filepath.Dir(weverse.Dir(configPath)), "x") }
func Read[T any](dir, name string, v *T) error { return weverse.Read(dir, name, v) }
func Write(dir, name string, v any) error      { return weverse.Write(dir, name, v) }

type User struct {
	ID        string   `json:"id"`
	Username  string   `json:"username"`
	Name      string   `json:"name"`
	Avatar    string   `json:"avatar"`
	Protected bool     `json:"protected"`
	PinnedIDs []string `json:"pinnedIds,omitempty"`
}
type Variant struct {
	URL     string `json:"url"`
	Bitrate int64  `json:"bitrate"`
}
type Media struct {
	Kind       string    `json:"kind"`
	URL        string    `json:"url,omitempty"`
	Cover      string    `json:"cover,omitempty"`
	Variants   []Variant `json:"variants,omitempty"`
	DurationMS int64     `json:"durationMS,omitempty"`
	Error      string    `json:"error,omitempty"`
}
type Event struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	Author          User    `json:"author"`
	Body            string  `json:"body"`
	URL             string  `json:"url"`
	Time            int64   `json:"time"`
	Media           []Media `json:"media"`
	ReplyToID       string  `json:"replyToId"`
	Reposted        *Event  `json:"reposted,omitempty"`
	Quoted          *Event  `json:"quoted,omitempty"`
	MediaOrderKnown bool    `json:"mediaOrderKnown"`
}
type Subscription struct {
	ID       string `json:"id"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Name     string `json:"name"`
	GroupID  int64  `json:"groupId"`
	Enabled  bool   `json:"enabled"`
	Posts    bool   `json:"posts"`
	Replies  bool   `json:"replies"`
	Reposts  bool   `json:"reposts"`
	AtAll    bool   `json:"atAll"`
}
type Settings struct {
	Enabled       bool           `json:"enabled"`
	PollSeconds   int            `json:"pollSeconds"`
	ProxyURL      string         `json:"proxyURL"`
	Subscriptions []Subscription `json:"subscriptions"`
}
type Status struct {
	LastCheck   string            `json:"lastCheck"`
	LastSuccess string            `json:"lastSuccess"`
	StartedAt   string            `json:"startedAt"`
	Events      int               `json:"events"`
	Forwarded   int               `json:"forwarded"`
	Error       string            `json:"error,omitempty"`
	ErrorCode   string            `json:"errorCode,omitempty"`
	Targets     map[string]string `json:"targets"`
}

func LoadSettings(dir string) (Settings, error) {
	s := Settings{PollSeconds: 120, Subscriptions: []Subscription{}}
	e := Read(dir, "settings.json", &s)
	return s, e
}
func Username(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") {
		u, e := url.Parse(value)
		if e != nil || u.Scheme != "https" || (u.Hostname() != "x.com" && u.Hostname() != "www.x.com" && u.Hostname() != "twitter.com" && u.Hostname() != "www.twitter.com") {
			return "", fmt.Errorf("请填写 X 用户名或主页链接")
		}
		value = strings.Trim(u.Path, "/")
	}
	value = strings.TrimPrefix(value, "@")
	if !regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`).MatchString(value) {
		return "", fmt.Errorf("请填写 X 用户名或主页链接")
	}
	return value, nil
}
func ValidateSettings(s Settings) error {
	if s.PollSeconds < 30 || s.PollSeconds > 3600 {
		return fmt.Errorf("检查间隔应为 30–3600 秒")
	}
	if s.ProxyURL != "" {
		u, e := url.Parse(s.ProxyURL)
		if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return fmt.Errorf("代理地址格式不正确")
		}
	}
	if len(s.Subscriptions) > 100 {
		return fmt.Errorf("X 最多支持 100 个订阅")
	}
	ids := map[string]bool{}
	targets := map[string]bool{}
	for i := range s.Subscriptions {
		sub := &s.Subscriptions[i]
		name, e := Username(sub.Username)
		if e != nil {
			return e
		}
		sub.Username = name
		target := fmt.Sprintf("%s:%d", strings.ToLower(name), sub.GroupID)
		if targets[target] {
			return fmt.Errorf("同一账号在同一 QQ 群只能添加一个订阅")
		}
		targets[target] = true
		if sub.ID == "" || ids[sub.ID] || sub.GroupID <= 0 {
			return fmt.Errorf("订阅编号或 QQ 群号不正确")
		}
		ids[sub.ID] = true
		if sub.UserID != "" && !regexp.MustCompile(`^[0-9]+$`).MatchString(sub.UserID) {
			return fmt.Errorf("X 用户编号不正确")
		}
		if !sub.Posts && !sub.Replies && !sub.Reposts {
			return fmt.Errorf("至少选择一种内容")
		}
	}
	return nil
}
func Matches(s Subscription, e Event) bool {
	return s.Enabled && (s.UserID == "" || s.UserID == e.Author.ID) && strings.EqualFold(s.Username, e.Author.Username) && ((s.Posts && (e.Kind == "post" || e.Kind == "quote")) || (s.Replies && e.Kind == "reply") || (s.Reposts && e.Kind == "repost"))
}

func SaveSettings(dir string, s Settings) error {
	if err := ValidateSettings(s); err != nil {
		return err
	}
	for i := range s.Subscriptions {
		s.Subscriptions[i].Username, _ = Username(s.Subscriptions[i].Username)
	}
	return Write(dir, "settings.json", s)
}
