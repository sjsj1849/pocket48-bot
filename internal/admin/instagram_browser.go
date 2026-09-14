package admin

import (
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"os"
	"path/filepath"
	"pocket48-bot/internal/instagram"
	"strconv"
	"time"
)

func (s *Server) handleInstagramBrowser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	fail := func(message string) { writeJSON(w, 400, apiError{Error: message}) }
	var input struct {
		Action string `json:"action"`
	}
	if decodeJSON(r, &input) != nil || (input.Action != "import" && input.Action != "sync" && input.Action != "open") {
		fail("请选择导入、同步或打开浏览器")
		return
	}
	data, e := os.ReadFile(filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage/browser-sidecar.json"))
	var endpoint struct {
		Port int `json:"port"`
	}
	if e != nil || json.Unmarshal(data, &endpoint) != nil || endpoint.Port < 1 || endpoint.Port > 65535 {
		fail("浏览器侧卡尚未就绪")
		return
	}
	conn, _, e := (&websocket.Dialer{HandshakeTimeout: 3 * time.Second}).DialContext(r.Context(), fmt.Sprintf("ws://127.0.0.1:%d/", endpoint.Port), nil)
	if e != nil {
		fail("无法连接浏览器侧卡")
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(40 * time.Second))
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	if conn.WriteJSON(map[string]string{"cmd": "instagram_panel_" + input.Action, "requestId": id}) != nil {
		fail("浏览器请求失败")
		return
	}
	for {
		var result struct {
			Type      string `json:"type"`
			RequestID string `json:"requestId"`
			Error     string `json:"error"`
			Imported  bool   `json:"imported"`
			Synced    bool   `json:"synced"`
			Pending   bool   `json:"pending"`
			Reason    string `json:"reason"`
		}
		if conn.ReadJSON(&result) != nil {
			fail("浏览器响应超时")
			return
		}
		if result.Type != "instagram_panel_result" || result.RequestID != id {
			continue
		}
		if result.Error != "" {
			fail("Instagram 浏览器操作失败")
			return
		}
		if input.Action == "import" && !result.Imported {
			fail("采集器尚无可导入的登录态")
			return
		}
		if input.Action == "sync" && !result.Synced {
			fail("浏览器尚未登录 Instagram")
			return
		}
		if input.Action == "sync" && result.Pending {
			dir := instagram.Dir(s.opts.ConfigPath)
			cfg, e := instagram.LoadSettings(dir)
			if e != nil {
				fail("无法读取 Instagram 设置")
				return
			}
			_, e = (instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}).Call(r.Context(), map[string]any{"operation": "session_browser_apply"})
			if e != nil {
				writeJSON(w, 200, map[string]any{"pending": true, "message": "浏览器登录态已暂存：" + e.Error()})
				return
			}
		}
		writeJSON(w, 200, map[string]any{"imported": result.Imported, "synced": result.Synced, "message": "浏览器操作完成"})
		return
	}
}
