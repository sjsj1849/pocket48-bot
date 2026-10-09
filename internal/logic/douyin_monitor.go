package logic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"github.com/zalando/go-keyring"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/outbound"
)

type douyinAccountCommand struct {
	SecUserID     string `json:"secUserId"`
	ProfileURL    string `json:"profileUrl,omitempty"`
	Name          string `json:"name,omitempty"`
	LiveID        string `json:"liveId,omitempty"`
	LastAwemeTime int64  `json:"lastAwemeTime,omitempty"`
	WorksEnabled  bool   `json:"worksEnabled"`
	LiveEnabled   bool   `json:"liveEnabled"`
}

type douyinPost struct {
	ID         string   `json:"id"`
	SecUserID  string   `json:"secUserId"`
	Nickname   string   `json:"nickname"`
	Desc       string   `json:"desc"`
	CreateTime int64    `json:"createTime"`
	Type       string   `json:"type"`
	URL        string   `json:"url"`
	Cover      string   `json:"cover"`
	Images     []string `json:"images"`
	VideoURL   string   `json:"videoUrl"`
	// Duration 是视频时长（秒），2026-10-05 新增。
	//
	// ★ 用途：跨平台去重必须能在**下载前**判定 ——
	//   抖音接口的 video.duration 本来就在（毫秒），之前没往外传，
	//   于是只能下载后 ffprobe，那时要等几十秒到两分钟，判定早就来不及。
	//   有它才能做到：先比对，命中就跳过下载与视频发送，但文字封面照发。
	Duration        int      `json:"duration"`
	LivePhotoVideos []string `json:"livePhotoVideos,omitempty"`
}

type douyinBrowserEvent struct {
	Type             string       `json:"type"`
	SecUserID        string       `json:"secUserId"`
	ProfileURL       string       `json:"profileUrl"`
	Nickname         string       `json:"nickname"`
	LiveID           string       `json:"liveId"`
	Posts            []douyinPost `json:"posts"`
	ImageBase64      string       `json:"imageBase64"`
	ExpiresIn        int          `json:"expiresIn"`
	Status           string       `json:"status"`
	Message          string       `json:"message"`
	GroupName        string       `json:"groupName"`
	GroupNumber      string       `json:"groupNumber"`
	ConversationID   string       `json:"conversationId"`
	ConversationType int          `json:"conversationType"`
	OwnerUID         string       `json:"ownerUid"`
	SelfUID          string       `json:"selfUid"`
	SenderUID        string       `json:"senderUid"`
	SenderSecUID     string       `json:"senderSecUid"`
	SenderName       string       `json:"senderName"`
	SenderNickname   string       `json:"senderNickname"`
	SenderRemark     string       `json:"senderRemark"`
	ServerMessageID  string       `json:"serverMessageId"`
	MessageType      int          `json:"messageType"`
	CreateTime       int64        `json:"createTime"`
	ReceivedAt       int64        `json:"receivedAt"`
	QuotedName       string       `json:"quotedName"`
	QuotedText       string       `json:"quotedText"`
	QuotedSenderUID  string       `json:"quotedSenderUid"`
	Text             string       `json:"text"`
	Link             string       `json:"link"`
	Images           []string     `json:"images,omitempty"`
	VideoURL         string       `json:"videoUrl,omitempty"`
	LivePhotoVideos  []string     `json:"livePhotoVideos,omitempty"`
	Index            string       `json:"index"`
	IsSelfChat       bool         `json:"isSelfChat,omitempty"`
}

type douyinLiveState struct {
	SessionID                string           `json:"session_id"`
	LiveID                   string           `json:"live_id"`
	RoomID                   string           `json:"room_id"`
	Name                     string           `json:"name,omitempty"`
	Title                    string           `json:"title,omitempty"`
	DetectedStartedAt        time.Time        `json:"detected_started_at"`
	DetectedEndedAt          time.Time        `json:"detected_ended_at,omitempty"`
	LastUpdatedAt            time.Time        `json:"last_updated_at"`
	Online                   bool             `json:"online"`
	CurrentOnline            int64            `json:"current_online,omitempty"`
	PeakOnline               int64            `json:"peak_online,omitempty"`
	TotalAudience            int64            `json:"total_audience,omitempty"`
	LikeCount                int64            `json:"like_count,omitempty"`
	GiftCount                int64            `json:"gift_count,omitempty"`
	DiamondTotal             int64            `json:"diamond_total,omitempty"`
	DiamondAvailable         bool             `json:"diamond_available,omitempty"`
	FanTicketTotal           int64            `json:"fan_ticket_total,omitempty"`
	PKLeftScore              int64            `json:"pk_left_score,omitempty"`
	PKRightScore             int64            `json:"pk_right_score,omitempty"`
	EstimatedSoundWave       int64            `json:"estimated_sound_wave,omitempty"`
	SoundWaveAvailable       bool             `json:"sound_wave_available,omitempty"`
	GiftEventCount           int64            `json:"gift_event_count,omitempty"`
	ProcessedGiftMessageIDs  []string         `json:"processed_gift_message_ids,omitempty"`
	ComboGiftCounts          map[string]int64 `json:"combo_gift_counts,omitempty"`
	StartNotificationPending bool             `json:"start_notification_pending,omitempty"`
	EndNotificationPending   bool             `json:"end_notification_pending,omitempty"`
	StartNotificationQueued  map[string]bool  `json:"start_notification_queued,omitempty"`
	EndNotificationQueued    map[string]bool  `json:"end_notification_queued,omitempty"`
	// Sent fields are retained as aggregate/backward-compatible markers. New
	// sessions set them only after every current target is queued.
	StartNotificationSent bool `json:"start_notification_sent"`
	EndNotificationSent   bool `json:"end_notification_sent"`
}

type DouyinMonitor struct {
	cfg          *config.Config
	outbound     outbound.Sender
	notifyAdmins func(string)

	mu              sync.Mutex
	started         bool
	stopping        bool
	wg              sync.WaitGroup
	liveCmd         *exec.Cmd
	liveCancels     map[string]context.CancelFunc
	liveConnected   map[string]bool
	liveStates      map[string]*douyinLiveState
	livePersistedAt map[string]time.Time
	liveSessionDir  string
	liveStore       *douyinLiveStore
	liveStorePath   string
	livePersistMu   sync.Mutex
	liveCookie      func(string) (string, error)
	enqueueGroup    func(int64, interface{}) bool
	browser         *WeiboAuthBridge
	imConversations map[string]douyinIMTarget // conversationID -> target
	imSelfUID       string
	imConnected     bool

	// Captcha window: count account_error "验证码中间页" hits that block post lists.
	captchaHits      []time.Time
	captchaLastAlert time.Time

	// Works health: alert when every enabled creator has failed inside one
	// polling window, then notify again only after every creator recovers.
	worksFailures   map[string]time.Time
	worksAlertSince time.Time
	// worksProxyRecoverAt rate-limits mihomo failover when every creator fails
	// with a browser-network error. Routes must pass a Douyin-specific probe;
	// XHS-Auto is preferred and DIRECT is the fallback.
	worksProxyRecoverAt time.Time
	proxyControllerURL  string
	proxyHTTPClient     *http.Client

	// IM disconnect watchdog: silent sidecar/bot restart → email only if still down after auto-heal.
	imDisconnectedAt      time.Time
	imLastDisconnectAlert time.Time
	imSidecarRestartAt    time.Time
	imBotRestartAt        time.Time
	imWatchdogStop        chan struct{}
	imWatchdogOnce        sync.Once
	requestBotRestart     func(reason string)
}

type douyinIMTarget struct {
	ConversationID string
	OwnerUID       string
	GroupNumber    string
	GroupName      string
}

func (m *DouyinMonitor) SetBrowserBridge(browser *WeiboAuthBridge) {
	m.mu.Lock()
	m.browser = browser
	m.mu.Unlock()
}

// SetRequestBotRestart wires a process-level restart callback (systemd Restart=always).
func (m *DouyinMonitor) SetRequestBotRestart(fn func(reason string)) {
	m.mu.Lock()
	m.requestBotRestart = fn
	m.mu.Unlock()
}

func NewDouyinMonitor(cfg *config.Config, sender outbound.Sender, notifyAdmins func(string)) *DouyinMonitor {
	m := &DouyinMonitor{
		cfg:             cfg,
		outbound:        sender,
		notifyAdmins:    notifyAdmins,
		liveCancels:     make(map[string]context.CancelFunc),
		liveConnected:   make(map[string]bool),
		liveStates:      make(map[string]*douyinLiveState),
		livePersistedAt: make(map[string]time.Time),
		liveSessionDir:  douyinLiveSessionStoreDir,
		liveStorePath:   douyinLiveDatabasePath,
		liveCookie: func(account string) (string, error) {
			return keyring.Get(douyinLiveCookieService, account)
		},
		imConversations:    make(map[string]douyinIMTarget),
		worksFailures:      make(map[string]time.Time),
		proxyControllerURL: "http://127.0.0.1:19090",
		proxyHTTPClient:    &http.Client{Timeout: 8 * time.Second},
		imWatchdogStop:     make(chan struct{}),
	}
	m.loadLiveStatesFromDisk()
	return m
}

func parseDouyinCommandLine(line, fallback string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		line = strings.TrimSpace(fallback)
	}
	if line == "" {
		return nil, nil
	}
	var out []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			out = append(out, current.String())
			current.Reset()
		}
	}
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '\'' || r == '"'):
			quote = r
		case quote == 0 && unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("command contains an unclosed quote")
	}
	flush()
	if len(out) > 0 && (strings.HasSuffix(strings.ToLower(out[0]), ".mjs") || strings.HasSuffix(strings.ToLower(out[0]), ".js")) {
		out = append([]string{"node"}, out...)
	}
	return out, nil
}

func (m *DouyinMonitor) accountsLocked() []douyinAccountCommand {
	seen := make(map[string]douyinAccountCommand)
	for _, group := range m.cfg.DouyinSubscriptions {
		for key, item := range group {
			if item == nil || item.Disabled || item.WorksDisabled && item.LiveDisabled {
				continue
			}
			sec := strings.TrimSpace(item.SecUserID)
			if sec == "" {
				sec = strings.TrimSpace(key)
			}
			if sec == "" {
				continue
			}
			current := seen[sec]
			current.SecUserID = sec
			if current.ProfileURL == "" {
				current.ProfileURL = item.ProfileURL
			}
			if current.Name == "" {
				current.Name = item.Name
			}
			if current.LiveID == "" {
				current.LiveID = item.LiveID
			}
			current.WorksEnabled = current.WorksEnabled || !item.WorksDisabled
			current.LiveEnabled = current.LiveEnabled || !item.LiveDisabled
			seen[sec] = current
		}
	}
	result := make([]douyinAccountCommand, 0, len(seen))
	for _, item := range seen {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SecUserID < result[j].SecUserID })
	return result
}

func (m *DouyinMonitor) desiredLiveIDsLocked() map[string]bool {
	result := make(map[string]bool)
	for _, group := range m.cfg.DouyinSubscriptions {
		for _, item := range group {
			if item != nil && !item.Disabled && !item.LiveDisabled && strings.TrimSpace(item.LiveID) != "" {
				result[item.LiveID] = true
			}
		}
	}
	return result
}

func (m *DouyinMonitor) Start() error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.stopping = false
	m.mu.Unlock()

	if err := m.startOptionalLiveProcess(); err != nil {
		log.Printf("[Douyin-live] sidecar start failed, will still try configured WebSocket: %v", err)
	}
	m.mu.Lock()
	desiredLive := m.desiredLiveIDsLocked()
	m.mu.Unlock()
	for liveID := range desiredLive {
		m.ensureLive(liveID)
	}
	m.retryPendingLiveNotifications()
	return nil
}

func (m *DouyinMonitor) startOptionalLiveProcess() error {
	parts, err := parseDouyinCommandLine(m.cfg.DouyinLiveSidecarCmd, "")
	if err != nil || len(parts) == 0 {
		return err
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.mu.Lock()
	m.liveCmd = cmd
	m.mu.Unlock()
	go scanSidecarLogs(stdout, "[Douyin-live:stdout]")
	go scanSidecarLogs(stderr, "[Douyin-live]")
	go func() {
		if err := cmd.Wait(); err != nil {
			m.mu.Lock()
			stopping := m.stopping
			m.mu.Unlock()
			if !stopping {
				log.Printf("[Douyin-live] sidecar exited: %v", err)
			}
		}
	}()
	return nil
}

func (m *DouyinMonitor) Sync() error {
	m.mu.Lock()
	started := m.started
	browser := m.browser
	desiredLive := m.desiredLiveIDsLocked()
	for liveID, cancel := range m.liveCancels {
		if !desiredLive[liveID] {
			cancel()
			delete(m.liveCancels, liveID)
		}
	}
	m.mu.Unlock()
	if !started {
		if err := m.Start(); err != nil {
			return err
		}
	}
	for liveID := range desiredLive {
		m.ensureLive(liveID)
	}
	m.retryPendingLiveNotifications()
	if browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := browser.EnsureStarted(); err != nil {
		return err
	}
	return browser.SyncDouyin()
}

func (m *DouyinMonitor) RequestLogin() error {
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		if err := m.Start(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	browser := m.browser
	m.mu.Unlock()
	if browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := browser.EnsureStarted(); err != nil {
		return err
	}
	return browser.RequestDouyinLogin()
}

func (m *DouyinMonitor) Scan() error {
	m.mu.Lock()
	started := m.started
	m.mu.Unlock()
	if !started {
		if err := m.Start(); err != nil {
			return err
		}
	}
	m.mu.Lock()
	browser := m.browser
	m.mu.Unlock()
	if browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := browser.EnsureStarted(); err != nil {
		return err
	}
	return browser.ScanDouyin()
}

func (m *DouyinMonitor) Status() (bool, int, int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	accounts := m.accountsLocked()
	ready := m.browser != nil && m.browser.IsStarted()
	connected := 0
	for _, ok := range m.liveConnected {
		if ok {
			connected++
		}
	}
	return ready, len(accounts), connected, m.imConnected
}

func (m *DouyinMonitor) Snapshot(groupID int64) []config.DouyinConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	group := m.cfg.DouyinSubscriptions[groupID]
	result := make([]config.DouyinConfig, 0, len(group))
	for _, item := range group {
		if item != nil {
			result = append(result, *item)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].SecUserID < result[j].SecUserID
	})
	return result
}

func (m *DouyinMonitor) HandleBrowserEvent(event douyinBrowserEvent) {
	switch event.Type {
	case "account":
		m.handleAccount(event)
	case "posts":
		m.handlePosts(event)
	case "qrcode":
		m.handleQRCode(event)
	case "im_group":
		m.handleIMGroup(event)
	case "im_message":
		m.handleIMMessage(event)
	case "im_status":
		m.handleIMStatus(event)
	case "account_error", "error":
		log.Printf("[Douyin] %s %s: %s", event.Type, event.SecUserID, event.Message)
		m.noteDouyinCaptchaError(event.SecUserID, event.Message)
		m.noteDouyinWorksFailure(event.SecUserID, event.Message)
	case "status":
		log.Printf("[Douyin] status=%s message=%s", event.Status, event.Message)
		if event.Status == "login_error" {
			m.notifyAdmins("⚠️ 抖音登录二维码生成失败：" + event.Message)
		}
	}
}

func (m *DouyinMonitor) enabledDouyinWorksAccountsLocked() map[string]bool {
	result := make(map[string]bool)
	for _, group := range m.cfg.DouyinSubscriptions {
		for sec, item := range group {
			if item != nil && !item.Disabled && !item.WorksDisabled {
				if id := strings.TrimSpace(firstNonEmptyText(item.SecUserID, sec)); id != "" {
					result[id] = true
				}
			}
		}
	}
	return result
}

func (m *DouyinMonitor) noteDouyinWorksFailure(secUserID, message string) {
	if !strings.Contains(message, "抖音作品") {
		return
	}
	if isDouyinRiskControlError(message) {
		log.Printf("[Douyin] works risk-control ignored for alerting account=%s error=%s", secUserID, truncateDouyinLogText(message, 140))
		return
	}
	secUserID = strings.TrimSpace(secUserID)
	if secUserID == "" {
		return
	}
	const window = 10 * time.Minute
	now := time.Now()
	m.mu.Lock()
	enabled := m.enabledDouyinWorksAccountsLocked()
	if !enabled[secUserID] {
		m.mu.Unlock()
		return
	}
	for sec, failedAt := range m.worksFailures {
		if !enabled[sec] || now.Sub(failedAt) > window {
			delete(m.worksFailures, sec)
		}
	}
	m.worksFailures[secUserID] = now
	failed, total := len(m.worksFailures), len(enabled)
	allFailed := total > 0 && failed == total
	shouldAlert := allFailed && m.worksAlertSince.IsZero()
	if shouldAlert {
		m.worksAlertSince = now
	}
	const proxyRecoverCooldown = 10 * time.Minute
	shouldRecoverProxy := allFailed && isDouyinProxyNetworkError(message) &&
		(m.worksProxyRecoverAt.IsZero() || now.Sub(m.worksProxyRecoverAt) >= proxyRecoverCooldown)
	if shouldRecoverProxy {
		m.worksProxyRecoverAt = now
	}
	m.mu.Unlock()
	log.Printf("[Douyin] works health failures=%d/%d window=%s", failed, total, window)
	if shouldRecoverProxy {
		go m.recoverDouyinProxy(message)
	}
	if shouldAlert && m.notifyAdmins != nil {
		m.notifyAdmins(fmt.Sprintf("⚠️ 抖音作品监控全链路异常\n%d/%d 个启用账号在最近 %s 内均拉取失败，新作品将无法通知。\n最近错误：%s", failed, total, window, truncateDouyinLogText(message, 180)))
	}
}

func isDouyinRiskControlError(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(msg, "argussecurityplugin sign invalid") ||
		strings.Contains(msg, "argus sign invalid")
}

func isDouyinProxyNetworkError(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	for _, marker := range []string{
		"failed to fetch",
		"err_connection_",
		"tls handshake",
		"ssl_error_",
		"connection closed",
		"connection reset",
		"connection refused",
		"proxy connection",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

type mihomoProxyStatus struct {
	Alive bool   `json:"alive"`
	Now   string `json:"now"`
}

func (m *DouyinMonitor) recoverDouyinProxy(reason string) {
	controller := strings.TrimRight(strings.TrimSpace(m.proxyControllerURL), "/")
	if controller == "" {
		controller = "http://127.0.0.1:19090"
	}
	client := m.proxyHTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}

	readStatus := func(group string) (mihomoProxyStatus, error) {
		var status mihomoProxyStatus
		req, err := http.NewRequest(http.MethodGet, controller+"/proxies/"+url.PathEscape(group), nil)
		if err != nil {
			return status, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return status, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return status, fmt.Errorf("mihomo GET %s: status=%d", group, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return status, err
		}
		return status, nil
	}

	probeDouyin := func(proxy string) error {
		probeURL := controller + "/proxies/" + url.PathEscape(proxy) + "/delay?timeout=6000&url=" + url.QueryEscape("https://www.douyin.com/")
		req, err := http.NewRequest(http.MethodGet, probeURL, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("status=%d", resp.StatusCode)
		}
		var result struct {
			Delay int `json:"delay"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return err
		}
		if result.Delay <= 0 {
			return fmt.Errorf("invalid delay=%d", result.Delay)
		}
		return nil
	}

	pool, poolErr := readStatus("XHS-Auto")
	target, targetNode := "", ""
	if poolErr == nil && pool.Alive && strings.TrimSpace(pool.Now) != "" && probeDouyin("XHS-Auto") == nil {
		target, targetNode = "XHS-Auto", pool.Now
	} else if err := probeDouyin("DIRECT"); err == nil {
		// A live proxy process with a dead outbound is common. DIRECT remains a
		// valid mihomo selector and is safer than repeatedly choosing unverified
		// exits; the browser keeps the same local proxy endpoint and cookies.
		target, targetNode = "DIRECT", "host route"
	} else {
		log.Printf("[Douyin] proxy auto-recovery skipped: no Douyin-capable route pool_alive=%v pool_node=%s pool_err=%v", pool.Alive, pool.Now, poolErr)
		return
	}
	current, currentErr := readStatus("GLOBAL")
	body, _ := json.Marshal(map[string]string{"name": target})
	req, err := http.NewRequest(http.MethodPut, controller+"/proxies/GLOBAL", strings.NewReader(string(body)))
	if err != nil {
		log.Printf("[Douyin] proxy auto-recovery request failed: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Douyin] proxy auto-recovery switch failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("[Douyin] proxy auto-recovery switch failed: status=%d", resp.StatusCode)
		return
	}
	from := current.Now
	if currentErr != nil || strings.TrimSpace(from) == "" {
		from = "unknown"
	}
	log.Printf("[Douyin] proxy auto-recovery switched GLOBAL from=%s to=%s node=%s reason=%s", from, target, targetNode, truncateDouyinLogText(reason, 120))
	if m.notifyAdmins != nil {
		m.notifyAdmins(fmt.Sprintf("🔄 抖音作品监控已自动切换网络路径\nGLOBAL：%s → %s（抖音探测：%s）\n程序将继续轮询并在作品列表恢复后发送恢复通知。", from, target, targetNode))
	}
}

func (m *DouyinMonitor) noteDouyinWorksSuccess(secUserID string) {
	secUserID = strings.TrimSpace(secUserID)
	if secUserID == "" {
		return
	}
	now := time.Now()
	m.mu.Lock()
	delete(m.worksFailures, secUserID)
	alertSince := m.worksAlertSince
	recovered := !alertSince.IsZero() && len(m.worksFailures) == 0
	if recovered {
		m.worksAlertSince = time.Time{}
	}
	m.mu.Unlock()
	if recovered && m.notifyAdmins != nil {
		m.notifyAdmins(fmt.Sprintf("✅ 抖音作品监控已恢复\n全部启用账号均已重新取得作品列表，本次异常持续约 %s。", now.Sub(alertSince).Round(time.Minute)))
	}
}

// noteDouyinCaptchaError counts captcha-blocked profile scans in a sliding window
// and notifies admins when the threshold is hit (email-only via notifyAdmins).
// Defaults: window=30m, threshold=6 hits, alert cooldown=60m.
func (m *DouyinMonitor) noteDouyinCaptchaError(secUserID, message string) {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	if !strings.Contains(msg, "验证码") {
		return
	}
	const (
		window    = 30 * time.Minute
		threshold = 6
		cooldown  = 60 * time.Minute
	)
	now := time.Now()
	m.mu.Lock()
	// prune old hits
	kept := m.captchaHits[:0]
	for _, t := range m.captchaHits {
		if now.Sub(t) <= window {
			kept = append(kept, t)
		}
	}
	m.captchaHits = append(kept, now)
	count := len(m.captchaHits)
	shouldAlert := count >= threshold && (m.captchaLastAlert.IsZero() || now.Sub(m.captchaLastAlert) >= cooldown)
	if shouldAlert {
		m.captchaLastAlert = now
	}
	m.mu.Unlock()

	log.Printf("[Douyin] captcha window hits=%d/%d sec=%s msg=%s", count, threshold, secUserID, msg)
	if !shouldAlert || m.notifyAdmins == nil {
		return
	}
	m.notifyAdmins(fmt.Sprintf(
		"⚠️ 抖音作品监控验证码告警\n最近 %s 内出现 %d 次验证码导致无法拉作品（阈值 %d）\n最近账号：%s\n%s",
		window, count, threshold, truncateDouyinLogText(secUserID, 24), msg,
	))
}

func (m *DouyinMonitor) handleIMStatus(event douyinBrowserEvent) {
	status := strings.TrimSpace(event.Status)
	msg := strings.TrimSpace(event.Message)
	now := time.Now()
	m.mu.Lock()
	connected := status == "connected"
	m.imConnected = connected
	if connected {
		// Recovered: clear disconnect clock. No recovery email — user only wants
		// alerts when auto-heal failed and manual work is needed.
		if !m.imDisconnectedAt.IsZero() {
			downFor := now.Sub(m.imDisconnectedAt)
			m.imDisconnectedAt = time.Time{}
			m.mu.Unlock()
			clearDouyinIMRecoveryMarker()
			log.Printf("[Douyin-IM] status=connected message=%s (recovered after %s)", msg, downFor.Round(time.Second))
			return
		}
		m.mu.Unlock()
		clearDouyinIMRecoveryMarker()
		log.Printf("[Douyin-IM] status=connected message=%s", msg)
		return
	}
	// disconnected / error / other non-connected — log only, no admin notify.
	if m.imDisconnectedAt.IsZero() {
		m.imDisconnectedAt = now
	}
	m.mu.Unlock()
	log.Printf("[Douyin-IM] status=%s message=%s", status, msg)
}

func firstNonEmptyText(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

const douyinIMRecoveryMarkerPath = "storage/douyin-im-recovery.json"

const douyinIMPostRestartAlertDelay = 3 * time.Minute

type douyinIMRecoveryMarker struct {
	Reason      string `json:"reason"`
	RestartedAt int64  `json:"restarted_at_ms"`
	AlertedAt   int64  `json:"alerted_at_ms,omitempty"`
}

func writeDouyinIMRecoveryMarker(reason string) {
	_ = os.MkdirAll("storage", 0o755)
	raw, _ := json.MarshalIndent(douyinIMRecoveryMarker{
		Reason:      reason,
		RestartedAt: time.Now().UnixMilli(),
	}, "", "  ")
	_ = os.WriteFile(douyinIMRecoveryMarkerPath, raw, 0o600)
}

func readDouyinIMRecoveryMarker() *douyinIMRecoveryMarker {
	raw, err := os.ReadFile(douyinIMRecoveryMarkerPath)
	if err != nil {
		return nil
	}
	var m douyinIMRecoveryMarker
	if json.Unmarshal(raw, &m) != nil || m.RestartedAt <= 0 {
		return nil
	}
	return &m
}

func clearDouyinIMRecoveryMarker() {
	_ = os.Remove(douyinIMRecoveryMarkerPath)
}

func touchDouyinIMRecoveryAlerted(marker *douyinIMRecoveryMarker) {
	if marker == nil {
		return
	}
	marker.AlertedAt = time.Now().UnixMilli()
	raw, _ := json.MarshalIndent(marker, "", "  ")
	_ = os.WriteFile(douyinIMRecoveryMarkerPath, raw, 0o600)
}

// StartIMWatchdog monitors prolonged IM disconnect and escalates silently:
// 1) after 45s: restart weibo-auth sidecar (IM lives there)
// 2) after 2m still down: exit process so systemd restarts bot (writes recovery marker)
// 3) email ONLY if still down 3m after auto restarts (manual intervention needed)
func (m *DouyinMonitor) StartIMWatchdog() {
	m.imWatchdogOnce.Do(func() {
		if m.imWatchdogStop == nil {
			m.imWatchdogStop = make(chan struct{})
		}
		go m.runIMWatchdog()
	})
}

func (m *DouyinMonitor) runIMWatchdog() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.imWatchdogStop:
			return
		case <-ticker.C:
			m.tickIMWatchdog()
		}
	}
}

func (m *DouyinMonitor) tickIMWatchdog() {
	if m.cfg == nil || !m.cfg.DouyinIMEnabled {
		return
	}
	now := time.Now()
	m.mu.Lock()
	connected := m.imConnected
	since := m.imDisconnectedAt
	browser := m.browser
	restartFn := m.requestBotRestart
	if connected {
		m.mu.Unlock()
		clearDouyinIMRecoveryMarker()
		return
	}

	// After bot restart for IM: marker survives process death. Only then may we email.
	if marker := readDouyinIMRecoveryMarker(); marker != nil {
		// Allow IM up to three minutes to reconnect after boot before paging a human.
		sinceRestart := now.Sub(time.UnixMilli(marker.RestartedAt))
		if sinceRestart >= douyinIMPostRestartAlertDelay {
			lastAlert := time.Time{}
			if marker.AlertedAt > 0 {
				lastAlert = time.UnixMilli(marker.AlertedAt)
			}
			if lastAlert.IsZero() || now.Sub(lastAlert) >= 30*time.Minute {
				notify := m.notifyAdmins
				m.mu.Unlock()
				touchDouyinIMRecoveryAlerted(marker)
				if notify != nil {
					notify(fmt.Sprintf(
						"🚨 抖音 IM 自动恢复失败，需要人工处理\n断线后已尝试：侧车重启 + Bot 重启\nBot 重启后仍未连上（约 %s）\n原因：%s\n请检查浏览器登录态 / 侧卡日志 / 抖音风控。",
						sinceRestart.Round(time.Second), firstNonEmptyText(marker.Reason, "unknown"),
					))
				}
				return
			}
		}
		// Marker present but still in grace period — don't escalate further this tick.
		m.mu.Unlock()
		return
	}

	if since.IsZero() {
		// Never saw a connected→disconnected transition this process; if IM stays
		// down from startup, start the clock so auto-heal can still run.
		m.imDisconnectedAt = now
		m.mu.Unlock()
		return
	}
	downFor := now.Sub(since)
	// Stage 1: sidecar restart after 45s (was 2m; user: auto-heal too conservative).
	if downFor >= 45*time.Second && (m.imSidecarRestartAt.IsZero() || now.Sub(m.imSidecarRestartAt) >= 5*time.Minute) {
		m.imSidecarRestartAt = now
		m.mu.Unlock()
		log.Printf("[Douyin-IM] still down for %s → restart weibo-auth sidecar (silent)", downFor.Round(time.Second))
		if browser != nil {
			if err := browser.Restart(); err != nil {
				log.Printf("[Douyin-IM] sidecar restart failed: %v", err)
			} else {
				log.Printf("[Douyin-IM] sidecar restart requested")
			}
		}
		return
	}
	// Stage 2: bot process restart after 2 minutes (was 5m).
	if downFor >= 2*time.Minute && (m.imBotRestartAt.IsZero() || now.Sub(m.imBotRestartAt) >= 10*time.Minute) {
		m.imBotRestartAt = now
		m.mu.Unlock()
		reason := fmt.Sprintf("douyin IM disconnected for %s", downFor.Round(time.Second))
		log.Printf("[Douyin-IM] still down for %s → restart bot process (silent, marker written)", downFor.Round(time.Second))
		writeDouyinIMRecoveryMarker(reason)
		if restartFn != nil {
			restartFn(reason)
		} else {
			log.Printf("[Douyin-IM] no restart callback; exiting for systemd restart")
			go func() {
				time.Sleep(500 * time.Millisecond)
				os.Exit(1)
			}()
		}
		return
	}
	m.mu.Unlock()
}

func parseDouyinIMGroupNumbers(raw string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	}) {
		p := strings.TrimSpace(part)
		if p != "" {
			out[p] = struct{}{}
		}
	}
	return out
}

func douyinIMGroupAllowed(configured, groupNumber string) bool {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return true // empty = accept all discovered groups
	}
	allowed := parseDouyinIMGroupNumbers(configured)
	if len(allowed) == 0 {
		return true
	}
	_, ok := allowed[strings.TrimSpace(groupNumber)]
	return ok
}

func (m *DouyinMonitor) handleIMGroup(event douyinBrowserEvent) {
	if !m.cfg.DouyinIMEnabled || strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(event.OwnerUID) == "" {
		return
	}
	if !douyinIMGroupAllowed(m.cfg.DouyinIMGroupNumber, event.GroupNumber) {
		log.Printf("[Douyin-IM] ignored group metadata with unexpected group number=%s", event.GroupNumber)
		return
	}
	m.mu.Lock()
	if m.imConversations == nil {
		m.imConversations = make(map[string]douyinIMTarget)
	}
	m.imConversations[event.ConversationID] = douyinIMTarget{
		ConversationID: event.ConversationID,
		OwnerUID:       event.OwnerUID,
		GroupNumber:    event.GroupNumber,
		GroupName:      event.GroupName,
	}
	m.imSelfUID = event.SelfUID
	m.mu.Unlock()
	log.Printf("[Douyin-IM] target group metadata ready: name=%s number=%s owner=%s conv=%s (tracked=%d)",
		event.GroupName, event.GroupNumber, event.OwnerUID, event.ConversationID, len(m.imConversations))
}

func (m *DouyinMonitor) handleIMMessage(event douyinBrowserEvent) {
	text := strings.TrimSpace(event.Text)
	images := uniqueHTTPURLs(event.Images)
	videoURL := strings.TrimSpace(event.VideoURL)
	livePhotoVideos := uniqueHTTPURLs(event.LivePhotoVideos)
	if text == "" && len(images) == 0 && videoURL == "" && len(livePhotoVideos) == 0 {
		return
	}
	// With real image URLs, drop redundant sticker captions like [表情]/[早点睡].
	if len(images) > 0 && isDouyinStickerCaption(text) {
		text = ""
	}
	if text == "" && len(images) > 0 {
		// Keep body empty so QQ shows "名：" + image only (no placeholder).
		text = ""
	}
	timeText := formatDouyinIMTime(event.CreateTime, event.ReceivedAt)
	m.mu.Lock()
	selfUID := m.imSelfUID
	target, hasTarget := m.imConversations[event.ConversationID]
	// fallback: single tracked conversation if event has empty id
	if !hasTarget && len(m.imConversations) == 1 {
		for _, v := range m.imConversations {
			target = v
			hasTarget = true
		}
	}
	m.mu.Unlock()
	// Own messages: only allow private notes-to-self (flagged isSelfChat by sidecar).
	// Own messages to other peers / groups are never mirrored.
	isOwnSender := event.SenderUID != "" && (event.SenderUID == event.SelfUID || event.SenderUID == selfUID)
	if isOwnSender && !(event.ConversationType == 1 && event.IsSelfChat) {
		return
	}
	conversationID := ""
	ownerUID := ""
	if hasTarget {
		conversationID = target.ConversationID
		ownerUID = target.OwnerUID
	}
	kind := classifyDouyinIMEvent(event, conversationID, ownerUID, selfUID)
	if kind == "" && event.ConversationType == 2 {
		// Diagnostic: group traffic that failed owner/conversation match.
		log.Printf("[Douyin-IM] skip group message type=%d sender=%s owner=%s conv=%s targetConv=%s text=%q images=%d",
			event.MessageType, event.SenderUID, ownerUID, event.ConversationID, conversationID, truncateDouyinLogText(text, 60), len(images))
		return
	}
	switch kind {
	case "group_owner":
		if !m.cfg.DouyinIMEnabled || m.cfg.BoundGroupID == 0 {
			return
		}
		// Drop group system notices that sometimes still leak with owner as sender
		// (e.g. type=1001 join-via-profile templates).
		if isDouyinGroupSystemNoticeText(text) {
			log.Printf("[Douyin-IM] skip group system notice type=%d text=%q", event.MessageType, truncateDouyinLogText(text, 80))
			return
		}
		boxName, lineName := resolveDouyinSenderLabels(event)
		groupName := strings.TrimSpace(target.GroupName)
		if groupName == "" {
			groupName = strings.TrimSpace(m.cfg.DouyinIMGroupName)
		}
		// Align with Pocket48 room header: 【备注/名|群】（英文 |）
		title := "【抖音群】"
		switch {
		case boxName != "" && groupName != "":
			title = "【" + boxName + "|" + groupName + "】"
		case groupName != "":
			title = "【" + groupName + "|抖音群】"
		case boxName != "":
			title = "【" + boxName + "|抖音群】"
		}
		body := formatDouyinSenderLine(lineName, text)
		log.Printf("[Douyin-IM] forward group_owner box=%s line=%s type=%d text=%q images=%d", boxName, lineName, event.MessageType, truncateDouyinLogText(text, 80), len(images))
		// Header + body first; images above timestamp when present (sticker/表情图 etc).
		segments := appendTextWithQQFaces(nil, title+"\n"+body)
		segments = appendDouyinIMCardTail(segments, images, event.Link, timeText)
		outbound.SendGroup(m.outbound, m.cfg.BoundGroupID, segments)
		if videoURL != "" {
			if localVideo := localizeDouyinVideo(videoURL); localVideo != "" {
				outbound.SendGroup(m.outbound, m.cfg.BoundGroupID, []napcat.MessageSegment{napcat.VideoSegment(localVideo, "")})
			}
		}
		for _, liveURL := range livePhotoVideos {
			if localVideo := localizeDouyinVideo(liveURL); localVideo != "" {
				outbound.SendGroup(m.outbound, m.cfg.BoundGroupID, []napcat.MessageSegment{napcat.VideoSegment(localVideo, "")})
			}
		}
	case "private_incoming", "private_self":
		if !m.cfg.DouyinIMEnabled || !m.cfg.DouyinIMPrivateEnabled {
			return
		}
		boxName, lineName := resolveDouyinSenderLabels(event)
		if kind == "private_self" {
			boxName = "我"
			lineName = "我"
		}
		quotedName := inferDouyinQuotedName(event, lineName, selfUID)
		// Share-card quotes without a name: if quoted UID is empty after filter but
		// quoted text looks like a share we treat unknown as empty (JS should set 我).
		// Reply-to-share covers: keep at most one image (CDN mirrors of same cover).
		if event.Link != "" || strings.HasPrefix(strings.TrimSpace(event.QuotedText), "[分享图文]") || strings.HasPrefix(strings.TrimSpace(event.QuotedText), "[视频]") {
			if len(images) > 1 {
				images = images[:1]
			}
		}
		// QQ keeps the two-line quoted layout ("我：[图片]\n葡萄吞十七：我的妈呀").
		// Feishu must NOT receive that text: its extractSender blindly treats the
		// first line's colon prefix as the card header, so the card showed the
		// *quoted* speaker (我 / our own nickname) instead of the real sender.
		// Feishu gets replyBody + a structured Quote instead.
		replyBody := text
		text = formatDouyinReplyText(lineName, text, quotedName, event.QuotedText)
		// Business forward (not an ops alert) — still QQ private to admins.
		// Same layout: title+body → images → timestamp.
		header := formatDouyinPrivateNotificationHeader(boxName, lineName, text)
		segments := appendTextWithQQFaces(nil, header)
		segments = appendDouyinIMCardTail(segments, images, event.Link, timeText)
		log.Printf("[Douyin-IM] forward %s type=%d text=%q images=%d link=%q", kind, event.MessageType, truncateDouyinLogText(text, 80), len(images), event.Link)
		// Download media once, then fan it out to every destination.
		// localVideos is kept alongside the QQ segments so the Feishu Document
		// reuses the same downloaded file instead of fetching the CDN again.
		var mediaSegments []napcat.MessageSegment
		var localVideos []string
		if videoURL != "" {
			if localVideo := localizeDouyinVideo(videoURL); localVideo != "" {
				mediaSegments = append(mediaSegments, napcat.VideoSegment(localVideo, ""))
				localVideos = append(localVideos, localVideo)
			}
		}
		for _, liveURL := range livePhotoVideos {
			if localVideo := localizeDouyinVideo(liveURL); localVideo != "" {
				mediaSegments = append(mediaSegments, napcat.VideoSegment(localVideo, ""))
				localVideos = append(localVideos, localVideo)
			}
		}
		// QQ private to admins.
		for _, uid := range uniqueAdminIDs(m.cfg) {
			outbound.SendPrivate(m.outbound, uid, segments)
			if len(mediaSegments) > 0 {
				outbound.SendPrivate(m.outbound, uid, mediaSegments)
			}
		}
		// Mirror to configured Feishu private targets so Douyin DMs stay visible
		// cross-platform too, not QQ-only. Structured Document: the sender comes
		// from Author and the replied-to message from Quote, so the card header can
		// never be hijacked by the quoted line.
		m.sendDouyinPrivateDocumentToFeishu(buildDouyinPrivateDocument(douyinPrivateDocInput{
			kind:       kind,
			boxName:    boxName,
			lineName:   lineName,
			body:       replyBody,
			quotedName: quotedName,
			quotedText: event.QuotedText,
			images:     images,
			videos:     localVideos,
			link:       event.Link,
			createTime: event.CreateTime,
			receivedAt: event.ReceivedAt,
		}))
	}
}

// douyinPrivateDocInput is the raw material for a Douyin DM Feishu card.
type douyinPrivateDocInput struct {
	kind       string
	boxName    string
	lineName   string
	body       string
	quotedName string
	quotedText string
	images     []string
	videos     []string
	link       string
	createTime int64
	receivedAt int64
}

// douyinPrivateDocument renders a Douyin DM as a structured Feishu Document.
//
// Body carries only the reply text; the quoted message goes into Quote so the
// card shows it as a grey block above the reply. Author is set explicitly, so
// Feishu never has to re-derive the sender from punctuation.
func buildDouyinPrivateDocument(in douyinPrivateDocInput) message.Document {
	sender := strings.TrimSpace(in.lineName)
	if in.kind == "private_self" {
		// Notes-to-self really is us, so「我」is the right label.
		// ★ Only this branch may claim the message is ours. Every other
		// incoming DM must keep the real sender — this used to fall through
		// with our own cached nickname and the Feishu header then showed
		// 鸠风 instead of 葡萄吞十七.
		sender = "我"
	}
	if sender == "" {
		sender = strings.TrimSpace(in.boxName)
	}
	if sender == "" {
		// Last resort: a visible placeholder beats an empty header that Feishu
		// would fill from the body (guessing「我」from the quoted line).
		sender = "抖音用户"
	}
	// ★ 正文必须自带「昵称：」前缀（2026-10-05 修）。
	//
	// 飞书卡片的引用块与正文是**两条独立渲染路径**：
	//   引用块 -> turnParts(quote.Author, ...)  -> 自动加「作者：」
	//   正文   -> content.text（就是 doc.Body） -> 裸文本，不加前缀
	// 而这里传的是未格式化的 replyBody，于是下面那条回复没有昵称前缀。
	// 现象就是「上面有『我：xxx』，下面只有裸文本」——
	// 用户原话：「两个都必须带冒号」。
	//
	//★ 只在**有引用**时才加：没有引用时正文就是消息全文，
	//   加了前缀会变成「葡萄吞十七：今天的照片」这种冗余形态
	//   （顶栏已经写了发送者）。QQ 侧 formatDouyinReplyText 也是这个规则。
	body := strings.TrimSpace(in.body)
	hasQuote := false
	if q := in.quotedText; q != "" && q != "[回复]" && !isDouyinGarbageQuoteText(q) {
		hasQuote = true
	}
	if hasQuote && body != "" && sender != "" {
		body = ensureDouyinSenderPrefix(body, sender)
	}

	doc := message.Document{
		Source:    "抖音",
		Kind:      "im_private",
		Author:    sender,
		Title:     sender,
		Body:      body,
		Link:      strings.TrimSpace(in.link),
		CreatedAt: parseDouyinIMTime(in.createTime, in.receivedAt),
	}
	// A placeholder quote ("[回复]") or a garbage token (sec_uid) carries no
	// information and would only add an empty grey block to the card.
	quotedText := strings.TrimSpace(in.quotedText)
	if quotedText != "" && quotedText != "[回复]" && !isDouyinGarbageQuoteText(quotedText) {
		doc.Quote = &message.Quote{Author: strings.TrimSpace(in.quotedName), Text: quotedText}
	}
	for _, image := range in.images {
		if image = strings.TrimSpace(image); image != "" {
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: image})
		}
	}
	for _, video := range in.videos {
		if video = strings.TrimSpace(video); video != "" {
			doc.Media = append(doc.Media, message.Media{Kind: "video", Source: video})
		}
	}
	return doc
}

// parseDouyinIMTime converts the sidecar timestamp to time.Time. Zero means
// "unknown", which makes Feishu omit the footer stamp rather than print ours.
func parseDouyinIMTime(createTime, receivedAt int64) time.Time {
	value := createTime
	if value <= 0 {
		value = receivedAt
	}
	if value <= 0 {
		return time.Time{}
	}
	if value < 1_000_000_000_000 {
		return time.Unix(value, 0)
	}
	return time.UnixMilli(value)
}

// sendDouyinPrivateDocumentToFeishu mirrors a Douyin DM to every configured
// Feishu private target through the structured Document path.
func (m *DouyinMonitor) sendDouyinPrivateDocumentToFeishu(doc message.Document) {
	if m == nil || m.outbound == nil || m.cfg == nil {
		return
	}
	var targets []string
	for _, t := range m.cfg.DeliveryTargets {
		if t.Platform == "feishu" && t.Kind == "private" {
			targets = append(targets, t.ID)
		}
	}
	if len(targets) == 0 {
		return
	}
	m.sendToTargets(targets, doc)
}

// sendToFeishuPrivate mirrors a Douyin private message to every configured
// Feishu private target, so Douyin DMs are visible on Feishu as well as QQ.
func (m *DouyinMonitor) sendToFeishuPrivate(segments []interface{}, mediaSegments []napcat.MessageSegment) {
	if m == nil || m.outbound == nil || m.cfg == nil {
		return
	}
	var targets []string
	for _, t := range m.cfg.DeliveryTargets {
		if t.Platform == "feishu" && t.Kind == "private" {
			targets = append(targets, t.ID)
		}
	}
	if len(targets) == 0 {
		return
	}
	m.sendToTargets(targets, segments)
	if len(mediaSegments) > 0 {
		m.sendToTargets(targets, mediaSegments)
	}
}

func appendDouyinIMCardTail(segments []interface{}, images []string, link, timeText string) []interface{} {
	for _, image := range images {
		if len(segments) >= 9 { // leave room for the link and timestamp
			break
		}
		segments = append(segments, napcat.ImageSegment(image))
	}
	if link = strings.TrimSpace(link); link != "" {
		segments = appendTextWithQQFaces(segments, "\n"+link)
	}
	if timeText = strings.TrimSpace(timeText); timeText != "" {
		segments = appendTextWithQQFaces(segments, "\n"+timeText)
	}
	return segments
}

func localizeDouyinVideo(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	local, err := downloadMediaFile(rawURL)
	if err != nil {
		log.Printf("[Douyin] video download failed url=%s: %v", truncateDouyinLogText(rawURL, 120), err)
		return ""
	}
	return local
}

func uniqueHTTPURLs(urls []string) []string {
	out := make([]string, 0, len(urls))
	seen := map[string]struct{}{}
	for _, raw := range urls {
		u := strings.TrimSpace(raw)
		if u == "" || (!strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://")) {
			continue
		}
		// De-dupe CDN mirrors of the same asset (query tokens differ, path same).
		key := u
		if i := strings.Index(u, "?"); i >= 0 {
			key = u[:i]
		}
		// Also collapse identical basenames under different hosts when path ends the same.
		if j := strings.LastIndex(key, "/"); j >= 0 && j+1 < len(key) {
			base := key[j+1:]
			if len(base) >= 8 {
				key = "base:" + base
			}
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, u)
		if len(out) >= 9 {
			break
		}
	}
	return out
}

func truncateDouyinLogText(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if max <= 0 || len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}

func inferDouyinQuotedName(event douyinBrowserEvent, senderName, selfUID string) string {
	quotedUID := strings.TrimSpace(event.QuotedSenderUID)
	// An explicit name decoded from the quote payload is more authoritative
	// than UID heuristics (which may reuse a stale self UID after reconnect).
	if name := strings.TrimSpace(event.QuotedName); name != "" {
		return name
	}
	// Self-quote always shows 我 — cached remark/nickname (e.g. our own account
	// saved in contact cache as 张若昀) must never override this.
	if quotedUID != "" && (quotedUID == event.SelfUID || quotedUID == selfUID) {
		return "我"
	}
	if quotedUID == "" {
		return ""
	}
	if quotedUID == event.SenderUID {
		return senderName
	}
	return ""
}

func formatDouyinReplyText(senderName, text, quotedName, quotedText string) string {
	quotedName = strings.TrimSpace(quotedName)
	quotedText = strings.TrimSpace(quotedText)
	text = strings.TrimSpace(text)
	if quotedText == "" {
		return text
	}
	// Prefer Chinese colon to match sender lines; force "我：" when name is 我.
	quotedLine := quotedText
	// Drop garbage quote tokens (Douyin sec_uid / long opaque ids mistaken for quote text).
	if isDouyinGarbageQuoteText(quotedText) {
		quotedText = ""
		return text
	}
	// Bare "[视频]" quote with no card detail is noise (Douyin splits video share + caption
	// into type=8 + type=7). Drop the empty quote so only the real caption remains.
	if quotedText == "[视频]" || quotedText == "[图片]" || quotedText == "[语音]" {
		if quotedName == "" {
			quotedText = ""
			return text
		}
	}
	if quotedName != "" {
		quotedLine = quotedName + "：" + quotedText
	}
	// Placeholder-only reply body: just show quote + sender line without bare 「[回复]」.
	// Sticker/image under a chip-id "quote" must not become 「（回复）」 — empty quote already dropped above.
	if text == "" || text == "[回复]" {
		senderName = strings.TrimSpace(senderName)
		if quotedText == "" {
			// No real quote: keep empty body (caller may still attach images).
			return ""
		}
		if senderName == "" {
			return quotedLine
		}
		// B 站式无正文回复（对端点了「回复」但正文为空，正文真的没被推送）：
		// 把占位的「（回复）」换成能看懂的对象提示，否则整条消息毫无信息量。
		hint := "（回复）"
		switch quotedText {
		case "[图片]":
			hint = "（回复了你的图片）"
		case "[视频]":
			hint = "（回复了你的视频）"
		case "[语音]":
			hint = "（回复了你的语音）"
		}
		return quotedLine + "\n" + senderName + "：" + hint
	}
	senderName = strings.TrimSpace(senderName)
	if senderName == "" {
		return quotedLine + "\n" + text
	}
	// Avoid double-prefix if body already has "名："
	body := strings.TrimSpace(text)
	if strings.HasPrefix(body, senderName+"：") || strings.HasPrefix(body, senderName+":") {
		return quotedLine + "\n" + body
	}
	return quotedLine + "\n" + senderName + "：" + body
}

// ensureDouyinSenderPrefix 给正文补上「昵称：」前缀，已有则不重复加。
//
// 判定「是否已有」比 HasPrefix(sender+"：") 更宽松一些：用户昵称
// 可能是「葡萄吞十七(唐欣怡)」，而正文里可能已经带了「葡萄吞十七：」
// （昵称与备注不一致，见 formatDouyinNamePair）。
// 因此只要正文开头出现「发送者名字 + 冒号」就算已有。
func ensureDouyinSenderPrefix(body, sender string) string {
	body = strings.TrimSpace(body)
	sender = strings.TrimSpace(sender)
	if body == "" || sender == "" {
		return body
	}
	if strings.HasPrefix(body, sender+"：") || strings.HasPrefix(body, sender+":") {
		return body
	}
	// 昵称与备注不一致的情况：正文已带「备注名：」则不重复加。
	if idx := strings.IndexAny(body, "：:"); idx > 0 {
		head := body[:idx]
		// 头部像昵称（长度合理、不含换行）就认为已带前缀。
		if len([]rune(head)) <= 20 && !strings.ContainsAny(head, "\n") {
			return body
		}
	}
	return sender + "：" + body
}

// isDouyinGarbageQuoteText rejects sec_uid / long opaque tokens / bare short
// integers that were mistakenly used as quoted message text.
func isDouyinGarbageQuoteText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "MS4wLjABAAAA") {
		return true
	}
	// Short bare integers: reaction/chip ids, not real chat quotes.
	if len(s) <= 6 {
		allDigit := true
		for _, r := range s {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	if len(s) >= 10 {
		allDigit := true
		for _, r := range s {
			if r < '0' || r > '9' {
				allDigit = false
				break
			}
		}
		if allDigit {
			return true
		}
	}
	if len(s) >= 40 {
		// long base64-ish / hex without CJK
		hasCJK := false
		for _, r := range s {
			if r >= 0x4e00 && r <= 0x9fff {
				hasCJK = true
				break
			}
		}
		if !hasCJK {
			return true
		}
	}
	return false
}

// Group chat header already carries sender|group (Pocket48-style). Body may include "名（备注）：".
// isDouyinGroupSystemNoticeText matches Douyin group join/leave/admin system
// templates that must never be mirrored to QQ (even if attributed to the owner).
func isDouyinGroupSystemNoticeText(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if strings.Contains(t, "加入了群聊") ||
		strings.Contains(t, "退出了群聊") ||
		strings.Contains(t, "被移出群聊") ||
		strings.Contains(t, "新成员可查看历史消息") ||
		strings.Contains(t, "通过") && strings.Contains(t, "个人主页加入") ||
		strings.Contains(t, "成为了群主") ||
		strings.Contains(t, "修改了群名") {
		return true
	}
	// Unresolved template tokens {0}/{1}
	if (strings.Contains(t, "{0}") || strings.Contains(t, "{1}")) &&
		(strings.Contains(t, "群聊") || strings.Contains(t, "成员") || strings.Contains(t, "入群")) {
		return true
	}
	return false
}

func formatDouyinIMGroupNotification(title, text, timeText string) string {
	return fmt.Sprintf("%s\n%s\n%s", title, text, timeText)
}

// Same shape as Pocket48 room forwards: header + "昵称: 内容" + timestamp.
// No "来自：" prefix — keep it short like room messages.
func formatDouyinIMNotification(title, name, text, timeText string) string {
	return fmt.Sprintf("%s\n%s: %s\n%s", title, name, text, timeText)
}

// resolveDouyinSenderLabels returns (boxName, lineName):
//   - boxName: 抖音昵称优先（标题框【昵称|抖音】）；无昵称再回落备注
//   - lineName: "抖音名(备注)" if both differ; else single display name
func resolveDouyinSenderLabels(event douyinBrowserEvent) (boxName, lineName string) {
	nick := strings.TrimSpace(event.SenderNickname)
	remark := strings.TrimSpace(event.SenderRemark)
	fallback := strings.TrimSpace(event.SenderName)
	if nick == "" {
		nick = fallback
	}
	if nick == "" && remark == "" {
		if event.SenderUID != "" {
			return "抖音用户(UID:" + event.SenderUID + ")", "抖音用户(UID:" + event.SenderUID + ")"
		}
		return "抖音用户", "抖音用户"
	}
	// Title box: nickname first (user wants 【葡萄吞十七|抖音】 not remark-only).
	//
	// ★ 2026-10-05：团名走 CanonicalGroupName 统一显示形态。
	//   抖音昵称本身已是 Hearts2Hearts，但换成别的组合（大小写混写）时
	//   就能与其它平台对齐。去重侧 ToLower 过，比对不受影响。
	if nick != "" {
		boxName = dedupe.CanonicalGroupName(nick)
	} else {
		boxName = remark
	}
	lineName = formatDouyinNamePair(nick, remark)
	return boxName, lineName
}

// formatDouyinNamePair → "抖音名(备注)" when both present and different.
// Align with Pocket48 English parentheses. Also accept Chinese （） from source nick.
// Special case when nick already embeds remark as "备注(小名)" / "小名(备注)":
//
//	nick "胡晓慧(小包)" / "胡晓慧（小包）" + remark "胡晓慧" → "小包(胡晓慧)"
//
// Other containment still collapses to the longer string to avoid "名(备注)" nesting.
func formatDouyinNamePair(nickname, remark string) string {
	nickname = strings.TrimSpace(nickname)
	remark = strings.TrimSpace(remark)
	if nickname == "" {
		return remark
	}
	if remark == "" || nickname == remark {
		return nickname
	}
	// nick = "备注(小名)" or "备注（小名）" → "小名(备注)"
	for _, open := range []string{"(", "（"} {
		close := ")"
		if open == "（" {
			close = "）"
		}
		if strings.HasPrefix(nickname, remark+open) && strings.HasSuffix(nickname, close) {
			alias := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(nickname, remark+open), close))
			if alias != "" && alias != remark {
				return alias + "(" + remark + ")"
			}
		}
		// nick = "小名(备注)" already ideal → normalize Chinese parens to English
		if strings.HasSuffix(nickname, open+remark+close) {
			if open == "(" {
				return nickname
			}
			// rewrite Chinese to English
			prefix := strings.TrimSuffix(nickname, open+remark+close)
			return prefix + "(" + remark + ")"
		}
	}
	// Other containment either way → longer string only, no double labels.
	if strings.Contains(nickname, remark) {
		return normalizeDouyinParens(nickname)
	}
	if strings.Contains(remark, nickname) {
		return normalizeDouyinParens(remark)
	}
	return nickname + "(" + remark + ")"
}

// normalizeDouyinParens rewrites Chinese full-width parentheses to ASCII ().
func normalizeDouyinParens(s string) string {
	return strings.NewReplacer("（", "(", "）", ")").Replace(s)
}

// lookupDouyinContactRemark reads remark from sidecar contact cache (same file IM uses).
// Cache path: {WeiboBrowserProfileDir}/douyin-contact-cache.json
func lookupDouyinContactRemark(cfg *config.Config, secUserID, nickname string) string {
	secUserID = strings.TrimSpace(secUserID)
	nickname = strings.TrimSpace(nickname)
	if cfg == nil {
		return ""
	}
	dir := strings.TrimSpace(cfg.WeiboBrowserProfileDir)
	if dir == "" {
		dir = "./storage/weibo-browser-profile"
	}
	path := dir + "/douyin-contact-cache.json"
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var payload struct {
		Contacts []struct {
			SecUID     string `json:"secUid"`
			Nickname   string `json:"nickname"`
			RemarkName string `json:"remarkName"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	for _, c := range payload.Contacts {
		if secUserID != "" && strings.TrimSpace(c.SecUID) == secUserID {
			return strings.TrimSpace(c.RemarkName)
		}
	}
	if nickname != "" {
		for _, c := range payload.Contacts {
			if strings.TrimSpace(c.Nickname) == nickname {
				return strings.TrimSpace(c.RemarkName)
			}
		}
	}
	return ""
}

// resolveDouyinWorksTitleNick: header 【昵称|抖音】 only — raw API/config nick, like Weibo screen_name.
func resolveDouyinWorksTitleNick(apiNickname, configName string) string {
	if n := strings.TrimSpace(apiNickname); n != "" {
		return n
	}
	return strings.TrimSpace(configName)
}

// resolveDouyinWorksBodyLabel: content-area name pair (备注规则), NOT for title box.
//
//	nick "胡晓慧（小包）" + remark "胡晓慧" → "小包(胡晓慧)"
//	nick "一盆蘸酱菜" + remark "卢天惠" → "一盆蘸酱菜(卢天惠)"
func resolveDouyinWorksBodyLabel(cfg *config.Config, secUserID, apiNickname, configName string) string {
	nick := resolveDouyinWorksTitleNick(apiNickname, configName)
	remark := lookupDouyinContactRemark(cfg, secUserID, nick)
	if remark == "" && strings.TrimSpace(configName) != "" {
		remark = lookupDouyinContactRemark(cfg, secUserID, strings.TrimSpace(configName))
	}
	if pair := formatDouyinNamePair(nick, remark); pair != "" {
		return pair
	}
	return nick
}

// Deprecated name kept for any external callers; body label only.
func resolveDouyinWorksDisplayName(cfg *config.Config, secUserID, apiNickname, configName string) string {
	return resolveDouyinWorksBodyLabel(cfg, secUserID, apiNickname, configName)
}

func formatDouyinSenderLine(lineName, text string) string {
	lineName = strings.TrimSpace(lineName)
	text = strings.TrimSpace(text)
	if lineName == "" {
		return text
	}
	if text == "" {
		// Image-only sticker: still show "名：" so the line isn't just the header.
		return lineName + "："
	}
	return lineName + "：" + text
}

// isDouyinStickerCaption reports placeholder labels that are redundant when a real image is attached.
func isDouyinStickerCaption(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	if t == "[表情]" || t == "[贴纸]" {
		return true
	}
	// Keep [图片]/[视频]/[语音] — those are media-type labels, not sticker names.
	if t == "[图片]" || t == "[视频]" || t == "[语音]" {
		return false
	}
	// [早点睡] / [比心] / [续火花] — short bracket light-interaction labels.
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") && !strings.Contains(t, "\n") {
		inner := t[1 : len(t)-1]
		if inner != "" && utf8.RuneCountInString(inner) <= 12 {
			return true
		}
	}
	return false
}

func formatDouyinPrivateNotification(boxName, lineName, text, timeText string) string {
	header := formatDouyinPrivateNotificationHeader(boxName, lineName, text)
	timeText = strings.TrimSpace(timeText)
	if timeText == "" {
		return header
	}
	return header + "\n" + timeText
}

// formatDouyinPrivateNotificationHeader is title+body without trailing timestamp,
// so callers can insert images above the time line.
func formatDouyinPrivateNotificationHeader(boxName, lineName, text string) string {
	boxName = strings.TrimSpace(boxName)
	if boxName == "" {
		boxName = "抖音用户"
	}
	body := strings.TrimSpace(text)
	// Reply stack already embeds sender across lines: keep as-is.
	// Plain body → "名(备注)：内容".
	if lineName != "" && !strings.Contains(body, "\n") {
		if !strings.HasPrefix(body, lineName+"：") && !strings.HasPrefix(body, lineName+":") {
			body = formatDouyinSenderLine(lineName, body)
		}
	}
	// Title: nickname first 【昵称|抖音】
	return fmt.Sprintf("【%s|抖音】\n%s", boxName, body)
}

func formatDouyinIMTime(createTime, receivedAt int64) string {
	value := createTime
	if value <= 0 {
		value = receivedAt
	}
	if value <= 0 {
		return time.Now().Format("2006-01-02 15:04:05")
	}
	if value < 1_000_000_000_000 {
		return time.Unix(value, 0).Format("2006-01-02 15:04:05")
	}
	return time.UnixMilli(value).Format("2006-01-02 15:04:05")
}

func classifyDouyinIMEvent(event douyinBrowserEvent, conversationID, ownerUID, selfUID string) string {
	switch event.ConversationType {
	case 2:
		if conversationID != "" && ownerUID != "" && event.ConversationID == conversationID && event.SenderUID == ownerUID {
			return "group_owner"
		}
	case 1:
		if event.SenderUID == "" {
			return ""
		}
		// Notes-to-self only when sidecar flagged isSelfChat (peer map / conv id check).
		if event.IsSelfChat && (event.SenderUID == selfUID || event.SenderUID == event.SelfUID) {
			return "private_self"
		}
		// Own outbound to other peers: ignore.
		if selfUID != "" && event.SenderUID == selfUID {
			return ""
		}
		if event.SelfUID != "" && event.SenderUID == event.SelfUID {
			return ""
		}
		return "private_incoming"
	}
	return ""
}

func (m *DouyinMonitor) handleAccount(event douyinBrowserEvent) {
	sec := strings.TrimSpace(event.SecUserID)
	if sec == "" {
		return
	}
	m.mu.Lock()
	changed := false
	enabled := false
	for _, group := range m.cfg.DouyinSubscriptions {
		item := group[sec]
		if item == nil {
			continue
		}
		if !item.Disabled && !item.LiveDisabled {
			enabled = true
		}
		if event.Nickname != "" && !item.NameManual && item.Name != event.Nickname {
			item.Name = event.Nickname
			changed = true
		}
		if event.ProfileURL != "" && item.ProfileURL != event.ProfileURL {
			item.ProfileURL = event.ProfileURL
			changed = true
		}
		if event.LiveID != "" && item.LiveID != event.LiveID {
			item.LiveID = event.LiveID
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		if err := m.cfg.Save(); err != nil {
			log.Printf("[Douyin] save account metadata: %v", err)
		}
		go func() {
			if err := m.Sync(); err != nil {
				log.Printf("[Douyin] resync after account update: %v", err)
			}
		}()
	}
	if enabled && event.LiveID != "" {
		m.ensureLive(event.LiveID)
	}
}

func latestTimestampedDouyinPost(posts []douyinPost, now time.Time) (douyinPost, bool) {
	var latest douyinPost
	maxTime := now.Add(5 * time.Minute).Unix()
	for _, post := range posts {
		if post.CreateTime <= 0 || post.CreateTime > maxTime {
			continue
		}
		if latest.CreateTime == 0 || post.CreateTime > latest.CreateTime {
			latest = post
		}
	}
	return latest, latest.CreateTime > 0
}

func unseenDouyinPosts(posts []douyinPost, lastTime int64, now time.Time) []douyinPost {
	maxTime := now.Add(5 * time.Minute).Unix()
	result := make([]douyinPost, 0)
	seen := make(map[string]bool)
	for _, post := range posts {
		if post.ID == "" || seen[post.ID] || post.CreateTime <= lastTime || post.CreateTime > maxTime {
			continue
		}
		seen[post.ID] = true
		result = append(result, post)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreateTime == result[j].CreateTime {
			return result[i].ID < result[j].ID
		}
		return result[i].CreateTime < result[j].CreateTime
	})
	return result
}

func (m *DouyinMonitor) handlePosts(event douyinBrowserEvent) {
	if len(event.Posts) == 0 {
		return
	}
	sec := strings.TrimSpace(event.SecUserID)
	m.noteDouyinWorksSuccess(sec)
	type dispatch struct {
		groupID int64
		cfg     config.DouyinConfig
		posts   []douyinPost
	}
	var jobs []dispatch
	m.mu.Lock()
	for groupID, group := range m.cfg.DouyinSubscriptions {
		item := group[sec]
		if item == nil || item.Disabled || item.WorksDisabled {
			continue
		}
		if event.Nickname != "" {
			item.Name = event.Nickname
		}
		now := time.Now()
		latest, ok := latestTimestampedDouyinPost(event.Posts, now)
		if !ok {
			continue
		}
		if item.LastAwemeTime == 0 {
			item.LastAwemeID = latest.ID
			item.LastAwemeTime = latest.CreateTime
			continue
		}
		posts := unseenDouyinPosts(event.Posts, item.LastAwemeTime, now)
		if len(posts) > 0 {
			jobs = append(jobs, dispatch{groupID: groupID, cfg: *item, posts: posts})
		}
		if latest.CreateTime > item.LastAwemeTime {
			item.LastAwemeID = latest.ID
			item.LastAwemeTime = latest.CreateTime
		}
	}
	m.mu.Unlock()
	if err := m.cfg.Save(); err != nil {
		log.Printf("[Douyin] save post cursor: %v", err)
	}
	for _, job := range jobs {
		for _, post := range job.posts {
			m.dispatchPost(job.groupID, job.cfg, post)
		}
	}
}

func truncateRunes(text string, max int) string {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max]) + "…"
}

func canonicalDouyinPostURL(post douyinPost) string {
	if post.ID == "" {
		return post.URL
	}
	kind := "video"
	if post.Type == "note" {
		kind = "note"
	}
	return fmt.Sprintf("https://www.douyin.com/%s/%s", kind, post.ID)
}

// sendToTargets delivers content to an explicit target id list. An empty list
// means the subscription has no destination configured, so nothing is sent.
func (m *DouyinMonitor) sendToTargets(targetIDs []string, content interface{}) {
	if m == nil || m.outbound == nil || m.cfg == nil {
		return
	}
	resolve := func(id string) outbound.Target {
		t := m.cfg.ResolveTarget(id)
		if t.Address == "" {
			return outbound.Target{}
		}
		kind := outbound.GroupChat
		if t.Kind == "private" {
			kind = outbound.PrivateChat
		}
		if t.Platform == "qq" {
			if nid, err := strconv.ParseInt(t.Address, 10, 64); err == nil {
				return outbound.Target{Platform: "qq", Kind: kind, ID: nid, Address: t.Address}
			}
		}
		return outbound.Target{Platform: t.Platform, Kind: kind, Address: t.Address}
	}
	outbound.SendToTargetIDs(m.outbound, resolve, targetIDs, content)
}

func (m *DouyinMonitor) dispatchPost(groupID int64, item config.DouyinConfig, post douyinPost) {
	// Weibo-aligned: title box = raw nick only; body = 小包(胡晓慧) / 一盆蘸酱菜(卢天惠).
	titleNick := resolveDouyinWorksTitleNick(post.Nickname, item.Name)
	if titleNick == "" {
		titleNick = item.SecUserID
	}
	bodyLabel := resolveDouyinWorksBodyLabel(m.cfg, item.SecUserID, post.Nickname, item.Name)
	typeName := "视频"
	if post.Type == "note" {
		typeName = "图文"
	}
	// 【昵称|抖音】 + 正文区「配对名发布了新视频」+ desc（对齐微博：标题只昵称，内容区再写名字）
	lines := []string{fmt.Sprintf("【%s|抖音】", titleNick)}
	// ★ 2026-10-04 去掉「XXX 发布了新视频」那一行（用户明确要求）：
	// 顶栏已经写了昵称，正文第一行再重复一次是纯冗余；而且顶栏一改
	// 这一行就会对不上（此前就有过「顶栏显示我、正文显示昵称」的问题）。
	// 改为直接用 desc 当正文首行。
	_ = typeName
	_ = bodyLabel
	if post.Desc != "" {
		lines = append(lines, truncateRunes(post.Desc, 600))
	}
	lines = append(lines, "", "抖音链接："+canonicalDouyinPostURL(post))
	segments := make([]interface{}, 0, 12)
	if item.AtAll {
		// @全体成员 独立成行，避免与正文首行粘连
		segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	segments = append(segments, napcat.TextSegment(strings.Join(lines, "\n")+"\n"))
	// ★ 必须打这一行（2026-10-05 补）。
	//
	// sendToTargets 内部**一条日志都没有**，抖音作品推送在 bot.log 里
	// 完全不留痕迹 —— 结果排查「17:05 抖音那条延迟两分钟」时，
	// grep 不到任何记录，被误判成「抖音没发」。
	// 同一个症状（B站/ X / Melon 都有日志）下，唯独抖音没有，
	// 这个不对称本身就是 bug。延迟排查只能靠作品发布时间反推。
	log.Printf("[Douyin] 作品已入推送队列: %s type=%s createTime=%s duration=%d desc=%.40s",
		titleNick, post.Type,
		time.Unix(post.CreateTime, 0).Format("15:04:05"), post.Duration, post.Desc)
	images := post.Images
	if len(images) == 0 && post.Cover != "" {
		images = []string{post.Cover}
	}
	for i, image := range images {
		if i >= 9 || !strings.HasPrefix(image, "http") {
			break
		}
		segments = append(segments, napcat.ImageSegment(image))
	}
	if post.CreateTime > 0 {
		segments = append(segments, napcat.TextSegment("\n"+time.Unix(post.CreateTime, 0).Format("2006-01-02 15:04:05")))
	}
	m.sendToTargets(item.TargetIDs, segments)
	// ★ 视频本体：跨平台去重**只在这里**生效（2026-10-05）。
	//
	// 用户口径：「视频本体只发第一次；20 分钟内这个时长的视频不下载、不发送；
	//          文字、封面这些都是会发的。」
	//
	// 上面的文字+封面已经发完，所以命中去重时只跳视频，不影响其它部分。
	// 判定必须在**下载前** —— 这正是需要 douyinPost.Duration 的原因。
	localVideo := ""
	configPath := m.cfg.ConfigPath()
	if douyinVideoAlreadySent(configPath, post) {
		log.Printf("[Douyin] 跳过视频（%d 秒内有平台已发过同一条）: id=%s %d秒 desc=%.40s",
			dedupe.MatchWindowMillis/60000, post.ID, post.Duration, post.Desc)
	} else if strings.HasPrefix(post.VideoURL, "http") {
		localVideo = localizeDouyinVideo(post.VideoURL)
		if localVideo != "" {
			m.sendToTargets(item.TargetIDs, []napcat.MessageSegment{napcat.VideoSegment(localVideo, "")})
		}
	}
	// 登记标题指纹 + 时长：既供 B站 / TikTok 侧比对，
	// 也让后来的平台能反过来拦下**本平台重复的视频本体**。
	//
	// ★ 时长是关键：仅靠标题指纹匹配不上「B 站多一段【Hearts2Hearts】前缀」
	// 这种情况（2026-10-04 线上重复推送的根因），必须叠加时长才判得准。
	if configPath != "" {
		// ★ 必须传**昵称/团名**，不能传 sec_uid（2026-10-04 修正）：
		// sec_uid 是抖音内部 ID，与 TikTok 的 username、B 站的作者名
		// 跨平台永不相等，「同作者」这一条永远不成立 ——
		// 于是整个跨语言兜底判定成了死代码（已上线三天才被发现）。
		// 实测三者归一化后是同一个值：抖音订阅名与 B 站作者都叫
		// Hearts2Hearts，TikTok username 叫 hearts2hearts。
		// titleNick 优先取 API 昵称、回落订阅配置名，正是这个值。
		douyinRecordTitle(configPath, post.Desc, post.CreateTime, post.Type,
			probeLocalVideoSeconds(localVideo), titleNick)
	}
	for _, videoURL := range uniqueHTTPURLs(post.LivePhotoVideos) {
		if localVideo := localizeDouyinVideo(videoURL); localVideo != "" {
			m.sendToTargets(item.TargetIDs, []napcat.MessageSegment{napcat.VideoSegment(localVideo, "")})
		}
	}
}

func (m *DouyinMonitor) handleQRCode(event douyinBrowserEvent) {
	if event.ImageBase64 == "" {
		return
	}
	expires := event.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	for _, uid := range uniqueAdminIDs(m.cfg) {
		outbound.SendPrivate(m.outbound, uid, []napcat.MessageSegment{
			napcat.TextSegment(fmt.Sprintf("抖音浏览器登录二维码，请在约 %d 分钟内使用抖音 App 扫码。", expires/60)),
			napcat.ImageSegment("base64://" + event.ImageBase64),
		})
	}
}

func uniqueAdminIDs(cfg *config.Config) []int64 {
	seen := make(map[int64]bool)
	var result []int64
	for _, id := range append([]int64{cfg.SuperAdmin}, cfg.AdminQQ...) {
		if id != 0 && !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result
}

func (m *DouyinMonitor) ensureLive(liveID string) {
	liveID = strings.TrimSpace(liveID)
	if liveID == "" {
		return
	}
	m.mu.Lock()
	if _, ok := m.liveCancels[liveID]; ok || m.stopping {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.liveCancels[liveID] = cancel
	if m.liveStates[liveID] == nil {
		m.liveStates[liveID] = &douyinLiveState{LiveID: liveID, RoomID: liveID, ComboGiftCounts: make(map[string]int64)}
	}
	m.mu.Unlock()
	m.wg.Add(1)
	go m.runLive(ctx, liveID)
}

func (m *DouyinMonitor) runLive(ctx context.Context, liveID string) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		delete(m.liveCancels, liveID)
		delete(m.liveConnected, liveID)
		m.mu.Unlock()
	}()
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		base := strings.TrimRight(strings.TrimSpace(m.cfg.DouyinLiveWSURL), "/")
		endpoint := base + "/" + url.PathEscape(liveID)
		if account := strings.TrimSpace(m.cfg.DouyinLiveCookieAccount); account != "" && m.liveCookie != nil {
			if secret, secretErr := m.liveCookie(account); secretErr == nil && secret != "" {
				if parsed, parseErr := url.Parse(endpoint); parseErr == nil {
					query := parsed.Query()
					query.Set("cookie_b64", base64.RawURLEncoding.EncodeToString([]byte(secret)))
					parsed.RawQuery = query.Encode()
					endpoint = parsed.String()
				}
			}
		}
		conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
		if err != nil {
			m.setLiveConnection(liveID, false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		m.setLiveConnection(liveID, true)
		cancelRead := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = conn.Close()
			case <-cancelRead:
			}
		}()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				_ = conn.Close()
				close(cancelRead)
				m.setLiveConnection(liveID, false)
				break
			}
			// Only system live-status messages are needed for start/end alerts.
			// Ignore high-volume chat/gift/stat payloads immediately so they are
			// neither copied into a queue nor retained in memory.
			m.handleLiveMessage(liveID, raw)
			select {
			case <-ctx.Done():
				_ = conn.Close()
				close(cancelRead)
				return
			default:
			}
		}
	}
}

func (m *DouyinMonitor) setLiveConnection(liveID string, connected bool) {
	m.mu.Lock()
	previous := m.liveConnected[liveID]
	m.liveConnected[liveID] = connected
	m.mu.Unlock()
	if previous != connected {
		status := "reconnecting"
		if connected {
			status = "connected"
		}
		log.Printf("[Douyin-live-health] status=%s live_id=%s", status, liveID)
	}
}

func (m *DouyinMonitor) handleLiveMessage(liveID string, raw []byte) {
	var body map[string]interface{}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	if body["type"] == "system" && body["event"] == "live_status" {
		code, _ := body["code"].(string)
		name, _ := body["live_name"].(string)
		title, _ := body["title"].(string)
		roomID := extractDouyinRoomID(body)
		switch code {
		case "ROOM_ONLINE":
			m.liveOnline(liveID, roomID, name, title)
		case "ROOM_ENDED":
			m.liveEnded(liveID, roomID, name, title)
		}
		return
	}
	// All non-system WebSocket events are intentionally ignored. The live
	// monitor now provides start/end alerts only.
}

func numberAsInt64(value interface{}) int64 {
	switch value := value.(type) {
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case int:
		return int64(value)
	case int64:
		return value
	case int32:
		return int64(value)
	case uint64:
		if value <= uint64(^uint64(0)>>1) {
			return int64(value)
		}
		return 0
	case uint:
		return int64(value)
	case json.Number:
		result, _ := value.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(value, 10, 64)
		return result
	default:
		return 0
	}
}

func douyinValueAtPath(value interface{}, path ...string) interface{} {
	current := value
	for _, part := range path {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current = mapValueFold(object, part)
		if current == nil {
			return nil
		}
	}
	return current
}

func extractDouyinNumberAtPaths(value interface{}, paths ...[]string) int64 {
	for _, path := range paths {
		if result := numberAsInt64(douyinValueAtPath(value, path...)); result > 0 {
			return result
		}
	}
	return 0
}

// extractDouyinCurrentOnline only accepts fields confirmed to represent the
// number currently in the room. In particular, a generic "total" is ignored.
func extractDouyinCurrentOnline(value interface{}) int64 {
	return extractDouyinNumberAtPaths(value,
		[]string{"payload", "onlineUserForAnchor"}, []string{"payload", "onlineUserCount"}, []string{"payload", "userCount"},
		[]string{"data", "onlineUserForAnchor"}, []string{"data", "onlineUserCount"}, []string{"data", "userCount"},
		[]string{"payload", "room", "onlineUserForAnchor"}, []string{"payload", "room", "onlineUserCount"}, []string{"payload", "room", "userCount"},
		[]string{"onlineUserForAnchor"}, []string{"onlineUserCount"}, []string{"userCount"},
	)
}

// extractDouyinTotalAudience only accepts explicit cumulative audience fields.
func extractDouyinTotalAudience(value interface{}) int64 {
	return extractDouyinNumberAtPaths(value,
		[]string{"payload", "audienceCount"}, []string{"payload", "totalUserCount"},
		[]string{"data", "audienceCount"}, []string{"data", "totalUserCount"},
		[]string{"payload", "room", "audienceCount"}, []string{"payload", "room", "totalUserCount"},
		[]string{"audienceCount"}, []string{"totalUserCount"},
	)
}

func extractDouyinRoomID(value interface{}) string {
	paths := [][]string{
		{"room_id"}, {"roomId"}, {"roomID"},
		{"payload", "room_id"}, {"payload", "roomId"}, {"payload", "roomID"}, {"payload", "room", "id"},
		{"data", "room_id"}, {"data", "roomId"}, {"data", "roomID"}, {"data", "room", "id"},
	}
	for _, path := range paths {
		if result := douyinString(douyinValueAtPath(value, path...)); result != "" && result != "0" {
			return result
		}
	}
	return ""
}

// Kept for source compatibility with older tests/extensions.
func extractDouyinOnline(value interface{}) int64 { return extractDouyinCurrentOnline(value) }

func newDouyinLiveState(liveID, roomID, name, title string, now time.Time) *douyinLiveState {
	sessionBase := safeDouyinLiveFilename(liveID)
	if roomID != "" {
		sessionBase += "-" + safeDouyinLiveFilename(roomID)
	}
	return &douyinLiveState{
		SessionID:                fmt.Sprintf("%s-%d", sessionBase, now.UnixNano()),
		LiveID:                   liveID,
		RoomID:                   roomID,
		Name:                     name,
		Title:                    title,
		DetectedStartedAt:        now,
		LastUpdatedAt:            now,
		Online:                   true,
		ComboGiftCounts:          make(map[string]int64),
		StartNotificationPending: true,
		StartNotificationQueued:  make(map[string]bool),
		EndNotificationQueued:    make(map[string]bool),
	}
}

func (m *DouyinMonitor) liveOnline(liveID, roomID, name, title string) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state != nil && state.Online {
		if roomID != "" && state.RoomID != "" && roomID != state.RoomID {
			previousRoomID := state.RoomID
			m.mu.Unlock()
			// A changed real room ID is an authoritative session boundary even
			// if the upstream omitted ROOM_ENDED during reconnect.
			m.liveEnded(liveID, previousRoomID, "", "")
			m.liveOnline(liveID, roomID, name, title)
			return
		}
		if state.RoomID == "" {
			state.RoomID = roomID
		}
		if state.Name == "" {
			state.Name = name
		}
		if state.Title == "" {
			state.Title = title
		}
		m.mu.Unlock()
		// A repeated ROOM_ONLINE is also the recovery trigger for a pending
		// notification after restart. Already queued groups are skipped.
		m.queueLiveNotification(liveID, true)
		return
	}
	m.mu.Unlock()

	// If the previous end notification was pending, give it one last enqueue
	// attempt before replacing the current pointer with the next session.
	if state != nil && state.EndNotificationPending {
		m.queueLiveNotification(liveID, false)
	}

	now := time.Now()
	m.mu.Lock()
	state = newDouyinLiveState(liveID, roomID, name, title, now)
	m.liveStates[liveID] = state
	m.mu.Unlock()
	// Persist pending before enqueue. A crash here retries after restart; a
	// crash after enqueue but before the queued marker can duplicate, not lose.
	m.persistLiveState(liveID, true)
	m.queueLiveNotification(liveID, true)
}

func (m *DouyinMonitor) liveEnded(liveID, roomID, name, title string) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil {
		m.mu.Unlock()
		return
	}
	if !state.Online {
		pending := state.EndNotificationPending
		m.mu.Unlock()
		if pending {
			m.queueLiveNotification(liveID, false)
		}
		return
	}
	if roomID != "" && state.RoomID != "" && roomID != state.RoomID {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	if state.RoomID == "" {
		state.RoomID = roomID
	}
	if name != "" {
		state.Name = name
	}
	if title != "" {
		state.Title = title
	}
	state.Online = false
	state.DetectedEndedAt = now
	state.LastUpdatedAt = now
	state.EndNotificationPending = true
	state.EndNotificationSent = false
	state.ComboGiftCounts = make(map[string]int64)
	m.mu.Unlock()
	m.persistLiveState(liveID, true)
	m.queueLiveNotification(liveID, false)
	m.pruneDouyinLiveHistory(50)
}

type douyinLiveTarget struct {
	groupID int64
	cfg     config.DouyinConfig
}

func (m *DouyinMonitor) liveTargets(liveID string) []douyinLiveTarget {
	var targets []douyinLiveTarget
	m.mu.Lock()
	for groupID, group := range m.cfg.DouyinSubscriptions {
		for _, item := range group {
			if item != nil && !item.Disabled && !item.LiveDisabled && item.LiveID == liveID {
				targets = append(targets, douyinLiveTarget{groupID: groupID, cfg: *item})
			}
		}
	}
	m.mu.Unlock()
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].groupID == targets[j].groupID {
			return targets[i].cfg.SecUserID < targets[j].cfg.SecUserID
		}
		return targets[i].groupID < targets[j].groupID
	})
	return targets
}

func douyinLiveTargetKey(target douyinLiveTarget) string {
	return strconv.FormatInt(target.groupID, 10) + "|" + target.cfg.SecUserID
}

func notificationAlreadyQueued(state *douyinLiveState, online bool, key string) bool {
	queued := state.EndNotificationQueued
	legacySent := state.EndNotificationSent
	if online {
		queued = state.StartNotificationQueued
		legacySent = state.StartNotificationSent
	}
	if queued[key] {
		return true
	}
	// Old c2110ac files only have the aggregate marker. Preserve their
	// duplicate suppression during migration.
	return legacySent && len(queued) == 0
}

func allDouyinTargetsQueued(state *douyinLiveState, online bool, targets []douyinLiveTarget) bool {
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !notificationAlreadyQueued(state, online, douyinLiveTargetKey(target)) {
			return false
		}
	}
	return true
}

func (m *DouyinMonitor) tryEnqueueGroupMessage(targetIDs []string, groupID int64, message interface{}) (queued bool) {
	defer func() {
		if recover() != nil {
			queued = false
		}
	}()
	if m.enqueueGroup != nil && len(targetIDs) == 0 {
		return m.enqueueGroup(groupID, message)
	}
	if m.outbound == nil {
		return false
	}
	m.sendToTargets(targetIDs, message)
	return true
}

func (m *DouyinMonitor) formatLiveNotification(target douyinLiveTarget, state douyinLiveState, online bool) []interface{} {
	titleNick := resolveDouyinWorksTitleNick(state.Name, target.cfg.Name)
	if titleNick == "" {
		titleNick = target.cfg.SecUserID
	}
	bodyLabel := resolveDouyinWorksBodyLabel(m.cfg, target.cfg.SecUserID, state.Name, target.cfg.Name)
	var text string
	if online {
		text = fmt.Sprintf("【%s|抖音直播】", titleNick)
		if bodyLabel != "" && bodyLabel != titleNick {
			text += "\n" + bodyLabel
		}
		text += "\n已开播"
		if state.Title != "" {
			text += "\n直播标题：" + state.Title
		}
		text += "\nhttps://live.douyin.com/" + state.LiveID
		detectedAt := state.DetectedStartedAt
		if detectedAt.IsZero() {
			detectedAt = state.LastUpdatedAt
		}
		if !detectedAt.IsZero() {
			text += "\n" + detectedAt.In(time.Local).Format("2006-01-02 15:04:05")
		}
	} else {
		text = fmt.Sprintf("【%s|抖音直播】", titleNick)
		if bodyLabel != "" && bodyLabel != titleNick {
			text += "\n" + bodyLabel
		}
		text += "\n直播已结束"
		detectedAt := state.DetectedEndedAt
		if detectedAt.IsZero() {
			detectedAt = state.LastUpdatedAt
		}
		if !detectedAt.IsZero() {
			text += "\n" + detectedAt.In(time.Local).Format("2006-01-02 15:04:05")
		}
	}
	segments := make([]interface{}, 0, 2)
	if online && target.cfg.AtAll {
		segments = append(segments, napcat.AtSegment("all"))
		text = "\n" + text
	}
	return append(segments, napcat.TextSegment(text))
}

func (m *DouyinMonitor) queueLiveNotification(liveID string, online bool) {
	targets := m.liveTargets(liveID)
	for _, target := range targets {
		key := douyinLiveTargetKey(target)
		m.mu.Lock()
		state := m.liveStates[liveID]
		if state == nil || notificationAlreadyQueued(state, online, key) {
			m.mu.Unlock()
			continue
		}
		sessionID := state.SessionID
		snapshot := *state
		m.mu.Unlock()

		segments := m.formatLiveNotification(target, snapshot, online)
		if !m.tryEnqueueGroupMessage(target.cfg.TargetIDs, target.groupID, segments) {
			continue
		}

		m.mu.Lock()
		state = m.liveStates[liveID]
		if state == nil || state.SessionID != sessionID {
			m.mu.Unlock()
			continue
		}
		if online {
			if state.StartNotificationQueued == nil {
				state.StartNotificationQueued = make(map[string]bool)
			}
			state.StartNotificationQueued[key] = true
			state.StartNotificationSent = allDouyinTargetsQueued(state, true, targets)
			state.StartNotificationPending = !state.StartNotificationSent
		} else {
			if state.EndNotificationQueued == nil {
				state.EndNotificationQueued = make(map[string]bool)
			}
			state.EndNotificationQueued[key] = true
			state.EndNotificationSent = allDouyinTargetsQueued(state, false, targets)
			state.EndNotificationPending = !state.EndNotificationSent
		}
		m.mu.Unlock()
		// Persist after each group so a partial multi-group enqueue resumes at
		// the first unqueued target after restart.
		m.persistLiveState(liveID, true)
	}
	// Recompute after subscription edits too: removing an unqueued target must
	// not leave the session permanently pending.
	m.mu.Lock()
	state := m.liveStates[liveID]
	changed := false
	if state != nil && allDouyinTargetsQueued(state, online, targets) {
		if online && (!state.StartNotificationSent || state.StartNotificationPending) {
			state.StartNotificationSent = true
			state.StartNotificationPending = false
			changed = true
		}
		if !online && (!state.EndNotificationSent || state.EndNotificationPending) {
			state.EndNotificationSent = true
			state.EndNotificationPending = false
			changed = true
		}
	}
	m.mu.Unlock()
	if changed {
		m.persistLiveState(liveID, true)
	}
}

func (m *DouyinMonitor) retryPendingLiveNotifications() {
	type pendingNotification struct {
		liveID string
		online bool
	}
	m.mu.Lock()
	pending := make([]pendingNotification, 0)
	for liveID, state := range m.liveStates {
		if state == nil {
			continue
		}
		if state.Online && state.StartNotificationPending {
			pending = append(pending, pendingNotification{liveID: liveID, online: true})
		}
		if !state.Online && state.EndNotificationPending {
			pending = append(pending, pendingNotification{liveID: liveID, online: false})
		}
	}
	m.mu.Unlock()
	for _, item := range pending {
		m.queueLiveNotification(item.liveID, item.online)
	}
}

func appendDouyinLiveSummary(text string, duration time.Duration, peak, total, diamonds int64, diamondAvailable bool) string {
	text += "\n监测时长：" + formatDouyinDuration(duration)
	if peak > 0 {
		text += "\n最高在线：" + formatDouyinNumber(peak)
	}
	if total > 0 {
		text += "\n累计场观：" + formatDouyinNumber(total)
	}
	if diamondAvailable && diamonds > 0 {
		text += "\n礼物钻石（WebSocket 采集值）：" + formatDouyinNumber(diamonds)
	}
	return text
}

func formatDouyinNumber(value int64) string {
	text := strconv.FormatInt(value, 10)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

func formatDouyinDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	total := int(duration.Seconds())
	return fmt.Sprintf("%d小时%d分%d秒", total/3600, total%3600/60, total%60)
}

func (m *DouyinMonitor) Add(groupID int64, secUserID, profileURL string, atAll bool) error {
	secUserID = strings.TrimSpace(secUserID)
	if secUserID == "" {
		return fmt.Errorf("缺少 sec_user_id")
	}
	m.mu.Lock()
	if m.cfg.DouyinSubscriptions == nil {
		m.cfg.DouyinSubscriptions = make(map[int64]map[string]*config.DouyinConfig)
	}
	if m.cfg.DouyinSubscriptions[groupID] == nil {
		m.cfg.DouyinSubscriptions[groupID] = make(map[string]*config.DouyinConfig)
	}
	old := m.cfg.DouyinSubscriptions[groupID][secUserID]
	if old == nil {
		old = &config.DouyinConfig{SecUserID: secUserID}
	}
	old.ProfileURL = profileURL
	old.AtAll = atAll
	old.Auto = false
	m.cfg.DouyinSubscriptions[groupID][secUserID] = old
	m.mu.Unlock()
	if err := m.cfg.Save(); err != nil {
		return err
	}
	return m.Sync()
}

func (m *DouyinMonitor) Remove(groupID int64, secUserID string) error {
	m.mu.Lock()
	if secUserID == "" {
		delete(m.cfg.DouyinSubscriptions, groupID)
	} else if group := m.cfg.DouyinSubscriptions[groupID]; group != nil {
		delete(group, secUserID)
		if len(group) == 0 {
			delete(m.cfg.DouyinSubscriptions, groupID)
		}
	}
	m.mu.Unlock()
	if err := m.cfg.Save(); err != nil {
		return err
	}
	return m.Sync()
}

func (m *DouyinMonitor) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	m.stopping = true
	liveCmd := m.liveCmd
	for _, cancel := range m.liveCancels {
		cancel()
	}
	m.mu.Unlock()
	if liveCmd != nil && liveCmd.Process != nil {
		_ = liveCmd.Process.Kill()
	}
	m.wg.Wait()
	m.mu.Lock()
	m.started = false
	m.liveCmd = nil
	store := m.liveStore
	m.liveStore = nil
	m.mu.Unlock()
	if store != nil {
		_ = store.close()
	}
}

func (m *DouyinMonitor) ensureLiveStore() error {
	m.mu.Lock()
	if m.liveStore != nil {
		m.mu.Unlock()
		return nil
	}
	path := m.liveStorePath
	m.mu.Unlock()
	store, err := openDouyinLiveStore(path)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.liveStore == nil {
		m.liveStore = store
		store = nil
	}
	m.mu.Unlock()
	if store != nil {
		_ = store.close()
	}
	return nil
}

var douyinURLPattern = regexp.MustCompile(`https?://[^\s]+`)

func allowedDouyinHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	return host == "douyin.com" || strings.HasSuffix(host, ".douyin.com") ||
		host == "iesdouyin.com" || strings.HasSuffix(host, ".iesdouyin.com")
}

func secUserIDFromURL(target *url.URL) string {
	if target == nil {
		return ""
	}
	parts := strings.Split(target.Path, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "user" && strings.TrimSpace(parts[i+1]) != "" {
			sec, err := url.PathUnescape(parts[i+1])
			if err == nil {
				return strings.TrimSpace(sec)
			}
		}
	}
	return ""
}

func ResolveDouyinTarget(ctx context.Context, input string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("目标不能为空")
	}
	match := douyinURLPattern.FindString(input)
	if match == "" {
		if regexp.MustCompile(`^[A-Za-z0-9_.-]{8,}$`).MatchString(input) {
			return input, "https://www.douyin.com/user/" + url.PathEscape(input), nil
		}
		return "", "", fmt.Errorf("请提供抖音主页链接或 sec_user_id")
	}
	match = strings.TrimRight(match, ").,，。]】")
	parsed, err := url.Parse(match)
	if err != nil || !allowedDouyinHost(parsed.Hostname()) {
		return "", "", fmt.Errorf("只支持 douyin.com 官方主页或分享链接")
	}
	if sec := secUserIDFromURL(parsed); sec != "" {
		return sec, parsed.String(), nil
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !allowedDouyinHost(req.URL.Hostname()) {
				return fmt.Errorf("抖音分享链接跳转到了非官方域名")
			}
			if len(via) >= 10 {
				return fmt.Errorf("抖音分享链接重定向次数过多")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, match, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("解析分享链接失败: %w", err)
	}
	_ = resp.Body.Close()
	finalURL := resp.Request.URL.String()
	if sec := secUserIDFromURL(resp.Request.URL); sec != "" {
		return sec, finalURL, nil
	}
	return "", "", fmt.Errorf("链接没有解析到抖音用户主页")
}
