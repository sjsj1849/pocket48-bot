package admin

import (
	"errors"
	"fmt"
	"net/http"
	"pocket48-bot/internal/xmonitor"
	"time"
)

func (s *Server) handleX(w http.ResponseWriter, r *http.Request) {
	dir := xmonitor.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	cfg, err := xmonitor.LoadSettings(dir)
	if err != nil {
		fail(fmt.Errorf("无法读取 X 配置"))
		return
	}
	client := xmonitor.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
	switch r.URL.Path {
	case "/api/x/settings":
		if r.Method == http.MethodGet {
			var session xBrowserSession
			_ = xmonitor.Read(dir, "session.json", &session)
			var status xmonitor.Status
			_ = xmonitor.Read(dir, "status.json", &status)
			writeJSON(w, 200, map[string]any{"settings": cfg, "sessionConfigured": validXBrowserSession(session), "status": status})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var next xmonitor.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		// Validate before network calls, then resolve stable user IDs server-side.
		if err := xmonitor.ValidateSettings(next); err != nil {
			fail(err)
			return
		}
		users := map[string]xmonitor.User{}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			name, _ := xmonitor.Username(sub.Username)
			user, found := users[name]
			if !found {
				for _, old := range cfg.Subscriptions {
					if old.ID == sub.ID && old.Username == name && old.UserID != "" && old.UserID == sub.UserID {
						user = xmonitor.User{ID: old.UserID, Username: old.Username, Name: old.Name}
						found = true
						break
					}
				}
			}
			if !found {
				var e error
				user, e = client.Lookup(r.Context(), name)
				if e != nil {
					fail(e)
					return
				}
				users[name] = user
			}
			sub.Username = user.Username
			sub.UserID = user.ID
			sub.Name = user.Name
		}
		if err := xmonitor.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"settings": next})
	case "/api/x/search":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		q := r.URL.Query().Get("q")
		if name, e := xmonitor.Username(q); e == nil {
			user, e := client.Lookup(r.Context(), name)
			if e == nil {
				writeJSON(w, 200, map[string]any{"users": []xmonitor.User{user}})
				return
			}
			var ce *xmonitor.Error
			if !errors.As(e, &ce) || ce.Code != "user_unavailable" {
				fail(e)
				return
			}
		}
		if len(q) == 0 || len(q) > 100 {
			fail(fmt.Errorf("请输入用户名、主页链接或昵称（最多 100 字节）"))
			return
		}
		result, e := client.Call(r.Context(), map[string]any{"operation": "search", "query": q, "limit": 10})
		if e != nil {
			fail(e)
			return
		}
		if result.Users == nil {
			result.Users = []xmonitor.User{}
		}
		writeJSON(w, 200, map[string]any{"users": result.Users})
	case "/api/x/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Username string `json:"username"`
		}
		if e := decodeJSON(r, &input); e != nil {
			fail(e)
			return
		}
		user, e := client.Lookup(r.Context(), input.Username)
		if e != nil {
			fail(e)
			return
		}
		events, e := client.Timeline(r.Context(), user.ID, 10)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"events": events, "message": "只读测试完成，未发送 QQ 消息"})
	default:
		writeJSON(w, 404, apiError{Error: "接口不存在"})
	}
}

func xService(configPath string, now time.Time) *serviceState {
	cfg, err := xmonitor.LoadSettings(xmonitor.Dir(configPath))
	card := &serviceState{ID: "x", Name: "X", Subtitle: "指定账号帖子、图片与视频", Status: "attention", StatusText: "未启用", Uptime: "—", Detail: fmt.Sprintf("每 %d 秒扫描", cfg.PollSeconds), LastEvent: "可在配置页启用 X 监控", LastTime: "—"}
	if err != nil {
		card.Status = "down"
		card.StatusText = "配置异常"
		card.LastEvent = "无法读取 X 配置"
		return card
	}
	if !cfg.Enabled {
		return card
	}
	count := 0
	for _, sub := range cfg.Subscriptions {
		if sub.Enabled {
			count++
		}
	}
	if count == 0 {
		card.StatusText = "待配置"
		card.LastEvent = "没有启用的 X 订阅"
		return card
	}
	card.StatusText = "检查中"
	card.LastEvent = "等待首次扫描"
	var status xmonitor.Status
	if xmonitor.Read(xmonitor.Dir(configPath), "status.json", &status) != nil {
		card.Status = "down"
		card.StatusText = "状态异常"
		card.LastEvent = "无法读取扫描状态"
		return card
	}
	checked, e := time.Parse(time.RFC3339, status.LastCheck)
	if e != nil {
		return card
	}
	card.LastTime = checked.Local().Format("15:04:05")
	if status.Error != "" {
		card.Status = "down"
		card.StatusText = "扫描异常"
		card.LastEvent = status.Error
		return card
	}
	if now.Sub(checked) > time.Duration(cfg.PollSeconds)*time.Second+4*time.Minute {
		card.Status = "down"
		card.StatusText = "扫描超时"
		card.LastEvent = "长时间没有完成扫描，请检查主控服务"
		return card
	}
	if status.LastCheck == status.LastSuccess {
		card.Status = "healthy"
		card.StatusText = "运行中"
	}
	card.LastEvent = fmt.Sprintf("扫描完成：%d 条帖子，%d 个订阅；新内容 %d 条已入推送队列", status.Events, count, status.Forwarded)
	return card
}
