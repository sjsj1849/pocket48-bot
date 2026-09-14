package admin

import (
	"fmt"
	"net/http"
	"pocket48-bot/internal/instagram"
	"time"
)

func (s *Server) handleInstagram(w http.ResponseWriter, r *http.Request) {
	dir := instagram.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	cfg, err := instagram.LoadSettings(dir)
	if err != nil {
		fail(fmt.Errorf("无法读取 Instagram 配置"))
		return
	}
	client := instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
	switch r.URL.Path {
	case "/api/instagram/settings":
		if r.Method == http.MethodGet {
			var session struct {
				Username string            `json:"username"`
				Cookies  map[string]string `json:"cookies"`
			}
			_ = instagram.Read(dir, "session.json", &session)
			var status instagram.Status
			_ = instagram.Read(dir, "status.json", &status)
			writeJSON(w, 200, map[string]any{"settings": cfg, "sessionConfigured": session.Cookies["sessionid"] != "", "sessionUsername": session.Username, "status": status})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var next instagram.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		// Validate before network calls, then resolve stable user IDs server-side.
		if err := instagram.ValidateSettings(next); err != nil {
			fail(err)
			return
		}
		users := map[string]instagram.User{}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			name, _ := instagram.Username(sub.Username)
			user, found := users[name]
			if !found {
				for _, old := range cfg.Subscriptions {
					if old.ID == sub.ID && old.Username == name && old.UserID != "" && old.UserID == sub.UserID {
						user = instagram.User{ID: old.UserID, Username: old.Username, Name: old.Name}
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
		if err := instagram.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"settings": next})
	case "/api/instagram/session":
		op := "session_check"
		var input struct {
			Cookies  string `json:"cookies"`
			Username string `json:"username"`
			Password string `json:"password"`
			Code     string `json:"code"`
		}
		switch r.Method {
		case http.MethodPost:
			if e := decodeJSON(r, &input); e != nil {
				fail(e)
				return
			}
			op = "session_import"
			if input.Username != "" || input.Password != "" {
				op = "session_login"
			} else if input.Code != "" {
				op = "session_2fa"
			}
		case http.MethodGet:
		case http.MethodDelete:
			op = "session_clear"
		default:
			methodNotAllowed(w)
			return
		}
		result, e := client.Call(r.Context(), map[string]any{"operation": op, "cookies": input.Cookies, "username": input.Username, "password": input.Password, "code": input.Code})
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, result)
	case "/api/instagram/search":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		user, e := client.Lookup(r.Context(), r.URL.Query().Get("q"))
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"users": []instagram.User{user}})
	case "/api/instagram/preview":
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
		events, e := client.Timeline(r.Context(), user.Username, 10)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"events": events, "message": "只读测试完成，未发送 QQ 消息"})
	default:
		writeJSON(w, 404, apiError{Error: "接口不存在"})
	}
}

func instagramService(configPath string, now time.Time) *serviceState {
	cfg, err := instagram.LoadSettings(instagram.Dir(configPath))
	card := &serviceState{ID: "instagram", Name: "Instagram", Subtitle: "指定账号帖子、图片与视频", Status: "attention", StatusText: "未启用", Uptime: "—", Detail: fmt.Sprintf("每 %d 秒扫描", cfg.PollSeconds), LastEvent: "可在配置页启用 Instagram 监控", LastTime: "—"}
	if err != nil {
		card.Status = "down"
		card.StatusText = "配置异常"
		card.LastEvent = "无法读取 Instagram 配置"
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
		card.LastEvent = "没有启用的 Instagram 订阅"
		return card
	}
	card.StatusText = "检查中"
	card.LastEvent = "等待首次扫描"
	var status instagram.Status
	if instagram.Read(instagram.Dir(configPath), "status.json", &status) != nil {
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
		if status.ErrorCode == "login_required" || status.ErrorCode == "invalid_session" {
			card.StatusText = "需登录"
		} else if status.ErrorCode == "rate_limit" {
			card.StatusText = "限流重试中"
		}
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
