package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type xBrowserCookie struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Domain  string  `json:"domain"`
	Expires float64 `json:"expires"`
}
type xBrowserSession struct {
	Cookies   []xBrowserCookie `json:"cookies"`
	UpdatedAt int64            `json:"updatedAt"`
}

func validXBrowserSession(session xBrowserSession) bool {
	if len(session.Cookies) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, c := range session.Cookies {
		domain := strings.TrimPrefix(c.Domain, ".")
		if (c.Name != "auth_token" && c.Name != "ct0") || seen[c.Name] || (domain != "x.com" && domain != "twitter.com") || len(c.Value) == 0 || len(c.Value) > 4096 || strings.ContainsAny(c.Value, "\r\n;") || (c.Expires > 0 && c.Expires <= float64(time.Now().Unix())) {
			return false
		}
		seen[c.Name] = true
	}
	return true
}

// The authenticated panel receives status only; cookies stay server-side.
func (s *Server) handleBrowserX(w http.ResponseWriter, r *http.Request) {
	dir := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "x")
	if r.Method == http.MethodGet {
		var session xBrowserSession
		_ = weverse.Read(dir, "session.json", &session)
		var login struct {
			Phase       string `json:"phase"`
			Message     string `json:"message"`
			LastCheck   int64  `json:"lastCheck"`
			NextRetryAt int64  `json:"nextRetryAt"`
		}
		_ = weverse.Read(dir, "login-status.json", &login)
		writeJSON(w, http.StatusOK, map[string]any{"sessionConfigured": validXBrowserSession(session), "updatedAt": session.UpdatedAt, "loginStatus": login})
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
	if conn.WriteJSON(map[string]string{"cmd": "x_panel_" + input.Action, "requestId": id}) != nil {
		fail("浏览器请求失败")
		return
	}
	for {
		var result struct {
			Type      string          `json:"type"`
			RequestID string          `json:"requestId"`
			Error     string          `json:"error"`
			Session   xBrowserSession `json:"session"`
		}
		if conn.ReadJSON(&result) != nil {
			fail("浏览器响应超时，请在下方浏览器检查登录页面")
			return
		}
		if result.Type != "x_panel_result" || result.RequestID != id {
			continue
		}
		if result.Error != "" {
			fail(result.Error)
			return
		}
		if input.Action == "sync" {
			if !validXBrowserSession(result.Session) {
				fail("X 登录尚未完成，请先填写邮箱验证码")
				return
			}
			result.Session.UpdatedAt = time.Now().UnixMilli()
			if os.MkdirAll(dir, 0700) != nil || os.Chmod(dir, 0700) != nil || weverse.Write(dir, "session.json", result.Session) != nil {
				fail("保存 X 登录态失败")
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
}
