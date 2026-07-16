package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	ServerMessageID  string       `json:"serverMessageId"`
	MessageType      int          `json:"messageType"`
	Text             string       `json:"text"`
	Link             string       `json:"link"`
	Index            string       `json:"index"`
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
	imConversationID string
	imOwnerUID       string
	imSelfUID        string
	imConnected      bool
}

func (m *DouyinMonitor) SetBrowserBridge(browser *WeiboAuthBridge) {
	m.mu.Lock()
	m.browser = browser
	m.mu.Unlock()
}

func NewDouyinMonitor(cfg *config.Config, client *napcat.Client, notifyAdmins func(string)) *DouyinMonitor {
	return &DouyinMonitor{
		cfg:          cfg,
		napcat:       client,
		notifyAdmins: notifyAdmins,
		liveCancels:  make(map[string]context.CancelFunc),
		liveStates:   make(map[string]*douyinLiveState),
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
		m.mu.Lock()
		m.imConnected = event.Status == "connected"
		m.mu.Unlock()
		log.Printf("[Douyin-IM] status=%s message=%s", event.Status, event.Message)
	case "account_error", "error":
		log.Printf("[Douyin] %s %s: %s", event.Type, event.SecUserID, event.Message)
	case "status":
		log.Printf("[Douyin] status=%s message=%s", event.Status, event.Message)
		if event.Status == "login_error" {
			m.notifyAdmins("⚠️ 抖音登录二维码生成失败：" + event.Message)
		}
	}
}

func (m *DouyinMonitor) handleIMGroup(event douyinBrowserEvent) {
	if !m.cfg.DouyinIMEnabled || strings.TrimSpace(event.ConversationID) == "" || strings.TrimSpace(event.OwnerUID) == "" {
		return
	}
	if configured := strings.TrimSpace(m.cfg.DouyinIMGroupNumber); configured != "" && event.GroupNumber != configured {
		log.Printf("[Douyin-IM] ignored group metadata with unexpected group number")
		return
	}
	m.mu.Lock()
	m.imConversationID = event.ConversationID
	m.imOwnerUID = event.OwnerUID
	m.imSelfUID = event.SelfUID
	m.mu.Unlock()
	log.Printf("[Douyin-IM] target group metadata ready: name=%s", event.GroupName)
}

func (m *DouyinMonitor) handleIMMessage(event douyinBrowserEvent) {
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return
	}
	if event.Link != "" {
		text += "\n" + event.Link
	}
	m.mu.Lock()
	conversationID := m.imConversationID
	ownerUID := m.imOwnerUID
	selfUID := m.imSelfUID
	m.mu.Unlock()
	switch classifyDouyinIMEvent(event, conversationID, ownerUID, selfUID) {
	case "group_owner":
		if !m.cfg.DouyinIMEnabled || m.cfg.BoundGroupID == 0 {
			return
		}
		name := strings.TrimSpace(event.SenderName)
		if name == "" {
			name = "群主"
		}
		groupName := strings.TrimSpace(m.cfg.DouyinIMGroupName)
		if groupName == "" {
			groupName = "抖音群"
		}
		m.napcat.SendGroupMessage(m.cfg.BoundGroupID, []interface{}{
			napcat.TextSegment(fmt.Sprintf("【抖音群｜%s】\n%s：%s", groupName, name, text)),
		})
	case "private_incoming":
		if !m.cfg.DouyinIMEnabled || !m.cfg.DouyinIMPrivateEnabled {
			return
		}
		name := strings.TrimSpace(event.SenderName)
		if name == "" {
			name = "抖音用户"
		}
		m.notifyAdmins(fmt.Sprintf("【抖音私信】\n来自：%s\n%s", name, text))
	}
}

func classifyDouyinIMEvent(event douyinBrowserEvent, conversationID, ownerUID, selfUID string) string {
	switch event.ConversationType {
	case 2:
		if conversationID != "" && ownerUID != "" && event.ConversationID == conversationID && event.SenderUID == ownerUID {
			return "group_owner"
		}
	case 1:
		if event.SenderUID != "" && event.SenderUID != selfUID {
			return "private_incoming"
		}
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
	name := strings.TrimSpace(post.Nickname)
	if name == "" {
		name = strings.TrimSpace(item.Name)
	}
	if name == "" {
		name = item.SecUserID
	}
	typeName := "视频"
	if post.Type == "note" {
		typeName = "图文"
	}
	lines := []string{fmt.Sprintf("【%s｜抖音】发布了新%s", name, typeName)}
	if post.Desc != "" {
		lines = append(lines, truncateRunes(post.Desc, 600))
	}
	lines = append(lines, "", "抖音链接："+canonicalDouyinPostURL(post))
	segments := make([]interface{}, 0, 12)
	if item.AtAll {
		segments = append(segments, napcat.AtSegment("all"))
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
		name := eventName
		if name == "" {
			name = target.cfg.Name
		}
		if name == "" {
			name = target.cfg.SecUserID
		}
		var text string
		if online {
			text = fmt.Sprintf("【%s｜抖音直播】\n已开播", name)
			if title != "" {
				text += "\n" + title
			}
			text += "\nhttps://live.douyin.com/" + liveID
		} else {
			text = fmt.Sprintf("【%s｜抖音直播】\n直播已结束\n直播时长：%s", name, formatDouyinDuration(duration))
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
