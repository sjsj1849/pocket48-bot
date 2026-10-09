package tiktokmonitor

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"pocket48-bot/internal/weverse"
)

// Dir 把 TikTok 的配置与其它平台放在同一层 storage 下。
//
// ★ 为什么要独立 settings.json（2026-10-04 面板补齐时新增）：
// 之前 TikTok 的订阅寄居在 config.json 的 TIKTOK_SUBSCRIPTIONS 里，
// 于是面板里根本没有 TikTok 这一页 —— 不是前端漏了按钮，而是压根没有
// 独立配置文件可读可写。抖音/B站/X/IG/Melon 都有 storage/<平台>/settings.json，
// TikTok 是唯一例外。现在补齐，口径与它们一致。
func Dir(configPath string) string {
	return filepath.Join(filepath.Dir(weverse.Dir(configPath)), "tiktok")
}

func Read[T any](dir, name string, v *T) error { return weverse.Read(dir, name, v) }
func Write(dir, name string, v any) error      { return weverse.Write(dir, name, v) }

// Subscription 是一个 TikTok 账号的监控订阅。
type Subscription struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"displayName,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
	TargetIDs   []string `json:"targetIds,omitempty"`
	Enabled     bool     `json:"enabled"`
	// AtAll 是否 @全体成员。与其它平台同义。
	AtAll bool `json:"atAll"`
}

// Settings 是 storage/tiktok/settings.json 的顶层结构。
type Settings struct {
	Enabled bool `json:"enabled"`
	// PollSeconds 作品轮询间隔（秒）。
	//
	// ★ 建议不低于 600：TikTok 的 api/post/item_list 连请求 5-6 次后
	// 会持续返回 0 字节且约 5 分钟不恢复。压到 300 秒实测可跑，
	// 再往下有把限流窗口拉长到一直好不了的风险。
	PollSeconds   int            `json:"pollSeconds"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// Status 是 status.json，供面板显示最近扫描结果。
type Status struct {
	LastCheck   string                    `json:"lastCheck"`
	LastSuccess string                    `json:"lastSuccess"`
	StartedAt   string                    `json:"startedAt"`
	Events      int                       `json:"events"`
	Forwarded   int                       `json:"forwarded"`
	Error       string                    `json:"error,omitempty"`
	Targets     map[string]string         `json:"targets"`
	PerAccount  map[string]*AccountStatus `json:"perAccount,omitempty"`
}

// AccountStatus 是单个账号的运行状态（数据来自 state.json 的游标）。
type AccountStatus struct {
	Username string `json:"username"`
	// DisplayName 面板展示名。可能为空（settings 里没填昵称）。
	DisplayName string `json:"displayName,omitempty"`
	Ready       bool   `json:"ready"`
	LastScan    string `json:"lastScan,omitempty"`
	LastSuccess string `json:"lastSuccess,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	FailCount   int    `json:"failCount"`
	SeenCount   int    `json:"seenCount"`
}

const (
	DefaultPollSeconds = 900
	MinPollSeconds     = 300
	MaxPollSeconds     = 7200
	MaxSubscriptions   = 50
)

func LoadSettings(dir string) (Settings, error) {
	s := Settings{
		PollSeconds:   DefaultPollSeconds,
		Subscriptions: []Subscription{},
	}
	e := Read(dir, "settings.json", &s)
	if s.PollSeconds <= 0 {
		s.PollSeconds = DefaultPollSeconds
	}
	return s, e
}

func SaveSettings(dir string, s Settings) error {
	if err := ValidateSettings(s); err != nil {
		return err
	}
	return Write(dir, "settings.json", s)
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.]{1,30}$`)

// Username 接受裸用户名（不含 @）或 TikTok 主页/作品链接，返回用户名。
//
// 接收链接是因为面板上用户经常直接粘贴视频链接来添加订阅。
func Username(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("请填写 TikTok 用户名或主页链接")
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Scheme == "" {
			return "", fmt.Errorf("链接格式不正确")
		}
		host := strings.ToLower(u.Hostname())
		host = strings.TrimPrefix(host, "www.")
		host = strings.TrimPrefix(host, "m.")
		if host != "tiktok.com" && host != "vm.tiktok.com" && !strings.HasSuffix(host, ".tiktok.com") {
			return "", fmt.Errorf("请填写 TikTok 用户名或主页链接")
		}
		// 作品链接形如 /@user/video/123 或 /t/ZTxxxxx/
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		for _, part := range parts {
			part = strings.TrimPrefix(part, "@")
			if part == "" || part == "video" || part == "t" {
				continue
			}
			if usernamePattern.MatchString(part) {
				value = part
				break
			}
		}
		if value == "" || strings.Contains(value, "://") {
			return "", fmt.Errorf("链接里没有找到 TikTok 用户名")
		}
	}
	value = strings.TrimPrefix(value, "@")
	if !usernamePattern.MatchString(value) {
		return "", fmt.Errorf("TikTok 用户名只能包含字母、数字、下划线和点")
	}
	return value, nil
}

func ValidateSettings(s Settings) error {
	if s.PollSeconds < MinPollSeconds || s.PollSeconds > MaxPollSeconds {
		return fmt.Errorf("检查间隔应为 %d–%d 秒（TikTok 接口限流较紧，不建议更快）", MinPollSeconds, MaxPollSeconds)
	}
	if len(s.Subscriptions) > MaxSubscriptions {
		return fmt.Errorf("TikTok 最多支持 %d 个订阅", MaxSubscriptions)
	}
	ids := map[string]bool{}
	names := map[string]bool{}
	for i := range s.Subscriptions {
		sub := &s.Subscriptions[i]
		name, err := Username(sub.Username)
		if err != nil {
			return err
		}
		sub.Username = name
		if sub.ID == "" || ids[sub.ID] {
			return fmt.Errorf("订阅编号不正确")
		}
		ids[sub.ID] = true
		low := strings.ToLower(name)
		if names[low] {
			return fmt.Errorf("账号 @%s 重复添加", name)
		}
		names[low] = true
	}
	return nil
}

// Usernames 返回需要监控的账号名列表（跳过停用的）。
func (s Settings) Usernames() []string {
	out := make([]string, 0, len(s.Subscriptions))
	for _, sub := range s.Subscriptions {
		if !sub.Enabled || sub.Username == "" {
			continue
		}
		out = append(out, sub.Username)
	}
	return out
}

// TargetIDsOf 返回某账号配置的投递目标。
func (s Settings) TargetIDsOf(username string) []string {
	for _, sub := range s.Subscriptions {
		if strings.EqualFold(sub.Username, username) && len(sub.TargetIDs) > 0 {
			return sub.TargetIDs
		}
	}
	return nil
}
