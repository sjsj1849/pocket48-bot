package admin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
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
	CPUPercent    int    `json:"cpuPercent"`
	MemoryPercent int    `json:"memoryPercent"`
	DiskPercent   int    `json:"diskPercent"`
	Uptime        string `json:"uptime"`
	OS            string `json:"os"`
}

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
	services := buildServiceStates(lines)
	attentionItems := make([]attention, 0, 2)
	for _, item := range services {
		if item.ID == "douyin_im" && item.Status != "healthy" {
			attentionItems = append(attentionItems, attention{ID: "douyin-im", Title: "Douyin 网页 IM 尚未连接", Description: "账号已经登录；新版网页未开放群聊初始化接口，侧卡会继续低频探测", Action: "查看浏览器", Target: "browser"})
		}
	}
	writeJSON(w, http.StatusOK, overviewResponse{
		UpdatedAt: time.Now().Format("15:04:05"),
		Services:  services,
		Activity:  parseActivity(lines, 12),
		Resources: readResources(),
		Attention: attentionItems,
	})
}

func buildServiceStates(lines []string) []serviceState {
	active, uptime := serviceActiveAndUptime()
	states := []serviceState{
		{ID: "bot", Name: "Bot", Subtitle: "主控服务与任务调度", Status: choose(active, "healthy", "down"), StatusText: choose(active, "运行中", "已停止"), Uptime: uptime, Detail: "pocket48-bot.service", LastEvent: "任务调度正常"},
		{ID: "qchat", Name: "QChat", Subtitle: "口袋48实时消息", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "WebSocket", LastEvent: "等待连接状态"},
		{ID: "pocket_live", Name: "Live NIM", Subtitle: "口袋48直播礼物与结束事件", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "NIM Chatroom", LastEvent: "等待直播链路状态"},
		{ID: "napcat", Name: "NapCat", Subtitle: "QQ 协议适配器", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "127.0.0.1:3001", LastEvent: "等待连接状态"},
		{ID: "weibo", Name: "Weibo", Subtitle: "微博浏览器认证", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待认证状态"},
		{ID: "douyin", Name: "Douyin", Subtitle: "抖音账号与作品监控", Status: "attention", StatusText: "检查中", Uptime: uptime, Detail: "Browser auth", LastEvent: "等待登录状态"},
		{ID: "douyin_im", Name: "Douyin IM", Subtitle: "抖音群聊只读连接", Status: "attention", StatusText: "未连接", Uptime: uptime, Detail: "群号 296090848505", LastEvent: "等待网页 IM 初始化"},
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
		case strings.Contains(line, "[NAPCAT] Sending "):
			setLatest("napcat", "healthy", "运行中", "消息发送链路正常")
		case strings.Contains(line, "[Weibo-auth] status=healthy"):
			setLatest("weibo", "healthy", "已认证", "认证状态已刷新")
		case strings.Contains(line, "[Douyin] status=healthy"):
			setLatest("douyin", "healthy", "已登录", "浏览器账号登录态有效")
		case strings.Contains(line, "[Douyin] status=login_required"):
			setLatest("douyin", "attention", "待登录", "浏览器账号需要登录")
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
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		load, _ := strconv.ParseFloat(fields[0], 64)
		result.CPUPercent = clamp(int(load/float64(runtime.NumCPU())*100), 0, 100)
	}
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		values := map[string]int64{}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) >= 2 {
				values[strings.TrimSuffix(fields[0], ":")], _ = strconv.ParseInt(fields[1], 10, 64)
			}
		}
		if values["MemTotal"] > 0 {
			result.MemoryPercent = int((values["MemTotal"] - values["MemAvailable"]) * 100 / values["MemTotal"])
		}
	}
	var stat syscall.Statfs_t
	if syscall.Statfs("/", &stat) == nil && stat.Blocks > 0 {
		result.DiskPercent = int((stat.Blocks - stat.Bavail) * 100 / stat.Blocks)
	}
	if data, err := os.ReadFile("/proc/uptime"); err == nil {
		seconds, _ := strconv.ParseFloat(strings.Fields(string(data))[0], 64)
		result.Uptime = humanDuration(time.Duration(seconds) * time.Second)
	}
	return result
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
