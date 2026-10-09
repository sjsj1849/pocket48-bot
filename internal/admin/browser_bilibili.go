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

	"pocket48-bot/internal/bilibili"

	"github.com/gorilla/websocket"
)

type bilibiliBrowserCookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Domain  string  `json:"domain"`
	Expires float64 `json:"expires"`
}

type bilibiliBrowserSession struct {
	Cookies   []bilibiliBrowserCookie `json:"cookies"`
	UpdatedAt int64                   `json:"updatedAt"`
}

// validBilibiliBrowserSession 只认 SESSDATA（决定是否被当作登录用户），
// 顺带允许 bili_jct / DedeUserID 这两个配套 cookie。
func validBilibiliBrowserSession(session bilibiliBrowserSession) bool {
	if len(session.Cookies) == 0 {
		return false
	}
	now := float64(time.Now().Unix())
	allowed := map[string]bool{"SESSDATA": true, "bili_jct": true, "DedeUserID": true, "DedeUserID__ckMd5": true}
	seen := map[string]bool{}
	sessdata := false
	for _, c := range session.Cookies {
		domain := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
		if !allowed[c.Name] || seen[c.Name] {
			return false
		}
		// 只接受 bilibili 域，防止别的站点的 cookie 混进来。
		if domain != "bilibili.com" {
			return false
		}
		if c.Value == "" || len(c.Value) > 4096 || strings.ContainsAny(c.Value, "\r\n;") {
			return false
		}
		if c.Expires > 0 && c.Expires <= now {
			return false
		}
		seen[c.Name] = true
		if c.Name == "SESSDATA" {
			sessdata = true
		}
	}
	return sessdata
}

// bilibiliCookieFromSession 把浏览器 cookie 拼成请求头用的 Cookie 串。
// 存进 settings.json 后，Go 采集侧就能带上 SESSDATA 突破匿名风控。
func bilibiliCookieFromSession(session bilibiliBrowserSession) string {
	parts := make([]string, 0, len(session.Cookies))
	// SESSDATA 放最前，与浏览器自身发送顺序一致。
	order := []string{"SESSDATA", "bili_jct", "DedeUserID", "DedeUserID__ckMd5"}
	for _, name := range order {
		for _, c := range session.Cookies {
			if c.Name == name {
				parts = append(parts, name+"="+c.Value)
			}
		}
	}
	return strings.Join(parts, "; ")
}

// The authenticated panel receives status only; cookies stay server-side.
func (s *Server) handleBrowserBilibili(w http.ResponseWriter, r *http.Request) {
	sessionDir := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "bilibili-browser")

	if r.Method == http.MethodGet {
		var session bilibiliBrowserSession
		_ = bilibili.Read[bilibiliBrowserSession](sessionDir, "session.json", &session)
		cookieConfigured := strings.TrimSpace(bilibiliCookieFromSession(session)) != ""
		writeJSON(w, http.StatusOK, map[string]any{
			"sessionConfigured": validBilibiliBrowserSession(session),
			"cookieConfigured":  cookieConfigured,
			"updatedAt":         session.UpdatedAt,
		})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}

	fail := func(message string) { writeJSON(w, http.StatusBadRequest, apiError{Error: message}) }
	var input struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil || (input.Action != "open" && input.Action != "sync") {
		fail("请选择打开登录或同步登录态")
		return
	}

	data, err := os.ReadFile(filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "browser-sidecar.json"))
	var endpoint struct {
		Port int `json:"port"`
	}
	if err != nil || json.Unmarshal(data, &endpoint) != nil || endpoint.Port < 1 || endpoint.Port > 65535 {
		fail("浏览器侧卡尚未就绪，请先启动 Bot")
		return
	}

	dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	conn, _, err := dialer.DialContext(r.Context(), fmt.Sprintf("ws://127.0.0.1:%d/", endpoint.Port), nil)
	if err != nil {
		fail("无法连接浏览器侧卡")
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))

	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	if conn.WriteJSON(map[string]string{"cmd": "bilibili_panel_" + input.Action, "requestId": id}) != nil {
		fail("浏览器请求失败")
		return
	}

	for {
		var result struct {
			Type      string                 `json:"type"`
			RequestID string                 `json:"requestId"`
			Error     string                 `json:"error"`
			Session   bilibiliBrowserSession `json:"session"`
		}
		if conn.ReadJSON(&result) != nil {
			fail("浏览器响应超时，请在下方浏览器完成登录后重试")
			return
		}
		if result.Type != "bilibili_panel_result" || result.RequestID != id {
			continue
		}
		if result.Error != "" {
			fail(result.Error)
			return
		}
		if input.Action == "sync" {
			if !validBilibiliBrowserSession(result.Session) {
				fail("还没有拿到 B 站登录态。请先在下方浏览器扫码或用短信登录，成功后再点同步登录态")
				return
			}
			result.Session.UpdatedAt = time.Now().UnixMilli()
			if os.MkdirAll(sessionDir, 0700) != nil || os.Chmod(sessionDir, 0700) != nil || bilibili.Write(sessionDir, "session.json", result.Session) != nil {
				fail("保存 B 站登录态失败")
				return
			}
			// 同步进 settings.json：Go 采集侧每次请求都会带上这个 Cookie。
			if err := s.applyBilibiliCookie(bilibiliCookieFromSession(result.Session)); err != nil {
				fail("登录态已保存，但写入 B 站配置失败：" + err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
}

// applyBilibiliCookie 把 Cookie 写进 settings.json，只改 Cookie 这一个键。
func (s *Server) applyBilibiliCookie(cookie string) error {
	dir := bilibili.Dir(s.opts.ConfigPath)
	cfg, err := bilibili.LoadSettings(dir)
	if err != nil {
		return err
	}
	cfg.Cookie = cookie
	return bilibili.SaveSettings(dir, cfg)
}
