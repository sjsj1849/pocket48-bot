package logic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gorilla/websocket"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
)

type weiboAuthEvent struct {
	Type             string       `json:"type"`
	WebCookie        string       `json:"webCookie,omitempty"`
	MobileCookie     string       `json:"mobileCookie,omitempty"`
	Reason           string       `json:"reason,omitempty"`
	ImageBase64      string       `json:"imageBase64,omitempty"`
	ExpiresIn        int          `json:"expiresIn,omitempty"`
	Status           string       `json:"status,omitempty"`
	Message          string       `json:"message,omitempty"`
	SecUserID        string       `json:"secUserId,omitempty"`
	ProfileURL       string       `json:"profileUrl,omitempty"`
	Nickname         string       `json:"nickname,omitempty"`
	LiveID           string       `json:"liveId,omitempty"`
	Posts            []douyinPost `json:"posts,omitempty"`
	GroupName        string       `json:"groupName,omitempty"`
	GroupNumber      string       `json:"groupNumber,omitempty"`
	ConversationID   string       `json:"conversationId,omitempty"`
	ConversationType int          `json:"conversationType,omitempty"`
	OwnerUID         string       `json:"ownerUid,omitempty"`
	SelfUID          string       `json:"selfUid,omitempty"`
	SenderUID        string       `json:"senderUid,omitempty"`
	SenderSecUID     string       `json:"senderSecUid,omitempty"`
	SenderName       string       `json:"senderName,omitempty"`
	ServerMessageID  string       `json:"serverMessageId,omitempty"`
	MessageType      int          `json:"messageType,omitempty"`
	CreateTime       int64        `json:"createTime,omitempty"`
	ReceivedAt       int64        `json:"receivedAt,omitempty"`
	QuotedName       string       `json:"quotedName,omitempty"`
	QuotedText       string       `json:"quotedText,omitempty"`
	Text             string       `json:"text,omitempty"`
	Link             string       `json:"link,omitempty"`
	Index            string       `json:"index,omitempty"`
}

type weiboAuthCommand struct {
	Cmd                    string                 `json:"cmd"`
	ProfileDir             string                 `json:"profileDir,omitempty"`
	Headless               bool                   `json:"headless"`
	RefreshMinutes         int                    `json:"refreshMinutes,omitempty"`
	WebCookie              string                 `json:"webCookie,omitempty"`
	MobileCookie           string                 `json:"mobileCookie,omitempty"`
	AllowQRCode            bool                   `json:"allowQRCode"`
	Reason                 string                 `json:"reason,omitempty"`
	WeiboEnabled           bool                   `json:"weiboEnabled"`
	DouyinEnabled          bool                   `json:"douyinEnabled"`
	DouyinPollSeconds      int                    `json:"douyinPollSeconds,omitempty"`
	DouyinAccounts         []douyinAccountCommand `json:"douyinAccounts,omitempty"`
	DouyinIMEnabled        bool                   `json:"douyinIMEnabled"`
	DouyinIMPrivateEnabled bool                   `json:"douyinIMPrivateEnabled"`
	DouyinIMGroupName      string                 `json:"douyinIMGroupName,omitempty"`
	DouyinIMGroupNumber    string                 `json:"douyinIMGroupNumber,omitempty"`
}

type WeiboAuthBridge struct {
	cfg *config.Config

	mu       sync.RWMutex
	writeMu  sync.Mutex
	cmd      *exec.Cmd
	conn     *websocket.Conn
	started  bool
	stopping bool
	stopRun  bool
	wg       sync.WaitGroup

	onCookies func(webCookie, mobileCookie, reason string)
	onQRCode  func(imageBase64 string, expiresIn int)
	onStatus  func(status, message string)
	onError   func(error)
	onDouyin  func(douyinBrowserEvent)
}

func (b *WeiboAuthBridge) SetDouyinCallback(callback func(douyinBrowserEvent)) {
	b.onDouyin = callback
}

func NewWeiboAuthBridge(cfg *config.Config) *WeiboAuthBridge {
	return &WeiboAuthBridge{cfg: cfg}
}

func (b *WeiboAuthBridge) SetCallbacks(
	onCookies func(webCookie, mobileCookie, reason string),
	onQRCode func(imageBase64 string, expiresIn int),
	onStatus func(status, message string),
	onError func(error),
) {
	b.onCookies = onCookies
	b.onQRCode = onQRCode
	b.onStatus = onStatus
	b.onError = onError
}

func parseWeiboAuthCommand(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		line = "node ./sidecar/weibo-auth/index.mjs"
	}
	var args []string
	var current strings.Builder
	var quote rune
	flush := func() {
		if current.Len() > 0 {
			args = append(args, current.String())
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
		return nil, fmt.Errorf("WEIBO_BROWSER_AUTH_CMD contains an unclosed quote")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("WEIBO_BROWSER_AUTH_CMD is empty")
	}
	if strings.HasSuffix(strings.ToLower(args[0]), ".mjs") || strings.HasSuffix(strings.ToLower(args[0]), ".js") {
		args = append([]string{"node"}, args...)
	}
	return args, nil
}

func (b *WeiboAuthBridge) Start() error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("weibo auth bridge already started")
	}
	b.started = true
	b.stopping = false
	b.stopRun = false
	b.mu.Unlock()

	parts, err := parseWeiboAuthCommand(b.cfg.BrowserSidecarCmd)
	if err != nil {
		b.reset()
		return err
	}
	args := append([]string{}, parts[1:]...)
	hasPort := false
	for _, arg := range args {
		if strings.HasPrefix(arg, "--wsPort=") {
			hasPort = true
			break
		}
	}
	if !hasPort {
		args = append(args, "--wsPort=0")
	}

	cmd := exec.Command(parts[0], args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		b.reset()
		return fmt.Errorf("weibo auth stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		b.reset()
		return fmt.Errorf("weibo auth stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		b.reset()
		return fmt.Errorf("start weibo auth sidecar: %w", err)
	}
	b.mu.Lock()
	b.cmd = cmd
	b.mu.Unlock()
	go scanSidecarLogs(stderr, "[Weibo-auth]")

	portCh := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		reported := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !reported && strings.HasPrefix(line, "PORT:") {
				port, parseErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PORT:")))
				if parseErr != nil {
					errCh <- fmt.Errorf("parse weibo auth port %q: %w", line, parseErr)
					return
				}
				reported = true
				portCh <- port
				continue
			}
			log.Printf("[Weibo-auth:stdout] %s", line)
		}
		if !reported {
			errCh <- fmt.Errorf("weibo auth sidecar exited before reporting port")
		}
	}()

	var port int
	select {
	case port = <-portCh:
	case startErr := <-errCh:
		_ = cmd.Process.Kill()
		b.reset()
		return startErr
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		b.reset()
		return fmt.Errorf("weibo auth sidecar startup timeout")
	}

	u := url.URL{Scheme: "ws", Host: fmt.Sprintf("127.0.0.1:%d", port), Path: "/"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		_ = cmd.Process.Kill()
		b.reset()
		return fmt.Errorf("connect to weibo auth sidecar: %w", err)
	}
	b.mu.Lock()
	b.conn = conn
	b.mu.Unlock()
	b.wg.Add(1)
	go b.readLoop()

	if err := b.send(weiboAuthCommand{
		Cmd:                    "start",
		ProfileDir:             b.cfg.BrowserProfileDir,
		Headless:               b.cfg.BrowserHeadless,
		RefreshMinutes:         b.cfg.WeiboBrowserRefreshMinutes,
		WebCookie:              b.cfg.WeiboCookie,
		MobileCookie:           b.cfg.WeiboMWeiboCookie,
		AllowQRCode:            true,
		WeiboEnabled:           b.cfg.WeiboBrowserAuthEnabled,
		DouyinEnabled:          b.cfg.DouyinEnabled,
		DouyinPollSeconds:      b.cfg.DouyinPollSeconds,
		DouyinAccounts:         douyinAccountsFromConfig(b.cfg),
		DouyinIMEnabled:        b.cfg.DouyinIMEnabled,
		DouyinIMPrivateEnabled: b.cfg.DouyinIMPrivateEnabled,
		DouyinIMGroupName:      b.cfg.DouyinIMGroupName,
		DouyinIMGroupNumber:    b.cfg.DouyinIMGroupNumber,
	}); err != nil {
		b.Stop()
		return err
	}
	log.Printf("[Weibo-auth] sidecar started pid=%d", cmd.Process.Pid)
	return nil
}

func (b *WeiboAuthBridge) reset() {
	b.mu.Lock()
	b.started = false
	b.cmd = nil
	b.conn = nil
	b.mu.Unlock()
}

func (b *WeiboAuthBridge) readLoop() {
	defer b.wg.Done()
	for {
		b.mu.RLock()
		conn := b.conn
		stopping := b.stopping
		b.mu.RUnlock()
		if conn == nil || stopping {
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			b.mu.RLock()
			stopping = b.stopping
			b.mu.RUnlock()
			if !stopping && b.onError != nil {
				b.onError(fmt.Errorf("weibo auth websocket read: %w", err))
			}
			return
		}
		var event weiboAuthEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			log.Printf("[Weibo-auth] invalid event: %v", err)
			continue
		}
		switch event.Type {
		case "cookies":
			if b.onCookies != nil {
				b.onCookies(event.WebCookie, event.MobileCookie, event.Reason)
			}
		case "qrcode":
			if b.onQRCode != nil {
				b.onQRCode(event.ImageBase64, event.ExpiresIn)
			}
		case "status":
			if b.onStatus != nil {
				b.onStatus(event.Status, event.Message)
			}
		case "error":
			if b.onError != nil {
				b.onError(fmt.Errorf("%s", event.Message))
			}
		case "log":
			log.Printf("[Weibo-auth] %s", event.Message)
		case "douyin_account", "douyin_posts", "douyin_qrcode", "douyin_status", "douyin_account_error", "douyin_error", "douyin_im_group", "douyin_im_message", "douyin_im_status":
			if b.onDouyin != nil {
				b.onDouyin(douyinBrowserEvent{
					Type: strings.TrimPrefix(event.Type, "douyin_"), SecUserID: event.SecUserID,
					ProfileURL: event.ProfileURL, Nickname: event.Nickname, LiveID: event.LiveID,
					Posts: event.Posts, ImageBase64: event.ImageBase64, ExpiresIn: event.ExpiresIn,
					Status: event.Status, Message: event.Message,
					GroupName: event.GroupName, GroupNumber: event.GroupNumber,
					ConversationID: event.ConversationID, ConversationType: event.ConversationType,
					OwnerUID: event.OwnerUID, SelfUID: event.SelfUID, SenderUID: event.SenderUID,
					SenderSecUID: event.SenderSecUID, SenderName: event.SenderName,
					ServerMessageID: event.ServerMessageID, MessageType: event.MessageType,
					CreateTime: event.CreateTime, ReceivedAt: event.ReceivedAt,
					QuotedName: event.QuotedName, QuotedText: event.QuotedText,
					Text: event.Text, Link: event.Link, Index: event.Index,
				})
			}
		}
	}
}

func (b *WeiboAuthBridge) send(command weiboAuthCommand) error {
	b.mu.RLock()
	conn := b.conn
	stopping := b.stopping
	b.mu.RUnlock()
	if conn == nil || stopping {
		return fmt.Errorf("weibo auth sidecar is not connected")
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return err
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (b *WeiboAuthBridge) RequestRefresh(reason string) error {
	return b.send(weiboAuthCommand{Cmd: "refresh", AllowQRCode: true, Reason: reason})
}

func douyinAccountsFromConfig(cfg *config.Config) []douyinAccountCommand {
	seen := make(map[string]douyinAccountCommand)
	for _, group := range cfg.DouyinSubscriptions {
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

func (b *WeiboAuthBridge) SyncDouyin() error {
	return b.send(weiboAuthCommand{Cmd: "douyin_sync", DouyinEnabled: true, DouyinPollSeconds: b.cfg.DouyinPollSeconds, DouyinAccounts: douyinAccountsFromConfig(b.cfg)})
}

func (b *WeiboAuthBridge) ScanDouyin() error {
	return b.send(weiboAuthCommand{Cmd: "douyin_scan"})
}

func (b *WeiboAuthBridge) RequestDouyinLogin() error {
	return b.send(weiboAuthCommand{Cmd: "douyin_login"})
}

func (b *WeiboAuthBridge) IsStarted() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.started && b.conn != nil && !b.stopping
}

func (b *WeiboAuthBridge) EnsureStarted() error {
	if b.IsStarted() {
		return nil
	}
	err := b.Start()
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "already started") {
		return err
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b.IsStarted() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("shared browser bridge startup timeout")
}

// BeginStop suppresses disconnect errors as soon as process shutdown starts.
// systemd may terminate child processes before the bridge reaches Stop.
func (b *WeiboAuthBridge) BeginStop() {
	b.mu.Lock()
	if b.started {
		b.stopping = true
	}
	b.mu.Unlock()
}

func (b *WeiboAuthBridge) Stop() {
	b.mu.Lock()
	if !b.started || b.stopRun {
		b.mu.Unlock()
		return
	}
	b.stopping = true
	b.stopRun = true
	conn := b.conn
	cmd := b.cmd
	b.conn = nil
	b.mu.Unlock()

	if conn != nil {
		payload, _ := json.Marshal(weiboAuthCommand{Cmd: "shutdown"})
		b.writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, payload)
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"))
		b.writeMu.Unlock()
		_ = conn.Close()
	}
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	b.wg.Wait()
	b.reset()
	log.Printf("[Weibo-auth] sidecar stopped")
}

func (b *Bot) startWeiboAuthBridge() {
	if err := b.weiboAuth.Start(); err != nil {
		log.Printf("[Browser] failed to start bridge: %v", err)
		b.notifyAdmins(fmt.Sprintf("⚠️ 平台浏览器侧卡启动失败：%v\n请检查 Node.js、侧卡依赖和 Chromium 是否已安装。", err))
	}
}

func (b *Bot) handleWeiboAuthCookies(webCookie, mobileCookie, reason string) {
	webCookie = strings.TrimSpace(webCookie)
	mobileCookie = strings.TrimSpace(mobileCookie)
	changed := false
	if webCookie != "" && webCookie != b.cfg.WeiboCookie {
		b.cfg.WeiboCookie = webCookie
		b.weiboMonitor.SetCookie(webCookie)
		changed = true
	}
	if mobileCookie != "" && mobileCookie != b.cfg.WeiboMWeiboCookie {
		b.cfg.WeiboMWeiboCookie = mobileCookie
		b.weiboMonitor.SetMWeiboCookie(mobileCookie)
		changed = true
	}
	if changed {
		if err := b.cfg.Save(); err != nil {
			b.handleWeiboAuthError(fmt.Errorf("保存自动更新的微博 Cookie: %w", err))
			return
		}
		log.Printf("[Weibo-auth] browser cookies hot-updated (reason=%s)", reason)
	}
	if reason == "login_restored" {
		b.notifyAdmins("✅ 微博浏览器登录态已恢复，weibo.com 与 m.weibo.cn Cookie 已热更新。")
	}
}

func (b *Bot) handleWeiboAuthQRCode(imageBase64 string, expiresIn int) {
	if strings.TrimSpace(imageBase64) == "" {
		return
	}
	if expiresIn <= 0 {
		expiresIn = 600
	}
	message := []napcat.MessageSegment{
		napcat.TextSegment(fmt.Sprintf("⚠️ 微博浏览器登录态已失效。请使用微博 App 扫描下方二维码完成登录，二维码约 %d 分钟内有效。", expiresIn/60)),
		napcat.ImageSegment("base64://" + imageBase64),
	}
	for _, adminID := range b.collectAdminRecipients() {
		if adminID != 0 {
			b.napcat.SendPrivateMessage(adminID, message)
		}
	}
}

func (b *Bot) handleWeiboAuthStatus(status, message string) {
	log.Printf("[Weibo-auth] status=%s message=%s", status, message)
	if status == "qrcode_expired" {
		b.notifyAdmins("⚠️ 微博登录二维码已过期。下次监控检测到 Cookie 失效时会自动生成新二维码。")
	}
}

func (b *Bot) handleWeiboAuthError(err error) {
	if err == nil {
		return
	}
	log.Printf("[Weibo-auth] %v", err)
	now := time.Now()
	b.mu.Lock()
	shouldNotify := b.lastWeiboAuthErrorAt.IsZero() || now.Sub(b.lastWeiboAuthErrorAt) >= time.Hour
	if shouldNotify {
		b.lastWeiboAuthErrorAt = now
	}
	b.mu.Unlock()
	if shouldNotify {
		b.notifyAdmins(fmt.Sprintf("⚠️ 微博浏览器认证侧卡异常：%v\n自动告警已进入 1 小时冷却；若持续异常，请检查侧卡日志或重启 Bot。", err))
	}
}
