package tiktokmonitor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultUser 默认监控对象：心连心（Hearts2Hearts）。
	DefaultUser = "hearts2hearts"

	// DefaultLimit 单次拉取的作品数。
	// 实测 TikTok 的 item_list 接口连请求 5-6 次后会持续返回空响应，
	// 一次别拉太多。
	DefaultLimit = 20

	// TimelineTimeout 列表接口超时。
	//
	// ★ 2026-10-04 改回 60 秒：之前放宽到 120 秒是为了掩盖一个假故障 ——
	// sidecar 拿到数据后浏览器收尾挂死，进程迟迟不退出，把调用方一起拖超时。
	// 根因修掉后（Python 侧收尾与采集同 loop + 硬超时，Go 侧读到结果即 kill），
	// 端到端实测只要 5.7 秒。超时值应该反映「采集本身」的真实上限，
	// 而不是任何能跑通的时间 —— 超时放太宽会让真故障迟迟发现不了。
	// 仍留 60 秒而非更紧：2 核机器同时跑微信浏览器和 QQ 机器人，
	// 偶尔一次 20-30 秒不算异常。
	TimelineTimeout = 60 * time.Second

	// DownloadTimeout 下载超时。作品页 + 浏览器内 fetch + 分片回传，
	// 8.5MB 实测约 45 秒，2 核机器上留足余量。
	DownloadTimeout = 180 * time.Second

	// DetailTimeout 单条作品详情超时（链接提取用）。
	//
	// 比 DownloadTimeout 宽：detail 要多解析一次 DOM 拿正文/封面，
	// 而且带视频下载时要「作品页 + 页内 fetch + 分片回传」。
	// 8.5MB 实测约 45 秒，给到 180 秒留足余量。
	DetailTimeout = 180 * time.Second

	// MaxSeen 上限，避免 state.json 无限增长。
	MaxSeen = 2048
)

// Cursor 记录一个订阅的已见作品。
type Cursor struct {
	Username    string    `json:"username"`
	UserID      string    `json:"userId"`
	Ready       bool      `json:"ready"`
	HighWater   int64     `json:"highWater"` // 已处理过的最大 createTime
	Seen        []string  `json:"seen"`      // 已处理的视频 id
	LastScan    time.Time `json:"lastScan"`
	LastSuccess time.Time `json:"lastSuccess"`
	FailCount   int       `json:"failCount"`
	LastError   string    `json:"lastError"`
}

// State 是 state.json 的顶层结构。
type State struct {
	Cursors map[string]Cursor `json:"cursors"`
}

// LoadState 读取游标状态。文件缺失或损坏都按空状态处理 ——
// 采集状态坏了不该让监控整体停摆，最坏情况是重推一轮（跨平台去重会挡住）。
func LoadState(dir string) State {
	st := State{Cursors: map[string]Cursor{}}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return st
	}
	if json.Unmarshal(raw, &st) != nil || st.Cursors == nil {
		return State{Cursors: map[string]Cursor{}}
	}
	return st
}

// Save 原子写状态。先写 .tmp 再改名，避免进程被杀时留下半截 JSON。
func (s State) Save(dir string) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "state.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Pending 挑出「本轮该处理的新作品」：未见过、createTime 高于水位线、按时间正序。
//
// 首轮（!Ready）一律返回 nil：**第一次跑不要把 20 条历史作品全推一遍**。
func Pending(cursor Cursor, username string, videos []Video) []Video {
	if !cursor.Ready {
		return nil
	}
	if !strings.EqualFold(cursor.Username, username) {
		// 换了监控对象视为重新开始，否则会把旧对象的历史当新内容推出去。
		return nil
	}

	known := make(map[string]bool, len(cursor.Seen))
	for _, id := range cursor.Seen {
		known[id] = true
	}

	out := make([]Video, 0, len(videos))
	for _, v := range videos {
		if v.ID == "" {
			continue
		}
		if known[v.ID] || v.CreateTime <= cursor.HighWater {
			continue
		}
		out = append(out, v)
	}
	// 正序：先发老的，符合「时间线」语义。
	sort.Slice(out, func(i, j int) bool { return out[i].CreateTime < out[j].CreateTime })
	return out
}

// Advance 把**成功处理过**的作品并进游标。
//
// 关键纪律：只有真正发出去的才推进水位线。发送失败的如果也 Advance，
// 就永久丢失了 —— 这是监控类系统最常见的漏推原因。
func Advance(cursor Cursor, username string, videos []Video, processed []string) Cursor {
	// 沿用旧 Seen
	createTimes := map[string]int64{}
	for _, v := range videos {
		createTimes[v.ID] = v.CreateTime
	}

	high := cursor.HighWater
	seen := map[string]bool{}
	if strings.EqualFold(cursor.Username, username) {
		for _, id := range cursor.Seen {
			seen[id] = true
		}
	}

	for _, id := range processed {
		if _, err := strconv.ParseUint(id, 10, 64); err != nil {
			// id 异常就不登记，下次还会拉到，能看出问题而不是静默吞掉。
			continue
		}
		seen[id] = true
		if ts := createTimes[id]; ts > high {
			high = ts
		}
	}

	// 按发布时间倒序（新的在前），超限时从尾部丢最旧的。
	// createTimes 里没有的老条目退化成按 id 排 —— TikTok 用雪花号，
	// 高位是时间戳，单调递增，当排序兜底足够。
	type entry struct {
		id string
		ts int64
	}
	list := make([]entry, 0, len(seen))
	for id := range seen {
		ts := createTimes[id]
		if ts == 0 {
			if n, err := strconv.ParseUint(id, 10, 64); err == nil {
				ts = int64(n >> 22) // 雪花号高位是秒级时间戳
			}
		}
		list = append(list, entry{id: id, ts: ts})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].ts != list[j].ts {
			return list[i].ts > list[j].ts
		}
		return list[i].id > list[j].id
	})
	if len(list) > MaxSeen {
		list = list[:MaxSeen]
	}

	ids := make([]string, len(list))
	for i, e := range list {
		ids[i] = e.id
	}

	return Cursor{
		Username:    username,
		UserID:      cursor.UserID,
		Ready:       true,
		HighWater:   high,
		Seen:        ids,
		LastScan:    time.Now(),
		LastSuccess: time.Now(),
		FailCount:   0,
	}
}

// NoteFailure 记一次失败，但不推进水位线。
func NoteFailure(cursor Cursor, username string, reason string) Cursor {
	cursor.Username = username
	cursor.LastScan = time.Now()
	cursor.FailCount++
	cursor.LastError = reason
	return cursor
}

// Seconds 取视频秒数（四舍五入）。
//
// 跨平台去重要跟抖音、B站对齐。TikTok 列表接口给的 duration 是秒（float，
// 实测 116.266667），而抖音/B站那边是整数秒 —— 这里统一取整，
// 否则 116.266667 与 116 会被 dedupe 的 3 秒容差勉强兜住，
// 但登记侧和匹配侧的口径不一致，早晚出乱子。
func Seconds(v Video) int {
	if v.Duration <= 0 {
		return 0
	}
	return int(v.Duration + 0.5)
}

// ValidateUserName 校验 TikTok 用户名，防止拼进 URL 出意外。
// 只允许字母数字下划线点和连字符，长度 2-30。
func ValidateUserName(s string) error {
	s = strings.TrimPrefix(strings.TrimSpace(s), "@")
	if s == "" {
		return fmt.Errorf("TikTok 用户名不能为空")
	}
	if len(s) < 2 || len(s) > 30 {
		return fmt.Errorf("TikTok 用户名长度不合法")
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '-':
		default:
			return fmt.Errorf("TikTok 用户名含非法字符")
		}
	}
	return nil
}

// Link 生成作品链接，用于飞书卡片上的跳转按钮。
func Link(v Video) string {
	name := v.AuthorName
	if name == "" {
		name = DefaultUser
	}
	return fmt.Sprintf("https://www.tiktok.com/@%s/video/%s", name, v.ID)
}
