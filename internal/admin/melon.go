package admin

import (
	"fmt"
	"net/http"
	"pocket48-bot/internal/melon"
	"time"
)

func (s *Server) handleMelon(w http.ResponseWriter, r *http.Request) {
	dir := melon.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	settings, err := melon.LoadSettings(dir)
	if err != nil {
		fail(fmt.Errorf("无法读取 Melon 配置"))
		return
	}
	client := melon.Client{ProxyURL: settings.ProxyURL}
	switch r.URL.Path {
	case "/api/melon/settings":
		if r.Method == http.MethodGet {
			var status melon.Status
			var musicWaveStatus melon.Status
			_ = melon.Read(dir, "status.json", &status)
			_ = melon.Read(dir, "music-wave-status.json", &musicWaveStatus)
			writeJSON(w, 200, map[string]any{"settings": settings, "status": status, "musicWaveStatus": musicWaveStatus})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var next melon.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		if err := melon.ValidateSettings(next); err != nil {
			fail(err)
			return
		}
		artists := map[string]melon.Artist{}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			artist, found := artists[sub.ArtistID]
			if !found {
				for _, old := range settings.Subscriptions {
					if old.ID == sub.ID && old.ArtistID == sub.ArtistID && old.ArtistName != "" {
						artist = melon.Artist{ID: old.ArtistID, Name: old.ArtistName}
						found = true
						break
					}
				}
			}
			if !found {
				artist, err = client.Lookup(r.Context(), sub.ArtistID)
				if err != nil {
					fail(err)
					return
				}
				artists[sub.ArtistID] = artist
			}
			sub.ArtistName = artist.Name
		}
		if err := melon.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"settings": next})
	case "/api/melon/search":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		query := r.URL.Query().Get("q")
		if query == "" || query == "Hearts2Hearts" || query == "H2H" || query == "하츠투하츠" {
			query = melon.Hearts2HeartsArtistID
		}
		artist, err := client.Lookup(r.Context(), query)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"artists": []melon.Artist{artist}})
	case "/api/melon/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			ArtistID string `json:"artistId"`
		}
		if err := decodeJSON(r, &input); err != nil {
			fail(err)
			return
		}
		events, err := client.Timeline(r.Context(), input.ArtistID)
		if err != nil {
			fail(err)
			return
		}
		musicWave, waveErr := client.MusicWave(r.Context(), input.ArtistID)
		if waveErr != nil {
			fail(waveErr)
			return
		}
		events = append(musicWave, events...)
		writeJSON(w, 200, map[string]any{"events": events, "message": "只读测试完成，未发送 QQ 消息"})
	default:
		writeJSON(w, 404, apiError{Error: "接口不存在"})
	}
}

func melonService(configPath string, now time.Time) *serviceState {
	settings, err := melon.LoadSettings(melon.Dir(configPath))
	card := &serviceState{ID: "melon", Name: "Melon", Subtitle: "Music Wave、Artist Note 与艺人动态", Status: "attention", StatusText: "未启用", Uptime: "—", Detail: fmt.Sprintf("聊天 %d 秒 / 动态 %d 秒", settings.MusicWavePollSeconds, settings.PollSeconds), LastEvent: "可在配置页启用 Melon 监控", LastTime: "—"}
	if err != nil {
		card.Status = "down"
		card.StatusText = "配置异常"
		card.LastEvent = "无法读取 Melon 配置"
		return card
	}
	if !settings.Enabled {
		return card
	}
	count := 0
	waveEnabled := false
	for _, sub := range settings.Subscriptions {
		if sub.Enabled {
			count++
			waveEnabled = waveEnabled || sub.MusicWave
		}
	}
	if count == 0 {
		card.StatusText = "待配置"
		card.LastEvent = "没有启用的 Melon 订阅"
		return card
	}
	var status melon.Status
	if melon.Read(melon.Dir(configPath), "status.json", &status) != nil {
		card.Status = "down"
		card.StatusText = "状态异常"
		card.LastEvent = "无法读取扫描状态"
		return card
	}
	checked, err := time.Parse(time.RFC3339, status.LastCheck)
	if err != nil {
		card.StatusText = "检查中"
		card.LastEvent = "等待首次扫描"
		return card
	}
	card.LastTime = checked.Local().Format("15:04:05")
	if status.Error != "" {
		card.Status = "down"
		card.StatusText = "扫描异常"
		card.LastEvent = status.Error
		return card
	}
	if now.Sub(checked) > time.Duration(settings.PollSeconds)*time.Second+4*time.Minute {
		card.Status = "down"
		card.StatusText = "扫描超时"
		card.LastEvent = "长时间没有完成扫描，请检查主控服务"
		return card
	}
	waveSummary := ""
	if waveEnabled {
		var wave melon.Status
		if melon.Read(melon.Dir(configPath), "music-wave-status.json", &wave) != nil {
			card.Status = "down"
			card.StatusText = "聊天状态异常"
			card.LastEvent = "无法读取 Music Wave 扫描状态"
			return card
		}
		waveChecked, waveErr := time.Parse(time.RFC3339, wave.LastCheck)
		if waveErr != nil {
			card.StatusText = "检查中"
			card.LastEvent = "等待 Music Wave 首次扫描"
			return card
		}
		if waveChecked.After(checked) {
			card.LastTime = waveChecked.Local().Format("15:04:05")
		}
		if wave.Error != "" {
			card.Status = "down"
			card.StatusText = "聊天扫描异常"
			card.LastEvent = wave.Error
			return card
		}
		if now.Sub(waveChecked) > time.Duration(settings.MusicWavePollSeconds)*time.Second+2*time.Minute {
			card.Status = "down"
			card.StatusText = "聊天扫描超时"
			card.LastEvent = "Music Wave 长时间没有完成扫描，请检查主控服务"
			return card
		}
		waveSummary = fmt.Sprintf("；Music Wave %d 条，新消息 %d 条", wave.Events, wave.Forwarded)
	}
	card.Status = "healthy"
	card.StatusText = "运行中"
	card.LastEvent = fmt.Sprintf("扫描完成：%d 条内容，%d 个订阅；新内容 %d 条已入推送队列%s", status.Events, count, status.Forwarded, waveSummary)
	return card
}
