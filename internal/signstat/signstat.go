// Package signstat 是超话签到监测的**唯一统计口径实现**。
//
// ★★ 为什么单独成包：这份逻辑历史上在三个地方各写了一遍——
//
//	bot 侧   internal/logic/weibo_sign_monitor.go      （单步偏离口径）
//	admin 侧 internal/admin/weibo_sign_monitor.go      （30 分钟窗口口径）
//	前端     admin-ui/src/pages/SignMonitor.tsx        （硬编码阈值 400）
//
// 三套判据各不相同的直接后果（都已实际发生）：
//   - 面板显示的异常与邮件发出的异常**结论不同**
//   - 阈值 800 高于全部真实波动（实测 p99=70）⇒ 告警**从未触发过**
//   - 前端图上的橙线与后端告警线不是同一个数
//
// 现在 bot 与 admin 都调用本包，前端只负责渲染。
package signstat

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// CrossoverSign 超话签到破万门槛。
//
// 破万后微博接口不再返回精确值，只给 1000 粒度的模糊值。实测形态：
//
//	精确值涨到 9993 → 卡在 10000 不动（连续多个采样点都是 10000）
//	→ 某时刻整千跳变（10000 → 11000）→ 又卡住不动
//
// 所以破万**不等于这条数据消失**，只是拿不到细节了（见 Series.CrossoverAt）。
const CrossoverSign = 10000

// DefaultThreshold 单步偏离的默认告警阈值。
//
// ★ 这个数字是**按真实数据定的**，不是拍的：
// 实测 10-07 全天 1912 个采样点，第一组「本步增量 - 同组中位数」的分布是
//
//	p50=4   p90=14   p99=70   max=567
//
// 原配置的 800 比真实刷量（ChoiJiwoo 22:14 的 +614）还高 ⇒ 告警从未触发。
// 取 150 = p99 的约 2 倍，既能抓住 +351/+564，误报又极少。
const DefaultThreshold = 150

// DefaultSuspectRatio 破万者「疑似异常」的速度倍数门槛。
//
// 破万者拿不到精确数据 ⇒ 只能看**速度**：拿它破万前自己的平均小时速度当基线，
// 如果模糊期隐含的速度远超基线，就说明「1 万 → 1 万 1」发生得比正常增长快得多
// ⇒ 疑似有人为干预。速度正常则完全不判。
//
// 实测郑伊安：破万前约 330 人/小时，10000→11000 用了 41 分钟
// ⇒ 隐含约 1463 人/小时 ≈ 基线的 4.4 倍 ⇒ 超阈值 ⇒ 疑似。
const DefaultSuspectRatio = 3.0

// DefaultFuzzyRatePerHour 破万后「整千跳变」的绝对小时速度门槛。
//
// ★ 1000 的来历（2026-10-08 用真实数据校准）：
//
//	跑完 8 个超话 29 小时采样，正常量级参考：
//	  未破万超话全天平均12~34 人/小时（凌晨低峰），
//	  活跃时段也就「一小时几百」；
//	  而郑伊安那次 40 分钟涨 1000 = 1494 人/小时。
//	门槛取 1000 ⇒ 抓得住那次，又不会把正常的几百误判。
//
// 判据用**绝对速度**而不是只看「相对自身基线」：用户原话「按照正常
// 情况来说，一个小时涨个几百才是正常的，它那几十分钟就涨了 1K，
// 这也是一个疑似异常的点」—— 异常是这一段本身太快，
// 不该被它自己历史节奏（临破万前冲榜本来就快）带偏。
const DefaultFuzzyRatePerHour = 1000.0

// ★★★ 超话年龄顺序（2026-10-08 用户指定）——**所有出口统一按这个排**。
//
// 用户原话：「不管是表格还是图表，如果要把 8 个超话都列出来，顺序是需要
// 固定的。就直接按她们的年龄顺序来排，好吗？这样比较清晰一点。」
//
// 顺序（1 → 8）：
//
//	Carmen / ChoiJiwoo / 柳河岚YUHA / stella / JUUN / ANA卢惟那 / 郑伊安IAN / YEON金奈延
//
// 为什么放 signstat：面板明细表、飞书/QQ 推送 PNG、邮件正文、小时表、
// CSV 附件都涉及 8 人排序。以前每处各自 sort.Slice(按名字)，
// 结果每张表的行序都不一样 —— 同一份数据看起来像几种东西。
//
// 未收录的名字排到末尾，再按名字排（不丢数据，也不乱序）。
var signMonitorAgeOrder = []string{
	"carmen",
	"choijiwoo",
	"柳河岚",
	"stella",
	"juun",
	"ana",
	"郑伊安",
	"yeon",
}

// signMonitorAgeRank 返回超话名的年龄序号；不在表里返回 len(顺序表)。
func signMonitorAgeRank(name string) int {
	lower := strings.ToLower(name)
	for i, key := range signMonitorAgeOrder {
		if strings.Contains(lower, key) {
			return i
		}
	}
	return len(signMonitorAgeOrder)
}

// AgeRank 是 signMonitorAgeRank 的导出版（logic 侧排小时表列要用）。
func AgeRank(name string) int { return signMonitorAgeRank(name) }

// SortByAge 按年龄顺序就地排序（稳定）。所有「列出 8 个超话」的地方都用它。
func SortByAge(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		ri, rj := signMonitorAgeRank(names[i]), signMonitorAgeRank(names[j])
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
}

// SortByAgeDetailRows 按年龄顺序就地排序明细行。
func SortByAgeDetailRows(rows []DetailRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := signMonitorAgeRank(rows[i].Name), signMonitorAgeRank(rows[j].Name)
		if ri != rj {
			return ri < rj
		}
		return rows[i].Name < rows[j].Name
	})
}

// crossoverBaselinePoints 破万前基线取多少个精确采样点（2 小时）。
//
// ★ 24 个点 = 2 小时（采样间隔 5 分钟）。取值理由见
//
//	detectSuspectedCrossover 里的说明：太短的窗口取到的正好是
//	破万前的冲刺段，基线偏高会漏掉明显偏快的整千跳变。
const crossoverBaselinePoints = 24

// loc 是签到数的业务时区。跨日归零判定必须用它，不能用服务器本地时区。
var loc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()

// Point 是一个采样点。
type Point struct {
	TS   int64 `json:"ts"`
	Sign int   `json:"sign"`
}

// Sample 是 jsonl 的一行原始采样。
type Sample struct {
	TS   int64  `json:"ts"`
	OID  string `json:"oid"`
	Name string `json:"name"`
	Sign int    `json:"sign"`
}

// Series 是一个超话在查询范围内的序列，外加破万信息。
type Series struct {
	OID    string  `json:"oid"`
	Name   string  `json:"name"`
	Points []Point `json:"points"`

	// CrossoverAt 是**第一次** ≥ CrossoverSign 的时刻；0 表示从未破万。
	// ★ 破万后曲线**不删除**，只是不再参与偏移/中位数/异常统计
	//   （画到破万点为止，见 AnalysisPoints）。
	CrossoverAt int64 `json:"crossoverAt"`

	// CrossoverFrom 是破万前的最后一个精确值。
	CrossoverFrom int `json:"crossoverFrom"`
}

// Crossed 报告该序列是否破过万。
func (s Series) Crossed() bool { return s.CrossoverAt > 0 }

// AnalysisPoints 返回**参与偏移统计**的采样点：破万点之前（不含）的精确段。
//
// ★ 这就是「破万不是剔除，是曲线停住」的实现：
//   - 曲线照画（前端拿完整 Points）
//   - 但统计只用这段精确数据
func (s Series) AnalysisPoints() []Point {
	if s.CrossoverAt == 0 {
		return s.Points
	}
	cut := -1
	for i, p := range s.Points {
		if p.TS >= s.CrossoverAt {
			cut = i
			break
		}
	}
	if cut <= 0 {
		if cut == 0 {
			return nil
		}
		return s.Points
	}
	return s.Points[:cut]
}

// Group 是一个分组（量级相近的成员放一起，才有意义做组内对比）。
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// Options 控制统计口径。
type Options struct {
	// Threshold 单步偏离的绝对阈值。<=0 时用 DefaultThreshold。
	Threshold int

	// MinGroupSize 组内至少要有多少个「未破万且有数据」的成员才判异常。
	// 默认 2（1 个人没有参照系）。
	MinGroupSize int

	// SuspectRatio 破万者疑似异常的速度倍数。<=0 时用 DefaultSuspectRatio。
	SuspectRatio float64

	// FuzzyRatePerHour 破万后「整千跳变」的绝对小时速度门槛。
	// <=0 时用 DefaultFuzzyRatePerHour。
	//
	// ★ 这是**主判据**（2026-10-08 用户纠正）：
	//   判据必须只看异常发生的那一段本身。用户原话：「它那个时间拉得比较
	//   正常，我们压根就不会判断它异常」—— 拿它自己的历史基线当唯一尺子
	//   会被「临破万前冲榜本来就快」带偏（实测 30 分钟基线 562/小时 vs
	//   实际跳变 1494/小时，ratio 只有 2.66，正好卡在 3 以下漏掉）。
	//   绝对速度直接回答「这一段本身快不快」，与历史基线无关。
	FuzzyRatePerHour float64
}

func (o Options) threshold() int {
	if o.Threshold <= 0 {
		return DefaultThreshold
	}
	return o.Threshold
}

func (o Options) minGroup() int {
	if o.MinGroupSize <= 0 {
		return 2
	}
	return o.MinGroupSize
}

func (o Options) suspectRatio() float64 {
	if o.SuspectRatio <= 0 {
		return DefaultSuspectRatio
	}
	return o.SuspectRatio
}

func (o Options) fuzzyRate() float64 {
	if o.FuzzyRatePerHour <= 0 {
		return DefaultFuzzyRatePerHour
	}
	return o.FuzzyRatePerHour
}

// Severity 区分确定异常与疑似异常。
type Severity string

const (
	// SeverityConfirmed 精确数据上的离群，可确定。
	SeverityConfirmed Severity = "confirmed"
	// SeveritySuspected 破万者的模糊速度异常，只是疑似。
	SeveritySuspected Severity = "suspected"
)

// Anomaly 是一次异常。
//
// ★ 连续多个采样点超阈值会**合并成一条**（见 Detect）。
// 历史上是逐点判断 + 10 分钟去抖，一次连续刷量会被拆成 3 条
// （实测 22:09 +351 / 22:14 +564 / 22:19 +71 其实是同一次）。
type Anomaly struct {
	OID       string `json:"oid"`
	Name      string `json:"name"`
	GroupName string `json:"groupName"`

	FromTS int64 `json:"fromTs"`
	ToTS   int64 `json:"toTs"`
	From   int   `json:"from"`
	To     int   `json:"to"`

	// TotalDelta 整段累计增量（To - From）。
	TotalDelta int `json:"totalDelta"`
	// PeakDelta 期间最大的单步偏离（判严重程度用）。
	PeakDelta int `json:"peakDelta"`
	// PeakTS 峰值时刻。
	PeakTS int64 `json:"peakTs"`
	// Steps 合并了多少个超阈值的采样点。
	Steps int `json:"steps"`

	GroupMedian int      `json:"groupMedian"`
	Threshold   int      `json:"threshold"`
	Reason      string   `json:"reason"`
	Severity    Severity `json:"severity"`

	// RatePerHour / BaselinePerHour 仅疑似异常有值。
	RatePerHour     float64 `json:"ratePerHour,omitempty"`
	BaselinePerHour float64 `json:"baselinePerHour,omitempty"`
}

// step 是一个采样点的单步增量。
type step struct {
	ts    int64
	from  int
	to    int
	delta int
}

// DaySegment 是按天分段的结果。
type DaySegment struct {
	Day   string
	Start int64
	End   int64
	Steps []step // 跨日归零的那一步**已被剔除**
}

// splitByDay 把序列按业务日分段，并在段内算单步增量。
//
// ★★ 跨日归零是这套数据的固有特征，不是异常：
//
//	实测 10-08 00:05，8 个超话同时暴跌：郑伊安 -10507、柳河岚 -9562、
//	ChoiJiwoo -9501、stella -8797、ANA -5639、JUUN -5376、YEON -3544、Carmen -2978。
//
// 这是签到数按天归零。若不剔除，选「近 24 小时」及以上时这一步会以
// -10000 量级的偏离进图，把 Y 轴撑爆（正常波动只有 ±14，被压成贴底直线），
// 同时还会触发一条「史上最大异常」告警。
//
// 所以跨到新一天的那一步**直接丢弃**，不参与任何统计。
func splitByDay(points []Point) []DaySegment {
	if len(points) == 0 {
		return nil
	}
	dayOf := func(ts int64) string {
		return time.UnixMilli(ts).In(loc).Format("2006-01-02")
	}
	segs := []DaySegment{}
	cur := DaySegment{Day: dayOf(points[0].TS), Start: points[0].TS, Steps: []step{}}
	for i := 1; i < len(points); i++ {
		prev, p := points[i-1], points[i]
		if dayOf(prev.TS) != dayOf(p.TS) {
			cur.End = prev.TS
			segs = append(segs, cur)
			cur = DaySegment{Day: dayOf(p.TS), Start: p.TS, Steps: []step{}}
			continue
		}
		cur.End = p.TS
		cur.Steps = append(cur.Steps, step{
			ts: p.TS, from: prev.Sign, to: p.Sign, delta: p.Sign - prev.Sign,
		})
	}
	if len(cur.Steps) > 0 || len(segs) == 0 {
		segs = append(segs, cur)
	}
	return segs
}

// medianInt64 取中位数，偶数个**取中间两个的平均**。
//
// ★ 不要写 sorted[n/2]——那是第三小，对 4 人组会偏 +250~4400，
// 人为压小偏离、让异常显得更小。
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	s := append([]int64(nil), values...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// DeviationPoint 是给图表用的一条偏离数据。
type DeviationPoint struct {
	TS   int64  `json:"ts"`
	OID  string `json:"oid"`
	Name string `json:"name"`
	Dev  int    `json:"dev"`
	Step int    `json:"step"`
	// Median 是这一步的基线（同组中位数，或 2 人组时对方那一步的增量）。
	Median int `json:"median"`
	// From/To 是这一步前后的**绝对签到数**（异常文案要用）。
	From int `json:"from"`
	To   int `json:"to"`
}

// DevBounds 是偏离极值（画 Y 轴用；正负共用同一 bound ⇒ 0 永远居中）。
type DevBounds struct {
	Pos int `json:"pos"`
	Neg int `json:"neg"`
}

// ExcludedMember 描述一个「破万后不再参与后续偏移统计」的成员。
//
// ★ 用词很关键：不是「破万者不计入偏移统计」（用户明确指出这说法有问题），
// 而是「破万前计入、破万后不再参与偏移计算」。曲线仍然画到破万点为止。
type ExcludedMember struct {
	OID         string `json:"oid"`
	Name        string `json:"name"`
	CrossoverAt int64  `json:"crossoverAt"`
	From        int    `json:"from"`
}

// GroupDeviation 是一组的偏离序列（图表主数据）。
type GroupDeviation struct {
	GroupName string                      `json:"groupName"`
	Stamps    []int64                     `json:"stamps"`
	Members   []string                    `json:"members"`
	Names     map[string]string           `json:"names"`
	Series    map[string][]DeviationPoint `json:"series"`
	Bounds    DevBounds                   `json:"bounds"`

	// Excluded 是破万成员，**曲线仍画**，只是统计不参与。
	Excluded []ExcludedMember `json:"excluded"`
}

// deviationFor 为一组算偏离序列。
//
// 口径（图表 / 告警 / 邮件必须完全一致，都走这里）：
//
//		dev[t] = (v[t] - v[t-1]) - median_k( v_k[t] - v_k[t-1] )
//
//	  - 2 人：基线 = 对方那一步的增量（不存在中位数，两线必定互为相反数）
//	  - ≥3 人：基线 = 组内中位数（偶数个取平均）
//	  - 破万者：曲线照画，但**不参与**基线与偏离计算
//	  - 跨日归零步：不参与
//
// ★ 自检金律：偏离必定正负各半。出现全正 = 基线算错了。
func deviationFor(groupName string, all []Series, opts Options) (GroupDeviation, bool) {
	// 拆成「参与统计」与「仅画图」两类
	live := make([]Series, 0, len(all))
	excluded := []ExcludedMember{}
	for _, s := range all {
		if s.Crossed() {
			excluded = append(excluded, ExcludedMember{
				OID: s.OID, Name: s.Name, CrossoverAt: s.CrossoverAt, From: s.CrossoverFrom,
			})
			continue
		}
		if len(s.Points) >= 2 {
			live = append(live, s)
		}
	}
	if len(live) < opts.minGroup() {
		return GroupDeviation{}, false
	}

	// ★ 破万者也要**画**进图里（2026-10-08 用户明确指出）：
	//   「破万者整条消失」是错的 —— 曲线应该画到破万点为止，
	//   只是破万之后不再参与偏移/中位数/异常统计。
	//
	// 所以分两类：
	//   oids(live) → 参与基线计算 + 画图
	//   crossedOIDs → **只画图**，画到破万点为止，不参与基线
	//
	// 之前把破万者直接 continue 掉、只在 excluded 留个名字，
	// 前端只认 Series ⇒ 图上只剩未破万的人。
	crossedOIDs := []string{}
	crossedAt := map[string]int64{}
	for _, e := range excluded {
		crossedOIDs = append(crossedOIDs, e.OID)
		crossedAt[e.OID] = e.CrossoverAt
	}

	names := map[string]string{}
	oids := []string{}
	perMember := map[string]map[string]DaySegment{}
	for _, s := range live {
		names[s.OID] = s.Name
		oids = append(oids, s.OID)
		m := map[string]DaySegment{}
		for _, seg := range splitByDay(s.Points) {
			m[seg.Day] = seg
		}
		perMember[s.OID] = m
	}
	// 破万者同样按天分段存好供画图取用（**不**计入 dayCount）
	for _, oid := range crossedOIDs {
		for _, s := range all {
			if s.OID != oid {
				continue
			}
			names[s.OID] = s.Name
			m := map[string]DaySegment{}
			for _, seg := range splitByDay(s.Points) {
				m[seg.Day] = seg
			}
			perMember[s.OID] = m
		}
	}

	// 只取「所有**参与统计的成员**都有数据」的天，保证同一时刻人人可比。
	//
	// ★ 必须显式遍历 oids(live)，不能遍历整个 perMember ——
	//   perMember 里还有只画图的破万者，把他们数进去会让
	//   「c == len(live)」永远不成立 ⇒ days 为空 ⇒ 整组出不了图。
	dayCount := map[string]int{}
	for _, oid := range oids {
		for day := range perMember[oid] {
			dayCount[day]++
		}
	}
	days := []string{}
	for day, c := range dayCount {
		if c == len(live) {
			days = append(days, day)
		}
	}
	sort.Strings(days)

	out := GroupDeviation{
		GroupName: groupName, Names: names, Members: oids,
		Series:   map[string][]DeviationPoint{},
		Stamps:   []int64{},
		Excluded: excluded,
	}
	for _, oid := range oids {
		out.Series[oid] = []DeviationPoint{}
	}
	// 破万者也要有位置（哪怕是空的），否则前端遍历 Series 时看不到人
	for _, oid := range crossedOIDs {
		out.Series[oid] = []DeviationPoint{}
	}
	if len(days) == 0 {
		return GroupDeviation{}, false
	}

	for _, day := range days {
		// 该天各成员的时刻并集
		stampSet := map[int64]bool{}
		for _, oid := range oids {
			for _, st := range perMember[oid][day].Steps {
				stampSet[st.ts] = true
			}
		}
		stamps := make([]int64, 0, len(stampSet))
		for ts := range stampSet {
			stamps = append(stamps, ts)
		}
		sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })

		for _, ts := range stamps {
			idx := map[string]step{}
			ok := true
			for _, oid := range oids {
				found := false
				for _, st := range perMember[oid][day].Steps {
					if st.ts == ts {
						idx[oid] = st
						found = true
						break
					}
				}
				if !found {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			deltas := make([]int64, 0, len(oids))
			for _, oid := range oids {
				deltas = append(deltas, int64(idx[oid].delta))
			}
			groupMedian := int(medianInt64(deltas))
			for _, oid := range oids {
				st := idx[oid]
				base := groupMedian
				if len(oids) == 2 {
					other := oids[0]
					if oid == other {
						other = oids[1]
					}
					base = idx[other].delta
				}
				dev := st.delta - base
				out.Series[oid] = append(out.Series[oid], DeviationPoint{
					TS: ts, OID: oid, Name: names[oid],
					Dev: dev, Step: st.delta, Median: base,
					From: st.from, To: st.to,
				})
				if dev > out.Bounds.Pos {
					out.Bounds.Pos = dev
				}
				if -dev > out.Bounds.Neg {
					out.Bounds.Neg = -dev
				}
			}

			// ★ 破万者：曲线画到破万点为止。
			//
			// 基线用**同组未破万者的中位数**（自己不参与基线，否则
			// 破万后「卡在 10000 不动」会把中位数拉成 0，害别人）。
			// 超过 CrossoverAt 之后不再产出点 ⇒ 线自然停住，
			// 既不消失、也不会把模糊值画成假数据。
			for _, oid := range crossedOIDs {
				if ts > crossedAt[oid] {
					continue
				}
				var st step
				found := false
				seg := perMember[oid][day]
				for _, x := range seg.Steps {
					if x.ts == ts {
						st = x
						found = true
						break
					}
				}
				if !found {
					continue
				}
				base := groupMedian
				dev := st.delta - base
				out.Series[oid] = append(out.Series[oid], DeviationPoint{
					TS: ts, OID: oid, Name: names[oid],
					Dev: dev, Step: st.delta, Median: base,
					From: st.from, To: st.to,
				})
				if dev > out.Bounds.Pos {
					out.Bounds.Pos = dev
				}
				if -dev > out.Bounds.Neg {
					out.Bounds.Neg = -dev
				}
			}
			out.Stamps = append(out.Stamps, ts)
		}
	}
	if len(out.Stamps) == 0 {
		return GroupDeviation{}, false
	}
	return out, true
}

// AnalyzeSamples 对原始采样 + 分组做完整分析（bot 侧入口）。
func AnalyzeSamples(samples []Sample, groups []Group, opts Options) Result {
	return AnalyzeSeries(BuildSeries(samples), groups, opts)
}

// BuildSeries 把原始采样聚成序列，并标记破万时刻。
//
// ★ CrossoverAt 取**第一次** ≥ 10000 的时刻（正序扫）。
// 不能倒序扫取最后一个：模糊值会反复跳（10000 → 11000 → 10000），
// 倒序会记成最后一次跳变，文案里的「破万时间」就是错的。
func BuildSeries(samples []Sample) []Series {
	byOID := map[string][]Point{}
	names := map[string]string{}
	order := []string{}
	for _, s := range samples {
		if _, ok := byOID[s.OID]; !ok {
			order = append(order, s.OID)
		}
		byOID[s.OID] = append(byOID[s.OID], Point{TS: s.TS, Sign: s.Sign})
		if s.Name != "" {
			names[s.OID] = s.Name
		}
	}
	out := make([]Series, 0, len(order))
	for _, oid := range order {
		pts := byOID[oid]
		sort.Slice(pts, func(i, j int) bool { return pts[i].TS < pts[j].TS })
		item := Series{OID: oid, Name: names[oid], Points: pts}
		for i, p := range pts {
			if p.Sign >= CrossoverSign {
				item.CrossoverAt = p.TS
				if i > 0 {
					item.CrossoverFrom = pts[i-1].Sign
				}
				break
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Points) != len(out[j].Points) {
			return len(out[i].Points) > len(out[j].Points)
		}
		return out[i].OID < out[j].OID
	})
	return out
}

// Result 是面板/报表需要的全量分析结果。
type Result struct {
	Groups    []GroupDeviation `json:"groups"`
	Anomalies []Anomaly        `json:"anomalies"`
	Options   Options          `json:"options"`

	// Series 是**完整**曲线（含破万后的模糊段），供前端绘制。
	// ★ 破万者不会被剔除，只是统计不参与。
	Series []Series `json:"series"`
}

// AnalyzeSeries 对已构建的序列做分析。
func AnalyzeSeries(all []Series, groups []Group, opts Options) Result {
	byOID := map[string]Series{}
	for _, s := range all {
		byOID[s.OID] = s
	}

	res := Result{Options: opts, Series: all, Groups: []GroupDeviation{}, Anomalies: []Anomaly{}}

	// 没配分组时，把所有未破万成员当成一组
	if len(groups) == 0 {
		members := []string{}
		for _, s := range all {
			if !s.Crossed() && len(s.Points) >= 2 {
				members = append(members, s.OID)
			}
		}
		if len(members) > 0 {
			groups = []Group{{Name: "全部分组", Members: members}}
		}
	}

	for _, g := range groups {
		members := []Series{}
		for _, oid := range g.Members {
			if s, ok := byOID[oid]; ok && len(s.Points) >= 1 {
				members = append(members, s)
			}
		}
		if len(members) == 0 {
			continue
		}
		dev, ok := deviationFor(g.Name, members, opts)
		if ok {
			res.Groups = append(res.Groups, dev)
		}
		res.Anomalies = append(res.Anomalies, detectInGroup(g.Name, members, opts)...)
	}

	// 破万者的疑似异常（速度判据）
	res.Anomalies = append(res.Anomalies, detectSuspectedCrossover(all, opts)...)

	// 最近的异常排前面；同一时刻按**幅度**从大到小
	// （★ 不能直接比 PeakDelta：负值会排到正值前面，
	//   导致「某人多涨」被「某人少涨」挤掉）。
	sort.Slice(res.Anomalies, func(i, j int) bool {
		if res.Anomalies[i].FromTS != res.Anomalies[j].FromTS {
			return res.Anomalies[i].FromTS > res.Anomalies[j].FromTS
		}
		return abs64(int64(res.Anomalies[i].PeakDelta)) > abs64(int64(res.Anomalies[j].PeakDelta))
	})
	return res
}

// detectInGroup 在一组内找异常，并把连续超阈值点合并成一条。
func detectInGroup(groupName string, members []Series, opts Options) []Anomaly {
	live := []Series{}
	for _, s := range members {
		if !s.Crossed() && len(s.Points) >= 2 {
			live = append(live, s)
		}
	}
	if len(live) < opts.minGroup() {
		return nil
	}
	thresh := opts.threshold()

	// 逐成员算偏离（与图完全同口径），再合并连续点
	perOID := map[string][]DeviationPoint{}
	names := map[string]string{}
	oids := []string{}
	perMember := map[string]map[string]DaySegment{}
	for _, s := range live {
		names[s.OID] = s.Name
		oids = append(oids, s.OID)
		m := map[string]DaySegment{}
		for _, seg := range splitByDay(s.Points) {
			m[seg.Day] = seg
		}
		perMember[s.OID] = m
	}

	dayCount := map[string]int{}
	for _, m := range perMember {
		for day := range m {
			dayCount[day]++
		}
	}
	days := []string{}
	for day, c := range dayCount {
		if c == len(live) {
			days = append(days, day)
		}
	}

	out := []Anomaly{}
	for _, day := range days {
		stampSet := map[int64]bool{}
		for _, oid := range oids {
			for _, st := range perMember[oid][day].Steps {
				stampSet[st.ts] = true
			}
		}
		stamps := make([]int64, 0, len(stampSet))
		for ts := range stampSet {
			stamps = append(stamps, ts)
		}
		sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })

		byOIDDev := map[string][]DeviationPoint{}
		for _, oid := range oids {
			byOIDDev[oid] = []DeviationPoint{}
		}
		for _, ts := range stamps {
			idx := map[string]step{}
			ok := true
			for _, oid := range oids {
				found := false
				for _, st := range perMember[oid][day].Steps {
					if st.ts == ts {
						idx[oid] = st
						found = true
						break
					}
				}
				if !found {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			deltas := make([]int64, 0, len(oids))
			for _, oid := range oids {
				deltas = append(deltas, int64(idx[oid].delta))
			}
			gm := int(medianInt64(deltas))
			for _, oid := range oids {
				st := idx[oid]
				base := gm
				if len(oids) == 2 {
					other := oids[0]
					if oid == other {
						other = oids[1]
					}
					base = idx[other].delta
				}
				byOIDDev[oid] = append(byOIDDev[oid], DeviationPoint{
					TS: ts, OID: oid, Name: names[oid],
					Dev: st.delta - base, Step: st.delta, Median: base,
					From: st.from, To: st.to,
				})
			}
		}

		// 合并连续超阈值点：一次连续刷量只报一条
		// （实测 22:09 +351 / 22:14 +564 / 22:19 +71 是同一次刷量的三个采样点）
		for _, oid := range oids {
			perOID[oid] = append(perOID[oid], byOIDDev[oid]...)
			var cur *Anomaly
			var prevTS int64
			flush := func() {
				if cur != nil {
					out = append(out, *cur)
					cur = nil
				}
			}
			for _, p := range byOIDDev[oid] {
				hit := abs64(int64(p.Dev)) >= int64(thresh)
				// ★ 合并条件（三条都要满足）：
				//   1. 间隔 ≤1 个采样点（15 分钟内）
				//   2. **方向一致** —— 刷量后回落到趋势线那一步是反向的，
				//      它确实是另一件事（一次瞬时刷量的痕迹），
				//      不该被并进「多涨」那条里。
				//   3. 已有未结束的段
				sameDir := cur != nil && (p.Dev > 0) == (cur.PeakDelta > 0)
				if hit && cur != nil && sameDir && p.TS-prevTS <= 15*60*1000 {
					cur.ToTS = p.TS
					cur.TotalDelta += p.Step
					cur.To = cur.From + cur.TotalDelta
					cur.Steps++
					// ★ 取**幅度**最大的那一步作为峰值。
					//   不能只看正负：单点刷量的回落步（-600）幅度同样大，
					//   但它不是「峰值」，写进去会让文案说「峰值 -600」。
					if abs64(int64(p.Dev)) > abs64(int64(cur.PeakDelta)) {
						cur.PeakDelta = p.Dev
						cur.PeakTS = p.TS
					}
					prevTS = p.TS
					continue
				}
				flush()
				prevTS = p.TS
				if !hit {
					continue
				}
				cur = &Anomaly{
					OID: oid, Name: names[oid], GroupName: groupName,
					FromTS: p.TS, ToTS: p.TS,
					// ★ From 存「异常开始前的签到数」（= 本步增量前的值），
					//   不是本步增量本身。这样 To = From + TotalDelta 恒成立。
					From: p.From, To: p.To,
					TotalDelta: p.Step, PeakDelta: p.Dev, PeakTS: p.TS, Steps: 1,
					GroupMedian: p.Median, Threshold: thresh, Severity: SeverityConfirmed,
					Reason: reasonFor(len(oids)),
				}
			}
			flush()
		}
	}

	// ★ 2 人组：两条偏离是互为相反数的**同一件事**
	//（谁多涨就等于谁少涨）。只报幅度更大的那条，否则一次刷量会变成
	//「某人多涨 +350」和「某人少涨 -350」两条，看着像两次事件。
	//
	// 去重按**时间区间**而不是 FromTS：同一时刻两人的 FromTS 是相同的，
	// 但合并后的区间可能起点不同（一人从第 1 步超阈，另一人从第 2 步）。
	if len(oids) == 2 {
		sort.Slice(out, func(i, j int) bool {
			return abs64(int64(out[i].PeakDelta)) > abs64(int64(out[j].PeakDelta))
		})
		filtered := []Anomaly{}
		var kept []Anomaly
		for _, a := range out {
			dup := false
			for _, k := range kept {
				// 区间有重叠 ⇒ 同一次事件
				if a.FromTS <= k.ToTS && k.FromTS <= a.ToTS {
					dup = true
					break
				}
			}
			if !dup {
				filtered = append(filtered, a)
				kept = append(kept, a)
			}
		}
		out = filtered
	}
	return out
}

// reasonFor 生成人类可读判据。
func reasonFor(members int) string {
	if members == 2 {
		return "两人差距超阈值(5分钟单步)"
	}
	return "同组离群(5分钟单步)"
}

// detectSuspectedCrossover 对破万者按「模糊期速度 vs 破万前基线速度」判疑似异常。
//
// ★ 用户的原话：「破万之后因为拿不到详细数据了，可能会在某一次 5 分钟探测的时候，
// 突然从 1 万变成 11000。这种数据变化不能直接认为是异常……只能根据它一个小时
// 的变化速度来看有没有可能是异常……如果它那个时间拉得比较正常，我们压根就不会
// 判断它异常。」
//
// 所以这里**不设绝对阈值**，只看比值：速度没明显超过自己的正常基线 ⇒ 完全不判。
func detectSuspectedCrossover(all []Series, opts Options) []Anomaly {
	ratio := opts.suspectRatio()
	out := []Anomaly{}
	for _, s := range all {
		if !s.Crossed() {
			continue
		}
		exact := s.AnalysisPoints()
		if len(exact) < 2 {
			continue
		}
		absRate := opts.fuzzyRate()
		// 破万前基线（相对判据用）：取破万前**最多 2 小时**的精确平均速度。
		//
		// ★ 为什么窗口要 2 小时而不是原来的 30 分钟：
		//   破万前那半小时通常正是冲榜冲刺段，速度天然偏高，拿它当基线
		//   会把门槛一起抬高，把明显偏快的跳变放过去。
		//   实测郑伊安（2026-10-07）：最后 30 分钟基线 562/小时，
		//   而她 22:45 那次跳变是 40 分钟涨 1000 = 1494/小时，
		//   ratio 只有 2.66，卡在阈值 3 以下 ⇒ 早期版本完全没报出来。
		//
		// ★ 基线无效（本来就在跌/不动）时**不能直接跳过**：绝对判据
		//   不依赖基线，那一段照样可能太快。
		baseline := 0.0
		tailStart := len(exact) - crossoverBaselinePoints
		if tailStart < 1 {
			tailStart = 1
		}
		var baseDelta int
		var baseSpan int64
		for i := tailStart; i < len(exact); i++ {
			// 跨日归零步不是真实跌幅，混进基线会把基线压成负数。
			if dayOf(exact[i].TS) != dayOf(exact[i-1].TS) {
				continue
			}
			baseDelta += exact[i].Sign - exact[i-1].Sign
			baseSpan += exact[i].TS - exact[i-1].TS
		}
		if baseSpan > 0 && baseDelta > 0 {
			baseline = float64(baseDelta) / (float64(baseSpan) / 3600000.0)
		}

		// 模糊期：破万之后的每次整千跳变。
		//
		// ★ 两个要点：
		//  1) 起点用**破万点本身**（第一个 ≥10000 的采样），不是最后一个精确点。
		//     跨越破万门槛的那一步不算「模糊期跳变」—— 真实数据是
		//     9993 → 10000（只 +7），不该按速度判；只有破万**之后**
		//     10000 → 11000 这种整千跳才是模糊值。
		//  2) 分母是「**距上一次数值发生变化**的时长」，不是「距上一个采样点」。
		//     破万后会长时间卡在 10000 不动（实测郑伊安卡了 40 分钟），
		//     按采样点算会把 40 分钟涨 1000 算成 1500/小时，像异常；
		//     按变化时刻算则与其自身基线相当 ⇒ 不判。
		lastChangeVal := 0
		lastChangeTS := int64(0)
		started := false
		for _, p := range s.Points {
			if p.TS < s.CrossoverAt {
				continue
			}
			if !started {
				lastChangeVal = p.Sign
				lastChangeTS = p.TS
				started = true
				continue
			}
			if p.Sign == lastChangeVal {
				continue // 卡住不动，跳过
			}
			span := float64(p.TS-lastChangeTS) / 3600000.0
			d := p.Sign - lastChangeVal
			if span > 0 && d >= 1000 {
				rate := float64(d) / span
				// ★ 两个判据取或，任一超标即判（2026-10-08 用户纠正）：
				//   1) 绝对速度 > 1000/小时 —— 主判据，直接回答
				//      「这一段本身快不快」。郑伊安那次 1494/小时，一击即中。
				//   2) 相对自身基线 > 3倍 —— 兜底，照顾那些绝对速度不算
				//      离谱、但比自己平时快很多的情况。
				//   早期版本只有判据 2，且基线窗口只有 30 分钟（正好取到
				//   临破万前的冲榜段）⇒ ratio 2.66 < 3，把这次漏掉了。
				fastAbs := rate > absRate
				fastRel := baseline > 0 && rate > baseline*ratio
				if fastAbs || fastRel {
					var why string
					switch {
					case fastAbs && fastRel:
						why = fmt.Sprintf("约 %.0f 人/小时，超过绝对门槛 %.0f，也超过其破万前 2 小时正常速度（%.0f 人/小时）的 %.1f 倍",
							rate, absRate, baseline, rate/baseline)
					case fastAbs:
						why = fmt.Sprintf("约 %.0f 人/小时，超过绝对门槛 %.0f（正常约一小时几百）",
							rate, absRate)
					default:
						why = fmt.Sprintf("约 %.0f 人/小时，是其破万前 2 小时正常速度（%.0f 人/小时）的 %.1f 倍",
							rate, baseline, rate/baseline)
					}
					out = append(out, Anomaly{
						OID: s.OID, Name: s.Name, GroupName: "",
						FromTS: lastChangeTS, ToTS: p.TS,
						From: lastChangeVal, To: p.Sign, TotalDelta: d,
						PeakDelta: d, PeakTS: p.TS, Steps: 1,
						Threshold:   int(absRate),
						Severity:    SeveritySuspected,
						RatePerHour: rate, BaselinePerHour: baseline,
						Reason: fmt.Sprintf(
							"已破万，接口只给千粒度模糊值，取不到这段时间的具体增量，"+
								"只能按整千跳变的速度判断：不到 %.0f 分钟就涨了 %d，%s。疑似异常，不等于确认刷量。",
							span*60, d, why),
					})
				}
			}
			lastChangeVal = p.Sign
			lastChangeTS = p.TS
		}
	}
	return out
}

// HourlyPoint 是一小时的汇总（每小时汇总表用）。
type HourlyPoint struct {
	Hour  int64          `json:"hour"`
	Total int            `json:"total"`
	Per   map[string]int `json:"per"`
}

// Hourly 按小时汇总各成员增量。
func Hourly(all []Series, loc2 *time.Location) []HourlyPoint {
	if loc2 == nil {
		loc2 = loc
	}
	buckets := map[int64]HourlyPoint{}
	for _, s := range all {
		pts := s.Points
		for i := 1; i < len(pts); i++ {
			prev, p := pts[i-1], pts[i]
			// 跨日归零不算增量
			if time.UnixMilli(prev.TS).In(loc2).Format("2006-01-02") !=
				time.UnixMilli(p.TS).In(loc2).Format("2006-01-02") {
				continue
			}
			h := time.UnixMilli(p.TS).In(loc2).Truncate(time.Hour).UnixMilli()
			b, ok := buckets[h]
			if !ok {
				b = HourlyPoint{Hour: h, Per: map[string]int{}}
			}
			d := p.Sign - prev.Sign
			b.Total += d
			b.Per[s.Name] += d
			buckets[h] = b
		}
	}
	out := make([]HourlyPoint, 0, len(buckets))
	for _, v := range buckets {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hour > out[j].Hour })
	return out
}

// ────────────────────────────────────────────────────────────────────────────
// 异常时段明细
//
// ★★ 面板与推送 PNG **必须共用这一个函数**（2026-10-08）。
// 用户要的东西其实是一份数据的两种呈现：面板上要看「异常那一小时里
// 8 个超话每 5 分钟各涨了多少」，推到飞书/QQ/邮箱的是**同一份表**
// 渲成的 PNG —— 不是另外画一张折线图/柱状图。
// 历史上两处各算一遍出现过「面板 +120、推送图上 +0」的口径不一致，
// 所以这里收敛成一处。
// ────────────────────────────────────────────────────────────────────────────

// maxDetailGapMin 相邻采样点间隔超过这个值，差值就没有「5 分钟增量」的意义
// （断采或跨日），这一格留空而不是填 0。
const maxDetailGapMin = 15

// DetailRow 明细表的一行（一个超话）。
//
// ★ Deltas 与 DetailWindow.Stamps 一一对应；nil = 这一格没有可信增量
//
//	（没采样 / 间隔超过 15 分钟 / 跨日归零 / 已破万）。
//	★★ 必须是 nil 不能是 0：0 会被读成「这一格没涨」，破万者看上去
//	就像整整一小时零增长 —— 那个结论是错的。
type DetailRow struct {
	OID    string `json:"oid"`
	Name   string `json:"name"`
	Deltas []*int `json:"deltas"`
	// Marks 与 Deltas 一一对应，说明「为什么没有值」：
	//   "" = 有值；"crossover" = 已破万（模糊值，增量不可知）；"gap" = 无采样
	//   / 间隔过大 / 跨日归零。
	// ★ 不做这个区分就会把「破万拿不到数」画成「一小时没涨」，结论是反的。
	Marks []string `json:"marks"`
	Net   *int     `json:"net"`
	Fuzzy bool     `json:"fuzzy"`
}

// MemberWindowDelta 一个成员在窗口内的净增。
type MemberWindowDelta struct {
	OID  string `json:"oid"`
	Name string `json:"name"`
	Net  int    `json:"net"`
}

// GroupWindowDelta 一个分组在窗口内的净增（分组增量柱状图用）。
type GroupWindowDelta struct {
	Name    string              `json:"name"`
	Net     int                 `json:"net"`
	Members []MemberWindowDelta `json:"members"`
}

// DetailWindow 是「某一次异常所在时间窗」的完整明细。
type DetailWindow struct {
	OID    string             `json:"oid"`
	Name   string             `json:"name"`
	From   int64              `json:"from"`
	To     int64              `json:"to"`
	Stamps []int64            `json:"stamps"`
	Rows   []DetailRow        `json:"rows"`
	Groups []GroupWindowDelta `json:"groups"`
}

// WindowDetail 统计 [from, to] 窗口内的逐点增量，并汇总各分组净增。
func WindowDetail(samples []Sample, groups []Group, from, to int64) DetailWindow {
	out := DetailWindow{
		From: from, To: to,
		Stamps: []int64{}, Rows: []DetailRow{}, Groups: []GroupWindowDelta{},
	}
	if to <= from || len(samples) == 0 {
		return out
	}

	byOID := map[string][]Sample{}
	names := map[string]string{}
	for _, s := range samples {
		byOID[s.OID] = append(byOID[s.OID], s)
		if s.Name != "" {
			names[s.OID] = s.Name
		}
	}
	oids := make([]string, 0, len(byOID))
	for oid := range byOID {
		oids = append(oids, oid)
		sort.Slice(byOID[oid], func(i, j int) bool { return byOID[oid][i].TS < byOID[oid][j].TS })
	}
	// 按名字稳定排序；异常者要不要置顶由调用方决定（面板与 PNG 都要置顶）
	sort.Slice(oids, func(i, j int) bool {
		ni, nj := names[oids[i]], names[oids[j]]
		if ni != nj {
			return ni < nj
		}
		return oids[i] < oids[j]
	})

	stampSet := map[int64]bool{}
	for _, oid := range oids {
		for _, p := range byOID[oid] {
			if p.TS >= from && p.TS <= to {
				stampSet[p.TS] = true
			}
		}
	}
	stamps := make([]int64, 0, len(stampSet))
	for ts := range stampSet {
		stamps = append(stamps, ts)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	out.Stamps = stamps

	for _, oid := range oids {
		pts := byOID[oid]
		idx := map[int64]int{}
		for i, p := range pts {
			idx[p.TS] = i
		}
		row := DetailRow{
			OID: oid, Name: names[oid],
			Deltas: make([]*int, len(stamps)),
			Marks:  make([]string, len(stamps)),
		}
		for ci, ts := range stamps {
			row.Marks[ci] = "gap"
			i, ok := idx[ts]
			if !ok || i == 0 {
				continue
			}
			prev, cur := pts[i-1], pts[i]
			if cur.TS-prev.TS > int64(maxDetailGapMin)*60000 {
				continue
			}
			if dayOf(prev.TS) != dayOf(cur.TS) {
				continue // 跨日归零步：不是真实跌幅
			}
			// ★★★ 破万后的步是「已知的下界」，不是「未知」（2026-10-08 用户纠正）：
			//
			//   接口在破万后只给千粒度模糊值，但**相邻两次采样之间的差值是
			//   看得见的**。实测郑伊安 22:45 那步 10000 → 11000，
			//   我们确知它至少涨了 1000；旧代码把整步丢掉只留一个 +0*，
			//   于是那一小时显示「+7」（只有破万那一步的 +7），
			//   而实际已知下限是 7 + 1000 = 1007 —— 把确定的数据也丢了。
			//
			//   现在记下观测到的差值，并打 crossover 标记，渲染成「+1000*」：
			//     · 星号 = 还有我们不清楚的额外增量（区间内其他时刻可能涨过）
			//     · 数字 = 已经确定的增量，必须显示
			//
			//   同理 cur == 10000 那一步两边都是精确值，也照常记数值。
			d := cur.Sign - prev.Sign
			v := d
			row.Deltas[ci] = &v
			if cur.Sign > CrossoverSign || prev.Sign >= CrossoverSign {
				row.Fuzzy = true
				row.Marks[ci] = "crossover"
			} else {
				row.Marks[ci] = ""
			}
		}
		// 窗口净增：窗口内最后一个点 − 窗口开始前最后一个点
		var last *Sample
		for i := range pts {
			if pts[i].TS > to {
				break
			}
			last = &pts[i]
		}
		if last != nil {
			var base *Sample
			for i := range pts {
				if pts[i].TS >= from {
					break
				}
				base = &pts[i]
			}
			if base == nil {
				base = &pts[0]
			}
			if dayOf(base.TS) == dayOf(last.TS) && last.TS >= base.TS {
				net := last.Sign - base.Sign
				row.Net = &net
				if base.Sign >= CrossoverSign || last.Sign >= CrossoverSign {
					row.Fuzzy = true
				}
			}
		}
		out.Rows = append(out.Rows, row)
	}

	// ★ 年龄顺序（2026-10-08 用户指定）：面板 / 推送 PNG / 邮件正文
	// 都在这里拿同一份已排序的 Rows，三处行序必然一致。
	SortByAgeDetailRows(out.Rows)

	if len(groups) == 0 {
		all := make([]string, 0, len(out.Rows))
		for _, r := range out.Rows {
			all = append(all, r.OID)
		}
		if len(all) > 0 {
			groups = []Group{{Name: "全部超话", Members: all}}
		}
	}
	netByOID := map[string]int{}
	for _, r := range out.Rows {
		if r.Net != nil {
			netByOID[r.OID] = *r.Net
		}
	}
	for _, g := range groups {
		gd := GroupWindowDelta{Name: g.Name, Members: []MemberWindowDelta{}}
		for _, oid := range g.Members {
			n, ok := netByOID[oid]
			if !ok {
				continue
			}
			gd.Net += n
			gd.Members = append(gd.Members, MemberWindowDelta{OID: oid, Name: names[oid], Net: n})
		}
		out.Groups = append(out.Groups, gd)
	}
	return out
}

// dayOf 取业务日（跨日归零判定必须用它，不能用服务器本地时区）。
func dayOf(ts int64) string {
	return time.UnixMilli(ts).In(loc).Format("2006-01-02")
}
