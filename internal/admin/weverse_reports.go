package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logic"
	"pocket48-bot/internal/weverse"
	"strconv"
	"sync"
	"time"
)

var weverseBackfillMu sync.Mutex

func (s *Server) handleWeverseReports(w http.ResponseWriter, r *http.Request) {
	dir := weverse.Dir(s.opts.ConfigPath)
	fail := func(e error) { writeJSON(w, 400, apiError{Error: e.Error()}) }
	settings, e := weverse.LoadReportSettings(dir)
	if e != nil {
		fail(e)
		return
	}
	if r.URL.Path == "/api/weverse/reports" {
		if r.Method == http.MethodPut {
			if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&settings); e != nil {
				fail(e)
				return
			}
			if e = weverse.SaveReportSettings(dir, settings); e != nil {
				fail(e)
				return
			}
			settings, e = weverse.LoadReportSettings(dir)
			if e != nil {
				fail(e)
				return
			}
		} else if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		cfg, e := config.LoadConfig(s.opts.ConfigPath)
		if e != nil {
			fail(e)
			return
		}
		var state map[string]any
		_ = weverse.Read(dir, "report-state.json", &state)
		var progress weverse.BackfillProgress
		_ = weverse.Read(dir, "report-backfill.json", &progress)
		if last, e := time.Parse(time.RFC3339, progress.UpdatedAt); e == nil && time.Since(last) > 3*time.Minute && progress.Running {
			progress.Running = false
			progress.Error = "历史回采中断，可重新回采"
		}
		h, e := weverse.OpenHistory(dir)
		if e != nil {
			fail(e)
			return
		}
		started, _ := h.RecordingStarted()
		members, _ := h.Members(settings.CommunityID)
		h.Close()
		writeJSON(w, 200, map[string]any{"settings": settings, "emailTo": cfg.AlertEmailTo, "emailEnabled": cfg.AlertEmailEnabled, "state": state, "backfill": progress, "recordingStarted": started, "memberCount": len(members)})
		return
	}
	year, _ := strconv.Atoi(r.URL.Query().Get("year"))
	month, _ := strconv.Atoi(r.URL.Query().Get("month"))
	var period weverse.ReportPeriod
	if r.URL.Query().Get("kind") == "weekly" {
		period, e = weverse.NewWeeklyReportPeriod(r.URL.Query().Get("date"))
	} else {
		period, e = weverse.NewReportPeriod(r.URL.Query().Get("kind"), year, month)
	}
	if e != nil {
		fail(e)
		return
	}
	if r.URL.Path == "/api/weverse/reports/backfill" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if !weverseBackfillMu.TryLock() {
			writeJSON(w, 409, apiError{Error: "已有历史回采任务正在运行"})
			return
		}
		monitor, e := weverse.LoadSettings(dir)
		if e != nil {
			weverseBackfillMu.Unlock()
			fail(e)
			return
		}
		slug := ""
		for _, sub := range monitor.Subscriptions {
			if sub.CommunityID == settings.CommunityID {
				slug = sub.Slug
				break
			}
		}
		if slug == "" {
			weverseBackfillMu.Unlock()
			fail(fmt.Errorf("请先订阅报表目标团体"))
			return
		}
		progress := weverse.BackfillProgress{Running: true, Period: period.Key, UpdatedAt: time.Now().Format(time.RFC3339)}
		if e = weverse.Write(dir, "report-backfill.json", progress); e != nil {
			weverseBackfillMu.Unlock()
			fail(e)
			return
		}
		go func() {
			defer weverseBackfillMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			c := weverse.NewClient(dir, monitor.ProxyURL)
			defer c.HTTP.CloseIdleConnections()
			h, e := weverse.OpenHistory(dir)
			if e == nil {
				defer h.Close()
				e = c.BackfillReport(ctx, h, settings, period, slug, func(p weverse.BackfillProgress) { progress = p; _ = weverse.Write(dir, "report-backfill.json", p) })
			}
			progress.Running = false
			progress.UpdatedAt = time.Now().Format(time.RFC3339)
			if e != nil {
				progress.Error = e.Error()
			}
			_ = weverse.Write(dir, "report-backfill.json", progress)
		}()
		writeJSON(w, 202, map[string]any{"accepted": true})
		return
	}
	if r.URL.Path == "/api/weverse/reports/send" {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
	} else if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	h, e := weverse.OpenHistory(dir)
	if e != nil {
		fail(e)
		return
	}
	report, e := h.BuildReport(settings, period)
	h.Close()
	if e != nil {
		fail(e)
		return
	}
	if len(report.Members) == 0 {
		fail(fmt.Errorf("尚未采集成员名单，请先开启监控或历史回采"))
		return
	}
	switch r.URL.Path {
	case "/api/weverse/reports/preview":
		writeJSON(w, 200, report)
	case "/api/weverse/reports/download":
		data, e := report.XLSX()
		if e != nil {
			fail(e)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="weverse-`+period.Key+`.xlsx"`)
		_, _ = w.Write(data)
	case "/api/weverse/reports/send":
		cfg, e := config.LoadConfig(s.opts.ConfigPath)
		if e == nil {
			e = logic.SendWeverseReport(cfg, report)
		}
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"sent": true, "emailTo": cfg.AlertEmailTo})
	default:
		http.NotFound(w, r)
	}
}
