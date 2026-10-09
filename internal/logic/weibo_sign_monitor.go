// 超话签到监测（2026-10-07 新增）。
//
// 为什么单独做监测：超话日报一天只跑一次，签到人数是**当日累计**，
// 破万后接口只给模糊值（"1万"），要精确值还得消耗每日一次的签到机会。
// 于是「一天之内签到突然多了一截」这件事在日报里完全看不出来 ——
// 而这恰恰是判断数据有没有水分的唯一窗口。
//
// 数据源是超话日报同款的只读接口（fetchSuperCountByOIDViaWeb →
// weibo.com/ajax_proxy/chaohua/page），label_list 里有「今日签到 2330人」
// 这样的**精确整数**，实测 0.6~0.7 秒一次、连打不拦，因此可以分钟级采样。
package logic

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/signstat"
)

// signMonitorFile 是采样明细落盘位置（jsonl，一行一个采样点）。
const signMonitorFile = "sign-monitor.jsonl"

// signAlertsFile 是「已推送过的异常」索引（json，整份重写）。
//
// ★ 必须落盘：检测每轮都拿全量历史重算，靠这个索引去重；
//
//	只在内存里的话，服务一重启就会把昨天那条异常再推一遍。
const signAlertsFile = "sign-alerts.json"

// maxAlertsPerSpike 同一段异常最多推几次。
//
// 连续刷量时新的采样点会被不断合并进同一段（ToTS 一直往后推），
// 每次都补发就会变成每 5 分钟一条 —— 设上限，超出只更新记录不再推送。
const maxAlertsPerSpike = 4

// signMonitorSample 一个采样点。
type signMonitorSample struct {
	TS   int64  `json:"ts"`   // unix 毫秒
	OID  string `json:"oid"`  // 去掉 "1022:" 前缀
	Name string `json:"name"` // 超话名（展示用，缺失时回落 oid）
	Sign int    `json:"sign"` // 今日签到人数（精确值）
}

// signMonitorSpike 一条异常涨幅记录。
type signMonitorSpike struct {
	OID       string `json:"oid"`
	Name      string `json:"name"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	Delta     int    `json:"delta"`
	WindowMin int    `json:"windowMinutes"`
	At        int64  `json:"at"` // 采样时刻
	// 组内判据的上下文（告警文案要用：光说"涨了 1500"没有可比性）
	GroupName      string `json:"groupName,omitempty"`
	GroupMedian    int    `json:"groupMedian,omitempty"`
	GroupThreshold int    `json:"groupThreshold,omitempty"`
	// Reason 说明是「组内离群」还是「绝对阈值」触发。
	Reason string `json:"reason,omitempty"`
	// PeakDelta 本次异常期间最大的单步偏离（判断严重程度）。
	PeakDelta int `json:"peakDelta,omitempty"`
	// PeakTS 峰值所在时刻。
	PeakTS int64 `json:"peakTs,omitempty"`
	// Steps 合并了多少个连续超阈值的采样点。
	//
	// ★ 连续刷量（实测 22:09 +351 / 22:14 +564 / 22:19 +71）会合并成一条，
	//   而不是被拆成三次事件。
	Steps int `json:"steps,omitempty"`
	// Severity: confirmed=确定异常；suspected=疑似（破万者速度异常）。
	Severity string `json:"severity,omitempty"`
	// FromTS / ToTS 异常区间（合并后的整段），推送文本与明细表定位用。
	FromTS int64 `json:"fromTs,omitempty"`
	ToTS   int64 `json:"toTs,omitempty"`
	// TotalDelta 整段累计增量（多步之和）；Delta 与它是同一个值（兼容旧字段）。
	TotalDelta int `json:"totalDelta,omitempty"`
}

// signMonitorStore 是采样存储：内存索引 + jsonl 追加。
type signMonitorStore struct {
	mu      sync.Mutex
	path    string
	maxAge  time.Duration
	samples []signMonitorSample // 按时间升序
	spikes  []signMonitorSpike
	// alerted 记录「oid + 方向」最近一次告警时间，避免同一个尖峰反复推消息。
	alerted map[string]int64
	// alerts 是**落盘**的「已告警过的异常」索引：oid|起始时刻 → 记录。
	//
	// ★★ 没有它就必然「一重启就把历史异常重发一遍」（2026-10-08 用户反馈）：
	//   每轮采样都用全量 samples 重算异常，靠「已经报过」的集合去重；
	//   而这个集合原本只在内存里，重启后是空的 ⇒ 昨天那条异常被整个重算出来，
	//   于是服务一重启就重新推一遍同样的告警。
	alerts     map[string]*signMonitorAlertRecord
	alertsPath string
}

// signMonitorAlertRecord 一条已告警异常的记录（持久化）。
type signMonitorAlertRecord struct {
	Key    string `json:"key"`
	OID    string `json:"oid"`
	Name   string `json:"name"`
	FromTS int64  `json:"fromTs"`
	ToTS   int64  `json:"toTs"`
	Delta  int    `json:"delta"`
	Steps  int    `json:"steps"`
	// AlertCount 这条异常已经推送过几次（含补发）。
	AlertCount int   `json:"alertCount"`
	LastAlert  int64 `json:"lastAlertAt"`
	UpdatedAt  int64 `json:"updatedAt"`
}

func newSignMonitorStore(path string, maxAge time.Duration) *signMonitorStore {
	alertsPath := ""
	if dir := filepath.Dir(path); dir != "" {
		alertsPath = filepath.Join(dir, signAlertsFile)
	}
	return &signMonitorStore{
		path:       path,
		maxAge:     maxAge,
		samples:    make([]signMonitorSample, 0, 2048),
		spikes:     make([]signMonitorSpike, 0, 64),
		alerted:    make(map[string]int64),
		alerts:     make(map[string]*signMonitorAlertRecord),
		alertsPath: alertsPath,
	}
}

// loadAlerts 读已告警异常的索引。缺文件是正常情况（首次运行）。
func (s *signMonitorStore) loadAlerts() error {
	if s.alertsPath == "" {
		return nil
	}
	raw, err := os.ReadFile(s.alertsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var recs []*signMonitorAlertRecord
	if json.Unmarshal(raw, &recs) != nil || len(recs) == 0 {
		return nil
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	for _, r := range recs {
		if r == nil || r.Key == "" || r.LastAlert < cutoff {
			continue
		}
		s.alerts[r.Key] = r
	}
	return nil
}

// saveAlertsLocked 落盘（调用方持锁）。
func (s *signMonitorStore) saveAlertsLocked() {
	if s.alertsPath == "" {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	recs := make([]*signMonitorAlertRecord, 0, len(s.alerts))
	for _, r := range s.alerts {
		if r.LastAlert < cutoff {
			continue
		}
		recs = append(recs, r)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].LastAlert < recs[j].LastAlert })
	raw, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	tmp := s.alertsPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	os.Rename(tmp, s.alertsPath)
}

// spikeAlertKey 一条异常的身份：超话 + 起始时刻。
//
// ★ 用**起始时刻**而不是幅度：同一段连续刷量会被不断合并、幅度一直变大，
//
//	按幅度算 key 就会被当成不同的事件 → 同一件事反复推送。
func spikeAlertKey(spike signMonitorSpike) string {
	return signMonitorAnomalyKey(spike.OID, spike.FromTS)
}

// alertRecordCount 返回这条异常已经推过几次（0 = 从没推过）。
func (s *signMonitorStore) alertRecordCount(spike signMonitorSpike) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec := s.alerts[spikeAlertKey(spike)]; rec != nil {
		return rec.AlertCount
	}
	return 0
}

// Len 返回当前保留的采样条数。
func (s *signMonitorStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.samples)
}

// load 读历史（最多 maxAge）。缺文件是正常的（首次运行）。
func (s *signMonitorStore) load() error {
	file, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	cutoff := time.Now().Add(-s.maxAge).UnixMilli()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample signMonitorSample
		if json.Unmarshal([]byte(line), &sample) != nil {
			continue // 写了一半的末行不能连带丢掉前面的数据
		}
		if sample.TS < cutoff {
			continue
		}
		s.samples = append(s.samples, sample)
	}
	return scanner.Err()
}

// append 追加一批采样并落盘。
func (s *signMonitorStore) append(samples []signMonitorSample) {
	if len(samples) == 0 {
		return
	}
	s.mu.Lock()
	s.samples = append(s.samples, samples...)
	sort.Slice(s.samples, func(i, j int) bool { return s.samples[i].TS < s.samples[j].TS })
	s.pruneLocked()
	s.mu.Unlock()
	s.rewrite()
}

// recordSpike 记一条异常，并判断是否应当推送。
//
// 返回 (shouldAlert, isUpdate)：
//   - shouldAlert：要不要发消息
//   - isUpdate：这次是**同一段异常的补发**（段被延长了），不是新异常
//
// ★ 为什么要有补发（2026-10-08 用户要求）：
//
//	第一轮只看到 22:09 那一步 ⇒ 发一张窗口到 22:09 的表；
//	第二轮 22:14 又涨一截、被合并进同一段 ⇒ 必须再发一次，
//	而且第二张表要**把两次都囊括进去**（窗口按最新的 ToTS 往前推）。
//	之前按「已报过就跳过」处理 ⇒ 用户永远只收到第一张、看不到后半段。
//
// ★ 判断「段被延长」而不是「幅度变了」：用 ToTS / Steps / Delta 三者取大，
//
//	任一项变大说明有新数据进来，值得刷新一次。
func (s *signMonitorStore) recordSpike(spike signMonitorSpike, cooldown, updateGap time.Duration) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := spikeAlertKey(spike)
	replaced := false
	for i := range s.spikes {
		if spikeAlertKey(s.spikes[i]) == key {
			// ★ 用最新的那条替换：面板 / 邮件里展示的窗口要跟着长到最新一步
			s.spikes[i] = spike
			replaced = true
			break
		}
	}
	if !replaced {
		s.spikes = append(s.spikes, spike)
		if len(s.spikes) > 200 {
			s.spikes = append([]signMonitorSpike(nil), s.spikes[len(s.spikes)-200:]...)
		}
	}

	now := time.Now().UnixMilli()
	rec := s.alerts[key]
	if rec == nil {
		// 全新的一段：沿用原有的同 OID 冷却，防止同一超话短时间内连发。
		ck := fmt.Sprintf("%s|%d", spike.OID, spike.Delta/100)
		if last, ok := s.alerted[ck]; ok && now-last < int64(cooldown/time.Millisecond) {
			return false, false
		}
		s.alerted[ck] = now
		s.alerts[key] = &signMonitorAlertRecord{
			Key: key, OID: spike.OID, Name: spike.Name,
			FromTS: spike.FromTS, ToTS: spike.ToTS,
			Delta: spike.Delta, Steps: spike.Steps,
			AlertCount: 1, LastAlert: now, UpdatedAt: now,
		}
		s.saveAlertsLocked()
		return true, false
	}

	grew := spike.ToTS > rec.ToTS || spike.Steps > rec.Steps || spike.Delta > rec.Delta
	if !grew {
		return false, false
	}
	rec.Name = spike.Name
	if spike.ToTS > rec.ToTS {
		rec.ToTS = spike.ToTS
	}
	if spike.Steps > rec.Steps {
		rec.Steps = spike.Steps
	}
	if spike.Delta > rec.Delta {
		rec.Delta = spike.Delta
	}
	rec.UpdatedAt = now
	if rec.AlertCount >= maxAlertsPerSpike {
		s.saveAlertsLocked()
		return false, true // 记录了，但不再刷屏
	}
	// updateGap = 一个采样周期：正常情况下每轮才可能有一次新数据，天然满足；
	//   传 0 表示不限（测试用）。
	if updateGap > 0 && now-rec.LastAlert < int64(updateGap/time.Millisecond) {
		s.saveAlertsLocked()
		return false, true
	}
	rec.AlertCount++
	rec.LastAlert = now
	s.saveAlertsLocked()
	return true, true
}

func (s *signMonitorStore) pruneLocked() {
	cutoff := time.Now().Add(-s.maxAge).UnixMilli()
	idx := 0
	for idx < len(s.samples) && s.samples[idx].TS < cutoff {
		idx++
	}
	if idx > 0 {
		s.samples = append([]signMonitorSample(nil), s.samples[idx:]...)
	}
}

// rewrite 整体重写（顺带完成裁剪，避免文件无限增长）。
func (s *signMonitorStore) rewrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	temp := s.path + ".tmp"
	file, err := os.Create(temp)
	if err != nil {
		return
	}
	writer := bufio.NewWriter(file)
	for _, sample := range s.samples {
		line, err := json.Marshal(sample)
		if err != nil {
			continue
		}
		writer.Write(line)
		writer.WriteByte('\n')
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(temp)
		return
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return
	}
	// 先 rename 再替换：中途崩了至少留下上一份完整数据。
	os.Rename(temp, s.path)
}

// series 返回指定时间窗内、按超话分组的采样序列（按 oid 聚合）。
func (s *signMonitorStore) series(sinceMS int64) ([]signMonitorSeries, []signMonitorSpike) {
	s.mu.Lock()
	defer s.mu.Unlock()
	byOID := make(map[string][]signMonitorSample)
	names := make(map[string]string)
	for _, sample := range s.samples {
		if sample.TS < sinceMS {
			continue
		}
		byOID[sample.OID] = append(byOID[sample.OID], sample)
		if sample.Name != "" {
			names[sample.OID] = sample.Name
		}
	}
	out := make([]signMonitorSeries, 0, len(byOID))
	for oid, list := range byOID {
		points := make([][2]int64, 0, len(list))
		for _, sample := range list {
			points = append(points, [2]int64{sample.TS, int64(sample.Sign)})
		}
		out = append(out, signMonitorSeries{OID: oid, Name: names[oid], Points: points})
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Points) != len(out[j].Points) {
			return len(out[i].Points) > len(out[j].Points)
		}
		return out[i].OID < out[j].OID
	})
	spikes := make([]signMonitorSpike, 0, len(s.spikes))
	for _, spike := range s.spikes {
		if spike.At >= sinceMS {
			spikes = append(spikes, spike)
		}
	}
	return out, spikes
}

// signMonitorSeries 是给面板用的一个超话时间序列。
type signMonitorSeries struct {
	OID    string     `json:"oid"`
	Name   string     `json:"name"`
	Points [][2]int64 `json:"points"` // [unixMilli, signCount]
}

// windowDelta 取「窗口起点之前最后一个采样点」与「最新采样点」的差值。
//
// 基准必须在窗口外：取窗口内最小值会把「先跌后涨」也判成暴涨。
func windowDelta(samples []signMonitorSample, windowMin int) (base, latest signMonitorSample, ok bool) {
	if len(samples) < 2 || windowMin <= 0 {
		return signMonitorSample{}, signMonitorSample{}, false
	}
	latest = samples[len(samples)-1]
	cutoff := latest.TS - int64(windowMin)*60*1000
	idx := -1
	for i := len(samples) - 2; i >= 0; i-- {
		if samples[i].TS <= cutoff {
			idx = i
			break
		}
	}
	if idx < 0 {
		return signMonitorSample{}, signMonitorSample{}, false
	}
	return samples[idx], latest, true
}

// medianInt 取中位数（偶数个取中间两个的平均）。
func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// detectGroupSpikes 用「组内对比」找异常。
//
// 2026-10-07 用户反馈：绝对阈值不合理 —— 同一分组里 3000 档和 12000 档差着
// 近四倍，同一个阈值对前者是暴涨、对后者只是日常波动。真正刺眼的是某一条
// 明显高于同组其他人，所以主判据是「比同组中位数高 N 倍」。
//
// 绝对阈值（spikeAbs）保留为兜底：整组一起刷时组内对比看不出来，
// 但绝对增量仍然会很大。
func detectGroupSpikes(deltas map[string]signMonitorDelta, groups []config.WeiboSignMonitorGroup, ratio float64, minDelta int) []signMonitorSpike {
	spikes := make([]signMonitorSpike, 0)
	for _, group := range groups {
		// ★ members 里的标识可能是 oid，也可能是名字，两种都认：
		//   名字不靠谱 —— 接口返回的是「郑伊安IAN」「ChoiJiwoo」「柳河岚YUHA」，
		//   而配置里写的是「Ian」「Jiwoo」「Yuha」，按名字匹配会全部落空
		//   （members<3 → 整组跳过 → 判据静默失效）。oid 才是稳定标识。
		members := make([]string, 0, len(group.Members))
		for _, key := range group.Members {
			if _, ok := deltas[key]; ok {
				members = append(members, key)
				continue
			}
			if name, ok := matchByDisplayName(deltas, key); ok {
				members = append(members, name)
			}
		}
		if len(members) < 3 {
			// 少于 3 个成员时中位数没有意义（一条涨了就"全场异常"）
			continue
		}
		values := make([]int, 0, len(members))
		for _, name := range members {
			values = append(values, deltas[name].Delta)
		}
		median := medianInt(values)
		threshold := int(float64(median) * ratio)
		if threshold < minDelta {
			threshold = minDelta
		}
		for _, name := range members {
			d := deltas[name]
			if d.Delta < threshold || d.Delta <= 0 {
				continue
			}
			spikes = append(spikes, signMonitorSpike{
				OID: d.OID, Name: d.Name,
				From: d.From, To: d.To, Delta: d.Delta,
				WindowMin: d.WindowMin, At: d.At,
				GroupName: group.Name, GroupMedian: median, GroupThreshold: threshold,
			})
		}
	}
	return spikes
}

// matchByDisplayName 按「大写后互为子串」匹配超话名。
//
// 用于兼容配置里写简称、接口返回全名的情况：
//
//	配置 "Ian"  ↔  接口 "郑伊安IAN"
//	配置 "Jiwoo" ↔  接口 "ChoiJiwoo"
//	配置 "Yuha"  ↔  接口 "柳河岚YUHA"
//
// 匹配时两边都转大写并检查双向子串，避免 "Ana" 命中 "ANA卢惟那" 之外的噪声。
func matchByDisplayName(deltas map[string]signMonitorDelta, key string) (string, bool) {
	upper := strings.ToUpper(strings.TrimSpace(key))
	if upper == "" {
		return "", false
	}
	for name := range deltas {
		other := strings.ToUpper(name)
		if strings.Contains(other, upper) || strings.Contains(upper, other) {
			return name, true
		}
	}
	return "", false
}

// signMonitorDelta 是某个超话在一个窗口内的涨幅。
type signMonitorDelta struct {
	OID       string
	Name      string
	From      int
	To        int
	Delta     int
	WindowMin int
	At        int64
}

// detectSpikes 找出窗口内的异常涨幅。
//
// 判据（两条满足其一即算异常）：
//  1. 绝对涨幅 >= spikeAbs —— 半小时涨 800 人，明显不是自然增长；
//  2. 相对涨幅 >= spikeRatio 且绝对值 >= 200 —— 小基数超话涨 30% 也算。
//
// 基准取「窗口起点之前最近一个采样点」，而不是窗口内的最小值：
// 最小值会把「先跌后涨」也判成暴涨。
func detectSpikes(samples []signMonitorSample, windowMin, absThreshold int, ratioThreshold float64) []signMonitorSpike {
	if len(samples) < 2 || windowMin <= 0 {
		return nil
	}
	latest := samples[len(samples)-1]
	cutoff := latest.TS - int64(windowMin)*60*1000
	// 找窗口起点之前最后一个基准点
	idx := -1
	for i := len(samples) - 2; i >= 0; i-- {
		if samples[i].TS <= cutoff {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil // 窗口内没有可比的历史点
	}
	base := samples[idx]
	delta := latest.Sign - base.Sign
	if delta <= 0 {
		return nil // 只关心暴涨；暴跌是另一回事（日志里另有统计）
	}
	if delta < absThreshold {
		if base.Sign <= 0 || delta < 200 {
			return nil
		}
		if float64(delta)/float64(base.Sign) < ratioThreshold {
			return nil
		}
	}
	spanMin := int((latest.TS - base.TS) / 60000)
	return []signMonitorSpike{{
		OID: latest.OID, Name: latest.Name,
		From: base.Sign, To: latest.Sign, Delta: delta,
		WindowMin: spanMin, At: latest.TS,
	}}
}

// runWeiboSignMonitorLoop 周期采样指定分组的超话签到数。
//
// 每轮：对分组内每个超话并发（上限 4）调一次超话页面接口，解析「今日签到 X 人」，
// 落盘并做涨幅检测。
func (b *Bot) runWeiboSignMonitorLoop() {
	cfg := b.cfg.WeiboSignMonitor
	if !cfg.Enabled {
		return
	}
	interval := cfg.Interval()
	retention := cfg.Retention()
	dir := filepath.Join(storageRootOf(b.cfg.ConfigPath()), "weibo")
	store := newSignMonitorStore(filepath.Join(dir, signMonitorFile), retention)
	if err := store.load(); err != nil {
		log.Printf("[WeiboSignMonitor] 读取历史失败: %v", err)
	}
	// ★ 已推送过的异常索引必须一起读回来，否则重启 = 把历史异常再发一遍。
	if err := store.loadAlerts(); err != nil {
		log.Printf("[WeiboSignMonitor] 读取告警索引失败: %v", err)
	}
	b.signMonitor = store
	// 首次启用索引时的迁移：见 seedAlertedHistory。
	b.seedAlertedHistory(store)
	log.Printf("[WeiboSignMonitor] 已启动 分组=%s 超话=%d 间隔=%s 保留=%s 已告警异常=%d",
		cfg.GroupKey, len(b.signMonitorTopics()), interval, retention, len(store.alerts))

	// 启动后立刻采一轮，面板马上有数；随后按间隔轮询。
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	b.sampleWeiboSignCounts(context.Background(), store)
	for range ticker.C {
		if !b.cfg.WeiboSignMonitor.Enabled {
			return
		}
		b.sampleWeiboSignCounts(context.Background(), store)
	}
}

// signMonitorTopics 返回监测范围内的超话（按配置分组过滤）。
func (b *Bot) signMonitorTopics() []config.WeiboSuperCountTopic {
	groupKey := strings.TrimSpace(b.cfg.WeiboSignMonitor.GroupKey)
	topics := b.getWeiboSuperCountTopics()
	out := make([]config.WeiboSuperCountTopic, 0, 16)
	for _, topic := range topics {
		if groupKey == "" || topic.GroupName == groupKey {
			out = append(out, *topic)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sampleWeiboSignCounts 采一轮并落盘 + 检测。
func (b *Bot) sampleWeiboSignCounts(ctx context.Context, store *signMonitorStore) {
	topics := b.signMonitorTopics()
	if len(topics) == 0 {
		return
	}
	cfg := b.cfg.WeiboSignMonitor
	now := time.Now()
	results := make([]signMonitorSample, len(topics))
	limit := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, topic := range topics {
		i, topic := i, topic
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			res, err := b.weiboMonitor.FetchSuperCountByOID(topic.OID, topic.Name)
			if err != nil || res == nil {
				log.Printf("[WeiboSignMonitor] 采样失败 oid=%s name=%s: %v", topic.OID, topic.Name, err)
				return
			}
			if res.SignCount <= 0 {
				// 拿不到精确值（接口偶发只给模糊文本）时保留 0，
				// 绝不用上一次的值顶替 —— 那会让曲线凭空多一段平台。
				log.Printf("[WeiboSignMonitor] 采样无数值 oid=%s name=%s text=%q", topic.OID, topic.Name, res.SignText)
				return
			}
			name := strings.TrimSpace(res.Name)
			if name == "" {
				name = topic.Name
			}
			results[i] = signMonitorSample{TS: now.UnixMilli(), OID: topic.OID, Name: name, Sign: res.SignCount}
		}()
	}
	wg.Wait()

	samples := make([]signMonitorSample, 0, len(results))
	for _, sample := range results {
		if sample.Sign > 0 {
			samples = append(samples, sample)
		}
	}
	if len(samples) == 0 {
		log.Printf("[WeiboSignMonitor] 本轮 %d 个超话都没取到数值", len(topics))
		return
	}
	store.append(samples)
	log.Printf("[WeiboSignMonitor] 已采样 %d/%d 个超话", len(samples), len(topics))

	// 涨幅检测：按 oid 聚齐历史点后逐个判。
	byOID := make(map[string][]signMonitorSample)
	for _, sample := range samples {
		byOID[sample.OID] = append(byOID[sample.OID], sample)
	}
	//★★★★★ 2026-10-08 判定口径再次统一（用户要求）
	//
	// 历史上有**三套**判据并存，导致「面板看到的异常 ≠ 邮件发出的异常」：
	//   - bot侧：单步增量 - 同组中位数（阈值 800）
	//   - admin 侧：30 分钟窗口涨幅（阈值 800 + 相对 25%）
	//   - 前端图：硬编码 400
	//
	// 而且阈值 800 **比真实波动高一个数量级**：实测 10-07 全天 1912 个采样点，
	// 第一组单步偏离的 p99 只有 70，真实刷量是 +351/+564/+71
	//⇒ **告警从来没有触发过一次**。
	//
	// 现在三处全部改走 internal/signstat（唯一实现）：
	//   - 跨日归零步剔除（实测 8 人同时跌 3000~10500，不是异常）
	//   - 破万者曲线截断而非整条剔除，且不参与基线
	//   - 连续超阈值点合并成一次异常
	//   - 阈值改为可配置，默认 150（= p99 的 2 倍）
	spikes := b.detectSignMonitorAnomalies(store, cfg)
	for _, spike := range spikes {
		shouldAlert, isUpdate := store.recordSpike(spike, cfg.SpikeCooldown(), cfg.Interval())
		if !shouldAlert {
			continue
		}
		log.Printf("[WeiboSignMonitor] 异常 oid=%s name=%s %d→%d (+%d) 峰值=%d 步数=%d 判据=%s 组=%s 中位数=%d 阈值=%d 补发=%t 第%d次",
			spike.OID, spike.Name, spike.From, spike.To, spike.Delta,
			spike.PeakDelta, spike.Steps, spike.Reason,
			spike.GroupName, spike.GroupMedian, spike.GroupThreshold,
			isUpdate, store.alertRecordCount(spike))
		b.alertWeiboSignSpike(spike, isUpdate)
		// 邮件报表：正文 + 异常明细 PNG + CSV
		b.sendSignMonitorSpikeReport(spike, isUpdate)
	}
}

// seedAlertedHistory 首次启用持久化索引时的迁移。
//
// 不加这段的话，升级后第一次重启必然「把历史异常全部重推一遍」—— 索引文件
// 本来是不存在的，历史异常一条都没记过，而检测每轮都拿全量数据重算。
// 那些异常在旧版本里早就推过了，重推就是骚扰。
//
// ★ 只补种**已经结束**的段（ToTS 早于一个采样周期之前）：正在进行的那段
//
//	必须留给实时检测，否则这次升级会把正在发生的异常吃掉一次告警。
func (b *Bot) seedAlertedHistory(store *signMonitorStore) {
	if store == nil || store.alertsPath == "" {
		return
	}
	if _, err := os.Stat(store.alertsPath); err == nil {
		return // 索引已存在，不是首次
	}
	cfg := b.cfg.WeiboSignMonitor
	groups := b.signStatGroups()
	all := store.allSamples()
	if len(groups) == 0 || len(all) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	cutoff := now - int64(cfg.Interval()/time.Millisecond)
	res := signstat.AnalyzeSamples(all, groups, signstat.Options{
		Threshold:        cfg.SpikeAbs(),
		SuspectRatio:     cfg.CrossoverSuspect(),
		FuzzyRatePerHour: cfg.CrossoverFuzzyRate(),
	})
	seeded := 0
	store.mu.Lock()
	for _, a := range res.Anomalies {
		if a.ToTS > cutoff {
			continue
		}
		key := signMonitorAnomalyKey(a.OID, a.FromTS)
		if store.alerts[key] != nil {
			continue
		}
		store.alerts[key] = &signMonitorAlertRecord{
			Key: key, OID: a.OID, Name: a.Name,
			FromTS: a.FromTS, ToTS: a.ToTS,
			Delta: a.TotalDelta, Steps: a.Steps,
			AlertCount: 1, LastAlert: now, UpdatedAt: now,
		}
		seeded++
	}
	store.saveAlertsLocked()
	store.mu.Unlock()
	if seeded > 0 {
		log.Printf("[WeiboSignMonitor] 首次启用告警索引：已把 %d 条历史异常标记为已推送，不会重复推送", seeded)
	}
}

// detectSignMonitorAnomalies 用 signstat 对整个 store 做一次分析，取出新增的异常。
//
// ★ 只处理「上次分析之后」的新异常，否则每轮都会把历史异常重报一遍。
func (b *Bot) detectSignMonitorAnomalies(store *signMonitorStore, cfg config.WeiboSignMonitorConfig) []signMonitorSpike {
	samples := store.allSamples()
	if len(samples) == 0 {
		return nil
	}
	groups := make([]signstat.Group, 0, len(cfg.Groups))
	for _, g := range cfg.Groups {
		groups = append(groups, signstat.Group{Name: g.Name, Members: g.Members})
	}
	res := signstat.AnalyzeSamples(samples, groups, signstat.Options{
		Threshold:        cfg.SpikeAbs(),
		SuspectRatio:     cfg.CrossoverSuspect(),
		FuzzyRatePerHour: cfg.CrossoverFuzzyRate(),
	})

	// 已推送过的不再重复推 —— 索引来自**落盘的** alerts，重启后依然生效。
	// （旧代码这里用 OID|At 建索引、却用 OID|FromTS 去查，两边对不上，
	//   等于没去重，全靠下游的冷却表兜着。）
	out := make([]signMonitorSpike, 0, len(res.Anomalies))
	for _, a := range res.Anomalies {
		if store.hasAlerted(a.OID, a.FromTS) {
			continue
		}
		out = append(out, anomalyToSpike(a))
	}
	return out
}

func signMonitorAnomalyKey(oid string, ts int64) string {
	return fmt.Sprintf("%s|%d", oid, ts)
}

// hasAlerted 报告这条异常（超话 + 起始时刻）是否已经推送过。
func (s *signMonitorStore) hasAlerted(oid string, fromTS int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.alerts[signMonitorAnomalyKey(oid, fromTS)]
	return ok
}

func anomalyToSpike(a signstat.Anomaly) signMonitorSpike {
	win := int((a.ToTS - a.FromTS) / 60000)
	if win <= 0 {
		win = 5
	}
	return signMonitorSpike{
		OID: a.OID, Name: a.Name,
		From: a.From, To: a.To, Delta: a.TotalDelta, WindowMin: win, At: a.FromTS,
		GroupName: a.GroupName, GroupMedian: a.GroupMedian, GroupThreshold: a.Threshold,
		Reason:    a.Reason,
		PeakDelta: a.PeakDelta, PeakTS: a.PeakTS, Steps: a.Steps,
		Severity: string(a.Severity),
		FromTS:   a.FromTS, ToTS: a.ToTS, TotalDelta: a.TotalDelta,
	}
}

// intsToI64 把 []int 转成 []int64（medianInt64 只接受 []int64）。
func intsToI64(v []int) []int64 {
	out := make([]int64, len(v))
	for i, n := range v {
		out[i] = int64(n)
	}
	return out
}

// detectGroupStepDeviations 检测「5 分钟单步增量相对同组中位数」的离群。
//
// ★ 口径（与 buildSignMonitorDeviationSVG 的偏离图完全一致）：
//
//	dev[t] = (v[t] - v[t-1]) - median_k( v_k[t] - v_k[t-1] )
//
// 为什么不用 30 分钟窗口（用户 22:57 纠正）：
//
//	采样就是 5 分钟一次，偏离图已经是这个口径；
//	30 分钟窗口会把一次尖峰摊薄（实测单步 +614 → 30 分钟后 +291），
//	既粗又和图上看到的不一致。
//
// 规则：
//   - 破万成员整体剔除（增量是 1K 模糊档位跳变，不是真实数据）
//   - 组内有效成员 < 2 时不判（没有参照系）
//   - 2 人：基线 = 对方那一步的增量（互为相反数）
//   - ≥3 人：基线 = 组内中位数（偶数个取平均）
//   - 去抖：相邻异常间隔 ≥ 2 个采样点
func detectGroupStepDeviations(store *signMonitorStore, cfg config.WeiboSignMonitorConfig,
	byOID map[string][]signMonitorSample) []signMonitorSpike {

	out := make([]signMonitorSpike, 0)

	for _, grp := range cfg.Groups {
		// ---- 收集组内未破万成员的增量序列，按时间戳对齐 ----
		type inc struct {
			name string
			oid  string
			ts   int64
			prev int // ★ 必须是 int：signMonitorSample.Sign 是 int
			step int
		}
		byTS := map[int64][]inc{}
		names := map[string]string{}
		oids := map[string]string{}
		for _, member := range grp.Members {
			list := byOID[member]
			if len(list) < 2 {
				continue
			}
			// 破万剔除（按该成员本轮值 + 历史最大值）
			crossed := false
			for _, s := range store.samplesFor(member, list[0].TS) {
				if s.Sign >= superTopicCrossoverSign {
					crossed = true
					break
				}
			}
			if list[0].Sign >= superTopicCrossoverSign {
				crossed = true
			}
			if crossed {
				continue
			}
			hist := store.samplesFor(member, list[0].TS)
			sorted := append([]signMonitorSample(nil), hist...)
			sort.Slice(sorted, func(a, b int) bool { return sorted[a].TS < sorted[b].TS })
			names[member] = sorted[len(sorted)-1].Name
			oids[member] = member
			for i := 1; i < len(sorted); i++ {
				byTS[sorted[i].TS] = append(byTS[sorted[i].TS], inc{
					name: sorted[i-1].Name,
					oid:  member,
					ts:   sorted[i].TS,
					prev: sorted[i-1].Sign,
					step: sorted[i].Sign - sorted[i-1].Sign,
				})
			}
		}
		if len(names) < 2 {
			continue
		}

		stamps := make([]int64, 0, len(byTS))
		for ts, list := range byTS {
			// ★ 同一时刻必须人人都有增量才可比（否则是不同时间窗口）
			if len(list) < len(names) {
				continue
			}
			stamps = append(stamps, ts)
		}
		if len(stamps) == 0 {
			continue
		}
		sort.Slice(stamps, func(a, b int) bool { return stamps[a] < stamps[b] })

		thresh := cfg.SpikeAbs()
		lastAt := int64(-1)
		for _, ts := range stamps {
			list := byTS[ts]
			steps := make([]int, len(list))
			for i, e := range list {
				steps[i] = e.step
			}
			var base int
			for i, e := range list {
				// ★ 2 人组：基线 = 对方那一步的增量（互为相反数，必定一正一负）
				if len(list) == 2 {
					other := 1 - i
					base = steps[other]
				} else {
					base = int(medianInt64(intsToI64(steps)))
				}
				dev := steps[i] - base
				if abs64(int64(dev)) < int64(thresh) {
					continue
				}
				// 去抖：同一尖峰只报第一条
				if lastAt >= 0 && ts-lastAt < int64(10*60*1000) {
					continue
				}
				lastAt = ts
				out = append(out, signMonitorSpike{
					OID: e.oid, Name: e.name,
					From: e.prev, To: e.prev + steps[i],
					Delta: steps[i], WindowMin: 5, At: ts,
					GroupName:      grp.Name,
					GroupMedian:    int(base),
					GroupThreshold: thresh,
					Reason:         "同组离群(5分钟单步)",
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At > out[j].At })
	return out
}

// samplesFor 取某超话在 atMS 之前（含）的历史采样。
func (s *signMonitorStore) samplesFor(oid string, atMS int64) []signMonitorSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]signMonitorSample, 0, 64)
	for _, sample := range s.samples {
		if sample.OID == oid && sample.TS <= atMS {
			out = append(out, sample)
		}
	}
	return out
}

// allSamples 返回全部采样的副本（供 signstat 分析）。
func (s *signMonitorStore) allSamples() []signstat.Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]signstat.Sample, 0, len(s.samples))
	for _, sample := range s.samples {
		out = append(out, signstat.Sample{
			TS: sample.TS, OID: sample.OID, Name: sample.Name, Sign: sample.Sign,
		})
	}
	return out
}

// spikesSnapshot 返回已记录异常的副本（避免遍历时持锁）。
func (s *signMonitorStore) spikesSnapshot() []signMonitorSpike {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]signMonitorSpike(nil), s.spikes...)
}

// alertWeiboSignSpike 把异常推给管理员。
func (b *Bot) alertWeiboSignSpike(spike signMonitorSpike, isUpdate bool) {
	name := spike.Name
	if name == "" {
		name = spike.OID
	}
	stampAt := time.Now()
	if spike.ToTS > 0 {
		stampAt = time.UnixMilli(spike.ToTS)
	}

	// ★ 补发（同一段异常又涨了）：必须让收件人一眼知道这是**同一件事的刷新**，
	//   而且这张表已经把前后几步全包进去了（窗口按最新的 ToTS 往前推 1 小时）。
	//   不标注的话用户会以为是又冒出一条新异常（2026-10-08 反馈）。
	round := 1
	if b.signMonitor != nil {
		round = b.signMonitor.alertRecordCount(spike)
	}
	head := "【超话签到监控|微博】"
	if isUpdate {
		head += fmt.Sprintf("\n🔄 更新：同一段异常又涨了，这里是截至 %s 的完整数据（第 %d 次推送）",
			stampAt.Format("15:04"), round)
	}

	// ---- 文本（2026-10-08 用户反馈重构）----
	//   1. 不再贴超话 UUID —— 用户明确说「这玩意儿贴给我干什么」；
	//   2. 首行【超话签到监控|微博】按飞书卡片约定解析出来源，
	//      尾行独立时间戳会被 extractTimestamp 抽进卡片底栏 ——
	//      这正是其他消息「标明来源和时间」的同一条链路，不用动 outbound。
	//   3. 结构分层：谁、从多少到多少、凭什么判异常、要不要处理。
	var text string
	if spike.Severity == "suspected" {
		// 破万者拿不到精确值，只能说「疑似」，**不能**写成确定异常。
		text = fmt.Sprintf("%s\n⚠️ 疑似异常（已破万）\n\n%s\n%s → %s（模糊值跳变）\n%s\n\n注意：破万后接口只给千粒度模糊值，"+
			"这只是「涨得太快」的疑点，不是确认的刷量。",
			head, name, signValueLabel(spike.From), signValueLabel(spike.To), spike.Reason)
	} else {
		text = fmt.Sprintf("%s\n⚠️ 签到数异常涨幅\n\n%s（%s）\n%s → %s，累计 +%d\n峰值偏离 +%d（同组中位数 +%d，阈值 ±%d）",
			head, name, spike.GroupName, signValueLabel(spike.From), signValueLabel(spike.To),
			spike.TotalDelta, spike.PeakDelta, spike.GroupMedian, spike.GroupThreshold)
		if spike.Steps > 1 {
			text += fmt.Sprintf("\n连续 %d 个采样点（%s 起），已合并为一次异常",
				spike.Steps, time.UnixMilli(spike.FromTS).Format("15:04"))
		}
		text += "\n\n请核对是否有刷量。"
	}
	// 尾行独立时间戳：飞书抽进底栏；QQ 里也是用户要的「标明时间」。
	text += "\n\n" + stampAt.Format("2006-01-02 15:04")

	// ---- 图片：明细表 PNG，与文本**合并成一条**消息 ----
	//
	// ★★ 图片必须用 base64 内嵌，**绝不能写临时文件传路径**（2026-10-08 实测踩坑）：
	//   QQ 走 GatewayClient.Send → 投进 channel 队列，由后台 goroutine 异步发送；
	//   这里的 defer os.Remove 会在**函数返回时立刻执行**，而消息还躺在队列里 ——
	//   后台真正发送时文件已经没了，napcat 读不到 ⇒ 群里只收到文字没有图。
	//   base64 直接塞进消息段，随消息走，没有任何文件生命周期问题
	//   （napcat 原生支持 base64://，飞书 readMedia 也支持）。
	//   douyin_monitor.go 早就这么干了（ImageSegment("base64://"+...)）。
	segs := []napcat.MessageSegment{napcat.TextSegment(text)}
	if png := b.buildSpikeDetailPNG(spike, round); len(png) > 0 {
		segs = append(segs, napcat.ImageSegment("base64://"+base64.StdEncoding.EncodeToString(png)))
	} else {
		// 渲染失败也要让用户知道为什么没图，别静默
		log.Printf("[WeiboSignMonitor] 告警明细表未生成（渲染失败或无数据），只发文字")
	}

	targets := b.cfg.WeiboSignMonitor.AlertTargets
	if len(targets) == 0 {
		targets = b.cfg.WeiboReportImageTargets
	}
	for _, targetID := range targets {
		target := b.cfg.ResolveTarget(targetID)
		if target.ID == "" {
			continue
		}
		b.sendTarget(target, segs)
	}
	log.Printf("[WeiboSignMonitor] 告警已发送 目标=%d 带图=%t 补发=%t", len(targets), len(segs) > 1, isUpdate)
}

// hourlyTable 是「某一天每小时 × 各超话增量」的统计结果。
//
// ★ PNG 与邮件正文 HTML **共用这一份**（2026-10-08）：
//
//	用户原话「你发的是日报的话，这个'超话日报'是发什么？就是发那一天，
//	就是发你 PNG 里的那个东西啊。你应该把你 PNG 里的内容同样写成 HTML，
//	在邮件本体里面这样发」⇒ 两处必须一模一样。
type hourlyTable struct {
	Hours      []int                  // 有数据的小时，升序
	OIDs       []string               // **按年龄顺序**
	Names      map[string]string      // oid -> 名字
	Buckets    map[int]map[string]int // hour -> oid -> 净增
	Fuzzy      map[string]bool        // "hour|oid" 该格含破万模糊值
	TotalByOID map[string]int         // 当日合计
	GrandTotal int
	Day        string // YYYY-MM-DD
}

func hourlyFuzzyKey(h int, oid string) string { return fmt.Sprintf("%d|%s", h, oid) }

// computeHourlyTable 只统计 at 所在的**那一个自然日**。
func computeHourlyTable(samples []signstat.Sample, at time.Time) (*hourlyTable, error) {
	loc := at.Location()
	dayKey := at.Format("2006-01-02")

	byOID := map[string][]signstat.Sample{}
	names := map[string]string{}
	for _, sm := range samples {
		byOID[sm.OID] = append(byOID[sm.OID], sm)
		if sm.Name != "" {
			names[sm.OID] = sm.Name
		}
	}

	tab := &hourlyTable{
		Names: names, Buckets: map[int]map[string]int{},
		Fuzzy: map[string]bool{}, TotalByOID: map[string]int{},
		Day: dayKey,
	}
	hourSeen := map[int]bool{}

	for oid, pts := range byOID {
		sort.Slice(pts, func(i, j int) bool { return pts[i].TS < pts[j].TS })
		for i := 1; i < len(pts); i++ {
			prev, cur := pts[i-1], pts[i]
			if cur.TS-prev.TS > 15*60*1000 {
				continue // 断采，差值无意义
			}
			pt := time.UnixMilli(prev.TS).In(loc)
			ct := time.UnixMilli(cur.TS).In(loc)
			if ct.Format("2006-01-02") != dayKey {
				continue // ★ 只统计报表当天
			}
			if pt.Format("2006-01-02") != ct.Format("2006-01-02") {
				continue // 跨日归零，不是真实增量
			}
			h := ct.Hour()
			hourSeen[h] = true
			if tab.Buckets[h] == nil {
				tab.Buckets[h] = map[string]int{}
			}
			// ★★★ 破万后的步也要计入（2026-10-08 用户纠正）：
			//   接口破万后只给千粒度模糊值，但相邻两次采样的差值**看得见的**
			//   （实测郑伊安 22:45 那步 10000 → 11000，确知至少 +1000）。
			//   旧代码在这里 continue，于是那一小时只显示破万那一步的「+7」，
			//   实际已知下限 7 + 1000 = 1007 被整段丢掉了。
			tab.Buckets[h][oid] += cur.Sign - prev.Sign
			if cur.Sign > superTopicCrossoverSign || prev.Sign >= superTopicCrossoverSign {
				tab.Fuzzy[hourlyFuzzyKey(h, oid)] = true
			}
		}
	}
	if len(hourSeen) == 0 {
		return nil, fmt.Errorf("no hourly data")
	}
	for h := range hourSeen {
		tab.Hours = append(tab.Hours, h)
	}
	sort.Ints(tab.Hours)

	for oid := range names {
		tab.OIDs = append(tab.OIDs, oid)
	}
	// ★ 按年龄顺序（2026-10-08 用户指定的固定顺序）
	sort.SliceStable(tab.OIDs, func(i, j int) bool {
		return signstat.AgeRank(names[tab.OIDs[i]]) < signstat.AgeRank(names[tab.OIDs[j]])
	})
	for _, h := range tab.Hours {
		for oid, d := range tab.Buckets[h] {
			tab.TotalByOID[oid] += d
			tab.GrandTotal += d
		}
	}
	return tab, nil
}

// signMonitorHourlyTableHTML 把小时表渲成 HTML —— **内容与 buildHourlyTablePNG
// 逐格一致**（同一个 computeHourlyTable 出来的数据）。
//
// ★ 2026-10-08 用户要求：「这个'超话日报'是发什么？就是发那一天，
//
//	就是你 PNG 里的那个东西啊。你应该把你PNG 里的内容同样写成 HTML，
//	在邮件本体里面这样发」—— 之前正文放的是一张毫无意义的
//	「当前/区间最低/区间最高/采样点」分组表，已经删掉。
func signMonitorHourlyTableHTML(tab *hourlyTable, at time.Time) string {
	if tab == nil || len(tab.Hours) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<h3 style="margin:22px 0 6px;font-size:15px">` + html.EscapeString(tab.Day) + ` 每小时增量</h3>`)
	b.WriteString(`<p style="margin:0 0 8px;color:#667085;font-size:12px">` +
		`每格 = 该小时内净增签到数。标注 <b style="color:#b54708">*</b> 表示该小时内该超话已破万：` +
		`<b>数字是已确认的增量</b>，* 表示还有拿不到的额外增量（<b>+0* 不等于没涨</b>）。` +
		`跨日归零步与断采（间隔 &gt;15 分钟）不计入。</p>`)
	b.WriteString(`<table style="border-collapse:collapse;width:100%;font-size:12.5px">`)
	b.WriteString(`<tr style="background:#f5f7fb">`)
	b.WriteString(`<th style="padding:5px 7px;border:1px solid #e4e7ec;text-align:left;color:#475467">时刻</th>`)
	for _, oid := range tab.OIDs {
		b.WriteString(`<th style="padding:5px 7px;border:1px solid #e4e7ec;text-align:right;color:#475467">` +
			html.EscapeString(tab.Names[oid]) + `</th>`)
	}
	b.WriteString(`<th style="padding:5px 7px;border:1px solid #e4e7ec;text-align:right;color:#475467">合计</th>`)
	b.WriteString(`</tr>`)

	// ★ 「进行中」只在**报表当天且当前小时**标；补发历史日期时最后一小时
	//   也是完整的 23 时，不能写成「进行中」（否则补发的日报永远带这字样）。
	liveDay := tab.Day == at.Format("2006-01-02")
	for _, h := range tab.Hours {
		label := fmt.Sprintf("%02d时", h)
		if liveDay && h == at.Hour() && h == tab.Hours[len(tab.Hours)-1] {
			label += `（进行中）`
		}
		b.WriteString(`<tr><td style="padding:5px 7px;border:1px solid #e4e7ec;text-align:left">` + label + `</td>`)
		rowSum := 0
		for _, oid := range tab.OIDs {
			d := tab.Buckets[h][oid]
			star := ""
			if tab.Fuzzy[hourlyFuzzyKey(h, oid)] {
				star = `*`
			}
			rowSum += d
			txt := "+0"
			style := `text-align:right;color:#667085`
			if d > 0 {
				txt = "+" + strconv.Itoa(d)
				style = `text-align:right;color:#e0484d;font-weight:600`
			} else if d < 0 {
				txt = strconv.Itoa(d)
				style = `text-align:right;color:#12a150`
			}
			b.WriteString(`<td style="padding:5px 7px;border:1px solid #e4e7ec;` + style + `">` + txt + star + `</td>`)
		}
		b.WriteString(`<td style="padding:5px 7px;border:1px solid #e4e7ec;text-align:right;font-weight:700">+` +
			strconv.Itoa(rowSum) + `</td>`)
		b.WriteString(`</tr>`)
	}

	// 合计行
	b.WriteString(`<tr style="background:#f5f7fb"><td style="padding:5px 7px;border:1px solid #e4e7ec;text-align:left;font-weight:600">当日合计</td>`)
	for _, oid := range tab.OIDs {
		d := tab.TotalByOID[oid]
		txt := "+" + strconv.Itoa(d)
		if d < 0 {
			txt = strconv.Itoa(d)
		}
		b.WriteString(`<td style="padding:5px 7px;border:1px solid #e4e7ec;text-align:right;font-weight:600">` + txt + `</td>`)
	}
	b.WriteString(`<td style="padding:5px 7px;border:1px solid #e4e7ec;text-align:right;font-weight:700">+` +
		strconv.Itoa(tab.GrandTotal) + `</td>`)
	b.WriteString(`</tr>`)
	b.WriteString(`</table>`)
	return b.String()
}

// buildHourlyTablePNG 生成「当天每小时增量」表格 PNG。
//
// ★ 表格**自己生成**、不走面板截图（2026-10-08 用户明确分工）：
//
//	「表格你直接生成 PNG 就行，应该还蛮好生成的」——截图只留给
//	手绘画不好的图表（偏移图）。表格用 HTML 排版反而更干净可控。
//
// ★★ 只统计 at 所在的**那一个自然日**（2026-10-08 修正）：
//
//	之前拿 retention 里的全部 Samples 做小时桶，一天发一次却把过去 72 小时
//	全列出来 —— 24 小时的表变成三天的流水账，手机上根本看不成。
//
// ★★★ 修过一个会静默产出错误数据的坑（2026-10-08）：
//
//	旧代码给最新一行的标签拼上「（进行中）」之后，又拿这个拼好的字符串去
//	查数据桶 ⇒ 必然查不到 ⇒ **最后一行整行显示 +0**，看上去像「这一小时
//	所有人集体停止签到」。标签与查表用的 key 必须是同一个 key。
func buildHourlyTablePNG(samples []signstat.Sample, at time.Time) ([]byte, error) {
	tab, err := computeHourlyTable(samples, at)
	if err != nil {
		return nil, err
	}

	oids := tab.OIDs
	names := tab.Names
	hours := tab.Hours
	buckets := tab.Buckets
	fuzzy := tab.Fuzzy
	totalByOID := tab.TotalByOID

	grandTotal := 0
	for _, d := range totalByOID {
		grandTotal += d
	}

	var bld strings.Builder
	bld.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><style>")
	bld.WriteString("body{margin:0;background:#fff;font-family:'PingFang SC','Microsoft YaHei',sans-serif;}")
	bld.WriteString("#report-card{padding:18px 20px;background:#fff;width:820px;box-sizing:border-box;}")
	bld.WriteString("h3{margin:0 0 4px;font-size:17px;color:#101828;}")
	bld.WriteString(".sub{margin:0 0 12px;font-size:12px;color:#98a2b3;}")
	bld.WriteString("table{border-collapse:collapse;width:100%;font-size:12.5px;}")
	bld.WriteString("th,td{border:1px solid #e4e7ec;padding:5px 7px;text-align:right;white-space:nowrap;}")
	bld.WriteString("th{background:#f5f7fb;color:#475467;font-weight:600;}")
	bld.WriteString("td:first-child,th:first-child{text-align:left;color:#344054;font-weight:600;}")
	bld.WriteString("td.up{color:#b42318;font-weight:600;}")
	bld.WriteString("td.dim{color:#98a2b3;}")
	bld.WriteString("td.total,th.total{background:#fcfcfd;font-weight:700;}")
	bld.WriteString("tr.sum td{background:#f5f7fb;font-weight:700;}")
	bld.WriteString(".legend{margin-top:8px;font-size:12px;color:#98a2b3;line-height:1.6;}")
	bld.WriteString("</style></head><body><div id=\"report-card\" data-raw=\"1\">")
	bld.WriteString("<h3>超话签到 · 每小时净增量</h3>")
	bld.WriteString("<p class=\"sub\">" + html.EscapeString(fmt.Sprintf(
		"%s（%d 个超话）· 截止 %s（跨日归零与断采步不计入）",
		tab.Day, len(oids), at.Format("15:04"))) + "</p>")
	bld.WriteString("<table><tr><th>时间</th>")
	for _, oid := range oids {
		bld.WriteString("<th>" + html.EscapeString(names[oid]) + "</th>")
	}
	bld.WriteString("<th class=\"total\">当日合计</th>")
	bld.WriteString("</tr>")

	// ★ 标签 key 与查表 key 必须是同一个（hour 数字），
	//   「（进行中）」只加在**显示用**的字符串上。
	//   且只在**报表当天**标 —— 补发历史日期时最后一小时是完整的。
	pngLiveDay := tab.Day == at.Format("2006-01-02")
	for _, h := range hours {
		label := fmt.Sprintf("%02d时", h)
		if pngLiveDay && h == at.Hour() && h == hours[len(hours)-1] {
			label += "（进行中）"
		}
		bld.WriteString("<tr><td>" + label + "</td>")
		for _, oid := range oids {
			d := buckets[h][oid]
			star := ""
			cls := ""
			if fuzzy[hourlyFuzzyKey(h, oid)] {
				star = "*" // 该时段内含破万后的模糊值
			}
			txt := "+0"
			if d > 0 {
				txt = "+" + strconv.Itoa(d)
				cls = " class=\"up\""
			} else if d < 0 {
				txt = strconv.Itoa(d)
			} else if star != "" {
				cls = " class=\"dim\""
			}
			bld.WriteString("<td" + cls + ">" + txt + star + "</td>")
		}
		rowSum := 0
		for _, oid := range oids {
			rowSum += buckets[h][oid]
		}
		bld.WriteString("<td class=\"total\">+" + strconv.Itoa(rowSum) + "</td>")
		bld.WriteString("</tr>")
	}
	// 当日合计行
	bld.WriteString("<tr class=\"sum\"><td>当日合计</td>")
	for _, oid := range oids {
		d := totalByOID[oid]
		txt := "+" + strconv.Itoa(d)
		if d < 0 {
			txt = strconv.Itoa(d)
		}
		bld.WriteString("<td>" + txt + "</td>")
	}
	bld.WriteString("<td class=\"total\">+" + strconv.Itoa(grandTotal) + "</td></tr>")
	bld.WriteString("</table>")
	bld.WriteString(`<p class="legend">单元格 = 该小时内净增签到数，红色为正增长。` +
		`带 * 表示该时段内该超话已破万：数字是<b>已确认的增量</b>，* 表示该区间内还有我们拿不到的额外增量（+0* 不等于没涨）。` +
		`跨日归零步与断采（间隔 &gt;15 分钟）不计入。</p>`)
	bld.WriteString("</div></body></html>")
	return renderHTMLToPNG(bld.String())
}

// signValueLabel 把签到数渲染成人读的值：破万档显示模糊值形式（如 1.1万），
// 因为破万后接口本来就只给千粒度，把 11000 写成「1.1万」更诚实，
// 也避免暗示这是精确值。
func signValueLabel(v int) string {
	if v >= 10000 {
		f := float64(v) / 10000
		return fmt.Sprintf("%.1f万", f)
	}
	return strconv.Itoa(v)
}

// spikeDetailWindowMin 异常明细表的时间跨度（分钟）。
//
// ★ 2026-10-08 用户明确：「我现在不想要半个小时的了，直接要一个小时的」。
//
//	窗口 = 异常时刻往前 1 小时，**异常时刻是表的最后一列**。
const spikeDetailWindowMin = 60

// signStatGroups 把配置里的分组转成 signstat 的分组类型。
func (b *Bot) signStatGroups() []signstat.Group {
	out := make([]signstat.Group, 0, len(b.cfg.WeiboSignMonitor.Groups))
	for _, g := range b.cfg.WeiboSignMonitor.Groups {
		out = append(out, signstat.Group{Name: g.Name, Members: g.Members})
	}
	return out
}

// spikeDetailWindow 算出这次异常的明细窗口（面板与推送 PNG 同一口径）。
func (b *Bot) spikeDetailWindow(spike signMonitorSpike) (signstat.DetailWindow, bool) {
	if b.signMonitor == nil {
		return signstat.DetailWindow{}, false
	}
	dw := signstat.WindowDetail(
		b.signMonitor.allSamples(),
		b.signStatGroups(),
		spike.ToTS-spikeDetailWindowMin*60*1000,
		spike.ToTS,
	)
	dw.OID = spike.OID
	dw.Name = spike.Name
	return dw, len(dw.Stamps) > 0
}

// buildSpikeDetailPNG 把「异常时段 5 分钟增量明细」这份**表**渲成 PNG。
//
// ★★ 这就是用户说的「那张图」（2026-10-08 原话）：
//
//	「我说了我不要真正的图了，我说的那个图是 PNG 的意思，
//	 就是你拿那个表的数据做成 PNG，发到飞书群、QQ 群里」
//
// 所以这里**不是**折线图也不是柱状图，就是那张表本身。
func (b *Bot) buildSpikeDetailPNG(spike signMonitorSpike, round int) []byte {
	dw, ok := b.spikeDetailWindow(spike)
	if !ok {
		return nil
	}
	htmlBody := buildSignMonitorDetailHTML(dw, spike, round)
	if htmlBody == "" {
		return nil
	}
	png, err := renderHTMLToPNG(htmlBody)
	if err != nil {
		log.Printf("[WeiboSignMonitor] 异常明细表渲染失败: %v", err)
		return nil
	}
	return png
}

// buildSignMonitorDetailHTML 把「异常时段 5 分钟增量明细」这份**表**渲成 HTML。
//
// 行 = 全部 8 个超话（异常者置顶、名字前加 ▲），列 = 时段内每个采样时刻，
// 格 = 该步增量，末列 = 这一小时净增。破万者破万后增量不可知，显示 +0*。
//
// ★ 数据全部来自 signstat.WindowDetail —— 面板上那张表就是同一份数据，
//
//	所以「面板上看到的」和「推送 PNG 上的」一定是同一个数字。
//	（2026-10-08 用户：别再自己另外画一张图，就要这份表渲成的 PNG。）
//
// round：这条异常已经推送到第几次（1 = 首次）。>1 时在标题标注是更新版，
// 免得收件人以为又冒出一条新异常。
func buildSignMonitorDetailHTML(dw signstat.DetailWindow, spike signMonitorSpike, round int) string {
	if len(dw.Stamps) == 0 {
		return ""
	}
	// 异常者置顶，其余保持 signstat 给的名字序（稳定，不抖动）
	rows := append([]signstat.DetailRow(nil), dw.Rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].OID == spike.OID && rows[j].OID != spike.OID
	})

	var bld strings.Builder
	bld.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><style>")
	bld.WriteString("body{margin:0;background:#fff;font-family:'PingFang SC','Microsoft YaHei',sans-serif;}")
	bld.WriteString("#report-card{padding:18px 20px;background:#fff;width:820px;box-sizing:border-box;}")
	bld.WriteString("h3{margin:0 0 4px;font-size:17px;color:#101828;}")
	bld.WriteString(".sub{margin:0 0 12px;font-size:12px;color:#98a2b3;}")
	bld.WriteString("table{border-collapse:collapse;width:100%;font-size:13px;}")
	bld.WriteString("th,td{border:1px solid #e4e7ec;padding:6px 8px;text-align:right;white-space:nowrap;}")
	bld.WriteString("th{background:#f5f7fb;color:#475467;font-weight:600;}")
	bld.WriteString("td:first-child,th:first-child{text-align:left;font-weight:600;color:#344054;}")
	bld.WriteString("td.net,th.net{background:#fcfcfd;font-weight:700;}")
	bld.WriteString("td.dim{color:#c0c6cf;}")
	bld.WriteString("td.hot{color:#b42318;font-weight:700;}")
	bld.WriteString("td.anom{background:#fef3f2;color:#b42318;font-weight:800;}")
	bld.WriteString("tr.anomrow td:first-child{color:#b42318;}")
	bld.WriteString(".note{margin:10px 0 0;font-size:12px;color:#98a2b3;line-height:1.6;}")
	bld.WriteString("</style></head><body>")
	bld.WriteString(`<div id="report-card" data-raw="1">`)
	title := fmt.Sprintf("异常时段 5 分钟增量明细 · %s", spike.Name)
	if round > 1 {
		title += fmt.Sprintf("（第 %d 次更新：同一段异常的后续数据已并入）", round)
	}
	bld.WriteString("<h3>⚠️ " + html.EscapeString(title) + "</h3>")
	bld.WriteString("<p class=\"sub\">" + html.EscapeString(fmt.Sprintf(
		"%s — %s（异常者置顶标 ▲，阈值为同组 ±%d；异常时刻在最后一列）",
		time.UnixMilli(dw.From).Format("01-02 15:04"),
		time.UnixMilli(dw.To).Format("15:04"),
		spike.GroupThreshold)) + "</p>")
	bld.WriteString("<table><tr><th>超话</th>")
	for _, ts := range dw.Stamps {
		bld.WriteString("<th>" + time.UnixMilli(ts).Format("15:04") + "</th>")
	}
	bld.WriteString("<th class=\"net\">净增</th>")
	bld.WriteString("</tr>")
	for _, m := range rows {
		cls := ""
		label := html.EscapeString(m.Name)
		if m.OID == spike.OID {
			cls = " class=\"anomrow\""
			label = "▲ " + label
		}
		bld.WriteString("<tr" + cls + "><td>" + label + "</td>")
		for ci, ts := range dw.Stamps {
			d := (*int)(nil)
			if ci < len(m.Deltas) {
				d = m.Deltas[ci]
			}
			mark := ""
			if ci < len(m.Marks) {
				mark = m.Marks[ci]
			}
			star := ""
			if mark == "crossover" {
				// 破万后的步：数字是**已知的下界**，星号表示还有额外增量未知。
				// 不能只写 +0* —— 那会把看得见的 1000 也丢掉（2026-10-08 用户纠正）。
				star = "*"
			}
			if d == nil {
				if star != "" {
					bld.WriteString("<td class=\"dim\">+0*</td>")
				} else {
					bld.WriteString("<td class=\"dim\">—</td>")
				}
				continue
			}
			c := ""
			if *d >= spike.GroupThreshold && spike.GroupThreshold > 0 {
				c = "hot"
			}
			if m.OID == spike.OID && ts >= spike.FromTS && ts <= spike.ToTS+5*60*1000 && *d > 0 {
				c = "anom"
			}
			bld.WriteString("<td class=\"" + c + "\">" + fmt.Sprintf("%+d", *d) + star + "</td>")
		}
		if m.Net == nil {
			bld.WriteString("<td class=\"net dim\">—</td>")
		} else {
			bld.WriteString("<td class=\"net\">" + fmt.Sprintf("%+d", *m.Net) + "</td>")
		}
		bld.WriteString("</tr>")
	}
	bld.WriteString("</table>")
	bld.WriteString("<p class=\"note\">* 已破万：数字是<b>已确认的增量</b>，* 表示该区间内还有拿不到的额外增量（+0 不等于没涨）。" +
		"— 表示该时刻无采样、与前一点间隔过大，或处在跨日归零那一步（差值无意义）。" +
		"橙色/红底格为超过阈值或落在异常时段内的增量。</p>")
	bld.WriteString("</div></body></html>")
	return bld.String()
}

// signMonitorChartHTML 把内联 SVG 包成给 html_to_png.mjs 的最小 HTML。
//
// ★ data-raw="1" 是关键（2026-10-07）：没有它时脚本找不到 #report-card，
// 会退回 fullPage 截图，高度至少是视口 1100px，而图表只有 ~280px 高
// ⇒ 图片下方 3/4 全是空白；随后 Pillow 又按 3:4 补边，空白更多。
// data-raw 让脚本走「紧凑截图 + 原图输出（不补边）」。
func signMonitorChartHTML(svg, title string) string {
	if svg == "" {
		return ""
	}
	return `<html><body style="margin:0;background:#fff">` +
		`<div id="report-card" data-raw="1" style="display:inline-block;background:#fff;padding:12px 14px">` +
		svg +
		`</div></body></html>`
}

// sendSignMonitorSpikeReport 异常即时报表（**只走邮件**）。
//
// ★★ 2026-10-08 用户定稿：推送里**只发那张表渲成的 PNG**，不再画任何图表。
//
//	「我说的我要的那个图，就是你直接拿那个表的数据生成一个图……
//	 我说的那个图是 PNG 的意思，就是你拿那个表的数据做成 PNG，
//	 发到飞书群、QQ 群里，是这个意思，懂吗？」
//
// 所以：
//   - 附件 = 异常时段明细表 PNG + 原始采样 CSV（**没有**柱状图 / 偏离图）
//   - 不再调 fanSignMonitorPNGs：飞书 / QQ 那条消息由 alertWeiboSignSpike
//     直接发（文本 + 同一张表 PNG），这里再扇一次只会变成重复的两条
//     （历史上就是这样 ⇒ 用户收到「你自己生成的那个图」）。
func (b *Bot) sendSignMonitorSpikeReport(spike signMonitorSpike, isUpdate bool) {
	if !signMonitorReportEnabled(b.cfg.WeiboSignMonitor, "spike") {
		return
	}
	store := b.signMonitor
	if store == nil {
		return
	}
	windowMin := b.cfg.WeiboSignMonitor.SpikeWindow()
	// round：这条异常推到第几次了（>1 = 补发），表标题要标出来。
	round := 1
	if isUpdate && b.signMonitor != nil {
		round = b.signMonitor.alertRecordCount(spike)
	}
	series, allSpikes := store.series(spike.At - int64(windowMin)*60*1000)
	if len(series) == 0 {
		return
	}
	// 找出这段时间增量最大的那一格作为中心
	peak := spike.At
	peakDelta := -1
	for _, s := range series {
		for i := 1; i < len(s.Points); i++ {
			if s.Points[i][0] > spike.At {
				break
			}
			d := int(s.Points[i][1] - s.Points[i-1][1])
			if d > peakDelta {
				peakDelta = d
				peak = s.Points[i][0]
			}
		}
	}
	from := time.UnixMilli(peak - int64(windowMin/2)*60*1000)
	to := time.UnixMilli(peak + int64(windowMin/2)*60*1000)

	attachments := make([]emailAttachment, 0, 3)
	// ① 异常时段明细表 PNG —— 与飞书 / QQ 那条消息里的是**同一张**
	//    （同一个 signstat.WindowDetail、同一个 HTML 渲染器）
	if png := b.buildSpikeDetailPNG(spike, round); len(png) > 0 {
		attachments = append(attachments, emailAttachment{
			Name:        fmt.Sprintf("weibo-sign-detail-%s.png", spike.Name),
			ContentType: "image/png",
			Data:        png,
		})
	} else {
		log.Printf("[WeiboSignMonitor] 异常明细表为空，邮件不带图")
	}
	// ② 原始采样 CSV
	attachments = append(attachments, emailAttachment{
		Name:        fmt.Sprintf("weibo-sign-spike-%s.csv", time.UnixMilli(peak).Format("20060102-1504")),
		ContentType: "text/csv; charset=utf-8",
		Data:        signMonitorCSV(series),
	})

	updateTag := ""
	if isUpdate {
		updateTag = "（更新）"
	}
	in := signMonitorReportInput{
		Title:           fmt.Sprintf("超话签到异常：%s%s", spike.Name, updateTag),
		Subtitle:        fmt.Sprintf("异常时段 %s — %s（采样间隔 %d 分钟）", from.Format("01-02 15:04"), to.Format("15:04"), int(b.cfg.WeiboSignMonitor.Interval().Minutes())),
		Groups:          b.signMonitorReportGroups(series),
		Spikes:          allSpikes,
		IntervalMinutes: int(b.cfg.WeiboSignMonitor.Interval().Minutes()),
		Since:           from, Until: to,
		AllSeries: series,
		// ★★★ 正文明细表**必须**与附件 PNG 用同一份 DetailWindow
		//   （2026-10-08 用户连着投诉这封邮件三次）。历史上有第二套独立
		//   实现，窗口靠「窗口内增量最大的那一格」猜 ⇒ 标题写郑伊安
		//   疑似异常、却贴出崔志宇的时段；破万判定也写成「历史上破没破过万」
		//   ⇒ 柳河岚 23:50 才破万，21:44 那几行被整行涂成「+0 已破万」。
		AnomOID:         spike.OID,
		DetailThreshold: spike.GroupThreshold,
	}
	if dw, ok := b.spikeDetailWindow(spike); ok {
		in.Detail = &dw
	}
	subject := fmt.Sprintf("超话签到异常 %s +%d%s", spike.Name, spike.Delta, updateTag)

	cfg := b.cfg.WeiboSignMonitor
	if !cfg.EmailReportEnabled {
		log.Printf("[WeiboSignMonitor] 邮件开关关闭，异常报表不发邮件（飞书/QQ 由告警消息直接发出）")
		return
	}
	if err := sendAdminHTMLEmail(b.cfg, subject, signMonitorReportHTML(in), "超话签到异常："+subject, attachments...); err != nil {
		log.Printf("[WeiboSignMonitor] 异常邮件发送失败: %v", err)
		return
	}
	log.Printf("[WeiboSignMonitor] 异常邮件已发送 subject=%s 附件=%d", subject, len(attachments))
}

// fanSignMonitorPNGs 把 PNG 附件扇出到配好的飞书 / QQ 去向。
//
// ★ 邮件与图片是**两条独立通道**：邮件关了图片照发（反之亦然），
//
//	这样「只想收图不想收邮件」也能配出来。
func fanSignMonitorPNGs(b *Bot, attachments []emailAttachment, cfg config.WeiboSignMonitorConfig, at time.Time) {
	targets := cfg.ImageTargets
	if len(targets) == 0 {
		targets = b.cfg.WeiboReportImageTargets
	}
	if len(targets) == 0 {
		return
	}
	sent := 0
	for _, att := range attachments {
		if att.ContentType != "image/png" {
			continue
		}
		for _, targetID := range targets {
			target := b.cfg.ResolveTarget(targetID)
			if target.ID == "" {
				continue
			}
			b.sendReportImage(target, att.Data, att.Name, at)
			sent++
		}
	}
	if sent > 0 {
		log.Printf("[WeiboSignMonitor] 异常图片扇出 %d 张 → %d 个去向", sent, len(targets))
	}
}

// runSignMonitorDailyReport 每天到点发一次汇总报表（覆盖当天 0:00 起的全部采样）。
func (b *Bot) runSignMonitorDailyReport() {
	cfg := b.cfg.WeiboSignMonitor
	if !signMonitorReportEnabled(cfg, "daily") {
		return
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	lastDate := ""
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if !b.cfg.WeiboSignMonitor.Enabled || !signMonitorReportEnabled(b.cfg.WeiboSignMonitor, "daily") {
			return
		}
		now := time.Now().In(loc)
		hour, minute := b.cfg.WeiboSignMonitor.DailyReportAt()
		if now.Hour() != hour || now.Minute() != minute {
			continue
		}
		today := now.Format("2006-01-02")
		if lastDate == today {
			continue
		}
		lastDate = today
		b.sendSignMonitorDailyReport(now, loc)
	}
}

// sendSignMonitorDailyReport 发送当日汇总。
func (b *Bot) sendSignMonitorDailyReport(now time.Time, loc *time.Location) {
	store := b.signMonitor
	if store == nil {
		return
	}
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UnixMilli()
	series, spikes := store.series(since)
	if len(series) == 0 {
		log.Printf("[WeiboSignMonitor] 当天没有采样数据，跳过日报")
		return
	}
	// ★★ spikes 只活在内存里（不落盘），所以重启后、以及 CLI 补发日报时
	//   必然是空的 —— 不重算，日报就会写「0 条异常」，而同一天面板上明明有一条。
	//   用 signstat 对当天数据重跑一遍即可，口径与面板/告警完全一致。
	if len(spikes) == 0 {
		until := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc).UnixMilli()
		spikes = b.recomputeDailySpikes(since, until)
		log.Printf("[WeiboSignMonitor] 日报异常重算 条数=%d", len(spikes))
	}
	// ★ 按组分别出图（2026-10-07）：8 个超话量级差 3 倍，全塞一张图会让
	// 三千档组贴着底部看不出走势。分组配置里没覆盖到的 oid 兜底进「未分组」。
	chartGroups := b.signMonitorReportGroups(series)
	if covered := map[string]bool{}; len(chartGroups) > 0 {
		for _, grp := range chartGroups {
			for _, s := range grp.Series {
				covered[s.OID] = true
			}
		}
		var orphans []signMonitorSeries
		for _, s := range series {
			if !covered[s.OID] {
				orphans = append(orphans, s)
			}
		}
		if len(orphans) > 0 {
			chartGroups = append(chartGroups, signMonitorReportGroup{Name: "未分组", Series: orphans})
		}
	} else {
		chartGroups = []signMonitorReportGroup{{Name: "全部超话", Series: series}}
	}

	attachments := make([]emailAttachment, 0, len(chartGroups)+1)
	// noChartNotes 收集「本次没能出图」的说明，渲染进邮件正文（黄色提示块）。
	noChartNotes := make([]string, 0, len(chartGroups))
	// ★ 日报不再发偏移图/任何截图（2026-10-08 用户定稿）：
	//   「日报也只发 PNG，不再截图了，只发当日的小时表」。
	//   偏移图、柱状图只在面板上看 —— 推送里塞图表只会收到
	//   「这图什么意思」的反馈；表格才是不需要解释的信息。
	_ = chartGroups

	// ★ 每小时总增量表：**自己生成 PNG**，不走截图（用户 2026-10-08 明确分工：
	//   「表格你直接生成 PNG 就行，应该还蛮好生成的」）。
	//   之前日报 HTML 里根本没有这张表（用户：我要的那个表也是没有的）。
	//   ★ PNG 与邮件正文 HTML 用**同一份** computeHourlyTable 数据。
	hourlyHTML := ""
	if tab, herr := computeHourlyTable(store.allSamples(), now); herr == nil {
		hourlyHTML = signMonitorHourlyTableHTML(tab, now)
		if hPNG, perr := buildHourlyTablePNG(store.allSamples(), now); perr == nil {
			attachments = append(attachments, emailAttachment{
				Name:        fmt.Sprintf("weibo-sign-daily-%s-hourly.png", now.Format("20060102")),
				ContentType: "image/png",
				Data:        hPNG,
			})
		} else {
			log.Printf("[WeiboSignMonitor] 每小时总增量表生成失败: %v", perr)
			noChartNotes = append(noChartNotes, "每小时总增量表生成失败")
		}
	} else {
		log.Printf("[WeiboSignMonitor] 每小时总增量表生成失败: %v", herr)
		noChartNotes = append(noChartNotes, "每小时总增量表生成失败")
	}

	attachments = append(attachments, emailAttachment{
		Name:        fmt.Sprintf("weibo-sign-daily-%s.csv", now.Format("20060102")),
		ContentType: "text/csv; charset=utf-8",
		Data:        signMonitorCSV(series),
	})
	in := signMonitorReportInput{
		Notes:           noChartNotes,
		Title:           fmt.Sprintf("超话签到日报 %s", now.Format("2006-01-02")),
		Subtitle:        fmt.Sprintf("分组「%s」· 0:00 至 %s · 采样间隔 %d 分钟", b.cfg.WeiboSignMonitor.GroupKey, now.Format("15:04"), int(b.cfg.WeiboSignMonitor.Interval().Minutes())),
		Groups:          b.signMonitorReportGroups(series),
		Spikes:          spikes,
		IntervalMinutes: int(b.cfg.WeiboSignMonitor.Interval().Minutes()),
		Since:           time.UnixMilli(since), Until: now,
		HourlyHTML: hourlyHTML,
	}
	// ★ 日报有异常时也带上「异常时段明细」，口径与异常邮件/PNG 完全一致。
	//   取最近那条异常的时段（异常者置顶高亮）。
	if len(spikes) > 0 {
		last := spikes[len(spikes)-1]
		// spikes 是按时间升序还是降序取决于构造，统一取 ToTS 最大的那条
		for _, sp := range spikes {
			if sp.ToTS >= last.ToTS {
				last = sp
			}
		}
		if dw, ok := b.spikeDetailWindow(last); ok {
			in.Detail = &dw
			in.AnomOID = last.OID
			in.DetailThreshold = last.GroupThreshold
		}
	}
	subject := in.Title
	if len(spikes) > 0 {
		subject += fmt.Sprintf("（%d 条异常）", len(spikes))
	}

	// ★ 面板开关（2026-10-08）：邮件与图片扇出各自独立控制。
	//
	// 之前是无条件发邮件、且图片从不发到飞书/QQ（只有文字告警），
	// 用户要求「能在面板上配置发到哪、要不要发」。
	cfg := b.cfg.WeiboSignMonitor
	if cfg.EmailReportEnabled {
		if err := sendAdminHTMLEmail(b.cfg, subject, signMonitorReportHTML(in), subject, attachments...); err != nil {
			log.Printf("[WeiboSignMonitor] 日报邮件发送失败: %v", err)
		} else {
			log.Printf("[WeiboSignMonitor] 日报邮件已发送 subject=%s", subject)
		}
	} else {
		log.Printf("[WeiboSignMonitor] 邮件开关关闭，跳过日报邮件")
	}

	// ★ 图片扇出到飞书 / QQ（用户 2026-10-08 要求）。
	//
	// 每组偏移图各发一张；去向为空时回落到 WEIBO_REPORT_IMAGE_TARGETS（保持历史行为）。
	targets := cfg.ImageTargets
	if len(targets) == 0 {
		targets = b.cfg.WeiboReportImageTargets
	}
	if len(targets) == 0 {
		return
	}
	sent := 0
	for _, att := range attachments {
		if att.ContentType != "image/png" {
			continue // CSV 只随邮件发
		}
		for _, targetID := range targets {
			target := b.cfg.ResolveTarget(targetID)
			if target.ID == "" {
				continue
			}
			b.sendReportImage(target, att.Data, att.Name, now)
			sent++
		}
	}
	log.Printf("[WeiboSignMonitor] 日报图片扇出 %d 张 → %d 个去向", sent, len(targets))
}

// signMonitorReportGroups 按配置的分组整理序列（报表里分组展示更易读）。
func (b *Bot) signMonitorReportGroups(series []signMonitorSeries) []signMonitorReportGroup {
	byOID := make(map[string]signMonitorSeries, len(series))
	for _, s := range series {
		byOID[s.OID] = s
	}
	out := make([]signMonitorReportGroup, 0, 4)
	for _, g := range b.cfg.WeiboSignMonitor.Groups {
		items := make([]signMonitorSeries, 0, len(g.Members))
		for _, member := range g.Members {
			if s, ok := byOID[member]; ok {
				items = append(items, s)
			}
		}
		if len(items) == 0 {
			continue
		}
		out = append(out, signMonitorReportGroup{Name: g.Name, Series: items, Members: g.Members})
	}
	return out
}

// buildCrossoverCutoffNote 生成「本次未出偏移图」的说明文案。
//
// ★ 用户 2026-10-07 14:48 要求：有效成员不足 2 人时不画图，
//
//	但**必须说明截止时间与原因** —— 否则图突然消失看起来像功能坏了。
//
// 参数：
//   - groupName 分组名
//   - valid    仍有效的成员（未破万），用于算截止时间
//   - crossed  已破万的成员数
//   - crossName 首个破万者名字（无则空）
//   - crossAt  首个破万时刻（UnixMilli，0表示未知）
func buildCrossoverCutoffNote(groupName string, valid []signMonitorSeries, crossed int, crossName string, crossAt int64) string {
	// 截止时间 = 最后一个有效成员的最后一个采样点
	var cutoff time.Time
	for _, s := range valid {
		if len(s.Points) == 0 {
			continue
		}
		last := time.UnixMilli(s.Points[len(s.Points)-1][0])
		if cutoff.IsZero() || last.After(cutoff) {
			cutoff = last
		}
	}
	cutoffText := "无有效采样"
	if !cutoff.IsZero() {
		cutoffText = cutoff.Format("01-02 15:04")
	}

	reason := "该组无有效采样数据"
	if crossed > 0 {
		reason = fmt.Sprintf("全部成员均已破万（签到数不再返回精确值）")
	}
	msg := fmt.Sprintf("%s：%s；偏移图无法继续绘制，组内偏移统计已截止至 %s",
		groupName, reason, cutoffText)
	if crossed > 0 && crossName != "" {
		at := "未知时刻"
		if crossAt > 0 {
			at = time.UnixMilli(crossAt).Format("01-02 15:04")
		}
		msg += fmt.Sprintf("（%s 等 %d 个已破万，最早破万时间 %s）", crossName, crossed, at)
	}
	return msg
}

// recomputeDailySpikes 用 signstat 重算 [since, until] 区间内的异常。
//
// spikes 只在内存里活着（不落盘），重启 / CLI 补发时必然为空。
// 不重算的话日报会写「0 条异常」，而同一天面板上明明有一条 ——
// 这种「两处口径不一致」正是最容易让人以为数据造假的地方。
func (b *Bot) recomputeDailySpikes(since, until int64) []signMonitorSpike {
	store := b.signMonitor
	if store == nil {
		return nil
	}
	groups := b.signStatGroups()
	if len(groups) == 0 {
		return nil
	}
	kept := make([]signstat.Sample, 0, 256)
	for _, s := range store.allSamples() {
		if s.TS >= since && s.TS <= until {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	res := signstat.AnalyzeSamples(kept, groups, signstat.Options{
		Threshold:        b.cfg.WeiboSignMonitor.SpikeAbs(),
		SuspectRatio:     b.cfg.WeiboSignMonitor.CrossoverSuspect(),
		FuzzyRatePerHour: b.cfg.WeiboSignMonitor.CrossoverFuzzyRate(),
	})
	out := make([]signMonitorSpike, 0, len(res.Anomalies))
	for _, a := range res.Anomalies {
		out = append(out, signMonitorSpike{
			OID: a.OID, Name: a.Name,
			From: a.From, To: a.To,
			Delta: a.TotalDelta, TotalDelta: a.TotalDelta,
			At: a.ToTS, FromTS: a.FromTS, ToTS: a.ToTS,
			GroupName: a.GroupName, GroupMedian: a.GroupMedian, GroupThreshold: a.Threshold,
			PeakDelta: a.PeakDelta, PeakTS: a.PeakTS, Steps: a.Steps,
			Severity: string(a.Severity), Reason: a.Reason,
			WindowMin: int(b.cfg.WeiboSignMonitor.Interval().Minutes()),
		})
	}
	return out
}

// ResendSignMonitorDailyReport 一次性补发**某一天**的签到监控日报（CLI / 排障用）。
//
// 为什么需要它：
//   - 日报每天只在 23:5x 发一次，改完样式要等 24 小时才能看到效果；
//   - 线上日报出过错（最后一整行 +0），需要按新口径重发一遍给用户确认。
//
// dateStr 形如 "2026-10-07"。补发的历史日期按当天 23:59 的口径出表；
// 补发当天则按当前时刻出表。
//
// ★ 这里**不调用 Start()**：不起定时循环、不连 NapCat/飞书长连，
//
//	只借用同一份 config 的出站通道，发完即退。
func ResendSignMonitorDailyReport(cfg *config.Config, dateStr string) error {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(dateStr), loc)
	if err != nil {
		return fmt.Errorf("bad date %q: %w", dateStr, err)
	}
	now := day.Add(23*time.Hour + 59*time.Minute)
	if day.Format("2006-01-02") == time.Now().In(loc).Format("2006-01-02") {
		now = time.Now().In(loc)
	}

	retention := cfg.WeiboSignMonitor.Retention()
	dir := filepath.Join(storageRootOf(cfg.ConfigPath()), "weibo")
	store := newSignMonitorStore(filepath.Join(dir, signMonitorFile), retention)
	if err := store.load(); err != nil {
		return fmt.Errorf("load samples: %w", err)
	}
	if store.Len() == 0 {
		return fmt.Errorf("no samples loaded from %s", store.path)
	}

	bot := NewBot(cfg)
	bot.signMonitor = store
	log.Printf("[WeiboSignMonitor] 补发日报 date=%s 采样=%d 条", day.Format("2006-01-02"), store.Len())
	bot.sendSignMonitorDailyReport(now, loc)

	// ★ 出站是异步队列（QQ/飞书各有 worker），进程一退队列里的消息就没了。
	//   之前就因为这个丢过一次告警图片，这里必须轮询等到排空再退出。
	for i := 0; i < 60; i++ {
		if bot.outbound == nil || bot.outbound.QueueDepth() == 0 {
			break
		}
		time.Sleep(time.Second)
	}
	fmt.Printf("ok: resent sign-monitor daily report for %s (%d samples)\n",
		day.Format("2006-01-02"), store.Len())
	return nil
}
