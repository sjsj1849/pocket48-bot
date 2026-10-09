// Package bilibili implements the B 站 (bilibili) platform: UP 主动态监控与
// 直播间开播/关播通知。数据全部来自 B 站公开 HTTP 接口，不依赖登录态，
// 也不需要浏览器 sidecar。
package bilibili

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"pocket48-bot/internal/weverse"
)

// Dir keeps B 站 settings next to the other platform stores so a config.json
// save can never clobber refreshed cursors.
func Dir(configPath string) string {
	return filepath.Join(filepath.Dir(weverse.Dir(configPath)), "bilibili")
}
func Read[T any](dir, name string, v *T) error { return weverse.Read(dir, name, v) }
func Write(dir, name string, v any) error      { return weverse.Write(dir, name, v) }

// Up is the resolved UP 主 identity (uid + display name + avatar).
type Up struct {
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

// LiveRoom is the subset of room/v1/Room/get_status_info_by_uids we care about.
type LiveRoom struct {
	UID        string `json:"uid"`
	RoomID     int64  `json:"roomId"`
	LiveStatus int    `json:"liveStatus"` // 0 未开播 / 1 直播中 / 2 轮播
	Title      string `json:"title"`
	Cover      string `json:"cover"`
	Online     int64  `json:"online"`
}

// URL is the human-facing live room link.
func (r LiveRoom) URL() string {
	if r.RoomID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://live.bilibili.com/%d", r.RoomID)
}

// Dynamic is a normalised space dynamic. Kind drives the notification wording:
// video / draw(图文) / text(纯文字) / forward(转发) / article.
type Dynamic struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Author string `json:"author"`
	Title  string `json:"title"`
	Text   string `json:"text"`
	Cover  string `json:"cover"`
	URL    string `json:"url"`
	Time   int64  `json:"time"` // unix ms；opus 流不带发布时间时为 0
	Length string `json:"length,omitempty"`
	// KindLabel is the human label used in notifications (新视频 / 新动态 / 新专栏).
	KindLabel string `json:"kindLabel,omitempty"`
	// Seconds 是视频总时长（秒）。长视频检测按它过滤，0 表示未知。
	Seconds int `json:"seconds,omitempty"`
	// View 是播放量，-1 表示接口未返回。
	View int64 `json:"view,omitempty"`
}

type Subscription struct {
	ID        string   `json:"id"`
	UID       string   `json:"uid"`
	Name      string   `json:"name"`
	Avatar    string   `json:"avatar"`
	GroupID   int64    `json:"groupId"`
	TargetIDs []string `json:"targetIds,omitempty"`
	Enabled   bool     `json:"enabled"`
	// Video 监控视频投稿（频率受限，走独立的低速节奏）。
	Video bool `json:"video"`
	// MinVideoSeconds 只推送时长不低于该值的投稿（秒）。0 表示不过滤。
	// 团综类长视频是核心诉求，而短视频往往与抖音重复，故按此过滤。
	MinVideoSeconds int `json:"minVideoSeconds,omitempty"`
	// Dynamics 监控图文动态与专栏文章。
	Dynamics bool `json:"dynamics"`
	Live     bool `json:"live"`
	AtAll    bool `json:"atAll"`
}

type Settings struct {
	Enabled bool `json:"enabled"`
	// PollSeconds 控制图文动态/专栏扫描节奏（秒）。
	PollSeconds int `json:"pollSeconds"`
	// VideoPollSeconds 控制视频投稿扫描节奏（秒）。
	//
	// ★ 2026-10-04 从 600 放宽到 120（实测依据）：
	// 原来的「必须放慢到 10 分钟」是**匿名态**下的结论 —— x/space/wbi/arc/search
	// 匿名请求稳定返回 -403/-352/412。而配置里的登录 Cookie 让这条链路走登录态，
	// 配额宽松得多。实测连续 24 轮、每 10 秒一轮（约 4 分钟、约 100 次请求）：
	//   - wbi/arc/search 登录态：0/24 轮失败，耗时 620–950ms
	//   - seasons_series_list 匿名：1/24 轮失败（-504 服务调用超时），
	//     但该来源本就只作补漏，wbi 拿到内容时整轮照常成功
	// 也就是说 10 秒一轮都扛得住，120 秒（比实测密度低 5 倍）非常安全。
	// 真实影响：视频推送延迟上限从 10 分钟降到 2 分钟
	//（此前「7:00 更新 7:09 推送」就是撞在 600 秒周期上，不是 bug）。
	VideoPollSeconds int `json:"videoPollSeconds"`
	// LivePollSeconds 控制直播状态扫描节奏（秒）。
	LivePollSeconds int `json:"livePollSeconds"`
	// Cookie 是可选的 SESSDATA 等登录 Cookie；登录态配额更宽松，可显著降低限流。
	Cookie        string         `json:"cookie,omitempty"`
	Subscriptions []Subscription `json:"subscriptions"`
}

type Status struct {
	NextRetryAt string            `json:"nextRetryAt,omitempty"`
	LastCheck   string            `json:"lastCheck"`
	LastSuccess string            `json:"lastSuccess"`
	StartedAt   string            `json:"startedAt"`
	Events      int               `json:"events"`
	Forwarded   int               `json:"forwarded"`
	Error       string            `json:"error,omitempty"`
	Targets     map[string]string `json:"targets"`
}

const (
	DefaultPollSeconds = 180
	// ★ 2026-10-04 从 120 进一步压到 60（下限本来就是 60）。
	//   实测登录态 x/space/wbi/arc/search 连续 24 轮、每 10 秒一轮
	//   零失败（耗时 620-950ms），60 秒比实测密度低 6 倍，非常安全。
	//   真实影响：B 站视频推送延迟上限从 2 分钟降到 1 分钟。
	DefaultVideoPollSeconds = 60
	DefaultLivePollSeconds  = 60
	MinPollSeconds          = 60
	MinVideoPollSeconds     = 60
	MinLivePollSeconds      = 20
	MaxPollSeconds          = 3600
	MaxSubscriptions        = 100
)

func LoadSettings(dir string) (Settings, error) {
	s := Settings{
		PollSeconds:      DefaultPollSeconds,
		VideoPollSeconds: DefaultVideoPollSeconds,
		LivePollSeconds:  DefaultLivePollSeconds,
		Subscriptions:    []Subscription{},
	}
	e := Read(dir, "settings.json", &s)
	if s.PollSeconds <= 0 {
		s.PollSeconds = DefaultPollSeconds
	}
	if s.VideoPollSeconds <= 0 {
		s.VideoPollSeconds = DefaultVideoPollSeconds
	}
	if s.LivePollSeconds <= 0 {
		s.LivePollSeconds = DefaultLivePollSeconds
	}
	return s, e
}

var uidPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// ResolveUID accepts a bare numeric UID or a space/live 主页链接 and returns the
// numeric UID. Numeric UID is the only stable key the APIs accept.
func ResolveUID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("请填写 B 站 UID 或空间主页链接")
	}
	if uidPattern.MatchString(value) {
		return value, nil
	}
	if strings.Contains(value, "://") {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			return "", fmt.Errorf("空间主页链接格式不正确")
		}
		host := strings.ToLower(u.Hostname())
		switch {
		case strings.HasSuffix(host, "bilibili.com"):
		default:
			return "", fmt.Errorf("请填写 B 站 UID 或空间主页链接")
		}
		for _, part := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if uidPattern.MatchString(part) {
				return part, nil
			}
		}
		// 形如 ?mid=123 或 live.bilibili.com/123 之外的兜底
		if mid := u.Query().Get("mid"); uidPattern.MatchString(mid) {
			return mid, nil
		}
		return "", fmt.Errorf("链接里没有找到 UID，请直接填写数字 UID")
	}
	if strings.HasPrefix(value, "@") {
		return "", fmt.Errorf("请填写数字 UID 或空间主页链接（暂不支持按昵称搜索）")
	}
	return "", fmt.Errorf("请填写 B 站 UID 或空间主页链接")
}

func ValidateSettings(s Settings) error {
	if s.PollSeconds < MinPollSeconds || s.PollSeconds > MaxPollSeconds {
		return fmt.Errorf("动态检查间隔应为 %d–%d 秒", MinPollSeconds, MaxPollSeconds)
	}
	if s.VideoPollSeconds < MinVideoPollSeconds || s.VideoPollSeconds > MaxPollSeconds {
		return fmt.Errorf("视频投稿检查间隔应为 %d–%d 秒（未配置登录 Cookie 时建议不低于 300 秒）", MinVideoPollSeconds, MaxPollSeconds)
	}
	if s.LivePollSeconds < MinLivePollSeconds || s.LivePollSeconds > MaxPollSeconds {
		return fmt.Errorf("直播检查间隔应为 %d–%d 秒", MinLivePollSeconds, MaxPollSeconds)
	}
	if len(s.Subscriptions) > MaxSubscriptions {
		return fmt.Errorf("B 站最多支持 %d 个订阅", MaxSubscriptions)
	}
	ids := map[string]bool{}
	targets := map[string]bool{}
	for i := range s.Subscriptions {
		sub := &s.Subscriptions[i]
		uid, err := ResolveUID(sub.UID)
		if err != nil {
			return err
		}
		sub.UID = uid
		if sub.ID == "" || ids[sub.ID] {
			return fmt.Errorf("订阅编号不正确")
		}
		ids[sub.ID] = true
		key := fmt.Sprintf("%s:%d", uid, sub.GroupID)
		if targets[key] {
			return fmt.Errorf("同一 UP 主在同一 QQ 群只能添加一个订阅")
		}
		targets[key] = true
		if !sub.Dynamics && !sub.Video && !sub.Live {
			return fmt.Errorf("至少选择「动态」「视频投稿」或「直播」其中一项")
		}
	}
	return nil
}

func SaveSettings(dir string, s Settings) error {
	if err := ValidateSettings(s); err != nil {
		return err
	}
	return Write(dir, "settings.json", s)
}

// UIDs returns the distinct UP 主 list that needs live polling.
func (s Settings) UIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, sub := range s.Subscriptions {
		if !sub.Enabled || !sub.Live || sub.UID == "" || seen[sub.UID] {
			continue
		}
		seen[sub.UID] = true
		out = append(out, sub.UID)
	}
	return out
}

// GroupIDOf is a helper for logs and target fallback.
func GroupIDOf(id string) int64 {
	value, _ := strconv.ParseInt(id, 10, 64)
	return value
}

// ParseDurationSeconds 把 B 站的 length 字段（"32:37" 或 "1:02:03"）解析成秒。
// arc/search 接口只给字符串时长，而长视频过滤靠 Seconds 判定，
// 解析不出就会让过滤失效（Seconds=0 直接跳过阈值比较）。
func ParseDurationSeconds(length string) int {
	length = strings.TrimSpace(length)
	if length == "" {
		return 0
	}
	total := 0
	for _, part := range strings.Split(length, ":") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return 0
		}
		total = total*60 + n
	}
	return total
}
