package logic

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
)

var xiaohongshuProfilePattern = regexp.MustCompile(`(?i)/user/profile/([a-z0-9]+)`)
var xiaohongshuUserIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{16,64}$`)

type xiaohongshuAccountCommand struct {
	UserID     string `json:"userId"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Name       string `json:"name,omitempty"`
}

type xiaohongshuNote struct {
	ID         string   `json:"id"`
	UserID     string   `json:"userId"`
	Nickname   string   `json:"nickname"`
	Title      string   `json:"title"`
	Desc       string   `json:"desc"`
	Type       string   `json:"type"`
	URL        string   `json:"url"`
	Cover      string   `json:"cover"`
	Images     []string `json:"images"`
	CreateTime int64    `json:"createTime"`
}

type xiaohongshuBrowserEvent struct {
	Type        string            `json:"type"`
	UserID      string            `json:"userId"`
	ProfileURL  string            `json:"profileUrl"`
	Nickname    string            `json:"nickname"`
	Notes       []xiaohongshuNote `json:"notes"`
	LiveActive  bool              `json:"liveActive"`
	LiveURL     string            `json:"liveUrl"`
	ImageBase64 string            `json:"imageBase64"`
	ExpiresIn   int               `json:"expiresIn"`
	Status      string            `json:"status"`
	Message     string            `json:"message"`
}

// xiaohongshuLiveConfirmTimes: require this many consecutive same liveActive
// readings before emitting 开播/下播 (reduces false flips from flaky DOM).
const xiaohongshuLiveConfirmTimes = 2

type xiaohongshuLivePending struct {
	active bool
	count  int
}

type XiaohongshuMonitor struct {
	cfg          *config.Config
	napcat       *napcat.Client
	notifyAdmins func(string)
	browser      *WeiboAuthBridge
	mu           sync.Mutex
	// livePending[userID] accumulates consecutive opposite-of-config samples.
	livePending map[string]xiaohongshuLivePending
	// loginAlertHits: sliding window of re-login / risk failures for email alerts.
	loginAlertHits []time.Time
	loginLastAlert time.Time
}

func NewXiaohongshuMonitor(cfg *config.Config, client *napcat.Client, notifyAdmins func(string)) *XiaohongshuMonitor {
	return &XiaohongshuMonitor{
		cfg:          cfg,
		napcat:       client,
		notifyAdmins: notifyAdmins,
		livePending:  make(map[string]xiaohongshuLivePending),
	}
}

func (m *XiaohongshuMonitor) SetBrowserBridge(browser *WeiboAuthBridge) { m.browser = browser }

func xiaohongshuAccountsFromConfig(cfg *config.Config) []xiaohongshuAccountCommand {
	seen := make(map[string]xiaohongshuAccountCommand)
	for _, group := range cfg.XiaohongshuSubscriptions {
		for key, item := range group {
			if item == nil {
				continue
			}
			id := strings.TrimSpace(item.UserID)
			if id == "" {
				id = strings.TrimSpace(key)
			}
			if id == "" {
				continue
			}
			current := seen[id]
			current.UserID = id
			if current.ProfileURL == "" {
				current.ProfileURL = item.ProfileURL
			}
			if current.Name == "" {
				current.Name = item.Name
			}
			seen[id] = current
		}
	}
	result := make([]xiaohongshuAccountCommand, 0, len(seen))
	for _, item := range seen {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result
}

func ResolveXiaohongshuTarget(ctx context.Context, target string) (string, string, error) {
	target = strings.TrimSpace(target)
	if xiaohongshuUserIDPattern.MatchString(target) && !strings.Contains(target, ".") {
		return target, "https://www.xiaohongshu.com/user/profile/" + target, nil
	}
	match := xiaohongshuProfilePattern.FindStringSubmatch(target)
	if len(match) == 2 {
		if parsed, err := url.Parse(target); err == nil {
			return match[1], parsed.String(), nil
		}
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" {
		return "", "", fmt.Errorf("请输入小红书个人主页链接或内部 user_id")
	}
	if !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "xhslink.com") {
		return "", "", fmt.Errorf("链接不是小红书个人主页")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 {
			return fmt.Errorf("重定向过多")
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("解析小红书分享链接失败: %w", err)
	}
	resp.Body.Close()
	finalURL := resp.Request.URL.String()
	match = xiaohongshuProfilePattern.FindStringSubmatch(finalURL)
	if len(match) != 2 {
		return "", "", fmt.Errorf("分享链接没有指向小红书个人主页")
	}
	return match[1], finalURL, nil
}

func (m *XiaohongshuMonitor) Sync() error {
	if m.browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := m.browser.EnsureStarted(); err != nil {
		return err
	}
	return m.browser.SyncXiaohongshu()
}

func (m *XiaohongshuMonitor) Scan() error {
	if m.browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := m.browser.EnsureStarted(); err != nil {
		return err
	}
	return m.browser.ScanXiaohongshu()
}

func (m *XiaohongshuMonitor) RequestLogin() error {
	if m.browser == nil {
		return fmt.Errorf("shared browser bridge is not configured")
	}
	if err := m.browser.EnsureStarted(); err != nil {
		return err
	}
	return m.browser.RequestXiaohongshuLogin()
}

func (m *XiaohongshuMonitor) Add(groupID int64, userID, profileURL string, atAll bool) error {
	userID = strings.TrimSpace(userID)
	if userID == "" || groupID == 0 {
		return fmt.Errorf("缺少 user_id 或目标群")
	}
	m.mu.Lock()
	if m.cfg.XiaohongshuSubscriptions == nil {
		m.cfg.XiaohongshuSubscriptions = make(map[int64]map[string]*config.XiaohongshuConfig)
	}
	if m.cfg.XiaohongshuSubscriptions[groupID] == nil {
		m.cfg.XiaohongshuSubscriptions[groupID] = make(map[string]*config.XiaohongshuConfig)
	}
	item := m.cfg.XiaohongshuSubscriptions[groupID][userID]
	if item == nil {
		item = &config.XiaohongshuConfig{UserID: userID}
	}
	item.ProfileURL, item.AtAll = profileURL, atAll
	m.cfg.XiaohongshuSubscriptions[groupID][userID] = item
	m.mu.Unlock()
	if err := m.cfg.Save(); err != nil {
		return err
	}
	return m.Sync()
}

func (m *XiaohongshuMonitor) Remove(groupID int64, userID string) error {
	m.mu.Lock()
	if userID == "" {
		delete(m.cfg.XiaohongshuSubscriptions, groupID)
	} else if group := m.cfg.XiaohongshuSubscriptions[groupID]; group != nil {
		delete(group, userID)
		if len(group) == 0 {
			delete(m.cfg.XiaohongshuSubscriptions, groupID)
		}
	}
	m.mu.Unlock()
	if err := m.cfg.Save(); err != nil {
		return err
	}
	return m.Sync()
}

func (m *XiaohongshuMonitor) Snapshot(groupID int64) []config.XiaohongshuConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]config.XiaohongshuConfig, 0)
	for _, item := range m.cfg.XiaohongshuSubscriptions[groupID] {
		if item != nil {
			result = append(result, *item)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UserID < result[j].UserID })
	return result
}

func (m *XiaohongshuMonitor) Status() (bool, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.browser != nil && m.browser.IsStarted(), len(xiaohongshuAccountsFromConfig(m.cfg))
}

func (m *XiaohongshuMonitor) HandleBrowserEvent(event xiaohongshuBrowserEvent) {
	switch event.Type {
	case "account":
		m.handleAccount(event)
	case "notes":
		m.handleNotes(event)
	case "qrcode":
		m.handleQRCode(event)
	case "account_error", "error":
		log.Printf("[Xiaohongshu] %s %s: %s", event.Type, event.UserID, event.Message)
		// Data-plane failure — surface for admin overview (healthy Cookie alone is wrong).
		log.Printf("[Xiaohongshu] status=degraded message=%s user=%s", event.Message, event.UserID)
		m.noteXiaohongshuLoginError(event.UserID, event.Message)
	case "status":
		log.Printf("[Xiaohongshu] status=%s message=%s", event.Status, event.Message)
		// login_required / qrcode_expired are real service issues → email (not QQ).
		st := strings.ToLower(strings.TrimSpace(event.Status))
		if st == "login_required" || st == "qrcode_expired" || st == "login_error" {
			m.noteXiaohongshuLoginError(event.UserID, event.Message)
		}
	}
}

// noteXiaohongshuLoginError emails when re-login/risk/notes-empty hits the window threshold.
// Defaults: window=30m, threshold=3 hits, cooldown=60m. Email-only via notifyAdmins.
func (m *XiaohongshuMonitor) noteXiaohongshuLoginError(userID, message string) {
	msg := strings.TrimSpace(message)
	if msg == "" {
		return
	}
	// Only real auth/data-block signals (not transient navigation glitches).
	if !strings.Contains(msg, "重新登录") &&
		!strings.Contains(msg, "需要登录") &&
		!strings.Contains(msg, "风控") &&
		!strings.Contains(msg, "安全限制") &&
		!strings.Contains(msg, "二维码") &&
		!strings.Contains(msg, "notes empty") &&
		!strings.Contains(msg, "笔记列表暂不可用") &&
		!strings.Contains(msg, "Account abnormal") &&
		!strings.Contains(msg, "账号异常") &&
		!strings.Contains(msg, "300011") &&
		!strings.Contains(msg, "300012") {
		return
	}
	const (
		window    = 30 * time.Minute
		threshold = 3
		cooldown  = 60 * time.Minute
	)
	now := time.Now()
	m.mu.Lock()
	kept := m.loginAlertHits[:0]
	for _, t := range m.loginAlertHits {
		if now.Sub(t) <= window {
			kept = append(kept, t)
		}
	}
	m.loginAlertHits = append(kept, now)
	count := len(m.loginAlertHits)
	shouldAlert := count >= threshold && (m.loginLastAlert.IsZero() || now.Sub(m.loginLastAlert) >= cooldown)
	if shouldAlert {
		m.loginLastAlert = now
	}
	m.mu.Unlock()

	log.Printf("[Xiaohongshu] login-alert window hits=%d/%d user=%s msg=%s", count, threshold, userID, msg)
	if !shouldAlert || m.notifyAdmins == nil {
		return
	}
	m.notifyAdmins(fmt.Sprintf(
		"⚠️ 小红书监控登录/拉帖告警\n最近 %s 内出现 %d 次异常（阈值 %d）\n最近账号：%s\n%s\n说明：面板 status=healthy 只看 Cookie，不等于能读到 notes；请重新扫码登录小红书。",
		window, count, threshold, userID, msg,
	))
}

func (m *XiaohongshuMonitor) handleAccount(event xiaohongshuBrowserEvent) {
	id := strings.TrimSpace(event.UserID)
	if id == "" {
		return
	}
	type liveJob struct {
		groupID int64
		item    config.XiaohongshuConfig
		active  bool
		liveURL string
	}
	var jobs []liveJob
	m.mu.Lock()
	if m.livePending == nil {
		m.livePending = make(map[string]xiaohongshuLivePending)
	}
	changed := false
	for groupID, group := range m.cfg.XiaohongshuSubscriptions {
		item := group[id]
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
		if !item.LiveInitialized {
			// Baseline only — never notify on first observe (avoids boot false "已结束").
			item.LiveInitialized = true
			item.LiveActive = event.LiveActive
			changed = true
			delete(m.livePending, id)
			continue
		}
		if item.LiveActive == event.LiveActive {
			delete(m.livePending, id)
			continue
		}
		// Debounce: need N consecutive opposite readings before flip + notify.
		pending := m.livePending[id]
		if pending.active != event.LiveActive {
			pending = xiaohongshuLivePending{active: event.LiveActive, count: 1}
		} else {
			pending.count++
		}
		if pending.count < xiaohongshuLiveConfirmTimes {
			m.livePending[id] = pending
			continue
		}
		delete(m.livePending, id)
		item.LiveActive = event.LiveActive
		changed = true
		jobs = append(jobs, liveJob{groupID: groupID, item: *item, active: event.LiveActive, liveURL: event.LiveURL})
	}
	m.mu.Unlock()
	if changed {
		_ = m.cfg.Save()
	}
	for _, job := range jobs {
		m.dispatchLive(job.groupID, job.item, job.active, job.liveURL)
	}
}

// dispatchLive formats live start/end aligned with Weibo QQ layout:
// @全体成员 (own line) / 【昵称|小红书直播】 / 正文 / 链接 / 时间戳
func (m *XiaohongshuMonitor) dispatchLive(groupID int64, item config.XiaohongshuConfig, active bool, liveURL string) {
	name := item.Name
	if name == "" {
		name = item.UserID
	}
	var segments []interface{}
	if item.AtAll {
		segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	header := fmt.Sprintf("【%s|小红书直播】\n", name)
	var body string
	if active {
		body = "已开播"
		if liveURL != "" {
			body += "\n" + liveURL
		}
	} else {
		body = "直播已结束"
	}
	text := header + body + "\n" + time.Now().Format("2006-01-02 15:04:05")
	segments = append(segments, napcat.TextSegment(text))
	m.napcat.SendGroupMessage(groupID, segments)
}

func xiaohongshuNoteTime(id string) int64 {
	if len(id) < 8 {
		return 0
	}
	value, err := strconv.ParseInt(id[:8], 16, 64)
	if err != nil || value < 1450000000 {
		return 0
	}
	return value
}

func (m *XiaohongshuMonitor) handleNotes(event xiaohongshuBrowserEvent) {
	if len(event.Notes) == 0 {
		return
	}
	log.Printf("[Xiaohongshu] notes %d user=%s nick=%s", len(event.Notes), event.UserID, event.Nickname)
	log.Printf("[Xiaohongshu] status=notes_ok message=notes=%d user=%s", len(event.Notes), event.UserID)
	for i := range event.Notes {
		if event.Notes[i].CreateTime == 0 {
			event.Notes[i].CreateTime = xiaohongshuNoteTime(event.Notes[i].ID)
		}
	}
	type job struct {
		groupID int64
		item    config.XiaohongshuConfig
		notes   []xiaohongshuNote
	}
	var jobs []job
	m.mu.Lock()
	for groupID, group := range m.cfg.XiaohongshuSubscriptions {
		item := group[event.UserID]
		if item == nil {
			continue
		}
		if event.Nickname != "" {
			item.Name = event.Nickname
		}
		latest := int64(0)
		latestID := ""
		for _, note := range event.Notes {
			if note.CreateTime > latest || (note.CreateTime == latest && note.ID > latestID) {
				latest, latestID = note.CreateTime, note.ID
			}
		}
		if latest == 0 {
			continue
		}
		if item.LastNoteTime == 0 {
			item.LastNoteTime, item.LastNoteID = latest, latestID
			continue
		}
		var unseen []xiaohongshuNote
		for _, note := range event.Notes {
			isNewer := note.CreateTime > item.LastNoteTime || (note.CreateTime == item.LastNoteTime && note.ID > item.LastNoteID)
			if note.ID != "" && isNewer && note.CreateTime <= time.Now().Add(5*time.Minute).Unix() {
				unseen = append(unseen, note)
			}
		}
		sort.Slice(unseen, func(i, j int) bool {
			if unseen[i].CreateTime == unseen[j].CreateTime {
				return unseen[i].ID < unseen[j].ID
			}
			return unseen[i].CreateTime < unseen[j].CreateTime
		})
		if len(unseen) > 0 {
			jobs = append(jobs, job{groupID, *item, unseen})
		}
		if latest > item.LastNoteTime || (latest == item.LastNoteTime && latestID > item.LastNoteID) {
			item.LastNoteTime, item.LastNoteID = latest, latestID
		}
	}
	m.mu.Unlock()
	_ = m.cfg.Save()
	for _, target := range jobs {
		for _, note := range target.notes {
			m.dispatchNote(target.groupID, target.item, note)
		}
	}
}

func (m *XiaohongshuMonitor) dispatchNote(groupID int64, item config.XiaohongshuConfig, note xiaohongshuNote) {
	name := note.Nickname
	if name == "" {
		name = item.Name
	}
	if name == "" {
		name = item.UserID
	}
	// Align with Weibo layout:
	// @全体成员 (own line) / 【昵称|小红书】 / 正文 / 小红书链接： / 图 / 时间戳
	var segments []interface{}
	if item.AtAll {
		segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	header := fmt.Sprintf("【%s|小红书】\n", name)
	var bodyParts []string
	if note.Title != "" {
		bodyParts = append(bodyParts, note.Title)
	}
	if note.Desc != "" && note.Desc != note.Title {
		bodyParts = append(bodyParts, truncateRunes(note.Desc, 600))
	}
	if note.Type == "video" && len(bodyParts) == 0 {
		bodyParts = append(bodyParts, "发布了新视频")
	}
	body := strings.Join(bodyParts, "\n")
	link := note.URL
	if link == "" {
		link = "https://www.xiaohongshu.com/explore/" + note.ID
	}
	// Same spacing as Weibo: body + blank line + 链接：url + newline, then images, then timestamp.
	textMid := body
	if textMid != "" {
		textMid += "\n\n"
	} else {
		textMid = ""
	}
	textMid += fmt.Sprintf("小红书链接：%s\n", link)
	segments = append(segments, napcat.TextSegment(header+textMid))

	images := note.Images
	if len(images) == 0 && note.Cover != "" {
		images = []string{note.Cover}
	}
	for i, image := range images {
		if i >= 9 || !strings.HasPrefix(image, "http") {
			break
		}
		segments = append(segments, napcat.ImageSegment(image))
	}

	ts := time.Now().Format("2006-01-02 15:04:05")
	if note.CreateTime > 0 {
		ts = time.Unix(note.CreateTime, 0).Format("2006-01-02 15:04:05")
	}
	segments = append(segments, napcat.TextSegment("\n"+ts))
	m.napcat.SendGroupMessage(groupID, segments)
}

func (m *XiaohongshuMonitor) handleQRCode(event xiaohongshuBrowserEvent) {
	if event.ImageBase64 == "" {
		return
	}
	expires := event.ExpiresIn
	if expires <= 0 {
		expires = 300
	}
	for _, uid := range uniqueAdminIDs(m.cfg) {
		m.napcat.SendPrivateMessage(uid, []napcat.MessageSegment{napcat.TextSegment(fmt.Sprintf("小红书登录二维码，请在约 %d 分钟内使用小红书 App 扫码。", expires/60)), napcat.ImageSegment("base64://" + event.ImageBase64)})
	}
}
