package admin

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/signstat"
)

// handleWeiboSignMonitor 给面板提供超话签到监测数据。
//
// GET /api/weibo/sign-monitor?hours=48
// GET /api/weibo/sign-monitor?date=2026-10-07
//
// 返回 {series, groups(分析结果), anomalies, hourly, config, settings, …}
//
// ★ admin 与 bot 是两个进程，面板拿不到 bot 内存里的 store，
//
//	所以这里直接读同一个 jsonl（storage/weibo/sign-monitor.jsonl）。
//
// ★★ 2026-10-08：所有统计口径都走 internal/signstat，
//
//	不再在本文件里重算一遍。历史上这里有一套 30 分钟窗口判据、
//	bot 有一套单步偏离判据、前端又有一套硬编码 400，
//	三者结论不一致（面板看到的异常 ≠ 邮件发出的异常）。
//	现在三处共用同一个包，阈值也统一从配置读。
func (s *Server) handleWeiboSignMonitor(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getWeiboSignMonitor(w, r)
	case http.MethodPost:
		s.updateWeiboSignMonitor(w, r)
	default:
		methodNotAllowed(w)
	}
}

// signMonitorPayload 是 POST 面板提交的配置。
//
// ★ 只在这里出现的键才允许被改；不在这里的键（groups 等）一律原样保留。
type signMonitorPayload struct {
	Enabled         *bool `json:"enabled"`
	IntervalMinutes *int  `json:"intervalMinutes"`
	SpikeAbsolute   *int  `json:"spikeAbsolute"`

	DailyReportEnabled   *bool   `json:"dailyReportEnabled"`
	DailyReportHour      *int    `json:"dailyReportHour"`
	DailyReportMinute    *int    `json:"dailyReportMinute"`
	AnomalyReportEnabled *bool   `json:"anomalyReportEnabled"`
	EmailReportEnabled   *bool   `json:"emailReportEnabled"`
	EmailTo              *string `json:"emailTo"`

	CrossoverSuspectRatio *float64 `json:"crossoverSuspectRatio"`
	CrossoverSuspectRate  *float64 `json:"crossoverSuspectRate"`

	ImageTargets *[]string `json:"imageTargets"`
	AlertTargets *[]string `json:"alertTargets"`
}

// updateWeiboSignMonitor 保存面板配置。
//
// ★ 必须按键合并写 config.json（读 map → 改指定键 → 写回）：
//
//	整体覆盖会把「仅面板维护的键」悄悄删掉，平台开关会莫名变回未启用。
func (s *Server) updateWeiboSignMonitor(w http.ResponseWriter, r *http.Request) {
	var payload signMonitorPayload
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请求体解析失败"})
		return
	}
	if payload.IntervalMinutes != nil && (*payload.IntervalMinutes < 1 || *payload.IntervalMinutes > 360) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "采样间隔需在 1~ 360 分钟之间"})
		return
	}
	if payload.SpikeAbsolute != nil && (*payload.SpikeAbsolute < 10 || *payload.SpikeAbsolute > 100000) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "异常阈值需在 10 ~ 100000 人之间"})
		return
	}
	if payload.CrossoverSuspectRatio != nil && (*payload.CrossoverSuspectRatio < 1 || *payload.CrossoverSuspectRatio > 100) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "破万疑似倍数需在 1 ~ 100 之间"})
		return
	}
	if payload.CrossoverSuspectRate != nil && (*payload.CrossoverSuspectRate < 50 || *payload.CrossoverSuspectRate > 100000) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "破万疑似速度需在 50 ~ 100000 人/小时之间"})
		return
	}
	if payload.DailyReportHour != nil && (*payload.DailyReportHour < 0 || *payload.DailyReportHour > 23) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "日报小时需在 0 ~ 23 之间"})
		return
	}
	if payload.DailyReportMinute != nil && (*payload.DailyReportMinute < 0 || *payload.DailyReportMinute > 59) {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "日报分钟需在 0 ~ 59 之间"})
		return
	}

	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	monitor := map[string]interface{}{}
	if blob, ok := raw["WEIBO_SIGN_MONITOR"]; ok {
		_ = json.Unmarshal(blob, &monitor)
	}

	setIf := func(key string, v interface{}, cond bool) {
		if cond {
			monitor[key] = v
		}
	}
	setIf("enabled", *payload.Enabled, payload.Enabled != nil)
	setIf("intervalMinutes", *payload.IntervalMinutes, payload.IntervalMinutes != nil)
	setIf("spikeAbsolute", *payload.SpikeAbsolute, payload.SpikeAbsolute != nil)
	setIf("dailyReportEnabled", *payload.DailyReportEnabled, payload.DailyReportEnabled != nil)
	setIf("dailyReportHour", *payload.DailyReportHour, payload.DailyReportHour != nil)
	setIf("dailyReportMinute", *payload.DailyReportMinute, payload.DailyReportMinute != nil)
	setIf("anomalyReportEnabled", *payload.AnomalyReportEnabled, payload.AnomalyReportEnabled != nil)
	setIf("emailReportEnabled", *payload.EmailReportEnabled, payload.EmailReportEnabled != nil)
	setIf("emailTo", strings.TrimSpace(*payload.EmailTo), payload.EmailTo != nil)
	setIf("crossoverSuspectRatio", *payload.CrossoverSuspectRatio, payload.CrossoverSuspectRatio != nil)
	setIf("crossoverSuspectRate", *payload.CrossoverSuspectRate, payload.CrossoverSuspectRate != nil)
	setIf("imageTargets", *payload.ImageTargets, payload.ImageTargets != nil)
	setIf("alertTargets", *payload.AlertTargets, payload.AlertTargets != nil)

	encoded, err := json.Marshal(monitor)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	raw["WEIBO_SIGN_MONITOR"] = encoded
	if err := writeJSONFile(s.opts.ConfigPath, raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	// 立刻回读，让前端拿到保存后的真实值
	s.getWeiboSignMonitor(w, r)
}

// signMonitorLoc 与 signstat 保持一致的业务时区。
var signMonitorLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

func (s *Server) getWeiboSignMonitor(w http.ResponseWriter, r *http.Request) {
	cfg := s.signMonitorCfg()
	q := r.URL.Query()

	// 时间范围：要么 date=YYYY-MM-DD（看那天全天），要么 hours=N（近 N 小时）。
	//
	// ★ date 优先于 hours。日历选某一天 = 那天 00:00 到当天结束
	//   （若选的是今天，则到当前时刻为止）。
	sinceMS, untilMS := int64(0), int64(0)
	dateStr := q.Get("date")
	rangeMode := "hours"
	rangeLabel := ""
	if dateStr != "" {
		if day, err := time.ParseInLocation("2006-01-02", dateStr, signMonitorLoc); err == nil {
			start := day
			end := day.AddDate(0, 0, 1)
			now := time.Now().In(signMonitorLoc)
			if now.Before(end) {
				end = now // 今天就看到现在
			}
			sinceMS = start.UnixMilli()
			untilMS = end.UnixMilli()
			rangeMode = "date"
			rangeLabel = start.Format("2006-01-02")
		}
	}
	if sinceMS == 0 {
		hours := 48
		if v := q.Get("hours"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 24*14 {
				hours = n
			}
		}
		sinceMS = time.Now().Add(-time.Duration(hours) * time.Hour).UnixMilli()
		untilMS = 0
		rangeLabel = "近 " + strconv.Itoa(hours) + " 小时"
	}

	// 存储布局：<root>/config.json + <root>/storage/…（storage 是 config 的子目录）。
	// ★ 只上跳一级 —— 多上跳一级会读到 /root/storage，读不到数据（series 全空）。
	path := filepath.Join(filepath.Dir(s.opts.ConfigPath), "storage", "weibo", "sign-monitor.jsonl")
	samples, err := readSignMonitorSamples(path, sinceMS, untilMS)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	// ★ 全部统计走 signstat —— 与 bot 告警、邮件报表同一份实现。
	res := signstat.AnalyzeSamples(samples, signMonitorGroupsOf(cfg), signstat.Options{
		Threshold:        cfg.SpikeAbs(),
		SuspectRatio:     cfg.CrossoverSuspect(),
		FuzzyRatePerHour: cfg.CrossoverFuzzyRate(),
	})

	// 异常时段明细：每条异常往前追 detailWindow 分钟（默认 60，用户 2026-10-08
	// 明确要「一小时」而不是半小时），异常时刻在最后一列。
	//
	// ★ 与推送 PNG 共用 signstat.WindowDetail —— 面板上看到的数字
	//   必须和发到飞书/QQ/邮箱那张 PNG 上的数字**完全一致**。
	detailMin := 60
	if v := q.Get("detailWindow"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 15 && n <= 360 {
			detailMin = n
		}
	}
	groups := signMonitorGroupsOf(cfg)
	details := make([]signstat.DetailWindow, 0, len(res.Anomalies))
	for _, a := range res.Anomalies {
		dw := signstat.WindowDetail(samples, groups, a.ToTS-int64(detailMin)*60*1000, a.ToTS)
		dw.OID = a.OID
		dw.Name = a.Name
		details = append(details, dw)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"series":       res.Series,
		"analysis":     res.Groups,
		"anomalies":    res.Anomalies,
		"hourly":       signstat.Hourly(res.Series, signMonitorLoc),
		"details":      details,
		"detailWindow": detailMin,

		"range": map[string]interface{}{
			"mode": rangeMode, "label": rangeLabel,
			"since": sinceMS, "until": untilMS,
		},
		// 旧字段保留兼容，前端改造完成后可删
		"hours":  int((time.Now().UnixMilli() - sinceMS) / 3600000),
		"spikes": legacySpikes(res.Anomalies),
		"config": s.signMonitorSettings(),
		"groups": s.signMonitorGroups(),
	})
}

// legacySpikes 把新结构投影成旧的 Spike 形状，给还没改完的前端用。
func legacySpikes(list []signstat.Anomaly) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(list))
	for _, a := range list {
		w := int((a.ToTS - a.FromTS) / 60000)
		if w <= 0 {
			w = 5
		}
		out = append(out, map[string]interface{}{
			"oid": a.OID, "name": a.Name,
			"from": a.From, "to": a.To, "delta": a.TotalDelta,
			"windowMinutes": w, "at": a.FromTS,
			"peakDelta": a.PeakDelta, "steps": a.Steps,
			"severity": string(a.Severity), "reason": a.Reason,
		})
	}
	return out
}

type signMonitorGroupResp struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// signMonitorGroups 读配置里的分组（members 是 oid）。
func (s *Server) signMonitorGroups() []signMonitorGroupResp {
	out := []signMonitorGroupResp{}
	for _, g := range s.signMonitorCfg().Groups {
		out = append(out, signMonitorGroupResp{Name: g.Name, Members: g.Members})
	}
	return out
}

// signMonitorGroupsOf 转成 signstat 的分组类型。
func signMonitorGroupsOf(cfg config.WeiboSignMonitorConfig) []signstat.Group {
	out := make([]signstat.Group, 0, len(cfg.Groups))
	for _, g := range cfg.Groups {
		out = append(out, signstat.Group{Name: g.Name, Members: g.Members})
	}
	return out
}

// signMonitorSettings 读 config.json 里的监测参数（给面板表单用）。
type signMonitorSettings struct {
	Enabled         bool    `json:"enabled"`
	IntervalMinutes int     `json:"intervalMinutes"`
	WindowMinutes   int     `json:"windowMinutes"`
	SpikeAbsolute   int     `json:"spikeAbsolute"`
	SpikeRatio      float64 `json:"spikeRatio"`
	RetentionHours  int     `json:"retentionHours"`
	GroupKey        string  `json:"groupKey"`

	DailyReportEnabled    bool     `json:"dailyReportEnabled"`
	DailyReportHour       int      `json:"dailyReportHour"`
	DailyReportMinute     int      `json:"dailyReportMinute"`
	AnomalyReportEnabled  bool     `json:"anomalyReportEnabled"`
	EmailReportEnabled    bool     `json:"emailReportEnabled"`
	EmailTo               string   `json:"emailTo"`
	CrossoverSuspectRatio float64  `json:"crossoverSuspectRatio"`
	CrossoverSuspectRate  float64  `json:"crossoverSuspectRate"`
	ImageTargets          []string `json:"imageTargets"`
	AlertTargets          []string `json:"alertTargets"`
}

// signMonitorCfg 读 config.json 里的完整监测配置。
func (s *Server) signMonitorCfg() config.WeiboSignMonitorConfig {
	var cfg config.WeiboSignMonitorConfig
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		return cfg
	}
	if blob, ok := raw["WEIBO_SIGN_MONITOR"]; ok {
		_ = json.Unmarshal(blob, &cfg)
	}
	return cfg
}

// signMonitorSettings 读 config.json 里的监测参数。
func (s *Server) signMonitorSettings() signMonitorSettings {
	cfg := s.signMonitorCfg()
	h, m := cfg.DailyReportAt()
	return signMonitorSettings{
		Enabled:         cfg.Enabled,
		IntervalMinutes: int(cfg.Interval().Minutes()),
		WindowMinutes:   cfg.SpikeWindow(),
		SpikeAbsolute:   cfg.SpikeAbs(),
		SpikeRatio:      cfg.SpikeRate(),
		RetentionHours:  int(cfg.Retention().Hours()),
		GroupKey:        cfg.GroupKey,

		DailyReportEnabled:    cfg.DailyReportEnabled,
		DailyReportHour:       h,
		DailyReportMinute:     m,
		AnomalyReportEnabled:  cfg.AnomalyReportEnabled || cfg.SpikeReportEnabled,
		EmailReportEnabled:    cfg.EmailReportEnabled,
		EmailTo:               cfg.EmailTo,
		CrossoverSuspectRatio: cfg.CrossoverSuspect(),
		CrossoverSuspectRate:  cfg.CrossoverFuzzyRate(),
		ImageTargets:          cfg.ImageTargets,
		AlertTargets:          cfg.AlertTargets,
	}
}

// readSignMonitorSamples 读 jsonl 采样。
//
// ★ jsonl 是「一连串顶层 JSON 对象」，没有数组包裹。
//
//	顶层调用 Decoder.More() 行为未定义（实测直接返回空 ⇒ 面板永远没数据），
//	正确做法是循环 Decode 直到 io.EOF。
//
// untilMS > 0 时按它截断（看某一天用）。
func readSignMonitorSamples(path string, sinceMS, untilMS int64) ([]signstat.Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []signstat.Sample{}, nil
		}
		return nil, err
	}
	defer file.Close()

	out := []signstat.Sample{}
	dec := json.NewDecoder(file)
	for {
		var s signstat.Sample
		if err := dec.Decode(&s); err != nil {
			break // EOF，或写了一半的末行：保留已解析的部分
		}
		if s.TS < sinceMS {
			continue
		}
		if untilMS > 0 && s.TS >= untilMS {
			continue
		}
		if s.Sign <= 0 {
			continue
		}
		out = append(out, s)
	}
	// 按时间排序，保证 BuildSeries 稳定
	sort.Slice(out, func(i, j int) bool { return out[i].TS < out[j].TS })
	return out, nil
}
