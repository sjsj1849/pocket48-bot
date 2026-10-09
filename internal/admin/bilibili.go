package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pocket48-bot/internal/bilibili"
)

// syncBilibiliMasterSwitch mirrors BILIBILI_ENABLED from config.json into the B 站
// settings file. The poll loop re-reads settings every cycle, so flipping the
// master switch takes effect without restarting the bot.
func syncBilibiliMasterSwitch(configPath string) {
	raw := map[string]json.RawMessage{}
	if err := readJSONFile(configPath, &raw); err != nil {
		return
	}
	enabled := false
	if encoded, ok := raw["BILIBILI_ENABLED"]; ok {
		_ = json.Unmarshal(encoded, &enabled)
	}
	dir := bilibili.Dir(configPath)
	cfg, err := bilibili.LoadSettings(dir)
	if err != nil || cfg.Enabled == enabled {
		return
	}
	cfg.Enabled = enabled
	_ = bilibili.SaveSettings(dir, cfg)
}

func (s *Server) handleBilibili(w http.ResponseWriter, r *http.Request) {
	dir := bilibili.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	cfg, err := bilibili.LoadSettings(dir)
	if err != nil {
		fail(fmt.Errorf("无法读取 B 站配置"))
		return
	}
	client := &bilibili.Client{Dir: dir, Cookie: cfg.Cookie}
	switch r.URL.Path {
	case "/api/bilibili/settings":
		if r.Method == http.MethodGet {
			var status bilibili.Status
			_ = bilibili.Read(dir, "status.json", &status)
			// Cookie 是敏感凭据，只回传"是否已配置"，不回传内容。
			safe := cfg
			safe.Cookie = ""
			writeJSON(w, http.StatusOK, map[string]any{"settings": safe, "cookieConfigured": strings.TrimSpace(cfg.Cookie) != "", "status": status})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var next bilibili.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		// 留空表示保留原有 Cookie，避免保存运行设置时把凭据清掉。
		if strings.TrimSpace(next.Cookie) == "" {
			next.Cookie = cfg.Cookie
		}
		if err := bilibili.ValidateSettings(next); err != nil {
			fail(err)
			return
		}
		// 名称在服务端解析；已有订阅直接复用，避免保存开关时多打 B 站接口。
		cache := map[string]bilibili.Up{}
		for _, old := range cfg.Subscriptions {
			if old.Name != "" {
				cache[old.UID] = bilibili.Up{UID: old.UID, Name: old.Name, Avatar: old.Avatar}
			}
		}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			up, found := cache[sub.UID]
			if !found {
				up, err = client.User(r.Context(), sub.UID)
				if err != nil {
					fail(err)
					return
				}
				cache[sub.UID] = up
			}
			sub.Name = up.Name
			sub.Avatar = up.Avatar
		}
		if err := bilibili.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		safe := next
		safe.Cookie = ""
		writeJSON(w, http.StatusOK, map[string]any{"settings": safe})
	case "/api/bilibili/search":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		uid, e := bilibili.ResolveUID(r.URL.Query().Get("q"))
		if e != nil {
			fail(e)
			return
		}
		up, e := client.User(r.Context(), uid)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ups": []bilibili.Up{up}})
	case "/api/bilibili/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			UID string `json:"uid"`
			// MinVideoSeconds 与实际推送保持一致：前端按当前订阅的阈值传，
			// 否则「只读测试」会把被过滤掉的短视频也列出来，看着像没生效。
			MinVideoSeconds int `json:"minVideoSeconds"`
		}
		if e := decodeJSON(r, &input); e != nil {
			fail(e)
			return
		}
		uid, e := bilibili.ResolveUID(input.UID)
		if e != nil {
			fail(e)
			return
		}
		var items []bilibili.Dynamic
		var problems []string
		for name, fetch := range map[string]func() ([]bilibili.Dynamic, error){
			"视频投稿": func() ([]bilibili.Dynamic, error) {
				// 优先合集接口：匿名可用且带时长/标题。
				threshold := input.MinVideoSeconds
				if threshold < 0 {
					threshold = 0
				}
				if items, err := client.SpaceSeasonVideos(r.Context(), uid, threshold); err == nil && len(items) > 0 {
					return items, nil
				}
				return client.SpaceVideos(r.Context(), uid)
			},
			"图文动态": func() ([]bilibili.Dynamic, error) { return client.SpaceOpus(r.Context(), uid) },
			"专栏":   func() ([]bilibili.Dynamic, error) { return client.SpaceArticles(r.Context(), uid) },
		} {
			found, err := fetch()
			if err != nil {
				problems = append(problems, name+"："+err.Error())
				continue
			}
			items = append(items, found...)
		}
		if len(items) == 0 && len(problems) > 0 {
			fail(fmt.Errorf("拉取失败：%s", strings.Join(problems, "；")))
			return
		}
		if len(items) > 15 {
			items = items[:15]
		}
		message := "只读测试完成，未发送任何消息"
		if len(problems) > 0 {
			message += "；部分来源失败（" + strings.Join(problems, "；") + "）"
		}
		writeJSON(w, http.StatusOK, map[string]any{"dynamics": items, "message": message})
	default:
		writeJSON(w, http.StatusNotFound, apiError{Error: "接口不存在"})
	}
}

func bilibiliService(configPath string, now time.Time) *serviceState {
	dir := bilibili.Dir(configPath)
	cfg, err := bilibili.LoadSettings(dir)
	card := &serviceState{
		ID: "bilibili", Name: "B站", Subtitle: "UP 主动态、投稿与直播间开播/下播",
		Status: "attention", StatusText: "未启用", Uptime: "—",
		Detail:    fmt.Sprintf("动态每 %d 秒 · 投稿每 %d 秒 · 直播每 %d 秒", cfg.PollSeconds, cfg.VideoPollSeconds, cfg.LivePollSeconds),
		LastEvent: "可在配置页启用 B 站监控", LastTime: "—",
	}
	if err != nil {
		card.Status = "down"
		card.StatusText = "配置异常"
		card.LastEvent = "无法读取 B 站配置"
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
		card.LastEvent = "没有启用的 B 站订阅"
		return card
	}
	var status bilibili.Status
	if bilibili.Read(dir, "status.json", &status) != nil {
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
		if strings.Contains(status.Error, "频率") || strings.Contains(status.Error, "412") || strings.Contains(status.Error, "-799") {
			card.Status = "attention"
			card.StatusText = "被B站限流"
		}
		card.LastEvent = status.Error
		return card
	}
	grace := time.Duration(cfg.VideoPollSeconds+4*60) * time.Second
	if now.Sub(checked) > grace {
		card.Status = "down"
		card.StatusText = "扫描超时"
		card.LastEvent = "长时间没有完成扫描，请检查主控服务"
		return card
	}
	if status.LastCheck == status.LastSuccess {
		card.Status = "healthy"
		card.StatusText = "运行中"
	}
	card.LastEvent = fmt.Sprintf("扫描完成：%d 条内容，%d 个订阅；新内容 %d 条已入推送队列", status.Events, count, status.Forwarded)
	return card
}
