package admin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type serviceState struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Subtitle   string `json:"subtitle"`
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	Uptime     string `json:"uptime"`
	Detail     string `json:"detail"`
	LastEvent  string `json:"lastEvent"`
	LastTime   string `json:"lastTime"`
}

type activityItem struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

type resources struct {
	// Percents are 0–100 with one decimal so the meters visibly move on quiet hosts.
	CPUPercent    float64 `json:"cpuPercent"`
	MemoryPercent float64 `json:"memoryPercent"`
	DiskPercent   float64 `json:"diskPercent"`
	Uptime        string  `json:"uptime"`
	OS            string  `json:"os"`
}

// cpuSample keeps the previous /proc/stat snapshot so consecutive overview
// polls (5s) can compute a real instantaneous usage without sleeping every time.
var (
	cpuSampleMu    sync.Mutex
	lastCPUIdle    uint64
	lastCPUTotal   uint64
	lastCPUSampled time.Time
)

type overviewResponse struct {
	UpdatedAt string         `json:"updatedAt"`
	Services  []serviceState `json:"services"`
	Activity  []activityItem `json:"activity"`
	Resources resources      `json:"resources"`
	Attention []attention    `json:"attention"`
}

type attention struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Target      string `json:"target"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	lines, _ := tailLines(s.opts.LogPath, 5000)
	flags := loadOverviewFeatureFlags(s.opts.ConfigPath)
	services := buildServiceStates(lines, flags)
	writeJSON(w, http.StatusOK, overviewResponse{
		UpdatedAt: time.Now().Format("15:04:05"),
		Services:  services,
		Activity:  parseActivity(lines, 12),
		Resources: readResources(),
		Attention: buildOverviewAttention(services),
	})
}

// overviewFeatureFlags controls which platform cards appear on the overview page.
// Disabled platforms (e.g. XIAOHONGSHU_ENABLED=false) are omitted so the dashboard
// only reflects services actually in use.
type overviewFeatureFlags struct {
	DouyinEnabled      bool
	DouyinIMEnabled    bool
	XiaohongshuEnabled bool
}

func loadOverviewFeatureFlags(configPath string) overviewFeatureFlags {
	// Defaults: show core services; platform cards follow config (missing key = off for XHS,
	// on for douyin/im only if historically true — read JSON booleans explicitly).
	flags := overviewFeatureFlags{
		DouyinEnabled:      true,
		DouyinIMEnabled:    true,
		XiaohongshuEnabled: false,
	}
	raw := map[string]json.RawMessage{}
	if err := readJSONFile(configPath, &raw); err != nil {
		return flags
	}
	readBool := func(key string, def bool) bool {
		enc, ok := raw[key]
		if !ok || len(enc) == 0 {
			return def
		}
		var v bool
		if err := json.Unmarshal(enc, &v); err != nil {
			return def
		}
		return v
	}
	flags.DouyinEnabled = readBool("DOUYIN_ENABLED", true)
	flags.DouyinIMEnabled = readBool("DOUYIN_IM_ENABLED", true) && flags.DouyinEnabled
	flags.XiaohongshuEnabled = readBool("XIAOHONGSHU_ENABLED", false)
	return flags
}

// buildOverviewAttention lists only items that need a human. Auto-heal states
// (e.g. Douyin IM reconnecting) stay on the service card but not here.
func buildOverviewAttention(services []serviceState) []attention {
	attentionItems := make([]attention, 0, 4)
	for _, item := range services {
		if item.Status == "healthy" {
			continue
		}
		switch item.ID {
		case "douyin_im":
			// Short reconnect / not-yet-connected is auto-heal territory (bot
			// watchdog restarts sidecar then bot). Only surface hard failures
			// or misconfig that actually need a human.
			if item.StatusText == "重连中" || item.StatusText == "未连接" {
				continue
			}
			title := "Douyin 网页 IM 异常"
			desc := item.LastEvent
			if item.StatusText == "待配置" {
				title = "Douyin 网页 IM 待配置"
				desc = "未唯一匹配目标群聊，请检查群号配置"
			} else if item.StatusText == "连接异常" {
				title = "Douyin 网页 IM 连接异常"
			}
			if desc == "" {
				desc = item.StatusText
			}
			attentionItems = append(attentionItems, attention{
				ID: "douyin-im", Title: title, Description: desc, Action: "查看浏览器", Target: "browser",
			})
		case "xiaohongshu":
			title := "小红书监控异常"
			desc := item.LastEvent
			switch item.StatusText {
			case "待登录":
				title = "小红书需要登录"
				desc = "浏览器账号需要重新扫码登录小红书"
			case "拉帖异常", "Cookie已有", "已就绪":
				title = "小红书无法拉帖"
				desc = "Cookie 可能仍在，但 notes 不可用（登录半残/风控）；请在面板浏览器重新登录并确认能看到笔记"
			case "认证异常":
				title = "小红书认证异常"
			}
			if desc == "" {
				desc = item.StatusText + " — " + item.LastEvent
			}
			attentionItems = append(attentionItems, attention{
				ID: "xiaohongshu", Title: title, Description: desc, Action: "查看浏览器", Target: "browser",
			})
		case "douyin":
			if item.StatusText == "待登录" || item.Status == "down" {
				attentionItems = append(attentionItems, attention{
					ID: "douyin", Title: "抖音浏览器需要登录", Description: item.LastEvent, Action: "查看浏览器", Target: "browser",
				})
			}
		case "weibo":
			if item.Status == "down" || item.StatusText == "待登录" {
				attentionItems = append(attentionItems, attention{
					ID: "weibo", Title: "微博认证异常", Description: item.LastEvent, Action: "查看浏览器", Target: "browser",
				})
			}
		case "napcat":
			if item.Status == "down" {
				attentionItems = append(attentionItems, attention{
					ID: "napcat", Title: "NapCat/QQ 连接中断", Description: item.LastEvent, Action: "查看服务", Target: "services",
				})
			}
		}
	}
	return attentionItems
}

func buildServiceStates(lines []string, flags overviewFeatureFlags) []serviceState {
	active, uptime := serviceActiveAndUptime()
	states := []serviceState{
		{ID: "bot", Name: "Bot", Subtitle: "主控服务与任务调度", Status: choose(active, "healthy", "down"), StatusText: choose(active, "运行中", "已停止"), Uptime: uptime, Detail: "pocket48-bot.service", LastEvent: "任务调度正常"},
		{ID: "qchat", Name: "QChat", Subtitle: "口袋48实时消息", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "WebSocket", LastEvent: "等待连接状态"},
		{ID: "pocket_live", Name: "Live NIM", Subtitle: "口袋48直播礼物与结束事件", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "NIM Chatroom", LastEvent: "等待直播链路状态"},
		{ID: "napcat", Name: "NapCat", Subtitle: "QQ 协议适配器", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "127.0.0.1:3001", LastEvent: "等待连接状态"},
		{ID: "weibo", Name: "Weibo", Subtitle: "微博浏览器认证", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待认证状态"},
	}
	if flags.DouyinEnabled {
		states = append(states, serviceState{ID: "douyin", Name: "Douyin", Subtitle: "抖音账号与作品监控", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待登录状态"})
	}
	if flags.XiaohongshuEnabled {
		states = append(states, serviceState{ID: "xiaohongshu", Name: "Xiaohongshu", Subtitle: "小红书帖子与开播提醒", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待登录状态"})
	}
	if flags.DouyinIMEnabled {
		states = append(states, serviceState{ID: "douyin_im", Name: "Douyin IM", Subtitle: "抖音群聊只读连接", Status: "attention", StatusText: "未连接", Uptime: uptime, Detail: "群号 296090848505", LastEvent: "等待网页 IM 初始化"})
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		timeText := logTime(line)
		setLatest := func(id, status, statusText, event string) {
			for index := range states {
				if states[index].ID == id && states[index].LastTime == "" {
					states[index].Status = status
					states[index].StatusText = statusText
					states[index].LastEvent = event
					states[index].LastTime = timeText
				}
			}
		}
		switch {
		case strings.Contains(line, "[NIM-health] qchat=connected"):
			setLatest("qchat", "healthy", "运行中", "实时消息心跳正常")
		case strings.Contains(line, "[NIM-health] qchat=disconnected"):
			setLatest("qchat", "attention", "重连中", "实时消息心跳中断")
		case strings.Contains(line, "[NIM-live-health] status=connected"):
			setLatest("pocket_live", "healthy", "直播中", "直播礼物与结束事件链路已连接")
		case strings.Contains(line, "[NIM-live-health] status=idle"):
			setLatest("pocket_live", "healthy", "待命", "当前无直播，实时链路待命")
		case strings.Contains(line, "[NIM-live-health] status=reconnecting"):
			setLatest("pocket_live", "attention", "重连中", "直播实时链路正在恢复")
		case strings.Contains(line, "[NIM-live] active live discovery failed"), strings.Contains(line, "[NIM-live] GetLiveOne failed"):
			setLatest("pocket_live", "down", "发现异常", "直播发现接口调用失败")
		case strings.Contains(line, "[NIM-room] QChat connected"):
			setLatest("qchat", "healthy", "运行中", "WebSocket 已连接")
		case strings.Contains(line, "Connected to NapCat successfully"):
			setLatest("napcat", "healthy", "运行中", "会话连接正常")
		case strings.Contains(line, "NapCat read error (disconnected?)"):
			setLatest("napcat", "down", "连接中断", "WebSocket 已断开，正在自动重连")
		case strings.Contains(line, "Failed to connect to NapCat"):
			setLatest("napcat", "down", "连接中断", "无法连接 OneBot/llbot（127.0.0.1:3001），正在重试")
		case strings.Contains(line, "[NAPCAT] Sending "):
			setLatest("napcat", "healthy", "运行中", "消息发送链路正常")
		case strings.Contains(line, "[NapCat] status=disconnected"):
			setLatest("napcat", "down", "连接中断", "OneBot/llbot 已断开，正在自动重连")
		case strings.Contains(line, "[NapCat] status=connected"):
			setLatest("napcat", "healthy", "运行中", "OneBot/llbot 会话已恢复")
		case strings.Contains(line, "[Weibo-auth] status=healthy"):
			setLatest("weibo", "healthy", "已认证", "认证状态已刷新")
		// 抖音健康以作品监控 API 为准：能 HTTP 扫作品且 cookie=yes 即健康。
		// status=healthy/ready 仍识别，但会被高频 works scan 日志挤出 5000 行窗口。
		case strings.Contains(line, "douyin works scan via HTTP") && strings.Contains(line, "cookie=yes"):
			setLatest("douyin", "healthy", "运行中", "作品监控 API 正常（HTTP + Cookie）")
		case strings.Contains(line, "douyin works scan via HTTP") && strings.Contains(line, "cookie=no"):
			setLatest("douyin", "attention", "待登录", "作品扫描无 Cookie，需浏览器登录")
		case strings.Contains(line, "[Douyin] status=healthy"):
			setLatest("douyin", "healthy", "已登录", "浏览器账号登录态有效")
		case strings.Contains(line, "[Douyin] status=ready"):
			setLatest("douyin", "healthy", "运行中", "抖音作品监控已就绪")
		case strings.Contains(line, "[Douyin] status=login_required"):
			setLatest("douyin", "attention", "待登录", "浏览器账号需要登录")
		case strings.Contains(line, "[Douyin] status=login_error"):
			setLatest("douyin", "down", "认证异常", "浏览器登录状态检查失败")
		// Cookie-only "healthy/ready" is NOT enough — notes must load. Prefer failure
		// signals (scan bottom-up: first match wins, so put data-plane errors first).
		case strings.Contains(line, "[Xiaohongshu] status=notes_ok"):
			setLatest("xiaohongshu", "healthy", "运行中", "笔记列表可拉取")
		case strings.Contains(line, "[Xiaohongshu] status=login_required"):
			setLatest("xiaohongshu", "attention", "待登录", "浏览器账号需要登录")
		case strings.Contains(line, "[Xiaohongshu] status=login_error"):
			setLatest("xiaohongshu", "down", "认证异常", "浏览器登录状态检查失败")
		case strings.Contains(line, "[Xiaohongshu] status=degraded"),
			strings.Contains(line, "[Xiaohongshu] status=notes_unavailable"):
			setLatest("xiaohongshu", "attention", "拉帖异常", "登录 Cookie 可能有效，但笔记列表不可用")
		case strings.Contains(line, "[Xiaohongshu] account_error"),
			strings.Contains(line, "xiaohongshu blocked"),
			strings.Contains(line, "小红书需重新登录"),
			strings.Contains(line, "笔记列表暂不可用"),
			strings.Contains(line, "Account abnormal"):
			setLatest("xiaohongshu", "attention", "拉帖异常", "无法读取笔记/需重新登录或触发风控")
		case strings.Contains(line, "[Xiaohongshu] notes "):
			setLatest("xiaohongshu", "healthy", "运行中", "已收到笔记推送")
		case strings.Contains(line, "[Xiaohongshu] status=healthy"):
			// Cookie present only — show as attention until notes prove healthy.
			setLatest("xiaohongshu", "attention", "Cookie已有", "仅 Cookie 有效，尚未确认能拉 notes")
		case strings.Contains(line, "[Xiaohongshu] status=ready"):
			setLatest("xiaohongshu", "attention", "已就绪", "监控进程就绪，等待 notes 验证")
		case strings.Contains(line, "[Douyin-IM] status=connected"):
			setLatest("douyin_im", "healthy", "运行中", "群聊只读连接已建立")
		case strings.Contains(line, "[Douyin-IM] status=init_missing"):
			setLatest("douyin_im", "attention", "未连接", "新版网页未提供 IM 初始化接口")
		case strings.Contains(line, "[Douyin-IM] status=disconnected"):
			setLatest("douyin_im", "attention", "重连中", "群聊连接已断开，正在自动重连")
		case strings.Contains(line, "[Douyin-IM] status=group_not_found"), strings.Contains(line, "[Douyin-IM] status=group_ambiguous"):
			setLatest("douyin_im", "attention", "待配置", "未唯一匹配目标群聊")
		case strings.Contains(line, "[Douyin-IM] status=error"):
			setLatest("douyin_im", "down", "连接异常", "群聊连接发生错误")
		}
	}
	return states
}

func serviceActiveAndUptime() (bool, string) {
	status, _ := runCommand(3*time.Second, "systemctl", "is-active", botService)
	started, _ := runCommand(3*time.Second, "systemctl", "show", botService, "--property=ActiveEnterTimestamp", "--value")
	parsed, err := time.Parse("Mon 2006-01-02 15:04:05 MST", strings.TrimSpace(started))
	if err != nil {
		return status == "active", "—"
	}
	return status == "active", humanDuration(time.Since(parsed))
}

func parseActivity(lines []string, limit int) []activityItem {
	result := make([]activityItem, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(result) < limit; i-- {
		line := lines[i]
		if !interestingLog(line) {
			continue
		}
		level := "info"
		switch {
		case strings.Contains(line, "error"), strings.Contains(line, "失败"), strings.Contains(line, "异常"):
			level = "error"
		case strings.Contains(line, "warning"), strings.Contains(line, "待验证"), strings.Contains(line, "⚠"):
			level = "warning"
		case strings.Contains(line, "connected"), strings.Contains(line, "success"), strings.Contains(line, "正常"), strings.Contains(line, "healthy"):
			level = "success"
		}
		result = append(result, activityItem{Time: logTime(line), Level: level, Source: logSource(line), Message: cleanLogMessage(line)})
	}
	return result
}

func interestingLog(line string) bool {
	if strings.Contains(line, "resolved room") || strings.Contains(line, "Checking for UID") || strings.Contains(line, "active live discovery") {
		return false
	}
	return strings.Contains(line, "connected") || strings.Contains(line, "Connected") || strings.Contains(line, "status=") || strings.Contains(line, "Sending group message") || strings.Contains(line, "ordered message processed") || strings.Contains(line, "error") || strings.Contains(line, "失败") || strings.Contains(line, "异常")
}

func logTime(line string) string {
	if len(line) >= 19 && line[4] == '/' {
		return line[11:19]
	}
	return "—"
}

func logSource(line string) string {
	start := strings.Index(line, "[")
	end := strings.Index(line, "]")
	if start >= 0 && end > start {
		source := line[start+1 : end]
		if source == "INFO" || source == "Media" {
			return "Bot"
		}
		return source
	}
	return "Bot"
}

func cleanLogMessage(line string) string {
	if len(line) >= 20 && line[4] == '/' {
		line = strings.TrimSpace(line[20:])
	}
	if strings.HasPrefix(line, "[") {
		if end := strings.Index(line, "]"); end >= 0 {
			line = strings.TrimSpace(line[end+1:])
		}
	}
	if len([]rune(line)) > 120 {
		line = string([]rune(line)[:120]) + "…"
	}
	return line
}

func tailLines(path string, limit int) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	const maxRead = int64(2 << 20)
	start := info.Size() - maxRead
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	lines := make([]string, 0, limit)
	if start > 0 && scanner.Scan() {
		// Discard the first partial line.
	}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > limit {
			lines = lines[len(lines)-limit:]
		}
	}
	return lines, scanner.Err()
}

func readResources() resources {
	result := resources{OS: "Linux " + runtime.GOARCH}
	result.CPUPercent = readCPUPercent()
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		values := map[string]int64{}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 {
				values[strings.TrimSuffix(fields[0], ":")], _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
		if total := values["MemTotal"]; total > 0 {
			available := values["MemAvailable"]
			if available == 0 {
				available = values["MemFree"]
			}
			used := float64(total-available) * 100 / float64(total)
			result.MemoryPercent = round1(clampFloat(used, 0, 100))
		}
	}
	var stat syscall.Statfs_t
	if syscall.Statfs("/", &stat) == nil && stat.Blocks > 0 {
		used := float64(stat.Blocks-stat.Bavail) * 100 / float64(stat.Blocks)
		result.DiskPercent = round1(clampFloat(used, 0, 100))
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		seconds, _ := strconv.ParseFloat(strings.Fields(string(data))[0], 64)
		result.Uptime = humanDuration(time.Duration(seconds) * time.Second)
	}
	return result
}

// readCPUTimes returns idle and total jiffies from the aggregate "cpu " line.
func readCPUTimes() (idle, total uint64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal ...
		if len(fields) < 5 {
			return 0, 0, false
		}
		var sum uint64
		for i := 1; i < len(fields); i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				continue
			}
			sum += v
			if i == 4 { // idle
				idle = v
			}
			if i == 5 { // iowait counts as idle for usage
				idle += v
			}
		}
		return idle, sum, sum > 0
	}
	return 0, 0, false
}

// cpuUsageFromDelta converts two /proc/stat samples into a 0–100 usage percent.
func cpuUsageFromDelta(prevIdle, prevTotal, idle, total uint64) float64 {
	if total <= prevTotal {
		return 0
	}
	dTotal := total - prevTotal
	var dIdle uint64
	if idle >= prevIdle {
		dIdle = idle - prevIdle
	}
	if dIdle > dTotal {
		dIdle = dTotal
	}
	used := (1 - float64(dIdle)/float64(dTotal)) * 100
	return round1(clampFloat(used, 0, 100))
}

func readCPUPercent() float64 {
	idle, total, ok := readCPUTimes()
	if !ok {
		return 0
	}

	cpuSampleMu.Lock()
	needBootstrap := lastCPUTotal == 0 || time.Since(lastCPUSampled) > 30*time.Second
	if needBootstrap {
		// Store first sample, release lock, then sleep so we don't block other
		// overview readers holding the mutex for 200ms.
		lastCPUIdle, lastCPUTotal = idle, total
		lastCPUSampled = time.Now()
		cpuSampleMu.Unlock()
		time.Sleep(200 * time.Millisecond)
		idle2, total2, ok2 := readCPUTimes()
		cpuSampleMu.Lock()
		defer cpuSampleMu.Unlock()
		if !ok2 {
			return 0
		}
		// Prefer delta against the bootstrap sample we just took.
		usage := cpuUsageFromDelta(lastCPUIdle, lastCPUTotal, idle2, total2)
		lastCPUIdle, lastCPUTotal = idle2, total2
		lastCPUSampled = time.Now()
		return usage
	}

	usage := cpuUsageFromDelta(lastCPUIdle, lastCPUTotal, idle, total)
	lastCPUIdle, lastCPUTotal = idle, total
	lastCPUSampled = time.Now()
	cpuSampleMu.Unlock()
	return usage
}

func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func humanDuration(value time.Duration) string {
	if value < 0 {
		return "—"
	}
	days := int(value.Hours()) / 24
	hours := int(value.Hours()) % 24
	minutes := int(value.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d天 %d小时", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%d小时 %d分钟", hours, minutes)
	}
	return fmt.Sprintf("%d分钟", minutes)
}

func choose[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	limit = clamp(limit, 50, 1500)
	if limit == 50 && r.URL.Query().Get("limit") == "" {
		limit = 500
	}
	lines, err := tailLines(s.opts.LogPath, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	if query != "" {
		filtered := lines[:0]
		for _, line := range lines {
			if strings.Contains(strings.ToLower(line), query) {
				filtered = append(filtered, line)
			}
		}
		lines = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "updatedAt": time.Now().Format("15:04:05")})
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
