package logic

import (
	"context"
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

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
)

type douyinAccountCommand struct {
	SecUserID  string `json:"secUserId"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Name       string `json:"name,omitempty"`
	LiveID     string `json:"liveId,omitempty"`
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
	Index            string       `json:"index"`
	IsSelfChat       bool         `json:"isSelfChat,omitempty"`
}

type douyinLiveState struct {
	Online    bool
	StartedAt time.Time
	Peak      int64
	Name      string
	Title     string
}

type DouyinMonitor struct {
	cfg          *config.Config
	napcat       *napcat.Client
	notifyAdmins func(string)

	mu               sync.Mutex
	started          bool
	stopping         bool
	wg               sync.WaitGroup
	liveCmd          *exec.Cmd
	liveCancels      map[string]context.CancelFunc
	liveStates       map[string]*douyinLiveState
	browser          *WeiboAuthBridge
	imConversations  map[string]douyinIMTarget // conversationID -> target
	imSelfUID        string
	imConnected      bool

	// Captcha window: count account_error "验证码中间页" hits that block post lists.
	captchaHits      []time.Time
	captchaLastAlert time.Time

	// IM disconnect watchdog: silent sidecar/bot restart → email only if still down after auto-heal.
	imDisconnectedAt     time.Time
	imLastDisconnectAlert time.Time
	imSidecarRestartAt   time.Time
	imBotRestartAt       time.Time
	imWatchdogStop       chan struct{}
	imWatchdogOnce       sync.Once
	requestBotRestart    func(reason string)
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

func NewDouyinMonitor(cfg *config.Config, client *napcat.Client, notifyAdmins func(string)) *DouyinMonitor {
	return &DouyinMonitor{
		cfg:          cfg,
		napcat:       client,
		notifyAdmins: notifyAdmins,
		liveCancels:     make(map[string]context.CancelFunc),
		liveStates:      make(map[string]*douyinLiveState),
		imConversations: make(map[string]douyinIMTarget),
		imWatchdogStop:  make(chan struct{}),
	}
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
			if item == nil {
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
	accounts := m.accountsLocked()
	m.mu.Unlock()
	for _, account := range accounts {
		if account.LiveID != "" {
			m.ensureLive(account.LiveID)
		}
	}
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
	accounts := m.accountsLocked()
	started := m.started
	browser := m.browser
	desiredLive := make(map[string]bool)
	for _, account := range accounts {
		if account.LiveID != "" {
			desiredLive[account.LiveID] = true
		}
	}
	for liveID, cancel := range m.liveCancels {
		if !desiredLive[liveID] {
			cancel()
			delete(m.liveCancels, liveID)
			delete(m.liveStates, liveID)
		}
	}
	m.mu.Unlock()
	if !started {
		if err := m.Start(); err != nil {
			return err
		}
	}
	for _, account := range accounts {
		if account.LiveID != "" {
			m.ensureLive(account.LiveID)
		}
	}
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
	return ready, len(accounts), len(m.liveCancels), m.imConnected
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
	case "status":
		log.Printf("[Douyin] status=%s message=%s", event.Status, event.Message)
		if event.Status == "login_error" {
			m.notifyAdmins("⚠️ 抖音登录二维码生成失败：" + event.Message)
		}
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
// 3) email ONLY if still down ~90s after auto restarts (manual intervention needed)
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
		// Wait for IM to come back after boot before paging a human (was 3m; user: too slow).
		sinceRestart := now.Sub(time.UnixMilli(marker.RestartedAt))
		if sinceRestart >= 90*time.Second {
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
	if text == "" && len(images) == 0 {
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
	if event.Link != "" {
		if text != "" {
			text += "\n" + event.Link
		} else {
			text = event.Link
		}
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
		for _, image := range images {
			if len(segments) >= 9 { // leave room for trailing time text
				break
			}
			segments = append(segments, napcat.ImageSegment(image))
		}
		if timeText != "" {
			segments = appendTextWithQQFaces(segments, "\n"+timeText)
		}
		m.napcat.SendGroupMessage(m.cfg.BoundGroupID, segments)
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
		text = formatDouyinReplyText(lineName, text, quotedName, event.QuotedText)
		// Business forward (not an ops alert) — still QQ private to admins.
		// Same layout: title+body → images → timestamp.
		header := formatDouyinPrivateNotificationHeader(boxName, lineName, text)
		segments := appendTextWithQQFaces(nil, header)
		for _, image := range images {
			if len(segments) >= 9 {
				break
			}
			segments = append(segments, napcat.ImageSegment(image))
		}
		if timeText != "" {
			segments = appendTextWithQQFaces(segments, "\n"+timeText)
		}
		log.Printf("[Douyin-IM] forward %s type=%d text=%q images=%d link=%q", kind, event.MessageType, truncateDouyinLogText(text, 80), len(images), event.Link)
		for _, uid := range uniqueAdminIDs(m.cfg) {
			m.napcat.SendPrivateMessage(uid, segments)
		}
	}
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
	if name := strings.TrimSpace(event.QuotedName); name != "" {
		return name
	}
	quotedUID := strings.TrimSpace(event.QuotedSenderUID)
	if quotedUID == "" {
		return ""
	}
	if quotedUID == event.SenderUID {
		return senderName
	}
	if quotedUID == event.SelfUID || quotedUID == selfUID {
		return "我"
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
	if text == "" || text == "[回复]" {
		senderName = strings.TrimSpace(senderName)
		if senderName == "" {
			return quotedLine
		}
		return quotedLine + "\n" + senderName + "：（回复）"
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

// isDouyinGarbageQuoteText rejects sec_uid / long opaque tokens that were
// mistakenly used as quoted message text (e.g. MS4wLjABAAAA…).
func isDouyinGarbageQuoteText(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "MS4wLjABAAAA") {
		return true
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
	if nick != "" {
		boxName = nick
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
	for _, group := range m.cfg.DouyinSubscriptions {
		item := group[sec]
		if item == nil {
			continue
		}
		if event.Nickname != "" && item.Name != event.Nickname {
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
	if event.LiveID != "" {
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
	type dispatch struct {
		groupID int64
		cfg     config.DouyinConfig
		posts   []douyinPost
	}
	var jobs []dispatch
	m.mu.Lock()
	for groupID, group := range m.cfg.DouyinSubscriptions {
		item := group[sec]
		if item == nil {
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
	if bodyLabel != "" && bodyLabel != titleNick {
		lines = append(lines, fmt.Sprintf("%s发布了新%s", bodyLabel, typeName))
	} else {
		lines = append(lines, fmt.Sprintf("发布了新%s", typeName))
	}
	if post.Desc != "" {
		lines = append(lines, truncateRunes(post.Desc, 600))
	}
	lines = append(lines, "", "抖音链接："+canonicalDouyinPostURL(post))
	segments := make([]interface{}, 0, 12)
	if item.AtAll {
		segments = append(segments, napcat.AtSegment("all"))
		segments = append(segments, napcat.TextSegment("\n"))
	}
	segments = append(segments, napcat.TextSegment(strings.Join(lines, "\n")+"\n"))
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
	m.napcat.SendGroupMessage(groupID, segments)
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
		m.napcat.SendPrivateMessage(uid, []napcat.MessageSegment{
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
		m.liveStates[liveID] = &douyinLiveState{}
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
		conn, _, err := websocket.DefaultDialer.Dial(base+"/"+url.PathEscape(liveID), nil)
		if err != nil {
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
				break
			}
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

func (m *DouyinMonitor) handleLiveMessage(liveID string, raw []byte) {
	var body map[string]interface{}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	if body["type"] == "system" && body["event"] == "live_status" {
		code, _ := body["code"].(string)
		name, _ := body["live_name"].(string)
		title, _ := body["title"].(string)
		switch code {
		case "ROOM_ONLINE":
			m.liveOnline(liveID, name, title)
		case "ROOM_ENDED":
			m.liveEnded(liveID, name, title)
		}
		return
	}
	method, _ := body["method"].(string)
	if method != "WebcastRoomUserSeqMessage" && method != "WebcastRoomStatsMessage" {
		return
	}
	if online := extractDouyinOnline(body); online > 0 {
		m.mu.Lock()
		state := m.liveStates[liveID]
		if state != nil && state.Online && online > state.Peak {
			state.Peak = online
		}
		m.mu.Unlock()
	}
}

func numberAsInt64(value interface{}) int64 {
	switch value := value.(type) {
	case float64:
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

func extractDouyinOnline(value interface{}) int64 {
	priority := map[string]bool{"onlineuserforanchor": true, "onlineusercount": true, "usercount": true, "total": true}
	var walk func(interface{}) int64
	walk = func(node interface{}) int64 {
		switch node := node.(type) {
		case map[string]interface{}:
			for key, child := range node {
				if priority[strings.ToLower(key)] {
					if result := numberAsInt64(child); result > 0 {
						return result
					}
				}
			}
			for _, child := range node {
				if result := walk(child); result > 0 {
					return result
				}
			}
		case []interface{}:
			for _, child := range node {
				if result := walk(child); result > 0 {
					return result
				}
			}
		}
		return 0
	}
	return walk(value)
}

func (m *DouyinMonitor) liveOnline(liveID, name, title string) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil {
		state = &douyinLiveState{}
		m.liveStates[liveID] = state
	}
	if state.Online {
		m.mu.Unlock()
		return
	}
	state.Online = true
	state.StartedAt = time.Now()
	state.Peak = 0
	state.Name = name
	state.Title = title
	m.mu.Unlock()
	m.broadcastLive(liveID, true, name, title, 0, 0)
}

func (m *DouyinMonitor) liveEnded(liveID, name, title string) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	duration := time.Since(state.StartedAt)
	peak := state.Peak
	if name == "" {
		name = state.Name
	}
	if title == "" {
		title = state.Title
	}
	state.Online = false
	m.mu.Unlock()
	m.broadcastLive(liveID, false, name, title, duration, peak)
}

func (m *DouyinMonitor) broadcastLive(liveID string, online bool, eventName, title string, duration time.Duration, peak int64) {
	type target struct {
		groupID int64
		cfg     config.DouyinConfig
	}
	var targets []target
	m.mu.Lock()
	for groupID, group := range m.cfg.DouyinSubscriptions {
		for _, item := range group {
			if item != nil && item.LiveID == liveID {
				targets = append(targets, target{groupID: groupID, cfg: *item})
			}
		}
	}
	m.mu.Unlock()
	for _, target := range targets {
		titleNick := resolveDouyinWorksTitleNick(eventName, target.cfg.Name)
		if titleNick == "" {
			titleNick = target.cfg.SecUserID
		}
		bodyLabel := resolveDouyinWorksBodyLabel(m.cfg, target.cfg.SecUserID, eventName, target.cfg.Name)
		var text string
		if online {
			text = fmt.Sprintf("【%s|抖音直播】", titleNick)
			if bodyLabel != "" && bodyLabel != titleNick {
				text += "\n" + bodyLabel
			}
			text += "\n已开播"
			if title != "" {
				text += "\n" + title
			}
			text += "\nhttps://live.douyin.com/" + liveID
		} else {
			text = fmt.Sprintf("【%s|抖音直播】", titleNick)
			if bodyLabel != "" && bodyLabel != titleNick {
				text += "\n" + bodyLabel
			}
			text += "\n直播已结束\n直播时长：" + formatDouyinDuration(duration)
			if peak > 0 {
				text += fmt.Sprintf("\n最高在线人数：%d", peak)
			}
		}
		segments := make([]interface{}, 0, 2)
		if target.cfg.AtAll {
			segments = append(segments, napcat.AtSegment("all"))
		}
		segments = append(segments, napcat.TextSegment(text))
		m.napcat.SendGroupMessage(target.groupID, segments)
	}
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
	m.mu.Unlock()
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
