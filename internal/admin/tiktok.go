package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pocket48-bot/internal/tiktokmonitor"
)

// TikTok 面板接口（2026-10-04 补齐）。
//
// 为什么之前面板里没有 TikTok：配置寄居在 config.json 的 TIKTOK_SUBSCRIPTIONS，
// 而面板所有平台页都围绕各自的 storage/<平台>/settings.json 构建，
// TikTok 是唯一没有独立配置文件的平台 —— 所以不是前端漏了按钮，
// 是压根没有可读可写的存储。现在补上 internal/tiktokmonitor/store.go，
// 迁移后的口径：
//
//	TIKTOK_ENABLED（config.json，总开关） -> settings.enabled
//	TIKTOK_SUBSCRIPTIONS（config.json）    -> settings.subscriptions（一次性迁移，之后以 settings.json 为准）
//
// 面板只写 settings.json，**不写 config.json 的订阅键** —— 记忆里的教训：
// config.json 混有「Bot 结构体键」与「仅面板维护的键」，整体覆盖会静默删掉后者。

// syncTiktokMasterSwitch mirrors TIKTOK_ENABLED from config.json into the
// TikTok settings file. The monitor re-reads settings every cycle, so flipping
// the master switch takes effect without restarting the bot.
func syncTiktokMasterSwitch(configPath string) {
	raw := map[string]json.RawMessage{}
	if err := readJSONFile(configPath, &raw); err != nil {
		return
	}
	enabled := false
	if encoded, ok := raw["TIKTOK_ENABLED"]; ok {
		_ = json.Unmarshal(encoded, &enabled)
	}
	dir := tiktokmonitor.Dir(configPath)
	cfg, err := tiktokmonitor.LoadSettings(dir)
	if err != nil || cfg.Enabled == enabled {
		return
	}
	cfg.Enabled = enabled
	_ = tiktokmonitor.SaveSettings(dir, cfg)
}

// tiktokAccountStatuses 把 state.json 的游标状态整理成面板可显示的形态。
//
// 数据源是采集侧的 state.json 而非 status.json —— 前者每个账号都有
// lastScan/lastSuccess/failCount/seen 计数，面板判断「哪个账号在正常跑」
// 靠的就是它。
func tiktokAccountStatuses(dir string, cfg tiktokmonitor.Settings) []tiktokmonitor.AccountStatus {
	state := tiktokmonitor.LoadState(dir)
	out := make([]tiktokmonitor.AccountStatus, 0, len(cfg.Subscriptions))
	for _, sub := range cfg.Subscriptions {
		item := tiktokmonitor.AccountStatus{Username: sub.Username, DisplayName: sub.DisplayName}
		if cursor, ok := state.Cursors[strings.ToLower(sub.Username)]; ok {
			item.Ready = cursor.Ready
			item.FailCount = cursor.FailCount
			item.LastError = cursor.LastError
			item.SeenCount = len(cursor.Seen)
			if !cursor.LastScan.IsZero() {
				item.LastScan = cursor.LastScan.Format(time.RFC3339)
			}
			if !cursor.LastSuccess.IsZero() {
				item.LastSuccess = cursor.LastSuccess.Format(time.RFC3339)
			}
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) handleTiktok(w http.ResponseWriter, r *http.Request) {
	dir := tiktokmonitor.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }

	switch r.URL.Path {
	case "/api/tiktok/settings":
		cfg, err := tiktokmonitor.LoadSettings(dir)
		if err != nil {
			// 文件缺失不算错误：TikTok 是后加的平台，首次打开面板必然没有文件。
			cfg = tiktokmonitor.Settings{
				PollSeconds:   tiktokmonitor.DefaultPollSeconds,
				Subscriptions: []tiktokmonitor.Subscription{},
			}
		}
		// 迁移：settings.json 还没有订阅时，从 config.json 的
		// TIKTOK_SUBSCRIPTIONS 导入一次，避免用户重新配一遍投递目标。
		if len(cfg.Subscriptions) == 0 {
			if migrated := migrateTiktokFromConfig(s.opts.ConfigPath, cfg.Enabled); len(migrated) > 0 {
				cfg.Subscriptions = migrated
				_ = tiktokmonitor.SaveSettings(dir, cfg)
			}
		}

		if r.Method == http.MethodGet {
			var status tiktokmonitor.Status
			_ = tiktokmonitor.Read(dir, "status.json", &status)
			writeJSON(w, http.StatusOK, map[string]any{
				"settings": cfg,
				"status":   status,
				"accounts": tiktokAccountStatuses(dir, cfg),
				"limits": map[string]int{
					"min":   tiktokmonitor.MinPollSeconds,
					"max":   tiktokmonitor.MaxPollSeconds,
					"reset": tiktokmonitor.DefaultPollSeconds,
				},
			})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}

		var next tiktokmonitor.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		// 补齐展示名与头像：用户名可以在面板上显示，但空名字很难排查。
		known := map[string]tiktokmonitor.Subscription{}
		for _, old := range cfg.Subscriptions {
			known[strings.ToLower(old.Username)] = old
		}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			old, ok := known[strings.ToLower(sub.Username)]
			if !ok {
				continue
			}
			if sub.DisplayName == "" {
				sub.DisplayName = old.DisplayName
			}
			if sub.Avatar == "" {
				sub.Avatar = old.Avatar
			}
		}
		for i := range next.Subscriptions {
			if next.Subscriptions[i].DisplayName == "" {
				next.Subscriptions[i].DisplayName = next.Subscriptions[i].Username
			}
		}
		if err := tiktokmonitor.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"settings": next})
		return

	case "/api/tiktok/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		name, err := tiktokUsernameFromRequest(r)
		if err != nil {
			fail(err)
			return
		}
		// 真实探针：拉一次作品列表。这会拉起 sidecar 浏览器，约 5-10 秒，
		// 而且会消耗一次 item_list 配额（连请求 5-6 次会限流约 5 分钟），
		// 所以做成显式按钮而不是输入即搜。
		client := tiktokmonitor.Client{Dir: dir}
		videos, err := client.Timeline(r.Context(), name, tiktokmonitor.DefaultLimit)
		if err != nil {
			fail(fmt.Errorf("拉取作品列表失败：%w", err))
			return
		}
		out := make([]map[string]any, 0, len(videos))
		for _, v := range videos {
			out = append(out, map[string]any{
				"id":      v.ID,
				"desc":    v.Desc,
				"cover":   v.Cover,
				"url":     tiktokmonitor.Link(v),
				"time":    v.CreateTime * 1000,
				"seconds": tiktokmonitor.Seconds(v),
				"author":  v.AuthorNick,
				"play":    v.Stats.Play,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"videos":  out,
			"message": fmt.Sprintf("拉取到 %d 条作品（只读，不会推送）", len(out)),
		})
		return
	}

	writeJSON(w, http.StatusNotFound, apiError{Error: "接口不存在"})
}

// migrateTiktokFromConfig 从 config.json 的 TIKTOK_SUBSCRIPTIONS 导入订阅。
//
// 只在 settings.json 还没有订阅时调用一次（幂等）。
// 保留它是因为用户在 TikTok 上线时已经配过投递目标，迁移比让他重配一遍好。
func migrateTiktokFromConfig(configPath string, enabled bool) []tiktokmonitor.Subscription {
	raw := map[string]json.RawMessage{}
	if err := readJSONFile(configPath, &raw); err != nil {
		return nil
	}
	encoded, ok := raw["TIKTOK_SUBSCRIPTIONS"]
	if !ok || len(encoded) == 0 {
		return nil
	}
	legacy := map[string]struct {
		Username    string   `json:"username"`
		DisplayName string   `json:"display_name"`
		TargetIDs   []string `json:"targetIds"`
		Disabled    bool     `json:"disabled"`
	}{}
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return nil
	}
	out := make([]tiktokmonitor.Subscription, 0, len(legacy))
	for account, item := range legacy {
		name := strings.TrimSpace(item.Username)
		if name == "" {
			name = strings.TrimSpace(account)
		}
		clean, err := tiktokmonitor.Username(name)
		if err != nil {
			continue
		}
		display := strings.TrimSpace(item.DisplayName)
		if display == "" {
			display = clean
		}
		out = append(out, tiktokmonitor.Subscription{
			ID:          "tt-" + clean,
			Username:    clean,
			DisplayName: display,
			TargetIDs:   item.TargetIDs,
			Enabled:     enabled && !item.Disabled,
		})
	}
	return out
}

// tiktokUsernameFromRequest 从 query 或 body 里取 username 并规范化。
func tiktokUsernameFromRequest(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("username"))
	if raw == "" && r.Body != nil {
		var body struct {
			Username string `json:"username"`
		}
		if err := decodeJSON(r, &body); err == nil {
			raw = strings.TrimSpace(body.Username)
		}
	}
	return tiktokmonitor.Username(raw)
}

// tiktokService 生成总览页的 TikTok 卡片（2026-10-04 新增）。
//
// 数据源刻意选 state.json 而不是 status.json：TikTok 监控器目前不写
// status.json，它的健康状态全在每个账号的游标里（failCount/lastError/
// lastSuccess）。没有这张卡片时用户在总览页看不到 TikTok 的任何状况。
func tiktokService(configPath string, now time.Time) *serviceState {
	dir := tiktokmonitor.Dir(configPath)
	cfg, err := tiktokmonitor.LoadSettings(dir)
	card := &serviceState{
		ID: "tiktok", Name: "TikTok", Subtitle: "洋抖作品监控",
		Status: "attention", StatusText: "未启用", Uptime: "—",
		Detail:    fmt.Sprintf("每 %d 秒扫描", cfg.PollSeconds),
		LastEvent: "可在配置页启用 TikTok 监控", LastTime: "—",
	}
	if err != nil {
		// 文件缺失是常态（TikTok 是后加的平台），不能当成故障。
		cfg = tiktokmonitor.Settings{PollSeconds: tiktokmonitor.DefaultPollSeconds}
	}
	if !cfg.Enabled {
		return card
	}
	count := len(cfg.Usernames())
	if count == 0 {
		card.StatusText = "待配置"
		card.LastEvent = "没有启用的 TikTok 账号订阅"
		return card
	}

	state := tiktokmonitor.LoadState(dir)
	failing := 0
	var lastSuccess, lastError time.Time
	for _, name := range cfg.Usernames() {
		cursor, ok := state.Cursors[strings.ToLower(name)]
		if !ok {
			failing++
			continue
		}
		// 连续失败 3 次以上才判定异常：单次失败基本是接口限流，属正常现象。
		if cursor.FailCount >= 3 {
			failing++
		}
		if cursor.LastSuccess.After(lastSuccess) {
			lastSuccess = cursor.LastSuccess
		}
		if cursor.FailCount >= 3 && cursor.LastScan.After(lastError) {
			lastError = cursor.LastScan
		}
	}

	card.Detail = fmt.Sprintf("每 %d 秒扫描 · %d 个账号", cfg.PollSeconds, count)
	if !lastSuccess.IsZero() {
		card.LastTime = lastSuccess.Local().Format("15:04:05")
	}

	switch {
	case failing == count:
		card.Status = "down"
		card.StatusText = "全部账号异常"
		card.LastEvent = "所有账号都在连续失败，多半是 TikTok 接口限流；等 5 分钟后自动恢复"
	case failing > 0:
		card.Status = "attention"
		card.StatusText = fmt.Sprintf("%d/%d 个账号异常", failing, count)
		card.LastEvent = "部分账号连续失败，可能撞上了限流窗口（持续约 5 分钟）"
	case lastSuccess.IsZero():
		card.StatusText = "检查中"
		card.LastEvent = "已建立游标，等待首次成功扫描"
	default:
		card.Status = "healthy"
		card.StatusText = "运行正常"
		age := now.Sub(lastSuccess)
		card.LastEvent = fmt.Sprintf("最近成功扫描 %s", humanDuration(age))
	}
	return card
}
