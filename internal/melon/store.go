package melon

import (
	"fmt"
	"net/url"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"regexp"
	"strings"
)

const Hearts2HeartsArtistID = "4096106"

func Dir(configPath string) string {
	return filepath.Join(filepath.Dir(weverse.Dir(configPath)), "melon")
}

func Read[T any](dir, name string, value *T) error { return weverse.Read(dir, name, value) }
func Write(dir, name string, value any) error      { return weverse.Write(dir, name, value) }

type Event struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	URL      string   `json:"url"`
	Time     int64    `json:"time"`
	Images   []string `json:"images"`
	ArtistID string   `json:"artistId"`
	Author   string   `json:"author,omitempty"`
	Avatar   string   `json:"avatar,omitempty"`
}

type Artist struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
	URL    string `json:"url"`
}

type Subscription struct {
	ID               string   `json:"id"`
	ArtistID         string   `json:"artistId"`
	ArtistName       string   `json:"artistName"`
	GroupID          int64    `json:"groupId"`
	Enabled          bool     `json:"enabled"`
	Releases         bool     `json:"releases"`
	Magazines        bool     `json:"magazines"`
	Photos           bool     `json:"photos"`
	ArtistNotes      bool     `json:"artistNotes"`
	MusicWave        bool     `json:"musicWave"`
	AtAll            bool     `json:"atAll"`
	AtAllAuthorNames []string `json:"atAllAuthorNames,omitempty"`
}

type Settings struct {
	Enabled              bool           `json:"enabled"`
	PollSeconds          int            `json:"pollSeconds"`
	MusicWavePollSeconds int            `json:"musicWavePollSeconds"`
	ProxyURL             string         `json:"proxyURL,omitempty"`
	Subscriptions        []Subscription `json:"subscriptions"`
}

type Status struct {
	LastCheck   string            `json:"lastCheck"`
	LastSuccess string            `json:"lastSuccess"`
	StartedAt   string            `json:"startedAt"`
	Events      int               `json:"events"`
	Forwarded   int               `json:"forwarded"`
	Error       string            `json:"error,omitempty"`
	Targets     map[string]string `json:"targets"`
}

func LoadSettings(dir string) (Settings, error) {
	settings := Settings{PollSeconds: 120, MusicWavePollSeconds: 10, Subscriptions: []Subscription{}}
	err := Read(dir, "settings.json", &settings)
	if settings.MusicWavePollSeconds == 0 {
		settings.MusicWavePollSeconds = 10
	}
	return settings, err
}

func NormalizeArtistID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.User != nil || (u.Host != "melon.com" && u.Host != "www.melon.com") {
			return "", fmt.Errorf("请填写 Melon 艺人编号或艺人页链接")
		}
		value = u.Query().Get("artistId")
	}
	if !regexp.MustCompile(`^[0-9]{1,12}$`).MatchString(value) {
		return "", fmt.Errorf("Melon 艺人编号格式不正确")
	}
	return value, nil
}

func ValidateSettings(settings Settings) error {
	if settings.PollSeconds < 60 || settings.PollSeconds > 3600 {
		return fmt.Errorf("检查间隔应为 60–3600 秒")
	}
	if settings.MusicWavePollSeconds < 5 || settings.MusicWavePollSeconds > 60 {
		return fmt.Errorf("Music Wave 检查间隔应为 5–60 秒")
	}
	if settings.ProxyURL != "" {
		u, err := url.Parse(settings.ProxyURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return fmt.Errorf("代理地址格式不正确")
		}
	}
	if len(settings.Subscriptions) > 100 {
		return fmt.Errorf("Melon 最多支持 100 个订阅")
	}
	ids := map[string]bool{}
	targets := map[string]bool{}
	for i := range settings.Subscriptions {
		sub := &settings.Subscriptions[i]
		artistID, err := NormalizeArtistID(sub.ArtistID)
		if err != nil {
			return err
		}
		sub.ArtistID = artistID
		if sub.ID == "" || ids[sub.ID] || sub.GroupID <= 0 {
			return fmt.Errorf("订阅编号或 QQ 群号不正确")
		}
		ids[sub.ID] = true
		target := fmt.Sprintf("%s:%d", artistID, sub.GroupID)
		if targets[target] {
			return fmt.Errorf("同一艺人在同一 QQ 群只能添加一个订阅")
		}
		targets[target] = true
		if !sub.Releases && !sub.Magazines && !sub.Photos && !sub.ArtistNotes && !sub.MusicWave {
			return fmt.Errorf("至少选择一种 Melon 内容")
		}
		if len(sub.AtAllAuthorNames) > 20 {
			return fmt.Errorf("@全体成员的成员名单最多 20 人")
		}
		for _, name := range sub.AtAllAuthorNames {
			if len([]rune(strings.TrimSpace(name))) > 30 {
				return fmt.Errorf("@全体成员的成员名称过长")
			}
		}
	}
	return nil
}

func (s Subscription) MentionsAll(author string) bool {
	if !s.AtAll {
		return false
	}
	if len(s.AtAllAuthorNames) == 0 {
		return true
	}
	author = strings.ToUpper(strings.TrimSpace(author))
	for _, name := range s.AtAllAuthorNames {
		name = strings.ToUpper(strings.TrimSpace(name))
		if name != "" && (author == name || strings.Contains(author, "("+name+")")) {
			return true
		}
	}
	return false
}

func SaveSettings(dir string, settings Settings) error {
	if err := ValidateSettings(settings); err != nil {
		return err
	}
	return Write(dir, "settings.json", settings)
}

func Matches(sub Subscription, event Event) bool {
	if sub.ArtistID != event.ArtistID {
		return false
	}
	switch event.Kind {
	case "album", "video", "song":
		return sub.Releases
	case "magazine":
		return sub.Magazines
	case "photo":
		return sub.Photos
	case "artist_note":
		return sub.ArtistNotes
	case "music_wave":
		return sub.MusicWave
	default:
		return false
	}
}
