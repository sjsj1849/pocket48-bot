package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/gorilla/websocket"
	"net/http"
	"os"
	"path/filepath"
	"pocket48-bot/internal/weverse"
	"strconv"
	"time"
)

func (s *Server) weverseClient() (*weverse.Client, error) {
	dir := weverse.Dir(s.opts.ConfigPath)
	cfg, e := weverse.LoadSettings(dir)
	if e != nil {
		return nil, e
	}
	return weverse.NewClient(dir, cfg.ProxyURL), nil
}
func (s *Server) handleWeverse(w http.ResponseWriter, r *http.Request) {
	dir := weverse.Dir(s.opts.ConfigPath)
	fail := func(e error) { writeJSON(w, http.StatusBadRequest, apiError{Error: e.Error()}) }
	switch r.URL.Path {
	case "/api/weverse/settings":
		if r.Method == http.MethodGet {
			cfg, e := weverse.LoadSettings(dir)
			if e != nil {
				fail(e)
				return
			}
			var session weverse.Session
			_ = weverse.Read(dir, "session.json", &session)
			var status weverse.Status
			_ = weverse.Read(dir, "status.json", &status)
			writeJSON(w, 200, map[string]any{"settings": cfg, "sessionConfigured": session.AccessToken != "" || session.RefreshToken != "", "status": status})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var cfg weverse.Settings
		if e := decodeJSON(r, &cfg); e != nil {
			fail(e)
			return
		}
		if e := weverse.SaveSettings(dir, cfg); e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case "/api/weverse/search", "/api/weverse/members":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		c, e := s.weverseClient()
		if e != nil {
			fail(e)
			return
		}
		if r.URL.Path == "/api/weverse/search" {
			items, e := c.Search(r.Context(), r.URL.Query().Get("q"))
			if e != nil {
				fail(e)
				return
			}
			writeJSON(w, 200, map[string]any{"communities": items})
			return
		}
		id, e := strconv.ParseInt(r.URL.Query().Get("communityId"), 10, 64)
		if e != nil || id <= 0 {
			fail(fmt.Errorf("请选择团体"))
			return
		}
		members, e := c.Members(r.Context(), id)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"members": members})
	case "/api/weverse/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		c, e := s.weverseClient()
		if e != nil {
			fail(e)
			return
		}
		defer c.HTTP.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		events, e := c.Events(ctx)
		if e != nil {
			fail(e)
			return
		}
		cfg, e := weverse.LoadSettings(dir)
		if e != nil {
			fail(e)
			return
		}
		selected := []weverse.Event{}
		for i := len(events) - 1; i >= 0 && len(selected) < 10; i-- {
			event := events[i]
			matched, translate := false, false
			for _, sub := range cfg.Subscriptions {
				if weverse.Matches(sub, event) {
					matched = true
					translate = translate || sub.Translate
				}
			}
			if !matched {
				continue
			}
			if translate {
				c.TranslateEvent(ctx, &event)
			}
			selected = append(selected, event)
		}
		writeJSON(w, 200, map[string]any{"events": selected, "message": "只读测试完成，展示最近匹配内容；未发送 QQ 消息"})
	case "/api/weverse/browser":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Action string `json:"action"`
		}
		if e := decodeJSON(r, &input); e != nil || (input.Action != "open" && input.Action != "sync") {
			fail(fmt.Errorf("请选择打开登录或同步登录态"))
			return
		}
		b, e := os.ReadFile(filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "browser-sidecar.json"))
		var endpoint struct {
			Port int `json:"port"`
		}
		if e != nil || json.Unmarshal(b, &endpoint) != nil || endpoint.Port <= 0 || endpoint.Port > 65535 {
			fail(fmt.Errorf("浏览器侧卡未启动，请先在浏览器页启动会话"))
			return
		}
		dialer := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
		conn, _, e := dialer.DialContext(r.Context(), fmt.Sprintf("ws://127.0.0.1:%d/", endpoint.Port), nil)
		if e != nil {
			fail(fmt.Errorf("浏览器未连接"))
			return
		}
		defer conn.Close()
		conn.SetReadLimit(1 << 20)
		conn.SetReadDeadline(time.Now().Add(40 * time.Second))
		conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		id := strconv.FormatInt(time.Now().UnixNano(), 10)
		if e = conn.WriteJSON(map[string]string{"cmd": "weverse_panel_" + input.Action, "requestId": id}); e != nil {
			fail(fmt.Errorf("浏览器请求失败"))
			return
		}
		for {
			var v struct {
				Type      string          `json:"type"`
				RequestID string          `json:"requestId"`
				Error     string          `json:"error"`
				Session   weverse.Session `json:"session"`
			}
			if e = conn.ReadJSON(&v); e != nil {
				fail(fmt.Errorf("浏览器响应超时"))
				return
			}
			if v.Type != "weverse_panel_result" || v.RequestID != id {
				continue
			}
			if v.Error != "" {
				fail(fmt.Errorf("%s", v.Error))
				return
			}
			if input.Action == "sync" {
				if e = weverse.ImportSession(dir, v.Session); e != nil {
					fail(e)
					return
				}
			}
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
	default:
		writeJSON(w, 404, apiError{Error: "接口不存在"})
	}
}
