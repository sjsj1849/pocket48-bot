package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// 抖音与 TikTok 的面板登录入口。
//
// ★ 为什么不照抄 B 站的「cookie 存 session.json 再同步进配置」那套（2026-10-04）：
//
//  1. 抖音采集走 BrowserBridge，复用 sidecar 那个浏览器实例的 storage state
//     （见 douyin_monitor.go 的 SetBrowserBridge），它**不需要**单独一份
//     cookie 配置 —— DOUYIN_SUBSCRIPTIONS 里那个长 key 是 sec_user_id 标识，
//     不是浏览器 cookie。所以抖音只要「登录进了同一个浏览器」就自动生效。
//  2. TikTok 采集是 sidecar/tiktok-monitor/collector.py 自己开的
//     匿名上下文，采集本身也不吃登录态。
//
// 因此这两个平台的「同步登录态」语义被简化成：
//   - open：让 sidecar 打开对应站点登录页，用户在下方 noVNC 里手动登录；
//   - sync：**验证**该站点在浏览器里确实已登录，然后落一份登录态快照。
//
// 这样做的意义是给用户一个明确的「到底登录成功没有」的信号，
// 而不是像以前那样页面里根本没有入口、只能靠猜。

// douyinBrowserSnapshot 是抖音登录态快照。
type douyinBrowserSnapshot struct {
	Cookies   []browserSnapshotCookie `json:"cookies"`
	UpdatedAt int64                   `json:"updatedAt"`
}

type browserSnapshotCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Domain 不带前导点也接受，但下面 DomainAndPath 会补上。
	Domain string `json:"domain"`
	// Path 是 Playwright storage_state 的**必需**项：
	// 它要求 domain 与 path 成对，缺 path 会直接报
	// "Cookie should have a url or a domain/path pair"。
	//
	// ★ 2026-10-04 实测：面板把登录态写进 session.json 后，
	//   TikTok 采集侧**一次都没读过**它（collector.py 里 grep 'tiktok-browser'
	//   零命中）—— 面板白干了。这里补齐字段至少让快照成为合法格式，
	//   将来要接长连接采集时不必再返工。
	//
	// ★ 但**不要**因此把采集切到登录态：实测对照（匿名 vs 登录态，
	//   同一时刻同一账号）—— 匿名拿到 3 条作品 + 3 张封面，
	//   登录态直接撞上限流返回 0 字节。匿名反而更稳。
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

// DomainAndPath 返回 Playwright 认得的 (domain, path) 组合。
//
// TikTok 的 cookie 全是 .tiktok.com 前缀且 path 为根；
// 老快照（2026-10-04 之前写的）没有 path 字段，这里兜底成 "/"。
func (c browserSnapshotCookie) DomainAndPath() (string, string) {
	d := c.Domain
	if d == "" {
		return "", ""
	}
	p := c.Path
	if p == "" {
		p = "/"
	}
	return d, p
}

// douyinCookieDomains 是抖音系的域名后缀（含抖音与iesdouyin 两个域，
// 登录过程中两边都会种 cookie，只认一个会漏）。
var douyinCookieDomains = []string{"douyin.com", "iesdouyin.com"}

// validDouyinSession 校验快照里是否真有属于抖音域的有效 cookie。
//
// 判定依据取自实测：抖音登录后一定会拿到 sessionid（有时是 sessionid_ss）。
// 只要它存在且未过期就算登录成功。
//
// 绝不返回 cookie 内容，GET 只回布尔值。
func validDouyinSession(session douyinBrowserSnapshot) bool {
	if len(session.Cookies) == 0 {
		return false
	}
	now := float64(time.Now().Unix())
	marked := false
	for _, c := range session.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		matched := false
		for _, suffix := range douyinCookieDomains {
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if c.Expires > 0 && c.Expires <= now {
			continue
		}
		if c.Name == "sessionid" || c.Name == "sessionid_ss" {
			marked = true
		}
	}
	return marked
}

// tiktokBrowserSnapshot 是 TikTok 登录态快照。
type tiktokBrowserSnapshot struct {
	Cookies   []browserSnapshotCookie `json:"cookies"`
	UpdatedAt int64                   `json:"updatedAt"`
}

// validTiktokSession 校验 TikTok 登录态。
//
// TikTok 登录后的标志性 cookie 是 ttwid（游客也会有）+ sessionid。
// 实测真正代表「登录用户」的是 sessionid 与 sessionid_ss，
// 只有 ttwid 的话就是匿名状态，不能算登录。
var tiktokCookieDomains = []string{"tiktok.com", "tiktokcdn.com", "tiktokv.com"}

func validTiktokSession(session tiktokBrowserSnapshot) bool {
	if len(session.Cookies) == 0 {
		return false
	}
	now := float64(time.Now().Unix())
	for _, c := range session.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		matched := false
		for _, suffix := range tiktokCookieDomains {
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if c.Expires > 0 && c.Expires <= now {
			continue
		}
		if c.Name == "sessionid" || c.Name == "sessionid_ss" {
			return true
		}
	}
	return false
}

// storageDirOf 返回 <storage>/<name> 并确保目录存在、权限为 0700。
func (s *Server) storageDirOf(name string) (string, error) {
	dir := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// 目录里存的是登录凭据，权限必须收紧。
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// sidecarEndpoint 读浏览器侧卡的实际监听端口。
func (s *Server) sidecarEndpoint() (int, error) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "browser-sidecar.json"))
	var endpoint struct {
		Port int `json:"port"`
	}
	if err != nil || json.Unmarshal(data, &endpoint) != nil ||
		endpoint.Port < 1 || endpoint.Port > 65535 {
		return 0, fmt.Errorf("浏览器侧卡尚未就绪，请先启动 Bot")
	}
	return endpoint.Port, nil
}

// dialSidecar 连上浏览器侧卡。
//
// 用 Dial 而不是 DialContext：握手超时由 Dialer.HandshakeTimeout 控制（3 秒），
// 而整条命令的总体时限已经由 SetReadDeadline 兜住了 —— 再叠一层 ctx 反而
// 会让「用户还在慢慢登录」这种正常情况被请求上下文提前掐断。
func (s *Server) dialSidecar() (*websocket.Conn, error) {
	port, err := s.sidecarEndpoint()
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.Dial(fmt.Sprintf("ws://127.0.0.1:%d/", port), nil)
	if err != nil {
		return nil, fmt.Errorf("无法连接浏览器侧卡")
	}
	return conn, nil
}

// sidecarCookies 向侧卡发一条 open/sync 命令，等它回 cookie 快照。
//
// resultType 形如 "douyin_panel_result"，与侧卡里的下发类型对应。
func (s *Server) sidecarCookies(
	cmdPrefix, action, resultType string, timeout time.Duration,
) (*[]browserSnapshotCookie, error) {
	conn, err := s.dialSidecar()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_ = conn.SetReadDeadline(time.Now().Add(timeout))

	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := conn.WriteJSON(map[string]string{
		"cmd": cmdPrefix + "_panel_" + action, "requestId": id,
	}); err != nil {
		return nil, fmt.Errorf("浏览器请求失败")
	}

	for {
		var result struct {
			Type      string `json:"type"`
			RequestID string `json:"requestId"`
			Error     string `json:"error"`
			Session   struct {
				Cookies []browserSnapshotCookie `json:"cookies"`
			} `json:"session"`
			Cookies []browserSnapshotCookie `json:"cookies"`
		}
		if err := conn.ReadJSON(&result); err != nil {
			return nil, fmt.Errorf("浏览器响应超时，请在下方浏览器完成登录后重试")
		}
		if result.Type != resultType || result.RequestID != id {
			continue
		}
		if result.Error != "" {
			return nil, fmt.Errorf("%s", result.Error)
		}
		// 侧卡有两种回法：包在 session.cookies 里，或直接给 cookies。
		cookies := result.Session.Cookies
		if len(cookies) == 0 {
			cookies = result.Cookies
		}
		return &cookies, nil
	}
}

// handleBrowserDouyin 处理面板上的抖音登录入口。
func (s *Server) handleBrowserDouyin(w http.ResponseWriter, r *http.Request) {
	s.handlePlatformLogin(w, r, platformLoginSpec{
		name:       "抖音",
		dirName:    "douyin-browser",
		fileName:   "session.json",
		cmdPrefix:  "douyin",
		resultType: "douyin_panel_result",
		// 打开登录页只是切个页面，很快；同步要等用户登录完，给足时间。
		openTimeout:    60 * time.Second,
		syncTimeout:    90 * time.Second,
		notLoggedInMsg: "还没有拿到抖音登录态。请先在下方浏览器里登录抖音（扫码即可），成功后再点同步登录态",
		valid: func(cookies []browserSnapshotCookie) bool {
			return validDouyinSession(douyinBrowserSnapshot{Cookies: cookies})
		},
	})
}

// handleBrowserTiktok 处理面板上的 TikTok 登录入口。
//
// 采集本身不依赖登录（匿名上下文即可），这个入口的价值是：
// 用户想确认「我的 TikTok 账号在这台机器上已登录」时有个明确入口，
// 同时给后续需要登录态的功能（如查看仅自己可见的数据）留好路。
func (s *Server) handleBrowserTiktok(w http.ResponseWriter, r *http.Request) {
	s.handlePlatformLogin(w, r, platformLoginSpec{
		name:           "TikTok",
		dirName:        "tiktok-browser",
		fileName:       "session.json",
		cmdPrefix:      "tiktok",
		resultType:     "tiktok_panel_result",
		openTimeout:    60 * time.Second,
		syncTimeout:    90 * time.Second,
		notLoggedInMsg: "还没有拿到 TikTok 登录态。请先在下方浏览器里登录 TikTok，成功后再点同步登录态",
		valid: func(cookies []browserSnapshotCookie) bool {
			return validTiktokSession(tiktokBrowserSnapshot{Cookies: cookies})
		},
	})
}

// platformLoginSpec 描述一个平台的登录入口行为。
type platformLoginSpec struct {
	name       string // 展示名，用于错误文案
	dirName    string // storage 下的子目录
	fileName   string // 快照文件名
	cmdPrefix  string // 侧卡命令前缀（douyin / tiktok）
	resultType string // 侧卡结果事件类型

	openTimeout time.Duration
	syncTimeout time.Duration

	notLoggedInMsg string
	// valid 判定拿到的 cookie 是否代表「已登录」。
	valid func([]browserSnapshotCookie) bool
}

// handlePlatformLogin 是抖音 / TikTok 共用的处理流程。
func (s *Server) handlePlatformLogin(w http.ResponseWriter, r *http.Request, spec platformLoginSpec) {
	dir, err := s.storageDirOf(spec.dirName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: "创建登录态目录失败"})
		return
	}
	path := filepath.Join(dir, spec.fileName)

	// GET 只回布尔值，**绝不回 cookie 内容**（有泄漏风险，也有测试断言）。
	if r.Method == http.MethodGet {
		var snapshot douyinBrowserSnapshot
		_ = readJSONFile(path, &snapshot)
		writeJSON(w, http.StatusOK, map[string]any{
			"sessionConfigured": spec.valid(snapshot.Cookies),
			"updatedAt":         snapshot.UpdatedAt,
		})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	var input struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil ||
		(input.Action != "open" && input.Action != "sync") {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择打开登录或同步登录态"})
		return
	}

	timeout := spec.openTimeout
	if input.Action == "sync" {
		timeout = spec.syncTimeout
	}
	cookies, err := s.sidecarCookies(spec.cmdPrefix, input.Action, spec.resultType, timeout)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	if input.Action == "sync" {
		if !spec.valid(*cookies) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: spec.notLoggedInMsg})
			return
		}
		snapshot := douyinBrowserSnapshot{
			Cookies:   *cookies,
			UpdatedAt: time.Now().UnixMilli(),
		}
		if err := writeJSONFile(path, snapshot); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "保存" + spec.name + "登录态失败"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeJSONFile 以 0600 原子写入 JSON 文件。
//
// 登录凭据必须 0600，且要先写临时文件再 rename ——
// 否则进程被杀时可能留下半个文件，下次同步就再也读不出来了。
//
// 注意不要复用 config.go 里的 writeJSONAtomic：那个是给「已存在的配置文件」
// 用的（先 Stat 再沿用原权限），这里要能在文件不存在时创建并强制 0600。
// 读侧则直接用 overview.go 里已有的 readJSONFile，不重复定义。
func writeJSONFile(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
