package monitor

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"math/big"
	"math/rand"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/napcat"
)

const (
	weiboUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	weiboInterval  = 45 * time.Second
)

type WeiboMonitor struct {
	napcat                 *napcat.Client
	configs                map[int64]map[string]*WeiboConfig
	superPostConfigs       map[int64]map[string]*WeiboSuperPostMonitorConfig
	mu                     sync.RWMutex
	running                bool
	stopCh                 chan struct{}
	Cookie                 string
	MWeiboCookie           string
	AppConfig              *WeiboAppAuth
	OnCookieInvalid        func(uid string)
	cookieAlertMu          sync.Mutex
	lastCookieInvalidAlert time.Time
	cookieInvalidStreak    map[string]int
	cookieBlockedState     map[string]bool
	lastNeg100LogAt        map[string]time.Time
	backoffLevel           map[string]int
	nextCheckAt            map[string]time.Time
}

const weiboCookieAlertCooldown = 6 * time.Hour
const weiboCookieInvalidStreakThreshold = 3
const weiboNeg100LogCooldown = 5 * time.Minute
const weiboMaxBackoffLevel = 4

type WeiboConfig struct {
	GroupID     int64
	UID         string
	ContainerID string
	LastID      string
	AtAll       bool
	Enabled     bool
	OnNewWeibo  func(uid, lastID string)
	LastWeiboID string
}

type WeiboSuperPostMonitorConfig struct {
	GroupID        int64
	UID            string
	OID            string
	Name           string
	AtAll          bool
	Enabled        bool
	LastPostID     string
	OnNewSuperPost func(uid, oid, lastPostID string)
}

type WeiboCard struct {
	Scheme     string `json:"scheme"`
	Text       string `json:"text"`
	RawText    string `json:"raw_text"`
	CreatedAt  string `json:"created_at"`
	MblogID    string `json:"mblogid"`
	Bid        string `json:"bid"`
	IsLongText bool   `json:"isLongText"`
	LongText   *struct {
		LongTextContent string `json:"longTextContent"`
	} `json:"longText,omitempty"`
	Pics []struct {
		Type     string `json:"type"`
		VideoSrc string `json:"videoSrc"`
		Large    struct {
			URL string `json:"url"`
		} `json:"large"`
	} `json:"pics"`
	ID    interface{} `json:"id"`
	MBlog *WeiboCard  `json:"mblog,omitempty"`
	Title *struct {
		Text string `json:"text"`
	} `json:"title,omitempty"`
	PageInfo *struct {
		Type      string `json:"type"`
		VideoURL  string `json:"video_url"`
		MediaInfo struct {
			StreamURL   string `json:"stream_url"`
			StreamURLHD string `json:"stream_url_hd"`
			MP4HDURL    string `json:"mp4_hd_url"`
		} `json:"media_info"`
		PageTitle string `json:"page_title"`
		PagePic   struct {
			URL string `json:"url"`
		} `json:"page_pic"`
	} `json:"page_info"`
	MixMediaInfo *struct {
		Items []struct {
			Type string `json:"type"`
			Data struct {
				Type     string `json:"type"`
				VideoURL string `json:"video_url"`
				PageInfo *struct {
					Type      string `json:"type"`
					VideoURL  string `json:"video_url"`
					MediaInfo struct {
						StreamURL   string `json:"stream_url"`
						StreamURLHD string `json:"stream_url_hd"`
						MP4HDURL    string `json:"mp4_hd_url"`
					} `json:"media_info"`
					PagePic struct {
						URL string `json:"url"`
					} `json:"page_pic"`
				} `json:"page_info"`
				MediaInfo struct {
					StreamURL   string `json:"stream_url"`
					StreamURLHD string `json:"stream_url_hd"`
					MP4HDURL    string `json:"mp4_hd_url"`
				} `json:"media_info"`
				PagePic struct {
					URL string `json:"url"`
				} `json:"page_pic"`
				PicInfo struct {
					Largest struct {
						URL string `json:"url"`
					} `json:"largest"`
				} `json:"pic_info"`
			} `json:"data"`
		} `json:"items"`
	} `json:"mix_media_info"`
	User struct {
		ScreenName string      `json:"screen_name"`
		ID         interface{} `json:"id"`
		IDStr      string      `json:"idstr"`
		UID        string      `json:"uid"`
	} `json:"user"`
}

type WeiboInfoResponse struct {
	OK   int `json:"ok"`
	Data struct {
		TabsInfo struct {
			Tabs []struct {
				TabKey      string `json:"tabKey"`
				Containerid string `json:"containerid"`
			} `json:"tabs"`
		} `json:"tabsInfo"`
	} `json:"data"`
}

type WeiboContainerResponse struct {
	OK   int `json:"ok"`
	Data struct {
		Cards []WeiboCard `json:"cards"`
	} `json:"data"`
}

func NewWeiboMonitor(napcat *napcat.Client) *WeiboMonitor {
	rand.Seed(time.Now().UnixNano())
	return &WeiboMonitor{
		napcat:              napcat,
		configs:             make(map[int64]map[string]*WeiboConfig),
		superPostConfigs:    make(map[int64]map[string]*WeiboSuperPostMonitorConfig),
		stopCh:              make(chan struct{}),
		cookieInvalidStreak: make(map[string]int),
		cookieBlockedState:  make(map[string]bool),
		lastNeg100LogAt:     make(map[string]time.Time),
		backoffLevel:        make(map[string]int),
		nextCheckAt:         make(map[string]time.Time),
	}
}

func (m *WeiboMonitor) SetCookie(cookie string) {
	m.cookieAlertMu.Lock()
	m.resetCookieHealthStateLocked()
	m.cookieAlertMu.Unlock()

	m.mu.Lock()
	m.Cookie = strings.TrimSpace(cookie)
	m.mu.Unlock()
}

func (m *WeiboMonitor) SetMWeiboCookie(cookie string) {
	m.cookieAlertMu.Lock()
	m.resetCookieHealthStateLocked()
	m.cookieAlertMu.Unlock()

	m.mu.Lock()
	m.MWeiboCookie = strings.TrimSpace(cookie)
	m.mu.Unlock()
}

func (m *WeiboMonitor) SetAppAuth(app *WeiboAppAuth) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if app == nil {
		m.AppConfig = nil
		return
	}
	copied := *app
	m.AppConfig = &copied
}

func (m *WeiboMonitor) resetCookieHealthStateLocked() {
	m.cookieInvalidStreak = make(map[string]int)
	m.cookieBlockedState = make(map[string]bool)
	m.lastNeg100LogAt = make(map[string]time.Time)
	m.backoffLevel = make(map[string]int)
	m.nextCheckAt = make(map[string]time.Time)
	m.lastCookieInvalidAlert = time.Time{}
}

func (m *WeiboMonitor) shouldSkipCheck(uid string) (bool, time.Duration) {
	m.cookieAlertMu.Lock()
	defer m.cookieAlertMu.Unlock()
	next := m.nextCheckAt[uid]
	if !next.IsZero() && time.Now().Before(next) {
		return true, time.Until(next)
	}
	return false, 0
}

func (m *WeiboMonitor) markRateLimited(uid string) time.Duration {
	m.cookieAlertMu.Lock()
	defer m.cookieAlertMu.Unlock()

	level := m.backoffLevel[uid]
	if level < weiboMaxBackoffLevel {
		level++
	}
	m.backoffLevel[uid] = level

	base := 2 * time.Minute
	delay := base * time.Duration(1<<level)
	jitter := time.Duration(rand.Intn(40)-20) * time.Second
	delay += jitter
	if delay < time.Minute {
		delay = time.Minute
	}
	m.nextCheckAt[uid] = time.Now().Add(delay)
	return delay
}

func (m *WeiboMonitor) clearRateLimit(uid string) {
	m.cookieAlertMu.Lock()
	defer m.cookieAlertMu.Unlock()
	m.backoffLevel[uid] = 0
	m.nextCheckAt[uid] = time.Time{}
}

func (m *WeiboMonitor) onCookieInvalid(uid string) (shouldAlert bool, shouldLog bool, streak int, justBlocked bool) {
	m.cookieAlertMu.Lock()
	defer m.cookieAlertMu.Unlock()

	m.cookieInvalidStreak[uid]++
	streak = m.cookieInvalidStreak[uid]
	justBlocked = !m.cookieBlockedState[uid]
	m.cookieBlockedState[uid] = true

	now := time.Now()
	if justBlocked || streak == weiboCookieInvalidStreakThreshold || now.Sub(m.lastNeg100LogAt[uid]) >= weiboNeg100LogCooldown {
		shouldLog = true
		m.lastNeg100LogAt[uid] = now
	}

	if streak < weiboCookieInvalidStreakThreshold {
		return false, shouldLog, streak, justBlocked
	}

	now = time.Now()
	if now.Sub(m.lastCookieInvalidAlert) < weiboCookieAlertCooldown {
		return false, shouldLog, streak, justBlocked
	}
	m.lastCookieInvalidAlert = now
	return true, shouldLog, streak, justBlocked
}

func (m *WeiboMonitor) markCookieHealthy(uid string) (recovered bool) {
	m.cookieAlertMu.Lock()
	defer m.cookieAlertMu.Unlock()
	recovered = m.cookieBlockedState[uid] || m.cookieInvalidStreak[uid] > 0
	m.cookieInvalidStreak[uid] = 0
	m.cookieBlockedState[uid] = false
	return recovered
}

func (m *WeiboMonitor) CheckCookie(uid string) (bool, string, error) {
	return m.CheckWebCookie(uid)
}

func (m *WeiboMonitor) CheckWebCookie(uid string) (bool, string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := "https://weibo.com/ajax/config/get_config"
	req, _ := http.NewRequest("GET", url, nil)

	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", "https://weibo.com/")
	req.Header.Set("Client-Version", "3.0.0")

	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("http=%d", resp.StatusCode)
	}

	var result struct {
		OK interface{} `json:"ok"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "", err
	}

	okCode := parseWeiboCode(result.OK)
	if okCode == 1 {
		return true, "ok=1", nil
	}
	return false, fmt.Sprintf("ok=%d", okCode), nil
}

func (m *WeiboMonitor) CheckMWeiboCookie(uid string) (bool, string, error) {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return false, "", fmt.Errorf("uid 不能为空")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://m.weibo.cn/api/container/getIndex?containerid=107603%s", uid)
	req, _ := http.NewRequest("GET", url, nil)

	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.MWeiboCookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://m.weibo.cn/u/%s", uid))

	resp, err := client.Do(req)
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, "", fmt.Errorf("http=%d", resp.StatusCode)
	}

	var result WeiboContainerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, "", err
	}
	if result.OK == 1 {
		return true, "ok=1", nil
	}
	return false, fmt.Sprintf("ok=%d", result.OK), nil
}

func buildWeiboCookieHeader(cookie string) string {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return ""
	}

	// 兼容三种输入：
	// 1) 纯 SUB 值：_2A25...
	// 2) SUB=xxx
	// 3) 完整 Cookie 串：SUB=xxx; SUBP=xxx; ...
	if strings.Contains(cookie, ";") {
		return cookie
	}
	if strings.HasPrefix(cookie, "SUB=") {
		return cookie
	}
	if strings.Contains(cookie, "=") {
		return cookie
	}
	return "SUB=" + cookie
}

func extractCookieValue(cookieHeader string, key string) string {
	parts := strings.Split(cookieHeader, ";")
	prefix := key + "="
	for _, part := range parts {
		item := strings.TrimSpace(part)
		if strings.HasPrefix(item, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(item, prefix))
		}
	}
	return ""
}

func applyWeiboRequestHeaders(req *http.Request, cookieHeader string) {
	req.Header.Set("User-Agent", weiboUserAgent)
	req.Header.Set("Referer", "https://weibo.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("MWeibo-Pwa", "1")
	if cookieHeader != "" {
		req.Header.Set("Cookie", cookieHeader)
		if token := extractCookieValue(cookieHeader, "XSRF-TOKEN"); token != "" {
			req.Header.Set("X-XSRF-TOKEN", token)
		}
	}
	req.Header.Set("Client-Version", "3.0.0")
}

func isWeiboIDNewer(candidate string, current string) bool {
	candidate = strings.TrimSpace(candidate)
	current = strings.TrimSpace(current)
	if candidate == "" {
		return false
	}
	if current == "" {
		return true
	}
	ci, cok := new(big.Int).SetString(candidate, 10)
	bi, bok := new(big.Int).SetString(current, 10)
	if cok && bok {
		return ci.Cmp(bi) > 0
	}
	if len(candidate) != len(current) {
		return len(candidate) > len(current)
	}
	return candidate > current
}

func normalizeContainerID(uid, containerID string) string {
	canonical := "107603" + uid
	if strings.TrimSpace(containerID) == "" {
		return canonical
	}
	if strings.HasPrefix(containerID, "107603") {
		return containerID
	}
	return canonical
}

func (m *WeiboMonitor) AddConfig(groupID int64, uid string, atAll bool, lastID string, onNew func(string, string)) error {
	config := &WeiboConfig{
		GroupID:    groupID,
		UID:        uid,
		AtAll:      atAll,
		LastID:     lastID,
		Enabled:    true,
		OnNewWeibo: onNew,
	}
	containerID, err := m.getWeiboContainerID(uid)
	if err != nil {
		return fmt.Errorf("获取微博containerID失败: %v", err)
	}
	config.ContainerID = normalizeContainerID(uid, containerID)

	m.mu.Lock()
	if _, ok := m.configs[groupID]; !ok {
		m.configs[groupID] = make(map[string]*WeiboConfig)
	}
	m.configs[groupID][uid] = config
	m.mu.Unlock()

	return nil
}

func (m *WeiboMonitor) RemoveConfig(groupID int64, uid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if uid == "" { // Remove all for the group
		delete(m.configs, groupID)
		return
	}
	if groupConfigs, ok := m.configs[groupID]; ok {
		delete(groupConfigs, uid)
		if len(groupConfigs) == 0 {
			delete(m.configs, groupID)
		}
	}
}

func normalizeWeiboSuperPostKey(uid, oid string) string {
	return strings.TrimSpace(uid) + "|" + strings.TrimSpace(oid)
}

func (m *WeiboMonitor) AddSuperPostConfig(groupID int64, uid, oid, name string, atAll bool, lastPostID string, onNew func(string, string, string)) error {
	uid = strings.TrimSpace(uid)
	oid = strings.TrimSpace(oid)
	if uid == "" || oid == "" {
		return fmt.Errorf("uid/oid 不能为空")
	}
	cfg := &WeiboSuperPostMonitorConfig{
		GroupID:        groupID,
		UID:            uid,
		OID:            oid,
		Name:           strings.TrimSpace(name),
		AtAll:          atAll,
		Enabled:        true,
		LastPostID:     strings.TrimSpace(lastPostID),
		OnNewSuperPost: onNew,
	}
	key := normalizeWeiboSuperPostKey(uid, oid)
	m.mu.Lock()
	if _, ok := m.superPostConfigs[groupID]; !ok {
		m.superPostConfigs[groupID] = make(map[string]*WeiboSuperPostMonitorConfig)
	}
	m.superPostConfigs[groupID][key] = cfg
	m.mu.Unlock()
	return nil
}

func (m *WeiboMonitor) RemoveSuperPostConfig(groupID int64, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(key) == "" {
		delete(m.superPostConfigs, groupID)
		return
	}
	if groupConfigs, ok := m.superPostConfigs[groupID]; ok {
		delete(groupConfigs, key)
		if len(groupConfigs) == 0 {
			delete(m.superPostConfigs, groupID)
		}
	}
}

func (m *WeiboMonitor) Start() {
	if m.running {
		return
	}
	m.running = true
	go m.run()
}

func (m *WeiboMonitor) Stop() {
	if !m.running {
		return
	}
	m.running = false
	close(m.stopCh)
	m.stopCh = make(chan struct{})
}

func (m *WeiboMonitor) run() {
	time.Sleep(5 * time.Second)
	ticker := time.NewTicker(weiboInterval)
	defer ticker.Stop()
	m.checkWeibo()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkWeibo()
		}
	}
}

func (m *WeiboMonitor) checkWeibo() {
	m.mu.RLock()
	var configsToCheck []*WeiboConfig
	var superPostConfigsToCheck []*WeiboSuperPostMonitorConfig
	for _, groupConfigs := range m.configs {
		for _, config := range groupConfigs {
			if config.Enabled {
				configsToCheck = append(configsToCheck, config)
			}
		}
	}
	for _, groupConfigs := range m.superPostConfigs {
		for _, config := range groupConfigs {
			if config.Enabled {
				superPostConfigsToCheck = append(superPostConfigsToCheck, config)
			}
		}
	}
	m.mu.RUnlock()

	log.Printf("[Weibo] Checking %d weibo configs, %d superpost configs\n", len(configsToCheck), len(superPostConfigsToCheck))
	for _, config := range configsToCheck {
		if skip, wait := m.shouldSkipCheck(config.UID); skip {
			log.Printf("[Weibo] UID %s in backoff, next check in %s", config.UID, wait.Round(time.Second))
			continue
		}
		go m.checkWeiboForConfig(config)
		time.Sleep(time.Duration(1200+rand.Intn(1200)) * time.Millisecond)
	}
	for _, config := range superPostConfigsToCheck {
		go m.checkWeiboSuperPostForConfig(config)
		time.Sleep(time.Duration(1200+rand.Intn(1200)) * time.Millisecond)
	}
}

func (m *WeiboMonitor) checkWeiboForConfig(config *WeiboConfig) {
	normalizedCID := normalizeContainerID(config.UID, config.ContainerID)
	if config.ContainerID != normalizedCID {
		config.ContainerID = normalizedCID
		log.Printf("[Weibo] Corrected ContainerID for UID %s: %s", config.UID, normalizedCID)
	}

	fmt.Printf("[Weibo] Checking for UID: %s, ContainerID: %s\n", config.UID, config.ContainerID)

	if cardID, card, err := m.fetchLatestWeiboViaWebAPI(config); err == nil && cardID != "" && card != nil {
		m.clearRateLimit(config.UID)
		if m.markCookieHealthy(config.UID) {
			log.Printf("[Weibo] UID %s 已从异常状态恢复（source=web）", config.UID)
		}
		m.handleWebAPISuccess(config, cardID, card)
		return
	}

	// (app API 已废弃：api.weibo.cn 需要每次动态签名，静态凭据不可回放)

	// ③ 回退到 m.weibo.cn API（需要 MWeiboCookie）
	latestID, source, okCode, err := m.fetchLatestWeiboIDViaMWeiboAPI(config)
	if err == nil && okCode == 1 && latestID != "" {
		m.clearRateLimit(config.UID)
		if m.markCookieHealthy(config.UID) {
			log.Printf("[Weibo] UID %s 已从异常状态恢复（source=%s）", config.UID, source)
		}
		m.handleMWeiboSuccess(config, latestID)
		return
	}

	shouldAlert, shouldLog, streak, justBlocked := m.onCookieInvalid(config.UID)
	delay := m.markRateLimited(config.UID)
	if shouldLog {
		reason := ""
		switch {
		case err != nil:
			reason = fmt.Sprintf("error=%v", err)
		case okCode != 1:
			reason = fmt.Sprintf("ok=%d", okCode)
		default:
			reason = "latestID empty"
		}
		if justBlocked {
			log.Printf("[Weibo] UID %s mweibo 检查异常，进入受限状态（%s，连续%d次，退避%s）", config.UID, reason, streak, delay.Round(time.Second))
		} else {
			log.Printf("[Weibo] UID %s mweibo 仍异常（%s，连续%d次，退避%s）", config.UID, reason, streak, delay.Round(time.Second))
		}
	}
	if m.OnCookieInvalid != nil && shouldAlert {
		go m.OnCookieInvalid(config.UID)
	}
	if newCID, cidErr := m.getWeiboContainerID(config.UID); cidErr == nil && strings.HasPrefix(newCID, "107603") && newCID != config.ContainerID {
		config.ContainerID = newCID
		fmt.Printf("[Weibo] Updated ContainerID for UID %s: %s\n", config.UID, newCID)
	}
}

// ---- app API (api.weibo.cn) 帖子获取 ----

// ---- weibo.com Web API 帖子获取 ----

// weiboMymblogResponse weibo.com/ajax/statuses/mymblog 响应
type weiboMymblogResponse struct {
	Data struct {
		SinceID string               `json:"since_id"`
		List    []weiboMymblogPost   `json:"list"`
	} `json:"data"`
	OK int `json:"ok"`
}

type weiboMymblogPost struct {
	ID        int64  `json:"id"`
	IDStr     string `json:"idstr"`
	MblogID   string `json:"mblogid"`
	CreatedAt string `json:"created_at"`
	Text      string `json:"text"`
	TextRaw   string `json:"text_raw"`
	IsLongText bool  `json:"isLongText"`
	PicNum    int    `json:"pic_num"`
	PicIDs    []string `json:"pic_ids,omitempty"`
	PicInfos  map[string]weiboPicInfo `json:"pic_infos,omitempty"`
	PageInfo  *weiboPageInfo          `json:"page_info,omitempty"`
	IsTop     int    `json:"isTop"`
	User      struct {
		ScreenName string `json:"screen_name"`
		ID         int64  `json:"id"`
		IDStr      string `json:"idstr"`
	} `json:"user"`
}

type weiboPicInfo struct {
	Large struct {
		URL string `json:"url"`
	} `json:"large"`
	Original struct {
		URL string `json:"url"`
	} `json:"original,omitempty"`
	Largest struct {
		URL string `json:"url"`
	} `json:"largest,omitempty"`
}

type weiboPageInfo struct {
	Type       interface{}  `json:"type"`
	PageTitle  string       `json:"page_title"`
	PagePic    *weiboPagePic `json:"page_pic,omitempty"`
	MediaInfo  *struct {
		StreamURL   string `json:"stream_url"`
		StreamURLHD string `json:"stream_url_hd"`
		MP4HDURL    string `json:"mp4_hd_url"`
		ReplayLD    string `json:"replay_ld"`
		ReplayHD    string `json:"replay_hd"`
	} `json:"media_info,omitempty"`
}

// weiboPagePic 兼容 page_pic 字段可能是字符串（URL）或对象 {url:...} 的情况
type weiboPagePic struct {
	URL string
}

func (p *weiboPagePic) UnmarshalJSON(data []byte) error {
	// 尝试作为对象解析
	var obj struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.URL != "" {
		p.URL = obj.URL
		return nil
	}
	// 尝试作为字符串解析
	var str string
	if err := json.Unmarshal(data, &str); err == nil && str != "" {
		p.URL = str
		return nil
	}
	// 都不是则忽略
	return nil
}

// fetchLatestWeiboViaWebAPI 通过 weibo.com API 获取用户最新微博
// 返回 latestID, card, error。一次请求即含完整卡片数据。
func (m *WeiboMonitor) fetchLatestWeiboViaWebAPI(config *WeiboConfig) (string, *WeiboCard, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.Contains(req.URL.Host, "passport.weibo.com") {
				return fmt.Errorf("being redirected to visitor system")
			}
			return nil
		},
	}
	url := fmt.Sprintf("https://www.weibo.com/ajax/statuses/mymblog?uid=%s&page=1&feature=0", config.UID)
	req, _ := http.NewRequest("GET", url, nil)

	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://www.weibo.com/u/%s", config.UID))
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("client-version", "3.0.0")
	req.Header.Set("server-version", "v2026.06.25.1")

	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("web mymblog http=%d", resp.StatusCode)
	}

	var result weiboMymblogResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", nil, fmt.Errorf("web mymblog decode: %w", err)
	}
	if len(result.Data.List) == 0 {
		return "", nil, nil
	}

	// 找最新非置顶微博
	var latestPost *weiboMymblogPost
	for i := range result.Data.List {
		p := &result.Data.List[i]
		if p.IsTop != 0 {
			continue // 跳过置顶
		}
		latestPost = p
		break
	}
	if latestPost == nil {
		// 全是置顶，取第一个
		latestPost = &result.Data.List[0]
	}

	cardID := latestPost.IDStr
	if cardID == "" {
		cardID = latestPost.MblogID
	}
	if cardID == "" {
		return "", nil, fmt.Errorf("web mymblog: empty id")
	}

	card := convertWeiboPostToCard(latestPost, config.UID)
	return cardID, card, nil
}

// convertWeiboPostToCard 将 weibo.com 帖子格式转为标准 WeiboCard
func convertWeiboPostToCard(post *weiboMymblogPost, uid string) *WeiboCard {
	card := &WeiboCard{
		ID:         post.IDStr,
		MblogID:    post.MblogID,
		Text:       post.Text,
		RawText:    post.TextRaw,
		CreatedAt:  post.CreatedAt,
		IsLongText: post.IsLongText,
		User: struct {
			ScreenName string      `json:"screen_name"`
			ID         interface{} `json:"id"`
			IDStr      string      `json:"idstr"`
			UID        string      `json:"uid"`
		}{
			ScreenName: post.User.ScreenName,
			ID:         post.User.ID,
			IDStr:      post.User.IDStr,
			UID:        uid,
		},
	}

	// 转换图片
	if post.PicNum > 0 && len(post.PicInfos) > 0 {
		for _, info := range post.PicInfos {
			picURL := info.Large.URL
			if picURL == "" {
				picURL = info.Largest.URL
			}
			if picURL == "" {
				picURL = info.Original.URL
			}
			if picURL == "" {
				continue
			}
			card.Pics = append(card.Pics, struct {
				Type     string `json:"type"`
				VideoSrc string `json:"videoSrc"`
				Large    struct {
					URL string `json:"url"`
				} `json:"large"`
			}{
				Large: struct {
					URL string `json:"url"`
				}{URL: picURL},
			})
		}
	}

	// 转换视频/页面信息
	if post.PageInfo != nil {
		pi := struct {
			Type      string `json:"type"`
			VideoURL  string `json:"video_url"`
			MediaInfo struct {
				StreamURL   string `json:"stream_url"`
				StreamURLHD string `json:"stream_url_hd"`
				MP4HDURL    string `json:"mp4_hd_url"`
			} `json:"media_info"`
			PageTitle string `json:"page_title"`
			PagePic   struct {
				URL string `json:"url"`
			} `json:"page_pic"`
		}{
			Type:      fmt.Sprintf("%v", post.PageInfo.Type),
			PageTitle: post.PageInfo.PageTitle,
		}
		if post.PageInfo.PagePic != nil {
			pi.PagePic.URL = post.PageInfo.PagePic.URL
		}
		if post.PageInfo.MediaInfo != nil {
			// 优先用 replay 地址（直播回放），其次 stream_url
			videoURL := post.PageInfo.MediaInfo.ReplayHD
			if videoURL == "" {
				videoURL = post.PageInfo.MediaInfo.ReplayLD
			}
			if videoURL == "" {
				videoURL = post.PageInfo.MediaInfo.StreamURL
			}
			if videoURL == "" {
				videoURL = post.PageInfo.MediaInfo.StreamURLHD
			}
			if videoURL == "" {
				videoURL = post.PageInfo.MediaInfo.MP4HDURL
			}
			pi.VideoURL = videoURL
			pi.MediaInfo.StreamURL = post.PageInfo.MediaInfo.StreamURL
			pi.MediaInfo.StreamURLHD = post.PageInfo.MediaInfo.StreamURLHD
			pi.MediaInfo.MP4HDURL = post.PageInfo.MediaInfo.MP4HDURL
		}
		card.PageInfo = &pi
	}

	return card
}

func (m *WeiboMonitor) fetchLatestWeiboIDViaMWeiboAPI(config *WeiboConfig) (string, string, int, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.Contains(req.URL.Host, "passport.weibo.com") {
				return fmt.Errorf("being redirected to visitor system")
			}
			return nil
		},
	}
	url := fmt.Sprintf("https://m.weibo.cn/api/container/getIndex?containerid=%s", config.ContainerID)
	req, _ := http.NewRequest("GET", url, nil)
	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.MWeiboCookie)
	if cookieHeader == "" {
		cookieHeader = buildWeiboCookieHeader(m.Cookie)
	}
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://m.weibo.cn/u/%s", config.UID))

	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()
	var result WeiboContainerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", 0, err
	}
	if result.OK != 1 {
		return "", "mweibo-api", result.OK, nil
	}
	cards := filterWeiboCards(result.Data.Cards)
	if len(cards) == 0 {
		return "", "mweibo-api", 1, fmt.Errorf("mweibo cards empty")
	}
	getCardID := func(c WeiboCard) string {
		switch v := c.ID.(type) {
		case string:
			return v
		case float64:
			return fmt.Sprintf("%.0f", v)
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	isPinnedCard := func(c WeiboCard) bool {
		titleText := ""
		if c.Title != nil {
			titleText = strings.TrimSpace(c.Title.Text)
		}
		if titleText == "" && c.MBlog != nil && c.MBlog.Title != nil {
			titleText = strings.TrimSpace(c.MBlog.Title.Text)
		}
		return strings.Contains(titleText, "置顶")
	}

	for _, card := range cards {
		cardID := strings.TrimSpace(getCardID(card))
		if cardID == "" {
			continue
		}
		if isPinnedCard(card) {
			log.Printf("[Weibo] UID %s 跳过置顶微博: %s", config.UID, cardID)
			continue
		}
		log.Printf("[Weibo] UID %s 使用 mweibo API 检测到最新非置顶微博: %s", config.UID, cardID)
		return cardID, "mweibo-api", 1, nil
	}

	for _, card := range cards {
		cardID := strings.TrimSpace(getCardID(card))
		if cardID != "" {
			log.Printf("[Weibo] UID %s 未找到非置顶微博，回退使用首条微博: %s", config.UID, cardID)
			return cardID, "mweibo-api", 1, nil
		}
	}
	return "", "mweibo-api", 1, fmt.Errorf("mweibo cards missing id")
}

func (m *WeiboMonitor) fetchMWeiboCardByID(config *WeiboConfig, targetID string) (*WeiboCard, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://m.weibo.cn/api/container/getIndex?containerid=%s", config.ContainerID)
	req, _ := http.NewRequest("GET", url, nil)
	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.MWeiboCookie)
	if cookieHeader == "" {
		cookieHeader = buildWeiboCookieHeader(m.Cookie)
	}
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://m.weibo.cn/u/%s", config.UID))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result WeiboContainerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.OK != 1 {
		return nil, fmt.Errorf("mweibo detail ok=%d", result.OK)
	}
	cards := filterWeiboCards(result.Data.Cards)
	for _, card := range cards {
		cardID := ""
		switch v := card.ID.(type) {
		case string:
			cardID = v
		case float64:
			cardID = fmt.Sprintf("%.0f", v)
		default:
			cardID = fmt.Sprintf("%v", v)
		}
		if strings.TrimSpace(cardID) == strings.TrimSpace(targetID) {
			cardCopy := card
			return &cardCopy, nil
		}
	}
	return nil, fmt.Errorf("mweibo card %s not found", targetID)
}

func (m *WeiboMonitor) handleMWeiboSuccess(config *WeiboConfig, latestID string) {
	if strings.TrimSpace(latestID) == "" {
		return
	}

	if config.LastWeiboID == "" {
		if config.LastID != "" {
			config.LastWeiboID = config.LastID
		} else {
			config.LastWeiboID = latestID
			if config.OnNewWeibo != nil {
				config.OnNewWeibo(config.UID, config.LastWeiboID)
			}
		}
		return
	}

	if !isWeiboIDNewer(latestID, config.LastWeiboID) {
		return
	}

	config.LastWeiboID = latestID
	if config.OnNewWeibo != nil {
		config.OnNewWeibo(config.UID, config.LastWeiboID)
	}

	card, err := m.fetchMWeiboCardByID(config, latestID)
	if err != nil {
		log.Printf("[Weibo] UID %s 获取 mweibo 卡片失败，改发纯链接: %v", config.UID, err)
		m.sendWeiboShareCard(config.GroupID, WeiboCard{}, config.UID, latestID)
		return
	}
	m.DispatchPerfectWeibo(config, *card, latestID)
	log.Printf("[Weibo] UID %s 使用 mweibo 正常分发新微博: %s", config.UID, latestID)
}

// handleWebAPISuccess 处理 weibo.com Web API 获取到的微博
func (m *WeiboMonitor) handleWebAPISuccess(config *WeiboConfig, cardID string, card *WeiboCard) {
	if strings.TrimSpace(cardID) == "" || card == nil {
		return
	}

	if config.LastWeiboID == "" {
		if config.LastID != "" {
			config.LastWeiboID = config.LastID
		} else {
			config.LastWeiboID = cardID
			if config.OnNewWeibo != nil {
				config.OnNewWeibo(config.UID, config.LastWeiboID)
			}
		}
		return
	}

	if !isWeiboIDNewer(cardID, config.LastWeiboID) {
		return
	}

	config.LastWeiboID = cardID
	if config.OnNewWeibo != nil {
		config.OnNewWeibo(config.UID, config.LastWeiboID)
	}
	m.DispatchPerfectWeibo(config, *card, cardID)
	log.Printf("[Weibo] UID %s 使用 weibo.com web API 正常分发新微博: %s", config.UID, cardID)
}

func (m *WeiboMonitor) fetchLatestWeiboIDFromHTML(uid string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://m.weibo.cn/u/%s", uid)
	req, _ := http.NewRequest("GET", url, nil)

	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.MWeiboCookie)
	if cookieHeader == "" {
		cookieHeader = buildWeiboCookieHeader(m.Cookie)
	}
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://m.weibo.cn/u/%s", uid))

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	content := string(body)
	if strings.Contains(content, "Sina Visitor System") {
		return "", fmt.Errorf("visitor system blocked")
	}

	re := regexp.MustCompile(`/status/([0-9]{10,})`)
	match := re.FindStringSubmatch(content)
	if len(match) >= 2 {
		return match[1], nil
	}

	re2 := regexp.MustCompile(`"id"\s*:\s*"([0-9]{10,})"`)
	match2 := re2.FindStringSubmatch(content)
	if len(match2) >= 2 {
		return match2[1], nil
	}

	return "", fmt.Errorf("no weibo id found in html")
}

func (m *WeiboMonitor) handleFallbackUpdate(config *WeiboConfig, latestID string) {
	if strings.TrimSpace(latestID) == "" {
		return
	}

	if config.LastWeiboID == "" {
		if config.LastID != "" {
			config.LastWeiboID = config.LastID
		} else {
			config.LastWeiboID = latestID
			if config.OnNewWeibo != nil {
				config.OnNewWeibo(config.UID, config.LastWeiboID)
			}
		}
		return
	}

	if !isWeiboIDNewer(latestID, config.LastWeiboID) {
		return
	}

	config.LastWeiboID = latestID
	if config.OnNewWeibo != nil {
		config.OnNewWeibo(config.UID, config.LastWeiboID)
	}
	m.sendFallbackWeiboLink(config, latestID)
	log.Printf("[Weibo] UID %s 使用HTML回退检测到新微博: %s", config.UID, latestID)
}

func (m *WeiboMonitor) sendFallbackWeiboLink(config *WeiboConfig, cardID string) {
	msg := "[微博监控降级模式]\n"
	if config.AtAll {
		msg += "[CQ:at,all]\n"
	}
	msg += fmt.Sprintf("检测到新微博（API受限，已走网页回退）\nhttps://weibo.com/%s/%s\n%s", config.UID, cardID, time.Now().Format("2006-01-02 15:04:05"))
	m.napcat.SendGroupMessage(config.GroupID, napcat.TextSegment(msg))
}

func (m *WeiboMonitor) DispatchPerfectWeibo(config *WeiboConfig, card WeiboCard, cardID string) {
	log.Printf("[DEBUG] DispatchPerfectWeibo called for group %d", config.GroupID)
	// 1. 发送文字正文+大图（正文内直接合并微博链接）
	textMsg := m.formatWeiboCleanText(card, cardID, config.AtAll, config.UID)
	log.Printf("[DEBUG] Sending weibo text: %+v", textMsg)
	m.napcat.SendGroupMessage(config.GroupID, textMsg)

	// 2. 发送视频窗口（支持多视频）
	videos := collectWeiboVideos(card)
	for i, video := range videos {
		videoSeg := napcat.VideoSegment(video.URL, video.Cover)
		m.napcat.SendGroupMessage(config.GroupID, []napcat.MessageSegment{videoSeg})
		log.Printf("[DEBUG] Sent weibo video %d/%d: %s", i+1, len(videos), video.URL)
	}
}

func (m *WeiboMonitor) formatWeiboCleanText(card WeiboCard, cardID string, atAll bool, uid string) []napcat.MessageSegment {
	var segments []napcat.MessageSegment
	header := fmt.Sprintf("【%s|微博】\n", card.User.ScreenName)
	segments = append(segments, napcat.TextSegment(header))

	if atAll {
		segments = append(segments, napcat.AtSegment("all"))
		segments = append(segments, napcat.TextSegment("\n"))
	}

	// 正文彻底清洗
	rawText := m.resolveWeiboText(card, cardID)
	cleanText := stripHTML(rawText)
	cleanText = removeWeiboSuffix(cleanText, card.User.ScreenName)
	cleanText = removeWeiboTailNoise(cleanText)
	cleanText = stripUnsupportedQQRunes(cleanText)

	jumpURL := fmt.Sprintf("https://weibo.com/%s/%s", uid, cardID)
	segments = append(segments, napcat.TextSegment(fmt.Sprintf("%s\n\n微博链接：%s\n", cleanText, jumpURL)))

	// 第一条消息包含封面图
	log.Printf("[DEBUG] Weibo card has %d pics", len(card.Pics))
	if len(card.Pics) > 0 {
		log.Printf("[DEBUG] Sending weibo images...")
		for i, pic := range card.Pics {
			log.Printf("[DEBUG] Image %d: %s", i, pic.Large.URL)
			segments = append(segments, napcat.ImageSegment(pic.Large.URL))
			if i >= 8 {
				break
			}
		}
	} else if card.PageInfo != nil && card.PageInfo.PagePic.URL != "" {
		log.Printf("[DEBUG] Sending page pic: %s", card.PageInfo.PagePic.URL)
		segments = append(segments, napcat.ImageSegment(card.PageInfo.PagePic.URL))
	}
	segments = append(segments, napcat.TextSegment("\n"+time.Now().Format("2006-01-02 15:04:05")))
	return segments
}

func (m *WeiboMonitor) sendWeiboShareCard(groupID int64, card WeiboCard, uid string, cardID string) {
	log.Printf("[DEBUG] sendWeiboShareCard called for group %d, uid %s, cardID %s", groupID, uid, cardID)
	jumpURL := fmt.Sprintf("https://weibo.com/%s/%s", uid, cardID)

	// 改为纯文本链接发送，确保 100% 可见
	msg := fmt.Sprintf("🔗 微博链接：\n%s", jumpURL)

	fmt.Printf("[Weibo] Sending plain link to group %d: %s\n", groupID, jumpURL)
	m.napcat.SendGroupMessage(groupID, napcat.TextSegment(msg))
}

func (m *WeiboMonitor) checkWeiboSuperPostForConfig(config *WeiboSuperPostMonitorConfig) {
	latestID, card, err := m.fetchLatestSuperPostCardByUID(config.OID, config.UID)
	if err != nil {
		log.Printf("[WeiboSuperPost] check failed uid=%s oid=%s err=%v", config.UID, config.OID, err)
		return
	}
	if strings.TrimSpace(latestID) == "" || card == nil {
		return
	}
	if strings.TrimSpace(config.LastPostID) == "" {
		config.LastPostID = latestID
		if config.OnNewSuperPost != nil {
			config.OnNewSuperPost(config.UID, config.OID, latestID)
		}
		return
	}
	if !isWeiboIDNewer(latestID, config.LastPostID) {
		return
	}
	config.LastPostID = latestID
	if config.OnNewSuperPost != nil {
		config.OnNewSuperPost(config.UID, config.OID, latestID)
	}
	proxyCfg := &WeiboConfig{GroupID: config.GroupID, UID: config.UID, AtAll: config.AtAll}
	m.DispatchPerfectWeibo(proxyCfg, *card, latestID)
	log.Printf("[WeiboSuperPost] dispatched uid=%s oid=%s post=%s", config.UID, config.OID, latestID)
}

func normalizeWeiboSuperPostOID(oid string) string {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return ""
	}
	if strings.HasPrefix(oid, "1022:") {
		return oid
	}
	return "1022:" + oid
}

func (m *WeiboMonitor) fetchLatestSuperPostCardByUID(oid, uid string) (string, *WeiboCard, error) {
	m.mu.RLock()
	app := m.AppConfig
	m.mu.RUnlock()
	if app == nil {
		return "", nil, fmt.Errorf("weibo app auth 未配置")
	}
	if strings.TrimSpace(app.Authorization) == "" {
		return "", nil, fmt.Errorf("weibo app authorization 缺失")
	}
	oid = normalizeWeiboSuperPostOID(oid)
	uid = strings.TrimSpace(uid)
	baseID := strings.TrimPrefix(oid, "1022:")
	if baseID == "" {
		return "", nil, fmt.Errorf("oid 不能为空")
	}
	path := strings.TrimSpace(app.RequestPath)
	if path == "" {
		path = "/2/statuses/container_timeline_topicpage"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(app.Host)
	if host == "" {
		host = "api.weibo.cn"
	}
	params := neturl.Values{}
	params.Set("containerid", baseID)
	params.Set("flowId", baseID+"_-_recommend")
	params.Set("page", "1")
	params.Set("count", "20")
	params.Set("page_common_ext", "topicPrompt:1|page:recommend=1|hide_page:1")
	if app.GSID != "" {
		params.Set("gsid", app.GSID)
	}
	if app.Aid != "" {
		params.Set("aid", app.Aid)
	}
	if app.S != "" {
		params.Set("s", app.S)
	}
	url := "https://" + host + path + "?" + params.Encode()
	bodyValues := neturl.Values{}
	bodyValues.Set("fid", baseID)
	bodyValues.Set("flowId", baseID)
	bodyValues.Set("refresh", "init")
	bodyValues.Set("featurecode", "10000001")
	bodyValues.Set("luicode", "10000001")
	bodyValues.Set("uicode", "10000011")
	req, _ := http.NewRequest("POST", url, strings.NewReader(bodyValues.Encode()))
	ua := strings.TrimSpace(app.UserAgent)
	if ua == "" {
		ua = "WeiboOversea/16.4.1 (iPhone15,2; iOS 26.3.1; Scale/3.00)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Authorization", app.Authorization)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Host", host)
	if app.SNRT != "" {
		req.Header.Set("SNRT", app.SNRT)
	}
	if app.XSessionID != "" {
		req.Header.Set("X-Sessionid", app.XSessionID)
	}
	if app.XEngineType != "" {
		req.Header.Set("x-engine-type", app.XEngineType)
	}
	if app.XShanhaiPass != "" {
		req.Header.Set("x-shanhai-pass", app.XShanhaiPass)
	}
	if app.XLogUID != "" {
		req.Header.Set("X-Log-Uid", app.XLogUID)
	}
	if app.XValidator != "" {
		req.Header.Set("X-Validator", app.XValidator)
	}
	if app.CronetRID != "" {
		req.Header.Set("cronet_rid", app.CronetRID)
	}
	if app.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", app.AcceptLanguage)
	}
	if app.GSID != "" {
		req.Header.Set("Cookie", "gsid="+app.GSID)
	}
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("topicpage http=%d", resp.StatusCode)
	}
	var result struct {
		Cards []WeiboCard `json:"cards"`
		Data  struct {
			Cards []WeiboCard `json:"cards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return "", nil, fmt.Errorf("topicpage invalid response: %w | body=%q", err, preview)
	}
	cards := result.Cards
	if len(cards) == 0 {
		cards = result.Data.Cards
	}
	for _, card := range filterWeiboCards(cards) {
		target := card
		if target.MBlog != nil {
			target = *target.MBlog
			if target.Scheme == "" {
				target.Scheme = card.Scheme
			}
		}
		authorUID := extractWeiboCardAuthorUID(target)
		if strings.TrimSpace(authorUID) != uid {
			continue
		}
		cardID := extractWeiboCardID(target)
		if strings.TrimSpace(cardID) == "" {
			continue
		}
		cardCopy := target
		return cardID, &cardCopy, nil
	}

	fallbackID, fallbackCard := scanWeiboCardByAuthorUID(body, uid)
	if strings.TrimSpace(fallbackID) != "" && fallbackCard != nil {
		return fallbackID, fallbackCard, nil
	}
	return "", nil, nil
}

func (m *WeiboMonitor) FetchLatestSuperPostForTest(oid, uid string) (string, *WeiboCard, error) {
	return m.fetchLatestSuperPostCardByUID(oid, uid)
}

func extractWeiboCardID(card WeiboCard) string {
	switch v := card.ID.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func scanWeiboSuperIdentityTexts(body []byte) (string, string) {
	if len(body) == 0 {
		return "", ""
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return "", ""
	}
	texts := make([]string, 0, 256)
	collectJSONStringValues(generic, &texts)
	creatorText := ""
	diamondText := ""
	for _, text := range texts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if creatorText == "" && strings.Contains(text, "创作官") {
			creatorText = text
		}
		if diamondText == "" && (strings.Contains(text, "钻咖") || strings.Contains(text, "粉丝钻") || strings.Contains(text, "粉钻")) {
			diamondText = text
		}
		if creatorText != "" && diamondText != "" {
			break
		}
	}
	return creatorText, diamondText
}

func scanWeiboCardByAuthorUID(body []byte, uid string) (string, *WeiboCard) {
	uid = strings.TrimSpace(uid)
	if uid == "" || len(body) == 0 {
		return "", nil
	}
	var generic any
	if err := json.Unmarshal(body, &generic); err != nil {
		return "", nil
	}
	cardMap, ok := findWeiboCardMapByUID(generic, uid)
	if !ok {
		return "", nil
	}
	buf, err := json.Marshal(cardMap)
	if err != nil {
		return "", nil
	}
	var card WeiboCard
	if err := json.Unmarshal(buf, &card); err != nil {
		return "", nil
	}
	cardID := extractWeiboCardID(card)
	if strings.TrimSpace(cardID) == "" {
		return "", nil
	}
	return cardID, &card
}

func findWeiboCardMapByUID(v any, uid string) (map[string]any, bool) {
	switch node := v.(type) {
	case map[string]any:
		if cardUID := extractUIDFromGenericWeiboNode(node); cardUID == uid {
			if _, hasID := node["id"]; hasID {
				return node, true
			}
			if mblog, ok := node["mblog"].(map[string]any); ok {
				if _, hasID := mblog["id"]; hasID {
					return mblog, true
				}
			}
		}
		for _, child := range node {
			if found, ok := findWeiboCardMapByUID(child, uid); ok {
				return found, true
			}
		}
	case []any:
		for _, item := range node {
			if found, ok := findWeiboCardMapByUID(item, uid); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func extractUIDFromGenericWeiboNode(node map[string]any) string {
	if user, ok := node["user"].(map[string]any); ok {
		for _, key := range []string{"idstr", "uid", "id"} {
			if v, ok := user[key]; ok {
				text := strings.TrimSpace(fmt.Sprintf("%v", v))
				text = strings.TrimSuffix(text, ".0")
				if text != "" && text != "<nil>" {
					return text
				}
			}
		}
	}
	if mblog, ok := node["mblog"].(map[string]any); ok {
		return extractUIDFromGenericWeiboNode(mblog)
	}
	return ""
}

func extractWeiboCardAuthorUID(card WeiboCard) string {
	if nested := card.MBlog; nested != nil {
		if uid := extractWeiboCardAuthorUID(*nested); uid != "" {
			return uid
		}
	}
	for _, v := range []string{strings.TrimSpace(card.User.IDStr), strings.TrimSpace(card.User.UID)} {
		if v != "" {
			return v
		}
	}
	switch v := card.User.ID.(type) {
	case string:
		if text := strings.TrimSpace(v); text != "" {
			return text
		}
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	var generic map[string]any
	buf, _ := json.Marshal(card)
	_ = json.Unmarshal(buf, &generic)
	if user, ok := generic["user"].(map[string]any); ok {
		for _, key := range []string{"idstr", "id", "uid"} {
			if v, ok := user[key]; ok {
				text := strings.TrimSpace(fmt.Sprintf("%v", v))
				if text != "" && text != "<nil>" {
					if strings.HasSuffix(text, ".0") {
						text = strings.TrimSuffix(text, ".0")
					}
					return text
				}
			}
		}
	}
	return ""
}

type weiboVideo struct {
	URL   string
	Cover string
}

func pickBestWeiboVideoURL(candidates ...string) string {
	for _, candidate := range candidates {
		url := strings.TrimSpace(candidate)
		if url != "" {
			return url
		}
	}
	return ""
}

func collectWeiboVideos(card WeiboCard) []weiboVideo {
	seen := make(map[string]bool)
	var videos []weiboVideo

	appendVideo := func(url, cover string) {
		url = strings.TrimSpace(url)
		cover = strings.TrimSpace(cover)
		if url == "" || seen[url] {
			return
		}
		seen[url] = true
		videos = append(videos, weiboVideo{URL: url, Cover: cover})
	}

	if card.PageInfo != nil {
		videoURL := pickBestWeiboVideoURL(
			card.PageInfo.VideoURL,
			card.PageInfo.MediaInfo.StreamURLHD,
			card.PageInfo.MediaInfo.MP4HDURL,
			card.PageInfo.MediaInfo.StreamURL,
		)
		if videoURL != "" && (isVideoTypeLabel(card.PageInfo.Type) || looksLikeVideoURL(videoURL)) {
			appendVideo(videoURL, card.PageInfo.PagePic.URL)
		}
	}

	if card.MixMediaInfo != nil {
		for _, item := range card.MixMediaInfo.Items {
			videoURL := pickBestWeiboVideoURL(
				item.Data.VideoURL,
				item.Data.MediaInfo.StreamURLHD,
				item.Data.MediaInfo.MP4HDURL,
				item.Data.MediaInfo.StreamURL,
			)
			if videoURL == "" && item.Data.PageInfo != nil {
				videoURL = pickBestWeiboVideoURL(
					item.Data.PageInfo.VideoURL,
					item.Data.PageInfo.MediaInfo.StreamURLHD,
					item.Data.PageInfo.MediaInfo.MP4HDURL,
					item.Data.PageInfo.MediaInfo.StreamURL,
				)
			}
			if videoURL == "" {
				continue
			}

			typeHint := strings.ToLower(strings.TrimSpace(item.Type + " " + item.Data.Type))
			if item.Data.PageInfo != nil {
				typeHint += " " + strings.ToLower(strings.TrimSpace(item.Data.PageInfo.Type))
			}
			if !isVideoTypeLabel(typeHint) && !looksLikeVideoURL(videoURL) {
				continue
			}

			cover := strings.TrimSpace(item.Data.PagePic.URL)
			if cover == "" && item.Data.PageInfo != nil {
				cover = strings.TrimSpace(item.Data.PageInfo.PagePic.URL)
			}
			if cover == "" {
				cover = strings.TrimSpace(item.Data.PicInfo.Largest.URL)
			}
			appendVideo(videoURL, cover)
		}
	}

	if len(videos) == 0 {
		for _, pic := range card.Pics {
			if !isVideoTypeLabel(pic.Type) {
				continue
			}
			videoURL := pickBestWeiboVideoURL(pic.VideoSrc)
			if videoURL == "" {
				continue
			}
			appendVideo(videoURL, pic.Large.URL)
		}
	}

	return videos
}

func isVideoTypeLabel(label string) bool {
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" {
		return false
	}
	return strings.Contains(label, "video") || strings.Contains(label, "movie") || strings.Contains(label, "mp4")
}

func looksLikeVideoURL(url string) bool {
	url = strings.ToLower(strings.TrimSpace(url))
	if url == "" {
		return false
	}
	return strings.Contains(url, ".mp4") || strings.Contains(url, ".m3u8") || strings.Contains(url, "/video/")
}

func (m *WeiboMonitor) resolveWeiboText(card WeiboCard, cardID string) string {
	if card.LongText != nil {
		if longText := strings.TrimSpace(card.LongText.LongTextContent); longText != "" {
			return longText
		}
	}

	rawText := strings.TrimSpace(card.Text)
	if rawText == "" {
		return ""
	}
	if (!card.IsLongText && !strings.Contains(rawText, "全文")) || strings.TrimSpace(cardID) == "" {
		return rawText
	}

	longText, err := m.fetchWeiboLongText(cardID)
	if err != nil {
		log.Printf("[Weibo] fetch long text failed for %s: %v", cardID, err)
		return rawText
	}
	if strings.TrimSpace(longText) == "" {
		return rawText
	}
	return longText
}

func (m *WeiboMonitor) fetchWeiboLongText(cardID string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	url := fmt.Sprintf("https://m.weibo.cn/statuses/extend?id=%s", cardID)
	req, _ := http.NewRequest("GET", url, nil)

	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		OK   int `json:"ok"`
		Data struct {
			LongTextContent string `json:"longTextContent"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.OK != 1 {
		return "", fmt.Errorf("unexpected ok=%d", result.OK)
	}
	return strings.TrimSpace(result.Data.LongTextContent), nil
}

// filterVideoLinks 过滤掉文本中的视频链接
func (m *WeiboMonitor) filterVideoLinks(text string) string {
	// 移除视频链接标签，如: <a href="...">鸠枫的微博视频</a>
	re := regexp.MustCompile(`<a\s+href="[^"]*video\.weibo\.com[^"]*"[^>]*>.*?</a>`)
	text = re.ReplaceAllString(text, "")

	// 移除视频链接，如: <span class="surl-text">鸠枫的微博视频</span>
	re2 := regexp.MustCompile(`<span class="surl-text">[^<]*微博视频[^<]*</span>`)
	text = re2.ReplaceAllString(text, "")

	// 移除开头的 @ 符号
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "@") {
		text = strings.TrimSpace(text[1:])
	}

	// 移除空行
	lines := strings.Split(text, "\n")
	var filtered []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			filtered = append(filtered, line)
		}
	}

	return strings.Join(filtered, "\n")
}

func removeWeiboSuffix(text string, name string) string {
	// 极致清洗正则：匹配名字前后的非空字符，确保精准锁定并切除后缀及其随后的所有字符
	re := regexp.MustCompile(`\s*` + regexp.QuoteMeta(name) + `\s*的微博视频[\s\S]*$`)
	text = re.ReplaceAllString(text, "")

	// 移除常见的各种乱七八糟的残留
	text = strings.ReplaceAll(text, "&#44;", ",")
	text = strings.ReplaceAll(text, "微博视频", "")
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, "L")

	return strings.TrimSpace(text)
}

func removeWeiboTailNoise(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if trimmed == "查看图片" || trimmed == "网页链接" || trimmed == "查看原图" {
			continue
		}
		cleanLines = append(cleanLines, trimmed)
	}

	joined := strings.Join(cleanLines, "\n")
	joined = strings.TrimSpace(joined)
	joined = strings.TrimSuffix(joined, "查看图片")
	joined = strings.TrimSuffix(joined, "网页链接")
	return strings.TrimSpace(joined)
}

func stripUnsupportedQQRunes(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	buf := make([]rune, 0, len(text))
	for _, r := range text {
		if r == '\n' || r == '\t' {
			buf = append(buf, r)
			continue
		}
		if r < 32 {
			continue
		}
		// 保留 emoji 和其他 Unicode 字符
		buf = append(buf, r)
	}

	clean := strings.TrimSpace(string(buf))
	if clean == "" {
		return "[微博正文]"
	}
	return clean
}

func escapeXml(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	return s
}

func (m *WeiboMonitor) getWeiboContainerID(uid string) (string, error) {
	cid := "107603" + uid
	client := &http.Client{Timeout: 10 * time.Second}
	url := fmt.Sprintf("https://m.weibo.cn/api/container/getIndex?containerid=%s", cid)
	req, _ := http.NewRequest("GET", url, nil)
	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	resp, err := client.Do(req)
	if err == nil {
		var result WeiboContainerResponse
		if json.NewDecoder(resp.Body).Decode(&result) == nil && result.OK == 1 && len(result.Data.Cards) > 0 {
			resp.Body.Close()
			return cid, nil
		}
		resp.Body.Close()
	}
	return cid, nil
}

func stripHTML(src string) string {
	lineBreakRe := regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>`)
	src = lineBreakRe.ReplaceAllString(src, "\n")
	re, _ := regexp.Compile("<[^>]*>")
	src = re.ReplaceAllString(src, "")
	src = strings.ReplaceAll(src, "&nbsp;", " ")
	src = html.UnescapeString(src)

	lines := strings.Split(src, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			cleanLines = append(cleanLines, trimmed)
		}
	}

	return strings.TrimSpace(strings.Join(cleanLines, "\n"))
}

func filterWeiboCards(cards []WeiboCard) []WeiboCard {
	var result []WeiboCard
	for _, card := range cards {
		actualCard := card
		if card.MBlog != nil {
			actualCard = *card.MBlog
			if actualCard.Scheme == "" {
				actualCard.Scheme = card.Scheme
			}
		}
		cardID := extractWeiboCardID(actualCard)
		if cardID != "" && (actualCard.User.ScreenName != "" || extractWeiboCardAuthorUID(actualCard) != "") {
			result = append(result, actualCard)
		}
	}
	return result
}

type WeiboSuperTopicItem struct {
	OID   string
	Name  string
	State string
}

type WeiboSuperSignResult struct {
	OID         string
	Name        string
	Code        int
	Message     string
	Success     bool
	AlreadyDone bool
	Rank        int // 今日签到排名，若已签到可能为0
}

type WeiboSuperCountResult struct {
	OID                string
	Name               string
	SignCount          int
	TodayInteraction   string
	SignText           string
	Heat24h            string
	SuperLikeText      string
	SuperLikeCount     int
	PostLabel          string
	PostCount          string
	FansLabel          string
	FansCount          string
	LevelText          string
	LevelIconURL       string
	CreatorOfficerText string
	FanDiamondText     string
	DailyRankText      string
	CheckinExpText     string
	CheckinStreakText  string
	Source             string
}

type WeiboAppAuth struct {
	RawCapture     string
	Host           string
	RequestPath    string
	RequestBody    string
	CapturedOID    string
	Authorization  string
	GSID           string
	Aid            string
	S              string
	XSessionID     string
	XValidator     string
	XShanhaiPass   string
	XLogUID        string
	XEngineType    string
	CronetRID      string
	SNRT           string
	AcceptLanguage string
	AcceptEncoding string
	UserAgent      string
}

func (m *WeiboMonitor) FetchWeiboSuperTopics() ([]WeiboSuperTopicItem, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	items := make([]WeiboSuperTopicItem, 0, 64)

	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("https://weibo.com/ajax/profile/topicContent?tabid=231093_-_chaohua&page=%d", page)
		req, _ := http.NewRequest("GET", url, nil)
		m.mu.RLock()
		cookieHeader := buildWeiboCookieHeader(m.Cookie)
		m.mu.RUnlock()
		applyWeiboRequestHeaders(req, cookieHeader)

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			resp.Body.Close()
			return nil, fmt.Errorf("topicContent http=%d", resp.StatusCode)
		}

		var result struct {
			OK   int `json:"ok"`
			Data struct {
				List []struct {
					ID      interface{} `json:"id"`
					Name    string      `json:"name"`
					Title   string      `json:"title"`
					OID     string      `json:"oid"`
					Scheme  string      `json:"scheme"`
					SignTip string      `json:"sign_tip"`
					State   string      `json:"state"`
				} `json:"list"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		if result.OK != 1 {
			return nil, fmt.Errorf("topicContent ok=%d", result.OK)
		}
		if len(result.Data.List) == 0 {
			break
		}

		for _, entry := range result.Data.List {
			oid := strings.TrimSpace(entry.OID)
			if oid == "" {
				oid = extractOIDFromScheme(entry.Scheme)
			}
			if oid == "" {
				continue
			}
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				name = strings.TrimSpace(entry.Title)
			}
			items = append(items, WeiboSuperTopicItem{OID: oid, Name: name, State: strings.TrimSpace(entry.State)})
		}
	}

	return items, nil
}

func (m *WeiboMonitor) SignWeiboSuperTopic(oid string) (*WeiboSuperSignResult, error) {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return nil, fmt.Errorf("oid 不能为空")
	}

	baseID := strings.TrimPrefix(oid, "1022:")
	if baseID == "" {
		baseID = oid
	}
	params := neturl.Values{}
	params.Set("ajwvr", "6")
	params.Set("api", "http://i.huati.weibo.com/aj/super/checkin")
	params.Set("texta", "签到")
	params.Set("textb", "已签到")
	params.Set("status", "0")
	params.Set("id", baseID)
	params.Set("location", "page_100808_super_index")
	params.Set("timezone", "GMT+0800")
	params.Set("lang", "zh-cn")
	params.Set("plat", "Win32")
	params.Set("ua", weiboUserAgent)
	params.Set("screen", "1707*960")
	params.Set("__rnd", strconv.FormatInt(time.Now().UnixMilli(), 10))
	signURL := "https://weibo.com/p/aj/general/button?" + params.Encode()

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", signURL, nil)
	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://weibo.com/p/%s/super_index", baseID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("super checkin http=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Code interface{}     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 160 {
			preview = preview[:160] + "..."
		}
		return nil, fmt.Errorf("super checkin invalid response: %w | body=%q", err, preview)
	}

	code := parseWeiboCode(result.Code)
	if code == 382004 || code == 100001 || code == 100000 {
		log.Printf("[Weibo][Sign] raw response oid=%s code=%d msg=%q data=%s", oid, code, strings.TrimSpace(result.Msg), string(result.Data))
	}
	errNo := 0
	errCode := 0
	errMsg := ""
	out := &WeiboSuperSignResult{OID: oid, Code: code, Message: strings.TrimSpace(result.Msg)}
	if len(result.Data) > 0 {
	trimmed := strings.TrimSpace(string(result.Data))
	if strings.HasPrefix(trimmed, "{") {
		var dataObj struct {
			ErrNo       interface{} `json:"errno"`
			ErrMsg      string      `json:"errmsg"`
			ErrCode     interface{} `json:"errcode"`
			MemberRank  int         `json:"member_rank"`
			Rank        int         `json:"rank"`
			OrderNum    int         `json:"order_num"`
			Num         int         `json:"num"`
			TotalSign   int         `json:"total_sign"`
			CheckinRank int         `json:"checkin_rank"`
		}
		if err := json.Unmarshal(result.Data, &dataObj); err == nil {
			errNo = parseWeiboCode(dataObj.ErrNo)
			errCode = parseWeiboCode(dataObj.ErrCode)
			errMsg = strings.TrimSpace(dataObj.ErrMsg)
			out.Rank = firstNonZero(dataObj.MemberRank, dataObj.Rank, dataObj.OrderNum, dataObj.Num, dataObj.TotalSign, dataObj.CheckinRank)
		}
		// 如果上面没命中，从 alert_title / tipMessage 解析 "第X名"
		if out.Rank <= 0 {
			var fallback struct {
				AlertTitle  string `json:"alert_title"`
				TipMessage  string `json:"tipMessage"`
				AlertSub    string `json:"alert_subtitle"`
			}
			if err := json.Unmarshal(result.Data, &fallback); err == nil {
				for _, text := range []string{fallback.AlertTitle, fallback.TipMessage, fallback.AlertSub} {
					if n, ok := parseRankFromText(text); ok && n > 0 {
						out.Rank = n
						break
					}
				}
			}
		}
	}
	}
	// 如果 data 未命中，尝试从 msg 解析 "第X名" 或 "第X位"
	if out.Rank <= 0 && strings.TrimSpace(result.Msg) != "" {
		if n, ok := parseRankFromText(result.Msg); ok {
			out.Rank = n
		}
	}

	switch code {
	case 100000:
		if errNo == 303404 || errCode == 516 {
			out.Success = false
			if out.Message == "" {
				out.Message = "签到失败: 超话不存在或接口未命中"
			}
			if errMsg != "" {
				out.Message += " (" + errMsg + ")"
			}
			break
		}
		out.Success = true
		if out.Message == "" {
			out.Message = "签到成功"
		}
	case 382004:
		out.Success = true
		out.AlreadyDone = true
		if out.Message == "" {
			out.Message = "今日已签到"
		}
	case 100001:
		if strings.Contains(out.Message, "已签到") || errNo == 0 {
			out.Success = true
			out.AlreadyDone = true
			if out.Message == "" {
				out.Message = "今日已签到"
			}
		} else if out.Message == "" {
			out.Message = "签到失败(code=100001)"
		}
	case 382010:
		if out.Message == "" {
			out.Message = "超话不存在或不可见"
		}
	default:
		if out.Message == "" {
			out.Message = fmt.Sprintf("签到失败(code=%d)", code)
		}
	}

	if !out.Success && errMsg != "" && !strings.Contains(out.Message, errMsg) {
		out.Message = strings.TrimSpace(out.Message) + " | " + errMsg
	}
	if !out.Success && (errNo != 0 || errCode != 0) {
		out.Message = fmt.Sprintf("%s (errno=%d, errcode=%d)", strings.TrimSpace(out.Message), errNo, errCode)
	}

	return out, nil
}

func parseWeiboCode(v interface{}) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		if err == nil {
			return i
		}
	}
	return 0
}

// firstNonZero 返回第一个非零值，全零则返回0
func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

// parseRankFromText 从文本中解析 "第X名" 或 "第X位" 的数字
func parseRankFromText(text string) (int, bool) {
	re := regexp.MustCompile(`第\s*([0-9,]+)\s*[名位]`)
	m := re.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) < 2 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.ReplaceAll(m[1], ",", ""))
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func extractOIDFromScheme(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	u, err := neturl.Parse(s)
	if err != nil {
		return ""
	}
	q := u.Query()
	if oid := strings.TrimSpace(q.Get("oid")); oid != "" {
		return oid
	}
	if id := strings.TrimSpace(q.Get("id")); id != "" {
		return id
	}
	return ""
}

func truncateText(text string, maxLen int) string {
	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}
	return string(runes[:maxLen]) + "..."
}

func (m *WeiboMonitor) FetchSuperCountByOID(oid string, nameHint string) (*WeiboSuperCountResult, error) {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return nil, fmt.Errorf("oid 不能为空")
	}
	if res, err := m.fetchSuperCountByOIDViaApp(oid, nameHint); err == nil {
		return m.augmentSignCountWithRank(res), nil
	}
	res, err := m.fetchSuperCountByOIDViaWeb(oid, nameHint)
	if err != nil {
		return nil, err
	}
	return m.augmentSignCountWithRank(res), nil
}

// augmentSignCountWithRank 当签到人数来自 "1.2万" 这种近似值时，尝试通过签到获取精确排名
func (m *WeiboMonitor) augmentSignCountWithRank(res *WeiboSuperCountResult) *WeiboSuperCountResult {
	if res == nil || res.SignCount <= 0 {
		return res
	}
	// 只有文本含 "万" 才说明是近似值，需要精确排名
	if !strings.Contains(res.SignText, "万") {
		return res
	}
	signRes, err := m.SignWeiboSuperTopic(res.OID)
	if err != nil {
		log.Printf("[Weibo][Count] sign-in fallback failed oid=%s err=%v", res.OID, err)
		return res
	}
	if signRes.Rank > 0 && signRes.Rank != res.SignCount {
		log.Printf("[Weibo][Count] sign-in rank fallback oid=%s label=%d exact=%d", res.OID, res.SignCount, signRes.Rank)
		res.SignCount = signRes.Rank
		res.SignText = fmt.Sprintf("签到%d人", signRes.Rank)
	} else if signRes.Rank <= 0 {
		log.Printf("[Weibo][Count] sign-in rank zero oid=%s label=%d code=%d msg=%q", res.OID, res.SignCount, signRes.Code, signRes.Message)
	}
	return res
}

func (m *WeiboMonitor) fetchSuperCountByOIDViaWeb(oid string, nameHint string) (*WeiboSuperCountResult, error) {
	baseID := strings.TrimPrefix(oid, "1022:")
	if baseID == "" {
		baseID = oid
	}

	params := neturl.Values{}
	if strings.TrimSpace(nameHint) != "" {
		params.Set("extparam", strings.TrimSpace(nameHint))
	}
	params.Set("luicode", "20000174")
	params.Set("launchid", "10000360-page_H5")
	params.Set("lfid", baseID+"_-_feed")
	params.Set("v_p", "42")
	params.Set("flowId", baseID)

	url := "https://weibo.com/ajax_proxy/chaohua/page?" + params.Encode()
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	m.mu.RLock()
	cookieHeader := buildWeiboCookieHeader(m.Cookie)
	m.mu.RUnlock()
	applyWeiboRequestHeaders(req, cookieHeader)
	req.Header.Set("Referer", fmt.Sprintf("https://weibo.com/page/%s?containerid=%s", baseID, baseID))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("chaohua page http=%d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result struct {
		Data struct {
			OID       string `json:"oid"`
			Nick      string `json:"nick"`
			PageTitle string `json:"page_title"`
			LabelList []struct {
				Text string `json:"text"`
			} `json:"label_list"`
		} `json:"data"`
		Header struct {
			Data struct {
				OID       string `json:"oid"`
				Nick      string `json:"nick"`
				PageTitle string `json:"page_title"`
				LabelList []struct {
					Text string `json:"text"`
				} `json:"label_list"`
			} `json:"data"`
		} `json:"header"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 160 {
			preview = preview[:160] + "..."
		}
		return nil, fmt.Errorf("chaohua page invalid response: %w | body=%q", err, preview)
	}

	actualOID := strings.TrimSpace(result.Data.OID)
	if actualOID == "" {
		actualOID = strings.TrimSpace(result.Header.Data.OID)
	}
	if actualOID == "" {
		actualOID = oid
	}
	name := strings.TrimSpace(result.Data.Nick)
	if name == "" {
		name = strings.TrimSpace(result.Header.Data.Nick)
	}
	if name == "" {
		name = strings.TrimSpace(result.Data.PageTitle)
	}
	if name == "" {
		name = strings.TrimSpace(result.Header.Data.PageTitle)
	}
	if name == "" {
		name = strings.TrimSpace(nameHint)
	}
	if name == "" {
		name = actualOID
	}

	interaction := ""
	signText := ""
	signCount := 0
	labelTexts := make([]string, 0, len(result.Data.LabelList)+len(result.Header.Data.LabelList))
	for _, item := range result.Data.LabelList {
		labelTexts = append(labelTexts, strings.TrimSpace(item.Text))
	}
	for _, item := range result.Header.Data.LabelList {
		labelTexts = append(labelTexts, strings.TrimSpace(item.Text))
	}
	for _, text := range labelTexts {
		if text == "" {
			continue
		}
		if interaction == "" && strings.Contains(text, "今日互动") {
			interaction = text
		}
		if strings.Contains(text, "签到") && strings.Contains(text, "人") {
			if n, ok := parseSignCountFromLabelText(text); ok {
				signCount = n
				signText = text
				if strings.Contains(text, "今日互动") {
					break
				}
			}
		}
	}
	if signCount <= 0 {
		var generic any
		if err := json.Unmarshal(body, &generic); err == nil {
			texts := make([]string, 0, 64)
			collectJSONStringValues(generic, &texts)
			for _, text := range texts {
				if interaction == "" && strings.Contains(text, "今日互动") {
					interaction = text
				}
				if strings.Contains(text, "签到") && strings.Contains(text, "人") {
					if n, ok := parseSignCountFromLabelText(text); ok {
						signCount = n
						signText = text
						if strings.Contains(text, "今日互动") {
							break
						}
					}
				}
			}
		}
	}
	if signCount <= 0 {
		return nil, fmt.Errorf("未解析到签到人数")
	}

	return &WeiboSuperCountResult{
		OID:              actualOID,
		Name:             name,
		SignCount:        signCount,
		TodayInteraction: interaction,
		SignText:         signText,
		Source:           "web",
	}, nil
}

func (m *WeiboMonitor) fetchSuperCountByOIDViaApp(oid string, nameHint string) (*WeiboSuperCountResult, error) {
	m.mu.RLock()
	app := m.AppConfig
	m.mu.RUnlock()
	if app == nil {
		return nil, fmt.Errorf("weibo app auth 未配置")
	}
	if strings.TrimSpace(app.Authorization) == "" {
		return nil, fmt.Errorf("weibo app authorization 缺失")
	}

	baseID := strings.TrimPrefix(strings.TrimSpace(oid), "1022:")
	if baseID == "" {
		return nil, fmt.Errorf("oid 不能为空")
	}
	flowID := baseID + "_-_recommend"
	containerID := baseID
	channelID := "recommend"
	if strings.TrimSpace(app.CapturedOID) != "" {
		capturedBase := strings.TrimPrefix(strings.TrimSpace(app.CapturedOID), "1022:")
		if capturedBase == baseID {
			flowID = capturedBase + "_-_recommend"
			containerID = capturedBase
		}
	}

	path := strings.TrimSpace(app.RequestPath)
	if path == "" {
		path = "/2/statuses/container_timeline_topicpage"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(app.Host)
	if host == "" {
		host = "api.weibo.cn"
	}

	params := neturl.Values{}
	if rawCapture := strings.TrimSpace(html.UnescapeString(app.RawCapture)); rawCapture != "" {
		re := regexp.MustCompile(`https://api\.weibo\.cn[^\s'\"]+`)
		if rawURL := strings.TrimSpace(re.FindString(rawCapture)); rawURL != "" {
			if parsedURL, err := neturl.Parse(rawURL); err == nil {
				if parsedURL.Path != "" {
					path = parsedURL.Path
				}
				for k, vals := range parsedURL.Query() {
					for _, v := range vals {
						params.Add(k, v)
					}
				}
			}
		}
	}
	if len(params) == 0 {
		params.Set("containerid", containerID)
		params.Set("flowId", flowID)
		params.Set("channelid", channelID)
		params.Set("page", "1")
		params.Set("count", "15")
		params.Set("page_common_ext", "topicPrompt:1|page:recommend=1|hide_page:1")
	}
	params.Set("containerid", containerID)
	params.Set("flowId", flowID)
	if params.Get("channelid") == "" {
		params.Set("channelid", channelID)
	}
	if app.GSID != "" {
		params.Set("gsid", app.GSID)
	}
	if app.Aid != "" {
		params.Set("aid", app.Aid)
	}
	if app.S != "" {
		params.Set("s", app.S)
	}
	url := "https://" + host + path + "?" + params.Encode()

	requestBody := strings.TrimSpace(html.UnescapeString(app.RequestBody))
	if requestBody == "" {
		bodyValues := neturl.Values{}
		bodyValues.Set("fid", containerID)
		bodyValues.Set("flowId", containerID)
		bodyValues.Set("refresh", "init")
		bodyValues.Set("featurecode", "10000001")
		bodyValues.Set("luicode", "10000001")
		bodyValues.Set("uicode", "10000011")
		requestBody = bodyValues.Encode()
	} else {
		if values, err := neturl.ParseQuery(requestBody); err == nil {
			values.Set("flowId", containerID)
			values.Set("fid", containerID)
			requestBody = values.Encode()
		}
	}

	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("POST", url, strings.NewReader(requestBody))
	ua := strings.TrimSpace(app.UserAgent)
	if ua == "" {
		ua = "WeiboOversea/16.4.1 (iPhone15,2; iOS 26.3.1; Scale/3.00)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Authorization", app.Authorization)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	req.Header.Set("Host", host)
	if app.SNRT != "" {
		req.Header.Set("SNRT", app.SNRT)
	}
	if app.XSessionID != "" {
		req.Header.Set("X-Sessionid", app.XSessionID)
	}
	if app.XEngineType != "" {
		req.Header.Set("x-engine-type", app.XEngineType)
	}
	if app.XShanhaiPass != "" {
		req.Header.Set("x-shanhai-pass", app.XShanhaiPass)
	}
	if app.XLogUID != "" {
		req.Header.Set("X-Log-Uid", app.XLogUID)
	}
	if app.XValidator != "" {
		req.Header.Set("X-Validator", app.XValidator)
	}
	if app.CronetRID != "" {
		req.Header.Set("cronet_rid", app.CronetRID)
	}
	// Let Go handle compression negotiation to avoid opaque framed bytes in body logs.
	if app.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", app.AcceptLanguage)
	}
	if app.GSID != "" {
		req.Header.Set("Cookie", "gsid="+app.GSID)
	}

	logPreview := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if len(s) <= n {
			return s
		}
		return s[:n] + "..."
	}

	_ = logPreview
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Weibo][AppCount] request failed oid=%s err=%v", oid, err)
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[Weibo][AppCount] read body failed oid=%s err=%v", oid, err)
		return nil, err
	}
	bodyPreview := logPreview(string(body), 500)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("app topicpage http=%d body=%q", resp.StatusCode, bodyPreview)
	}

	var result struct {
		Header struct {
			Data struct {
				Nick          string `json:"nick"`
				PageTitle     string `json:"page_title"`
				OID           string `json:"oid"`
				TitleIconList []struct {
					IconURL string `json:"icon_url"`
				} `json:"title_icon_list"`
				LabelList []struct {
					Text string `json:"text"`
				} `json:"label_list"`
				DescMore []struct {
					Desc    string `json:"desc"`
					DescNum string `json:"desc_num"`
				} `json:"desc_more"`
			} `json:"data"`
		} `json:"header"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		log.Printf("[Weibo][AppCount] json decode failed oid=%s err=%v body=%q", oid, err, preview)
		return nil, fmt.Errorf("app topicpage invalid response: %w | body=%q", err, preview)
	}

	actualOID := strings.TrimSpace(result.Header.Data.OID)
	if actualOID == "" {
		actualOID = oid
	}
	name := strings.TrimSpace(result.Header.Data.Nick)
	if name == "" {
		name = strings.TrimSpace(result.Header.Data.PageTitle)
	}
	if name == "" {
		name = strings.TrimSpace(nameHint)
	}
	if name == "" {
		name = actualOID
	}

	res := &WeiboSuperCountResult{OID: actualOID, Name: name, Source: "app"}
	if len(result.Header.Data.TitleIconList) > 0 {
		res.LevelIconURL = strings.TrimSpace(result.Header.Data.TitleIconList[0].IconURL)
		res.LevelText = inferWeiboLevelText(res.LevelIconURL)
	}
	for _, item := range result.Header.Data.LabelList {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		if res.TodayInteraction == "" && strings.Contains(text, "今日互动") {
			res.TodayInteraction = text
		}
		if res.Heat24h == "" && strings.Contains(text, "24小时热度") {
			res.Heat24h = text
		}
		if res.SuperLikeText == "" && strings.Contains(strings.ToUpper(text), "超LIKE") {
			res.SuperLikeCount = parseFirstNumber(text)
			res.SuperLikeText = normalizeWeiboSuperLikeText(text, res.SuperLikeCount)
		}
		if res.CreatorOfficerText == "" && strings.Contains(text, "创作官") {
			res.CreatorOfficerText = text
		}
		if res.FanDiamondText == "" && (strings.Contains(text, "钻咖") || strings.Contains(text, "粉丝钻") || strings.Contains(text, "粉钻")) {
			res.FanDiamondText = text
		}
		if strings.Contains(text, "签到") && strings.Contains(text, "人") {
			if n, ok := parseSignCountFromLabelText(text); ok {
				res.SignCount = n
				res.SignText = text
			}
		}
	}
	for _, item := range result.Header.Data.DescMore {
		desc := strings.TrimSpace(item.Desc)
		value := strings.TrimSpace(item.DescNum)
		if desc == "" && value == "" {
			continue
		}
		if res.PostLabel == "" && strings.Contains(desc, "帖") {
			res.PostLabel = desc
			res.PostCount = value
			continue
		}
		if res.FansLabel == "" {
			res.FansLabel = desc
			res.FansCount = value
			continue
		}
	}
	if res.CreatorOfficerText == "" || res.FanDiamondText == "" {
		creatorText, diamondText := scanWeiboSuperIdentityTexts(body)
		if res.CreatorOfficerText == "" {
			res.CreatorOfficerText = creatorText
		}
		if res.FanDiamondText == "" {
			res.FanDiamondText = diamondText
		}
	}
	if res.SuperLikeCount <= 0 || strings.TrimSpace(res.SuperLikeText) == "" {
		if likeText, likeCount, err := m.fetchSuperLikeByOIDViaAppCardlist(app, baseID); err != nil {
			log.Printf("[Weibo][AppCount] chaolike fallback failed oid=%s err=%v", oid, err)
		} else if likeCount > 0 {
			res.SuperLikeCount = likeCount
			res.SuperLikeText = normalizeWeiboSuperLikeText(likeText, likeCount)
			log.Printf("[Weibo][AppCount] chaolike fallback success oid=%s superlike=%q", oid, res.SuperLikeText)
		}
	}

	if res.SignCount <= 0 {
		log.Printf("[Weibo][AppCount] sign parse failed oid=%s name=%q interaction=%q heat=%q superlike=%q labels=%d desc_more=%d", oid, name, res.TodayInteraction, res.Heat24h, res.SuperLikeText, len(result.Header.Data.LabelList), len(result.Header.Data.DescMore))
		return nil, fmt.Errorf("未解析到签到人数")
	}

	log.Printf("[Weibo][AppCount] success oid=%s name=%q sign=%d superlike=%d", oid, name, res.SignCount, res.SuperLikeCount)
	return res, nil
}

func (m *WeiboMonitor) fetchSuperLikeByOIDViaAppCardlist(app *WeiboAppAuth, baseID string) (string, int, error) {
	baseID = strings.TrimSpace(baseID)
	baseID = strings.TrimPrefix(baseID, "1022:")
	if baseID == "" {
		return "", 0, fmt.Errorf("baseID 不能为空")
	}
	chaolikeBase := strings.TrimPrefix(baseID, "100808")
	if chaolikeBase == "" {
		chaolikeBase = baseID
	}
	containerID := "231140" + chaolikeBase + "_-_chaolikenew"

	host := strings.TrimSpace(app.Host)
	if host == "" {
		host = "api.weibo.cn"
	}
	path := "/2/cardlist"
	params := neturl.Values{}
	if rawCapture := strings.TrimSpace(html.UnescapeString(app.RawCapture)); rawCapture != "" {
		re := regexp.MustCompile(`https://api\.weibo\.cn[^\s'\"]+`)
		if rawURL := strings.TrimSpace(re.FindString(rawCapture)); rawURL != "" {
			if parsedURL, err := neturl.Parse(rawURL); err == nil {
				for k, vals := range parsedURL.Query() {
					for _, v := range vals {
						params.Add(k, v)
					}
				}
			}
		}
	}
	params.Set("containerid", containerID)
	params.Set("fid", containerID)
	if strings.TrimSpace(params.Get("count")) == "" {
		params.Set("count", "20")
	}
	if strings.TrimSpace(params.Get("page")) == "" {
		params.Set("page", "1")
	}
	if strings.TrimSpace(params.Get("moduleID")) == "" {
		params.Set("moduleID", "pagecard")
	}
	if strings.TrimSpace(params.Get("need_head_cards")) == "" {
		params.Set("need_head_cards", "1")
	}
	if strings.TrimSpace(params.Get("featurecode")) == "" {
		params.Set("featurecode", "10000085")
	}
	if strings.TrimSpace(params.Get("uicode")) == "" {
		params.Set("uicode", "10000011")
	}
	if app.GSID != "" {
		params.Set("gsid", app.GSID)
	}
	if app.Aid != "" {
		params.Set("aid", app.Aid)
	}
	if app.S != "" {
		params.Set("s", app.S)
	}

	url := "https://" + host + path + "?" + params.Encode()
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	ua := strings.TrimSpace(app.UserAgent)
	if ua == "" {
		ua = "WeiboOversea/16.4.1 (iPhone15,2; iOS 26.3.1; Scale/3.00)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Authorization", app.Authorization)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Host", host)
	if app.SNRT != "" {
		req.Header.Set("SNRT", app.SNRT)
	}
	if app.XSessionID != "" {
		req.Header.Set("X-Sessionid", app.XSessionID)
	}
	if app.XEngineType != "" {
		req.Header.Set("x-engine-type", app.XEngineType)
	}
	if app.XShanhaiPass != "" {
		req.Header.Set("x-shanhai-pass", app.XShanhaiPass)
	}
	if app.XLogUID != "" {
		req.Header.Set("X-Log-Uid", app.XLogUID)
	}
	if app.XValidator != "" {
		req.Header.Set("X-Validator", app.XValidator)
	}
	if app.CronetRID != "" {
		req.Header.Set("cronet_rid", app.CronetRID)
	}
	if app.AcceptLanguage != "" {
		req.Header.Set("Accept-Language", app.AcceptLanguage)
	}
	if app.GSID != "" {
		req.Header.Set("Cookie", "gsid="+app.GSID)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return "", 0, fmt.Errorf("chaolike cardlist http=%d body=%q", resp.StatusCode, preview)
	}

	var result struct {
		CardlistInfo struct {
			Title string `json:"title"`
		} `json:"cardlistInfo"`
		Cards []struct {
			Desc      string `json:"desc"`
			CardGroup []struct {
				Desc string `json:"desc"`
			} `json:"card_group"`
		} `json:"cards"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		preview := strings.TrimSpace(string(body))
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return "", 0, fmt.Errorf("chaolike cardlist invalid response: %w | body=%q", err, preview)
	}

	candidates := []string{}
	if strings.TrimSpace(result.CardlistInfo.Title) != "" {
		candidates = append(candidates, result.CardlistInfo.Title)
	}
	for _, card := range result.Cards {
		if strings.TrimSpace(card.Desc) != "" {
			candidates = append(candidates, strings.TrimSpace(card.Desc))
		}
		for _, group := range card.CardGroup {
			if strings.TrimSpace(group.Desc) != "" {
				candidates = append(candidates, strings.TrimSpace(group.Desc))
			}
		}
	}
	for _, text := range candidates {
		upper := strings.ToUpper(text)
		if strings.Contains(upper, "超LIKE") {
			if n := parseFirstNumber(text); n > 0 {
				return text, n, nil
			}
		}
	}
	return "", 0, fmt.Errorf("未在 chaolike cardlist 中解析到超LIKE人数")
}

func collectJSONStringValues(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for _, item := range x {
			collectJSONStringValues(item, out)
		}
	case []any:
		for _, item := range x {
			collectJSONStringValues(item, out)
		}
	case string:
		t := strings.TrimSpace(x)
		if t != "" {
			*out = append(*out, t)
		}
	}
}

func parseSignCountFromLabelText(text string) (int, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, false
	}
	re := regexp.MustCompile(`签到\s*([0-9][0-9,\.]*(?:万|千)?)\s*人`)
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return 0, false
	}
	return parseChineseNumberToInt(m[1])
}

func parseChineseNumberToInt(raw string) (int, bool) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	if raw == "" {
		return 0, false
	}
	multiplier := 1.0
	if strings.HasSuffix(raw, "万") {
		multiplier = 10000
		raw = strings.TrimSuffix(raw, "万")
	} else if strings.HasSuffix(raw, "千") {
		multiplier = 1000
		raw = strings.TrimSuffix(raw, "千")
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, false
	}
	if v <= 0 {
		return 0, false
	}
	return int(v * multiplier), true
}

func ParseWeiboSignCountFromLabelText(text string) (int, bool) {
	return parseSignCountFromLabelText(text)
}

func ParseChineseNumber(raw string) (int, bool) {
	return parseChineseNumberToInt(raw)
}

func firstNonEmptyNonMonitor(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func parseFirstNumber(text string) int {
	re := regexp.MustCompile(`([0-9][0-9,\.]*)(?:人|次|个)?`)
	m := re.FindStringSubmatch(strings.TrimSpace(text))
	if len(m) < 2 {
		return 0
	}
	n, _ := parseChineseNumberToInt(m[1])
	return n
}

func normalizeWeiboSuperLikeText(text string, count int) string {
	text = strings.TrimSpace(text)
	if count > 0 {
		return fmt.Sprintf("超LIKE %d人", count)
	}
	text = strings.ReplaceAll(text, "（", "(")
	text = strings.ReplaceAll(text, "）", ")")
	re := regexp.MustCompile(`(?i)超LIKE\s*\(([^)]+)\)`)
	if m := re.FindStringSubmatch(text); len(m) >= 2 {
		return "超LIKE " + strings.TrimSpace(m[1])
	}
	return text
}

func inferWeiboLevelText(iconURL string) string {
	iconURL = strings.ToLower(strings.TrimSpace(iconURL))
	if iconURL == "" {
		return ""
	}
	re := regexp.MustCompile(`active_level_page_([a-z]+)_([0-9]+)\.png`)
	m := re.FindStringSubmatch(iconURL)
	if len(m) < 3 {
		return ""
	}
	levelType := m[1]
	levelNum := m[2]
	switch levelType {
	case "silver":
		return "银" + levelNum
	case "gold":
		return "金" + levelNum
	case "diamond":
		return "钻" + levelNum
	default:
		return levelType + levelNum
	}
}
