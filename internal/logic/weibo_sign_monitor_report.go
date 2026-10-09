package logic

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/signstat"
)

// 超话签到监测的报表（2026-10-07）。
//
// 三种产出，全部复用日报现成管线：
//   - HTML 正文：给邮件客户端直接读；
//   - PNG 图表：renderHTMLToPNG（scripts/html_to_png.mjs，与日报同一套截图）；
//   - CSV 原始数据：用户要「原始数据表」，CSV 用 Excel 打开最省事。
//
// 什么时候发：
//   - 每日汇总：每天固定时刻（默认 23:50）把当天 0:00~24:00 的数据发一封；
//   - 异常即时：组内检测一命中就立刻发一封，带异常时段（±15 分钟）的柱状图。

const signMonitorReportColor = "#2f6bff"

// smPoint 是报表内部用的采样点。
// logic 侧 signMonitorSeries.Points 是 [][2]int64（unixMilli, sign），
// 直接用会到处做下标转换，这里统一成命名结构。
type smPoint struct {
	TS   int64
	Sign int
	OID  string
	Name string
}

// signMonitorReportInput 是一次报表需要的全部输入（从 store 取，不直接依赖全局）。
type signMonitorReportInput struct {
	Title    string
	Subtitle string
	Groups   []signMonitorReportGroup
	Spikes   []signMonitorSpike
	// Notes 是「本次没能出图的组」的说明（用户 2026-10-07 14:48 要求）：
	// 图消失时必须说清**为什么**（都破万了）和**统计截止到什么时候**，
	// 否则看起来像功能坏了。
	Notes []string
	// CSV 的额外表头（导出口径写进文件里，避免以后忘了列的含义）
	IntervalMinutes int
	Since, Until    time.Time
	// AllSeries 是全部原始序列 —— 5 分钟粒度明细表要全量（不分组）。
	// 柱状图只画异常所在组（量级相近才看得出谁突出），
	// 但明细表必须能看到全组每个人的每格增量。
	AllSeries []signMonitorSeries

	// HourlyHTML 是「当天每小时增量表」的 HTML —— **与日报那张 PNG 逐格一致**
	//（同一个 computeHourlyTable 算出来的）。
	//
	// ★ 2026-10-08 用户要求：「这个'超话日报'是发什么？就是发那一天，
	//   就是你 PNG 里的那个东西啊。你应该把你 PNG 里的内容同样写成 HTML，
	//   在邮件本体里面这样发」。正文以前放的是那张毫无意义的
	//   「当前/区间最低/区间最高/采样点」分组表，用户已明确要求删除。
	HourlyHTML string

	// Detail 是「异常时段明细」的口径数据 —— **与面板、飞书/QQ 推送的
	// PNG 表共用同一个 signstat.WindowDetail**（2026-10-08）。
	//
	// ★★ 历史上这里还有第二套独立实现（自己按 series 算差分、自己判断
	//   破万），出现过两类严重错误：
	//     ① 破万判定用「该超话有没有破过万」而不是「这一格是否在破万之后」
	//        ⇒ 柳河岚 23:50 才破万，21:44 的格子也被涂成「+0 已破万」；
	//     ② 窗口用「窗口内增量最大的那一格」猜出来，不是异常自己的时段
	//        ⇒ 郑伊安疑似异常却贴出崔志宇的时段。
	//   两处都会让正文表格与实际异常对不上。现在只认这一个字段。
	Detail *signstat.DetailWindow

	// AnomOID / DetailThreshold 用于明细表高亮异常者与超阈格的着色。
	AnomOID         string
	DetailThreshold int
}

type signMonitorReportGroup struct {
	Name    string
	Series  []signMonitorSeries // 带 Name / Points
	Members []string
}

// maxPairGap 返回两人组里「累计增量差距」的最大值。
//
// 语义：dev(t) = (A(t)-A(0)) - (B(t)-B(0))，即两人从各自起点算起谁多涨。
// 两人组不存在中位数，只有差 —— 这也正是用户 2026-10-07 指出的点。
// devBounds 是一组序列的偏离极值：Pos = 最大正向，Neg = 最大负向的绝对值。
type devBounds struct {
	Pos, Neg int
}

// pairDevBounds 两人组的偏离极值（带符号 —— 两人时只有差，方向就是「谁涨得多」）。
//
// ★ 不能只返回 max(|gap|)：只扫绝对值会同时丢掉「谁在上方」的信息，
//
//	以及 Y 轴范围所需的方向（2026-10-07 用户在第三组图上发现曲线被裁）。
func pairDevBounds(series []signMonitorSeries) devBounds {
	series = filterBelowCrossover(series)
	if len(series) != 2 {
		return devBounds{}
	}
	// ★★ 单步口径（与 buildSignMonitorDeviationSVG 一致）：
	//   两人时不存在中位数，基线 = 对方这一步的增量 ⇒ 互为相反数。
	//   原来用「从起点算起的累积差」，是另一个口径（且必然单调发散）。
	cum := make([][]int64, 2)
	for i, s := range series {
		if len(s.Points) < 2 {
			return devBounds{}
		}
		cum[i] = make([]int64, len(s.Points))
		for j := 1; j < len(s.Points); j++ {
			cum[i][j] = s.Points[j][1] - s.Points[j-1][1]
		}
	}
	n := len(cum[0])
	if len(cum[1]) < n {
		n = len(cum[1])
	}
	maxGap := int64(0)
	// j 从 1 起：j=0 是「起点没有增量」，恒为 0，不参与极值统计。
	for j := 1; j < n; j++ {
		g := cum[0][j] - cum[1][j]
		if -g > maxGap {
			maxGap = -g
		}
		if g > maxGap {
			maxGap = g
		}
	}
	return devBounds{Pos: int(maxGap), Neg: int(maxGap)}
}

// groupDevBounds ≥3 人组的偏离极值（逐时刻「单步增量的中位数」当基线）。
//
// ★ 口径必须与 buildSignMonitorDeviationSVG **完全一致**（单步，不是累积）——
//
//	两处漂移会让副标题的"最大偏离"和图上画的不是一个数。
func groupDevBounds(series []signMonitorSeries) devBounds {
	var b devBounds
	series = filterBelowCrossover(series)
	if len(series) < 3 {
		return b
	}
	stampsSet := map[int64]bool{}
	for _, s := range series {
		for _, p := range s.Points {
			stampsSet[p[0]] = true
		}
	}
	stamps := make([]int64, 0, len(stampsSet))
	for ts := range stampsSet {
		stamps = append(stamps, ts)
	}
	sort.Slice(stamps, func(a, b int) bool { return stamps[a] < stamps[b] })

	cur := make([]int64, len(series))
	prev := make([]int64, len(series))
	idx := make([]int, len(series))
	started := make([]bool, len(series))
	for i, s := range series {
		if len(s.Points) > 0 {
			cur[i] = s.Points[0][1]
			prev[i] = s.Points[0][1]
			started[i] = true
		}
	}
	incr := make([]int64, len(series))
	buf := make([]int64, 0, len(series))
	for _, ts := range stamps {
		for i, s := range series {
			for idx[i] < len(s.Points) && s.Points[idx[i]][0] <= ts {
				prev[i] = cur[i]
				cur[i] = s.Points[idx[i]][1]
				idx[i]++
			}
			incr[i] = cur[i] - prev[i]
		}
		// ★★ 单步偏离 = 这一步增量 - 同组这一步增量的中位数。
		//   第一个时刻没有「上一步」，跳过（画图函数里 j=0 的 dev 也是 0）。
		//   原来用cum = cur - starts（累积），口径与画图不一致 ⇒
		//   副标题写的 1162 而图上只有 80，同一张图两个数字。
		if ts == stamps[0] {
			continue
		}
		buf = append(buf[:0], incr...)
		med := medianInt64(buf)
		for _, v := range incr {
			d := int(v - med)
			if d > b.Pos {
				b.Pos = d
			}
			if -d > b.Neg {
				b.Neg = -d
			}
		}
	}
	_ = started
	return b
}

// medianInt64 取中位数，**偶数个取中间两个的平均**（与 medianInt 同口径）。
//
// ★ 2026-10-07 修正：偏离图原先用 sorted[len/2]，对 4 人组来说那是「第三小」
//
//	而不是「中间两个的平均」，实测偏�� 250~4400 —— 正好把异常显得更小。
//	告警判定（medianInt）一直用的是真中位数，两处口径不一致会让
//	「图上看起来正常但已告警」或反过来。**判定与画图必须同一口径。**
func medianInt64(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	s := make([]int64, len(values))
	copy(s, values)
	sort.Slice(s, func(a, b int) bool { return s[a] < s[b] })
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}

// signMonitorMaxDeviation 返回最大绝对偏离（副标题与告警文案用）。
//
// ★ 复用 devBounds —— 与偏离图画图**同一份计算**（口径漂移护栏）。
//
//	2 人组用差值口径（不存在中位数），≥3 人用组内中位数。
func signMonitorMaxDeviation(series []signMonitorSeries) int {
	if len(series) < 2 {
		return 0
	}
	var b devBounds
	if len(series) == 2 {
		b = pairDevBounds(series)
	} else {
		b = groupDevBounds(series)
	}
	if b.Pos > b.Neg {
		return b.Pos
	}
	return b.Neg
}

// ★ 不能一路写死 300（2026-10-07 实测）：5 分钟跨度和一整天跨度都用 300px 时，
//   - 短跨度：图上只有几条平线，300px 全是空白；
//   - 全天跨度（288 个采样点）：S 形曲线挤在 300px 里，早高峰的斜率看不清。
//
// 按跨度分档，两头都不浪费。
func signMonitorChartHeight(spanMS int64) int {
	switch {
	case spanMS <= 2*3600*1000:
		return 260 // 2 小时以内
	case spanMS <= 6*3600*1000:
		return 320
	default:
		return 380 // 超过 6 小时（含全天 24 小时）
	}
}

// signMonitorNiceStep 找一个「好看」的刻度步长：1/2/2.5/5/10 × 10^n。
//
// 目的是让 Y 轴刻度落在 2500 / 5000 / 7500 这种数上，而不是 2499 / 4998。
func signMonitorNiceStep(maxV int, targetTicks int) int {
	if maxV <= 0 || targetTicks <= 0 {
		return 1
	}
	raw := float64(maxV) / float64(targetTicks)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			step := int(m * mag)
			if step < 1 {
				step = 1
			}
			return step
		}
	}
	return int(mag * 10)
}

// signMonitorTickCount 按可用宽度决定 X 轴刻度数量。
//
// ★ 不能写死 5 个（2026-10-07）：跨度大时标签是「01-02 15:04」这种
// 11 个字符，约 70px 宽。innerW 只有 ~550px 时，6 个刻度间隔 92px
// 就会互相压字（实测「10-07 19:55」和「10-07 23:55」重叠）。
// 反过来跨度小的时候刻度又太稀。所以按标签实际宽度算能放几个。
func signMonitorTickCount(innerW int, span time.Duration) int {
	labelW := 46 // "15:04" 约 34px，留余量
	if span > 12*time.Hour {
		labelW = 78 // "01-02 15:04" 约 70px
	}
	n := int(innerW / labelW)
	if n < 2 {
		n = 2
	}
	if n > 10 {
		n = 10
	}
	return n
}

// buildSignMonitorLineSVG 画一张累计折线图的内联 SVG。
//
// title/subtitle 是分组标题。★ 必须**按组分别出图**，不能把量级差 3 倍的
// 超话（万档组 9000~10000 vs 三千档组 3000 多）挤在同一张图里 —— 那样
// Y 轴被万档组拉高，三千档组全贴在底部，既看不出组内对比也看不出走势。
func buildSignMonitorLineSVG(series []signMonitorSeries, title, subtitle string, width, height int) string {
	if len(series) == 0 {
		return ""
	}
	// 顶部留标题区（title 为空时 titleHead=0，行为与原来完全一致）。
	titleHead := 0
	if title != "" {
		titleHead = 40
	}
	// padRight 要放得下「名字 + 逗号数值」：12 个汉字 ≈ 84px + 空格 + "10,000" ≈ 35px，
	// 再留 16px 边距 ⇒ 140px。原先 120px 会把图例裁掉（「柳河岚YUHA 2,5」）。
	padLeft, padRight, padTop, padBottom := 56, 150, 16+titleHead, 28
	innerW := width - padLeft - padRight
	innerH := height - padTop - padBottom

	minTS, maxTS := int64(0), int64(0)
	maxV := 1
	first := true
	for _, s := range series {
		for _, p := range s.Points {
			if first || p[0] < minTS {
				minTS = p[0]
			}
			if p[0] > maxTS {
				maxTS = p[0]
			}
			if int(p[1]) > maxV {
				maxV = int(p[1])
			}
			first = false
		}
	}
	if first || maxTS <= minTS {
		return ""
	}
	span := float64(maxTS - minTS)
	yMax := float64(maxV) * 1.08
	x := func(ts int64) float64 { return float64(padLeft) + float64(ts-minTS)/span*float64(innerW) }
	y := func(v int64) float64 { return float64(padTop+innerH) - float64(v)/yMax*float64(innerH) }

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/>`)
	if title != "" {
		fmt.Fprintf(&b, `<text x="%d" y="22" font-size="15" font-weight="600" fill="#101828">%s</text>`,
			padLeft, html.EscapeString(title))
		if subtitle != "" {
			fmt.Fprintf(&b, `<text x="%d" y="40" font-size="11" fill="#98a2b3">%s</text>`,
				padLeft, html.EscapeString(subtitle))
		}
	}
	// 横向网格 + Y 轴刻度。
	//
	// ★ 刻度取整到「好看」的数（2026-10-07）：原先直接 maxV*i/4，
	// 万档组会打印出 2499 / 4998 / 7497 这种没人读的数；取整到 2500/5000/7500
	// 才能和「2,697 人」这种实际数值对照。
	step := signMonitorNiceStep(maxV, 4)
	for v := 0; v <= maxV+step/2; v += step {
		yy := y(int64(v))
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#e4e7ec"/>`,
			padLeft, yy, padLeft+innerW, yy)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" fill="#98a2b3" text-anchor="end">%s</text>`,
			padLeft-8, yy+4, comma(v))
	}
	// X 轴时间刻度（数量按宽度自适应，避免长标签互相压字）
	ticks := signMonitorTickCount(innerW, time.Duration(span)*time.Millisecond)
	for i := 0; i <= ticks; i++ {
		ts := minTS + int64(span*float64(i)/float64(ticks))
		xx := x(ts)
		label := time.UnixMilli(ts).Format("15:04")
		if span > 12*3600*1000 {
			label = time.UnixMilli(ts).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="11" fill="#98a2b3" text-anchor="middle">%s</text>`,
			xx, padTop+innerH+18, label)
	}
	// 折线
	colors := []string{"#2f6bff", "#e0484d", "#12a150", "#8a5cf5", "#f59e0b", "#0ea5e9", "#ec4899", "#64748b"}
	for i, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		pts := append([][2]int64(nil), s.Points...)
		sort.Slice(pts, func(a, b int) bool { return pts[a][0] < pts[b][0] })
		color := colors[i%len(colors)]
		var d strings.Builder
		for j, p := range pts {
			fmt.Fprintf(&d, "%s%.1f,%.1f", map[bool]string{true: "M", false: "L"}[j == 0], x(p[0]), y(p[1]))
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`, d.String(), color)
		last := pts[len(pts)-1]
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" fill="%s"/>`, x(last[0]), y(last[1]), color)
		// 图例放在右侧空白区
		fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="10" height="10" fill="%s"/>`,
			padLeft+innerW+12, float64(padTop+16+i*20), color)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="12" fill="#475467">%s %s</text>`,
			padLeft+innerW+28, float64(padTop+25+i*20),
			html.EscapeString(truncateStr(s.Name, 10)), comma(int(last[1])))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// buildSignMonitorBarSVG 画异常时段的柱状图：X 轴固定等分时间槽，
// 每个槽内并排 N 根柱子（组内每个人）。
// buildSignMonitorBarSVG 画「异常时段增量」柱状图。
//
// ★★ 2026-10-07 22:50 重构：拆成**上下双栏**（不是共用一个 Y 轴）。
//
// 原实现一张图里塞了**两种量纲**：
//   - 未破万成员的真实单步增量：+30 ~ +614
//   - 已破万成员的模糊档位跳变：+1000（1K 粒度，不是真实增量）
//
// 共用一个 Y 轴 ⇒ 真实数据被压到 8% 高度、柱子几乎看不见
// （用户 22:50报「图十分诡异」）；而且破万者的 1000 也不该和真实增量同框比较。
//
// 现在：上栏= 未破万（真实增量，独立刻度 + 柱顶标数值）；
// 下栏 = 已破万（模糊档位，独立刻度 + 斜纹 + 标注不可信）。两者互不干扰。
func buildSignMonitorBarSVG(series []signMonitorSeries, from, to time.Time, title string, width, height int) string {
	const (
		padLeft  = 64
		padRight = 178
		padTop   = 56
	)
	innerW := width - padLeft - padRight
	if innerW < 80 {
		innerW = 80
	}

	// ---- 分栏：valid = 未破万（真实数据），fuzzy = 已破万 ----
	valid := make([]signMonitorSeries, 0, len(series))
	fuzzy := make([]signMonitorSeries, 0, len(series))
	for _, s := range series {
		if c, _ := signMonitorCrossedAt(series, s.OID, 0); c {
			fuzzy = append(fuzzy, s)
		} else {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		// 全员破万 ⇒ 上栏留空，全给下栏
		fuzzy, valid = valid, fuzzy
	}

	// ---- 槽位划分 ----
	slotCount := int(to.Sub(from).Minutes())
	if slotCount < 1 {
		slotCount = 1
	}
	slotMS := int64(float64(to.Sub(from).Milliseconds()) / float64(slotCount))
	if slotMS < 60000 {
		slotMS = 60000
		slotCount = int(to.Sub(from).Milliseconds()) / int(slotMS)
	}

	type slot struct {
		idx   int
		value int64
	}

	// collect 收集一栏内每个成员的槽位增量，并返回该栏最大值（用于刻度）
	collect := func(group []signMonitorSeries) ([][]slot, int64) {
		per := make([][]slot, len(group))
		maxV := int64(0)
		for i, s := range group {
			pts := make([]smPoint, 0, len(s.Points))
			for _, p := range s.Points {
				pts = append(pts, smPoint{TS: p[0], Sign: int(p[1]), OID: s.OID, Name: s.Name})
			}
			sort.Slice(pts, func(a, b int) bool { return pts[a].TS < pts[b].TS })
			for j := 1; j < len(pts); j++ {
				if pts[j].TS < from.UnixMilli() || pts[j].TS > to.UnixMilli() {
					continue
				}
				delta := int64(pts[j].Sign - pts[j-1].Sign)
				idx := int((pts[j].TS - from.UnixMilli()) / slotMS)
				if idx < 0 || idx >= slotCount {
					continue
				}
				per[i] = append(per[i], slot{idx, delta})
				if delta > maxV {
					maxV = delta
				}
			}
		}
		return per, maxV
	}

	colors := []string{"#2f6bff", "#e0484d", "#12a150", "#8a5cf5", "#f59e0b", "#0ea5e9", "#ec4899", "#64748b"}
	gray := "#98a2b3"

	// ---- 布局：上栏 68%、下栏 32%（只有一栏时占满） ----
	barArea := 240
	upperH := barArea
	lowerH := 0
	if len(fuzzy) > 0 && len(valid) > 0 {
		upperH = int(float64(barArea) * 0.66)
		lowerH = barArea - upperH
	}
	upperTop := padTop
	lowerTop := upperTop + upperH
	upperBase := float64(lowerTop)
	lowerBase := float64(lowerTop + lowerH)

	upperSlots, upperMaxV := collect(valid)
	upperBound := int64(1)
	if upperMaxV > 0 {
		upperBound = niceBound(upperMaxV)
	}
	// 30% 留白给柱顶数值标签
	upperYMax := float64(upperBound) * 1.30

	clusterW := float64(innerW) / float64(slotCount)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`, width, height, width, height)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/>`)
	// 破万者的「模糊档位」斜纹
	b.WriteString(`<defs><pattern id="sm-fuzzy" width="4" height="4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">` +
		`<rect width="4" height="4" fill="#f2f4f7"/><line x1="0" y1="0" x2="0" y2="4" stroke="#b9c0cc" stroke-width="1.4"/></pattern></defs>`)

	if title != "" {
		fmt.Fprintf(&b, `<text x="%d" y="22" font-size="15" font-weight="600" fill="#101828">%s</text>`,
			padLeft, html.EscapeString(title))
	}
	fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" font-weight="600" fill="#475467">真实增量（未破万）</text>`,
		padLeft, upperTop-10)
	if len(fuzzy) > 0 {
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="11" font-weight="600" fill="#b54708">`+
			`已破万（1K 模糊档位，不是真实增量）</text>`, padLeft, lowerTop-8)
	}

	// draw 画一栏。fuzzyCol=true 时用斜纹 + 虚线边框（视觉上就不是真实数据）
	draw := func(all []signMonitorSeries, per [][]slot, names []signMonitorSeries,
		baseY float64, plotH int, yMax float64, fuzzyCol bool) {
		clusterN := float64(len(names))
		if clusterN <= 0 {
			return
		}
		bw := math.Max((clusterW*0.80)/clusterN, 2.2)
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#e4e7ec"/>`,
			padLeft, baseY, padLeft+innerW, baseY)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#98a2b3" text-anchor="end">%d</text>`,
			padLeft-8, baseY-5, int(yMax))
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#98a2b3" text-anchor="end">0</text>`,
			padLeft-8, baseY+4)

		for i, s := range names {
			color := colors[i%len(colors)]
			if fuzzyCol {
				color = gray
			}
			for _, sl := range per[i] {
				cx := float64(padLeft) + (float64(sl.idx)+0.5)*clusterW
				h := float64(sl.value) / yMax * float64(plotH)
				if h < 1 && sl.value > 0 {
					h = 1
				}
				x0 := cx - (clusterN*bw)/2 + float64(i)*bw
				w := math.Max(bw-1, 1)
				if fuzzyCol {
					h2 := math.Max(h, 4)
					tip := fmt.Sprintf("%s 已破万（%s 起拿不到精确值）；此处显示 +%d。"+
						"破万后接口按 1000 粒度模糊化，该数字是档位跳变，不代表真实增量",
						truncateStr(s.Name, 12),
						time.UnixMilli(crossAtOf(all, s.OID)).Format("01-02 15:04"), sl.value)
					fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" `+
						`fill="url(#sm-fuzzy)" stroke="%s" stroke-width="0.7" stroke-dasharray="2 1">`+
						`<title>%s</title></rect>`,
						x0, baseY-h2, w, h2, gray, html.EscapeString(tip))
					if w >= 1.5 {
						fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" fill="%s" `+
							`text-anchor="middle">+%d</text>`, x0+w/2, baseY-h2-4, gray, sl.value)
					}
					continue
				}
				fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s">`+
					`<title>%s +%d</title></rect>`,
					x0, baseY-h, w, h, color, html.EscapeString(truncateStr(s.Name, 12)), sl.value)
				// ★★ 柱顶直标数值：一根 614 的柱子会把 +50 压到 8% 高度，
				//   光看柱子读不出数 ⇒ 必须直接把数字写出来。
				// ★ 阈值放宽到 1.5：柱宽可能只有 2~3px（30 分钟 30 槽挤在
				//   518px 里），但**数值必须可读** —— 这是密集柱图的标准取舍。
				if sl.value > 0 && w >= 1.5 {
					fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="9" fill="%s" `+
						`text-anchor="middle">+%d</text>`, x0+w/2, baseY-h-4, color, sl.value)
				}
			}
		}
	}

	draw(series, upperSlots, valid, upperBase, upperH, upperYMax, false)

	if len(fuzzy) > 0 {
		fSlots, fMaxV := collect(fuzzy)
		lb := int64(1)
		if fMaxV > 0 {
			lb = niceBound(fMaxV)
		}
		draw(series, fSlots, fuzzy, lowerBase, lowerH, float64(lb)*1.30, true)
	}

	// ---- X 轴：每槽标一次时间（贴下栏底部） ----
	step := 1
	if slotCount > 8 {
		step = slotCount / 8
	}
	xAxisY := lowerBase + 18
	if len(fuzzy) == 0 {
		xAxisY = upperBase + 18
	}
	for i := 0; i < slotCount; i += step {
		ts := from.UnixMilli() + int64(i)*slotMS
		cx := float64(padLeft) + (float64(i)+0.5)*clusterW
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f" font-size="10" fill="#98a2b3" text-anchor="middle">%s</text>`,
			cx, xAxisY, time.UnixMilli(ts).Format("15:04"))
	}

	// ---- 图例 ----
	ly := float64(padTop + 4)
	for i, s := range valid {
		color := colors[i%len(colors)]
		fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="10" height="10" fill="%s"/>`,
			padLeft+innerW+14, ly, color)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#475467">%s</text>`,
			padLeft+innerW+28, ly+9, html.EscapeString(truncateStr(s.Name, 11)))
		ly += 17
	}
	for _, s := range fuzzy {
		fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="10" height="10" fill="url(#sm-fuzzy)" stroke="%s" stroke-width="0.7"/>`,
			padLeft+innerW+14, ly, gray)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#b54708">%s 破万</text>`,
			padLeft+innerW+28, ly+9, html.EscapeString(truncateStr(s.Name, 9)))
		ly += 17
	}

	// ---- 破万说明（用户 22:50：+0 / 模糊值都要说明原因） ----
	if len(fuzzy) > 0 {
		notes := make([]string, 0, len(fuzzy))
		for _, s := range fuzzy {
			notes = append(notes, fmt.Sprintf("%s 自 %s 起破万，签到数只返回 1000 粒度模糊值"+
				"（如 10000→11000），该数字是档位跳变，不代表真实增量",
				truncateStr(s.Name, 10),
				time.UnixMilli(crossAtOf(series, s.OID)).Format("01-02 15:04")))
		}
		note := strings.Join(notes, "；")
		if len([]rune(note)) > 108 {
			note = string([]rune(note)[:108]) + "…"
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="10" fill="#b54708">%s</text>`,
			padLeft, height-8, html.EscapeString(note))
	}

	b.WriteString(`</svg>`)
	return b.String()
}

// ★★ 破万时段判定（2026-10-07 22:30，用户要求）。
//
// 现象：超话签到数破万后，接口只返回 10000/12000 这种**模糊值**，
//
//	增量恒等于 0 —— 图上/表上会出现一串「+0」，看起来像「这人半小时没涨」。
//	实际含义是**抓不到数据**，不是真的没涨。
//
// 用法：对某个超话序列 + 一个时刻，返回 true 表示「该时刻已破万，
//
//	增量不可信（显示为 +0 是采集限制，不是真实零增长）」。
func signMonitorCrossedAt(series []signMonitorSeries, oid string, ts int64) (bool, int64) {
	for _, s := range series {
		if s.OID != oid {
			continue
		}
		pts := make([]smPoint, 0, len(s.Points))
		for _, p := range s.Points {
			pts = append(pts, smPoint{TS: p[0], Sign: int(p[1]), OID: s.OID, Name: s.Name})
		}
		sort.Slice(pts, func(a, b int) bool { return pts[a].TS < pts[b].TS })
		// ★ 正序扫记**第一次**破万时刻（倒序会记成最后一次，偏到当天末尾）
		for _, p := range pts {
			if p.Sign >= superTopicCrossoverSign {
				return true, p.TS
			}
		}
	}
	return false, 0
}

// crossAtOf 返回该超话第一次破万的时刻（不存在返回 0）。
func crossAtOf(series []signMonitorSeries, oid string) int64 {
	_, at := signMonitorCrossedAt(series, oid, 0)
	return at
}

// signMonitorCrossoverNote 生成破万说明文字（统一口径，图表与表格共用）。
func signMonitorCrossoverNote(series []signMonitorSeries, oid string) string {
	crossed, at := signMonitorCrossedAt(series, oid, 0)
	if !crossed {
		return ""
	}
	var name string
	for _, s := range series {
		if s.OID == oid {
			name = s.Name
			break
		}
	}
	if name == "" {
		name = oid
	}
	return fmt.Sprintf("%s 已破万（%s 起签到数不再返回精确值），其后的增量显示为 +0 是采集限制而非真的没涨",
		name, time.UnixMilli(at).Format("01-02 15:04"))
}

// signMonitorSpikeDetailTable 生成「异常时段逐格增量明细」HTML 表格。
//
// ★★★ 2026-10-08 重写：这份表格曾经有**第二套独立实现**，自己按 series
//
//	算差分、自己判断破万，结果和面板 / 推送 PNG 全面对不上：
//	  ① 破万判定写成「该超话历史上有没有破过万」，不是「这一格是否在破万之后」
//	     ⇒ 柳河岚 23:50 才破万，21:44 那几行却被整行涂成「+0 已破万」；
//	  ② 窗口用「窗口内增量最大的那一格」猜，不是异常自己的时段
//	     ⇒ 标题写郑伊安疑似异常，却贴出崔志宇的时段；
//	  ③ 破万后的真实增量（10000→11000 的 1000）被丢弃。
//	现在只认 signstat.DetailWindow —— 面板、飞书/QQ 的 PNG、邮件正文
//
//	三处共用同一份数据，口径不可能再分叉。
//
// 行 = 每个采样时刻，列 = 各超话（与面板一致，不转置）。
func signMonitorSpikeDetailTable(dw *signstat.DetailWindow, anomOID string, threshold int, intervalMin int) string {
	if dw == nil || len(dw.Stamps) == 0 || len(dw.Rows) == 0 {
		return ""
	}
	loc := time.UnixMilli(dw.From).Location()

	// 异常者置顶，其余按**年龄顺序**（2026-10-08 用户指定的固定顺序）
	rows := make([]signstat.DetailRow, len(dw.Rows))
	copy(rows, dw.Rows)
	signstat.SortByAgeDetailRows(rows)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].OID == anomOID && rows[j].OID != anomOID
	})

	var b strings.Builder
	fmt.Fprintf(&b, `<h3 style="margin:22px 0 6px;font-size:15px">异常时段 %d 分钟粒度明细</h3>`, intervalMin)
	fmt.Fprintf(&b, `<p style="margin:0 0 6px;color:#667085;font-size:12px">每格 = 与上一采样点的签到增量，异常时刻在<b>最后一列</b>。`+
		`标注 <b style="color:#b54708">*</b> 表示该超话已破万：<b>数字是已确认的增量</b>，`+
		`* 表示该区间内还有拿不到的额外增量（<b>+0* 不等于没涨</b>）。`+
		`下方黄色说明列出的是哪些超话带 *，只有各自破万时刻之后的那几格才带。</p>`)

	fmt.Fprintf(&b, `<table style="width:100%%;border-collapse:collapse;font-size:12px">`)
	fmt.Fprintf(&b, `<tr style="background:#f9fafb">`)
	fmt.Fprintf(&b, `<th style="text-align:left;padding:5px 8px;border-bottom:1px solid #e4e7ec">时刻</th>`)
	for _, r := range rows {
		name := html.EscapeString(truncateStr(r.Name, 10))
		if r.OID == anomOID {
			name = "▲ " + name
		}
		fmt.Fprintf(&b, `<th style="text-align:right;padding:5px 8px;border-bottom:1px solid #e4e7ec">%s</th>`, name)
	}
	fmt.Fprintf(&b, `<th style="text-align:right;padding:5px 8px;border-bottom:1px solid #e4e7ec">同组中位数</th>`)
	fmt.Fprintf(&b, `</tr>`)

	for ci, ts := range dw.Stamps {
		fmt.Fprintf(&b, `<tr>`)
		fmt.Fprintf(&b, `<td style="padding:4px 8px;border-bottom:1px solid #f2f4f7;color:#667085">%s</td>`,
			time.UnixMilli(ts).In(loc).Format("15:04"))

		deltas := make([]int, len(rows))
		anyCrossed := false
		for ri, r := range rows {
			var d *int
			if ci < len(r.Deltas) {
				d = r.Deltas[ci]
			}
			mark := ""
			if ci < len(r.Marks) {
				mark = r.Marks[ci]
			}
			star := ""
			if mark == "crossover" {
				star = "*"
				anyCrossed = true
			}
			style := "text-align:right;padding:4px 8px;border-bottom:1px solid #f2f4f7"
			if d == nil {
				if star != "" {
					fmt.Fprintf(&b, `<td style="%s;color:#b54708">+0*</td>`, style)
				} else {
					fmt.Fprintf(&b, `<td style="%s;color:#667085">—</td>`, style)
				}
				continue
			}
			deltas[ri] = *d
			switch {
			case *d > 0 && threshold > 0 && *d >= threshold:
				fmt.Fprintf(&b, `<td style="%s;color:#e0484d;font-weight:600">+%d%s</td>`, style, *d, star)
			case *d > 0:
				fmt.Fprintf(&b, `<td style="%s;color:#e0484d;font-weight:600">+%d%s</td>`, style, *d, star)
			case *d < 0:
				fmt.Fprintf(&b, `<td style="%s;color:#12a150">%d%s</td>`, style, *d, star)
			default:
				if star != "" {
					fmt.Fprintf(&b, `<td style="%s;color:#b54708">+0*</td>`, style)
				} else {
					fmt.Fprintf(&b, `<td style="%s;color:#667085">+0</td>`, style)
				}
			}
		}

		// 同组中位数：破万格是「已知下界」不是真实值，会把基线拉歪 ⇒ 剔除。
		// ★ 这里用「这一格是否带 crossover 标记」判断，不是「有没有破过万」。
		if anyCrossed {
			valid := make([]int, 0, len(deltas))
			for ri, r := range rows {
				mark := ""
				if ci < len(r.Marks) {
					mark = r.Marks[ci]
				}
				if mark == "crossover" || r.Deltas[ci] == nil {
					continue
				}
				valid = append(valid, deltas[ri])
			}
			if len(valid) > 0 {
				fmt.Fprintf(&b, `<td style="text-align:right;padding:4px 8px;border-bottom:1px solid #f2f4f7;color:#667085">%+d</td>`,
					medianInt64(toI64(valid)))
			} else {
				fmt.Fprintf(&b, `<td style="text-align:right;padding:4px 8px;border-bottom:1px solid #f2f4f7;color:#b9c0cc">—</td>`)
			}
		} else {
			fmt.Fprintf(&b, `<td style="text-align:right;padding:4px 8px;border-bottom:1px solid #f2f4f7;color:#667085">%+d</td>`,
				medianInt64(toI64(deltas)))
		}
		fmt.Fprintf(&b, `</tr>`)
	}
	fmt.Fprintf(&b, `</table>`)

	// ★ 表下按「每个超话实际破万时刻」说明，避免把整个窗口都误读成破万
	notes := []string{}
	for _, r := range rows {
		if !r.Fuzzy {
			continue
		}
		notes = append(notes, fmt.Sprintf("%s 在本表中标 * 的格子即破万之后", html.EscapeString(r.Name)))
	}
	if len(notes) > 0 {
		fmt.Fprintf(&b, `<p style="margin:8px 0 0;padding:8px 10px;background:#fffaeb;border-left:3px solid #f79009;`+
			`font-size:12px;color:#b54708">%s。星号只加在<b>各自破万时刻之后</b>的格子上。</p>`,
			strings.Join(notes, "；"))
	}
	return b.String()
}

// toI64 把 []int 转成 []int64（medianInt64 只吃 []int64）。
func toI64(v []int) []int64 {
	out := make([]int64, len(v))
	for i, n := range v {
		out[i] = int64(n)
	}
	return out
}

// signMonitorReportHTML 生成邮件正文（简易报表：结论 + 表格 + 关键数字）。
func signMonitorReportHTML(in signMonitorReportInput) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:-apple-system,'PingFang SC','Microsoft YaHei',sans-serif;color:#101828">`)
	fmt.Fprintf(&b, `<h2 style="margin:0 0 4px">%s</h2>`, html.EscapeString(in.Title))
	fmt.Fprintf(&b, `<p style="margin:0 0 16px;color:#667085">%s</p>`, html.EscapeString(in.Subtitle))

	// ★ Notes：本次没能出图的组（用户 2026-10-07 14:48 要求）。
	// 图消失时必须说清**为什么**（都破万了）和**统计截止到什么时候**，
	// 否则看起来像功能坏了。黄色提示块，和异常区块（红色）区分开。
	if len(in.Notes) > 0 {
		b.WriteString(`<div style="border:1px solid #fedf89;background:#fffaeb;padding:12px 14px;border-radius:10px;margin-bottom:16px">`)
		fmt.Fprintf(&b, `<b style="color:#b54708">%d 个分组本次未出偏移图</b><ul style="margin:8px 0 0;padding-left:20px">`, len(in.Notes))
		for _, n := range in.Notes {
			fmt.Fprintf(&b, `<li style="margin:4px 0;font-size:13px">%s</li>`, html.EscapeString(n))
		}
		b.WriteString(`</ul></div>`)
	}

	if len(in.Spikes) > 0 {
		b.WriteString(`<div style="border:1px solid #fda29b;background:#fff4f2;padding:12px 14px;border-radius:10px;margin-bottom:16px">`)
		fmt.Fprintf(&b, `<b style="color:#b42318">检测到 %d 条异常涨幅</b><ul style="margin:8px 0 0;padding-left:20px">`, len(in.Spikes))
		for _, s := range in.Spikes {
			group := ""
			if s.GroupName != "" {
				group = fmt.Sprintf("，同组「%s」中位数 +%d / 阈值 +%d", s.GroupName, s.GroupMedian, s.GroupThreshold)
			}
			fmt.Fprintf(&b, `<li>%s：%d → %d（<b>+%d</b> / %d 分钟%s）</li>`,
				html.EscapeString(s.Name), s.From, s.To, s.Delta, s.WindowMin, html.EscapeString(group))
		}
		b.WriteString(`</ul></div>`)
	} else {
		b.WriteString(`<p style="padding:10px 12px;background:#f2f4f7;border-radius:8px">本次统计区间内没有异常涨幅。</p>`)
	}

	// ★★★ 「当前 / 区间最低 / 区间最高 / 采样点」那张分组统计表已删除
	//   （2026-10-08 用户：「我不知道为什么要发这个，我从来没让你发过
	//   什么东西」「看着又不清晰，又没有什么用的，啥也看不出来」）。
	//   它混了跨日归零的暴跌（区间最低 493 这种值），本来就不可读；
	//   真正要看的就是下面的逐格增量明细。

	// ★ 日报正文主体 = 小时表（与附件 PNG 完全同一份数据）
	if in.HourlyHTML != "" {
		b.WriteString(in.HourlyHTML)
	}

	// 异常时段逐格增量明细：**只认 DetailWindow**（面板/PNG/邮件同一份数据）
	if in.Detail != nil && len(in.Detail.Stamps) > 0 {
		b.WriteString(signMonitorSpikeDetailTable(in.Detail, in.AnomOID, in.DetailThreshold, in.IntervalMinutes))
	}
	fmt.Fprintf(&b, `<p style="margin-top:20px;color:#98a2b3;font-size:12px">采样间隔 %d 分钟；原始逐点数据见同邮件的 CSV 附件。</p></div>`, in.IntervalMinutes)
	return b.String()
}

// signMonitorCSV 生成原始逐点数据（Excel 可直接打开，带 BOM 防中文乱码）。
func signMonitorCSV(series []signMonitorSeries) []byte {
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // BOM：Excel 打开中文不乱码
	w := csv.NewWriter(&buf)
	w.Write([]string{"时间", "超话", "OID", "今日签到", "较上一点增量"})
	byOID := make(map[string][]smPoint, len(series))
	rows := make([]smPoint, 0)
	for _, s := range series {
		pts := make([]smPoint, 0, len(s.Points))
		for _, p := range s.Points {
			pts = append(pts, smPoint{TS: p[0], Sign: int(p[1]), OID: s.OID, Name: s.Name})
		}
		byOID[s.OID] = pts
		rows = append(rows, pts...)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TS != rows[j].TS {
			return rows[i].TS < rows[j].TS
		}
		return rows[i].OID < rows[j].OID
	})
	for _, p := range rows {
		delta := ""
		pts := byOID[p.OID]
		for i := 1; i < len(pts); i++ {
			if pts[i].TS == p.TS {
				delta = fmt.Sprintf("%+d", pts[i].Sign-pts[i-1].Sign)
				break
			}
		}
		w.Write([]string{
			time.UnixMilli(p.TS).Format("2006-01-02 15:04:05"),
			p.Name, p.OID, fmt.Sprintf("%d", p.Sign), delta,
		})
	}
	w.Flush()
	return buf.Bytes()
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func truncateStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// signMonitorReportConfig 供 logic 读取报表开关。
// signMonitorReportEnabled 判定某一类报表要不要发。
//
// ★ daily 与 spike 是**两个独立开关**（2026-10-08 用户要求）：
//
//	daily 每天到点发，**不管有没有异常都发**
//	spike 只在检出异常时推
//
// 历史上只有一个 spikeReportEnabled，日报是无条件发的，
// 于是「想每天收日报但不想被异常打扰」这个组合配不出来。
//
// spike 开关兼容旧键：老配置只有 spikeReportEnabled 时仍然生效。
func signMonitorReportEnabled(cfg config.WeiboSignMonitorConfig, key string) bool {
	switch key {
	case "daily":
		return cfg.DailyReportEnabled
	case "spike":
		return cfg.AnomalyReportEnabled || cfg.SpikeReportEnabled
	}
	return false
}

// niceDevBound 把偏离极值向上取整到一个「好看」的刻度值。
//
// 与 signMonitorNiceStep 同思路，只是这里只需要 1/2/2.5/5/10 × 10^n 的向上取整：
// 刻度必须**大于等于**极值，否则曲线还是会被顶到边缘。
// superTopicCrossoverSign 是超话签到数的「破万」门槛。
//
// ★ 实测（2026-10-07）：破万后接口只返回 1000 粒度的模糊值
//
//	（柳河岚YUHA 10-03=10019 精确 → 10-04=10000 模糊；
//	  郑伊安IAN 10-03=11448 → 10-04=12000）。
//	继续用它算偏移只会得到噪声（分子分母都在按千位跳变）。
const superTopicCrossoverSign = 10000

// filterBelowCrossover 剔除已破万的序列，只留还能拿到精确值的成员。
//
// 这是**动态**过滤：破万时刻本身也是信号（什么时候破的万），
// 由 caller 结合 breakAt 记录到图上。
func filterBelowCrossover(series []signMonitorSeries) []signMonitorSeries {
	out := make([]signMonitorSeries, 0, len(series))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		// ★ 只要**历史上有任何一点** ≥10000 就剔除，不只看末点。
		//   破万后接口给的是模糊值（10000/12000 反复跳），末点可能恰好回落
		//   到 10000 以下 —— 那种情况下数据其实一样不可信。
		crossed := false
		for _, p := range s.Points {
			if p[1] >= superTopicCrossoverSign {
				crossed = true
				break
			}
		}
		if crossed {
			continue
		}
		out = append(out, s)
	}
	return out
}

// crossoverName 返回首个已破万超话的名字（用于图上注明）。
// 多个时返回数量+ 第一个名字。
func crossoverInfo(series []signMonitorSeries) (count int, firstName string, at time.Time) {
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		// ★ 正序扫描，取**第一次** ≥10000 的时刻（2026-10-07 用户要「破万的具体时间」）。
		//   原实现倒序扫，命中就 break ⇒ 记的是最后一个 ≥10000 的点，
		//   破万后又涨几千的话时间会偏到接近当天末尾，误导「什么时候开始刷的」。
		for i := 0; i < len(s.Points); i++ {
			if s.Points[i][1] >= superTopicCrossoverSign {
				count++
				if firstName == "" {
					firstName = s.Name
					at = time.UnixMilli(s.Points[i][0])
				}
				break
			}
		}
	}
	return count, firstName, at
}

// abs64 返回绝对值（用于「偏离是否超阈值」判定）。
func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// niceBound 向上取整到「以 5 或 0 结尾」的整数刻度。
//
// 用户 2026-10-07 提议的规则：取差值绝对值最大的那个，再往上找一个
// 以 5 或 0 结尾的数当刻度上界（36 → 50、7 → 10、3 → 5）。
// 每个数量级内取 1/2/3/5/10 里的第一个 ≥ v 的 —— 2.5 这种半档不要。
func niceBound(v int64) int64 {
	if v <= 0 {
		return 0
	}
	mag := int64(math.Pow(10, math.Floor(math.Log10(float64(v)))))
	for _, m := range []int64{1, 2, 3, 5, 10} {
		step := m * mag
		if step > 0 && v <= step {
			return step
		}
	}
	return mag * 10
}

// signPrefix 返回带符号前缀（正值给 "+"，负值给 "-"，0 给空）。
func signPrefix(v int64) string {
	if v > 0 {
		return "+"
	}
	if v < 0 {
		return ""
	}
	return ""
}

// anomalyWindows 找出所有「异常点」的下标。
//
// ★ 2026-10-07 18:00 用户纠正口径：dev 本身已经是
//
//	「**这一步**增量 - 同组这一步增量的中位数」，
//	所以直接比阈值即可 —— **不需要**再减「半小时前的累积」
//	（原实现减了一次等于「增量的增量」，正常波动也会超阈值 ⇒ 过度反应）。
//
// 判定：任一人的 |dev| ≥ minJump（单步，5 分钟一步）。
// 连续超阈值**只报一次**（取段首），间隔 ≥2 个采样点才算新事件。
func anomalyWindows(dev [][]int64, stamps []int64, minJump int) []int {
	if minJump <= 0 || len(stamps) == 0 {
		return nil
	}
	n := len(stamps)
	anyOver := func(j int) bool {
		for i := range dev {
			if abs64(dev[i][j]) >= int64(minJump) {
				return true
			}
		}
		return false
	}
	var out []int
	lastReported := -1
	const minGap = 2 // 去抖：间隔 <2 个采样点视为同一次
	for j := 0; j < n; j++ {
		if !anyOver(j) {
			continue
		}
		if lastReported >= 0 && j-lastReported < minGap {
			continue
		}
		out = append(out, j)
		lastReported = j
	}
	return out
}

// buildSignMonitorDeviationSVG 画「相对组内中位数的偏离」曲线。
//
// ★ 为什么需要它（2026-10-07 实测）：一整天的**累计**曲线里，万档组 4 条线
//
//	起点都是 0、终点都在 8000~10000，四条线互相挨着，肉眼分不出谁异常。
//	上午 15:10 那次 +1500 的突增，摊在 9000 的纵轴里只有 17% 的抬升，
//	完全被淹没了 —— 而「找出谁在刷量」正是这个功能的唯一目的。
//
// 做法：同一时刻减去**组内中位数曲线**（逐时刻算，不是一次性减终值）。
// 于是「大家都在涨」被抵消掉，图上剩下的就是「谁比同伴多涨了多少」。
// labelThreshold 是「线尾标数值」的门槛（绝对人数）。
//
// ★ 必须与 detectSpikes 告警用同一个 `spikeAbsolute`（默认 800）——
//
//	否则会出现「图上没标但已告警」或「图上标了但阈值没到」的错位。
//	早先用 bound/3 是错的：bound 按实际最大偏离取，数据量小时人人超阈值。
//
// halfHourSamples 半小时对应的采样点数（5 分钟采样 ⇒ 6 个点）。
const halfHourSamples = 6

// minAnomalyJump 异常判定阈值：**半小时内比同组中位数多涨 ≥ 400 人**。
//
// ★ 2026-10-07 18:00 用户定：「就定个半小时 400 吧，800 太宽了」。
//
//	口径是「半小时的**多涨量**」（偏离的增量），不是偏离累积值 ——
//	累积值会永久超阈值导致误报（见 anomalyWindows 注释）。
const minAnomalyJump = 400

// labelThreshold 已并入 minAnomalyJump —— 保留参数是为了兼容现有调用签名。
func buildSignMonitorDeviationSVG(series []signMonitorSeries, title, subtitle string, width, height, labelThreshold int) string {
	_ = labelThreshold
	// ★ 先剔除已破万的成员（2026-10-07 用户决策）：破万后接口给 1000粒度模糊值。
	//
	//   ★ 门槛是 **≥2**（用户 14:48 明确规则）：
	//     ≥3 人 → 基线 = 组内中位数
	//      2 人 → 基线 = 对方那个人（差值口径），**继续画图**
	//      1 人 → 无法比较，返回空，由调用方标注「截止时间 + 原因」
	series = filterBelowCrossover(series)
	if len(series) < 2 {
		return ""
	}
	// ★ 2 人组走差值口径：中位数不存在，只有「谁比另一个人多涨」。
	twoPerson := len(series) == 2

	titleHead := 0
	if title != "" {
		titleHead = 40
	}

	// ★★ 图例要能列**所有异常时刻**（用户 17:01），宽度与高度都依赖异常时刻个数，
	//   所以必须先算出数据、再定pad / 布局。
	// padLeft：Y 轴刻度（±3,000 约 46px）；padFoot：X 轴标签
	const (
		padLeft = 64
		padFoot = 28
	)
	padTop := 16 + titleHead

	// ---------- 采样时刻对齐 ----------
	allTS := map[int64]bool{}
	for _, s := range series {
		for _, p := range s.Points {
			allTS[p[0]] = true
		}
	}
	stamps := make([]int64, 0, len(allTS))
	for ts := range allTS {
		stamps = append(stamps, ts)
	}
	if len(stamps) < 2 {
		return ""
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })

	// 每人在各时刻的累计值（缺采样点用前值保持）
	vals := make([][]int64, len(series))
	for i, s := range series {
		m := map[int64]int64{}
		for _, p := range s.Points {
			m[p[0]] = p[1]
		}
		vals[i] = make([]int64, len(stamps))
		var last int64
		for j, ts := range stamps {
			if v, ok := m[ts]; ok {
				last = v
			}
			vals[i][j] = last
		}
	}

	// ---------- 偏离计算 ----------
	// dev = (累计值 - 各自起始值) - 逐时刻基线
	//   ≥3 人：基线 = 组内「累计增量」的中位数（偶数取中间两个平均）
	//    2 人：基线 = 对方那个人的累计增量（互为相反数，必定一正一负）
	cum := make([][]int64, len(series))
	for i := range series {
		cum[i] = make([]int64, len(stamps))
		base := vals[i][0]
		for j := range stamps {
			cum[i][j] = vals[i][j] - base
		}
	}
	// ★★★ 偏离 = **本步增量** - **同组本步增量的中位数**（用户 18:00 纠正）
	//
	//   旧口径 dev = 累计值 - 累计值中位数 是**累积偏离**，
	//   数学上等于「每次增量偏离的累加」⇒ **必然单调发散**，
	//   图上只会看到「越来越规律地拉大」，异常被平滑掉了（用户原话：
	//   「这就是问题所在，这张图越往后越有规律，就这个斜率」）。
	//
	//   正确：每个采样点算「我这一步比同组多涨/少涨多少」，围绕 0 上下波动，
	//   某次刷量就是一个**尖峰**，一眼可见。
	dev := make([][]int64, len(series))
	incr := make([][]int64, len(series)) // 各人的逐步增量
	for i := range series {
		incr[i] = make([]int64, len(stamps))
		for j := 1; j < len(stamps); j++ {
			incr[i][j] = vals[i][j] - vals[i][j-1]
		}
	}
	for i := range series {
		dev[i] = make([]int64, len(stamps))
		for j := range stamps {
			if j == 0 {
				continue // 起点没有增量，偏离记0
			}
			if twoPerson {
				// 2 人：基线 = 对方这一步的增量（互为相反数，必定一正一负）
				other := 1
				if i == 1 {
					other = 0
				}
				dev[i][j] = incr[i][j] - incr[other][j]
			} else {
				buf := make([]int64, 0, len(series))
				for k := range series {
					buf = append(buf, incr[k][j])
				}
				dev[i][j] = incr[i][j] - medianInt64(buf)
			}
		}
	}

	minTS, maxTS := stamps[0], stamps[len(stamps)-1]

	// ---------- 异常时刻（**全部**，不是只取最早） ----------
	// 用户 17:01：「如果有多个异常值，就需要把这几个异常值的 4 个数据都贴出来」
	// 判定：任一人偏离超阈值 ⇒ 该时刻异常（最密集，避免图上重复堆叠）
	// ★★ 异常窗口判定（2026-10-07 17:10 用户纠正，18:00 定阈值）：
	//
	//   旧逻辑「偏离值 ≥ 阈值」是**错的** —— 偏离是累积量，
	//   某人一旦比同伴多涨 800，之后每个采样点都超阈值 ⇒ 永久误报。
	//   （实测真实数据偏离已 +1838，那是「今天 IAN 就是比 stella 多涨」，
	//     不是「刚刚突然涨了很多」。）
	//
	//   正确：看**半小时内比同组多涨了多少**（偏离的增量），
	//   ≥ minJump（400）才算异常。
	anomalyIdx := anomalyWindows(dev, stamps, minAnomalyJump)

	// ---------- 图幅尺寸（依赖图例行数） ----------
	// 图例每项：正常态 1 行数值；异常态 len(anomalyIdx) 行
	// 正常态每项 1 行；异常态每项2 行文字（半小时多涨量 + 累计偏离）× 窗口数
	legendRows := 1
	if len(anomalyIdx) > 0 {
		legendRows = len(anomalyIdx) * 2
	}
	legendNameW := 96 // 名字（8 汉字 ≈ 88px）
	legendValW := 84  // 「异常 15:04」+ 数值
	padRight := legendNameW + legendValW + 16

	// 高度：图例总高 + 标题头 + 底部标签
	perPerson := func() int { return 22 + legendRows*14 }
	legendH := perPerson()*len(series) + 8
	needH := padTop + legendH + padFoot
	h := height
	if needH > h {
		h = needH // 图例撑不下时自动加高（不要压到绘图区）
	}

	innerW := width - padLeft - padRight
	innerH := h - padTop - padFoot

	// ---------- Y 轴范围（正负共用同一上界，以 5/0 结尾） ----------
	maxPos, maxNeg := int64(0), int64(0)
	for i := range series {
		for j := range stamps {
			d := dev[i][j]
			if d > maxPos {
				maxPos = d
			}
			if -d > maxNeg {
				maxNeg = -d
			}
		}
	}
	absMax := maxPos
	if maxNeg > absMax {
		absMax = maxNeg
	}
	bound := niceBound(absMax)
	if bound < 1 {
		bound = 1
	}
	yTop := float64(bound) * 1.12
	yBot := -yTop

	x := func(ts int64) float64 {
		return float64(padLeft) + float64(ts-minTS)/float64(maxTS-minTS)*float64(innerW)
	}
	y := func(v int64) float64 {
		return float64(padTop) + (yTop-float64(v))/(yTop-yBot)*float64(innerH)
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`,
		width, h, width, h)
	b.WriteString(`<rect width="100%" height="100%" fill="#ffffff"/>`)
	if title != "" {
		fmt.Fprintf(&b, `<text x="%d" y="22" font-size="15" font-weight="600" fill="#101828">%s</text>`,
			padLeft, html.EscapeString(title))
		if subtitle != "" {
			fmt.Fprintf(&b, `<text x="%d" y="40" font-size="11" fill="#98a2b3">%s</text>`,
				padLeft, html.EscapeString(subtitle))
		}
	}

	// ---------- Y 轴网格 ----------
	step := niceBound(bound / 3)
	if step < 1 {
		step = 1
	}
	for v := -bound; v <= bound; v += step {
		yy := y(v)
		if v == 0 {
			continue
		}
		fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#eef0f3"/>`,
			padLeft, yy, padLeft+innerW, yy)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#98a2b3" text-anchor="end">%s</text>`,
			padLeft-8, yy+4, comma(int(v)))
	}
	// 零线（同组中位数基线）加粗
	zeroY := y(0)
	fmt.Fprintf(&b, `<line x1="%d" y1="%.1f" x2="%d" y2="%.1f" stroke="#98a2b3" stroke-width="1.4" stroke-dasharray="5 4"/>`,
		padLeft, zeroY, padLeft+innerW, zeroY)
	fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" font-weight="600" fill="#667085" text-anchor="end">+%s</text>`,
		padLeft-8, y(int64(yTop/1.12))+4, comma(int(bound)))
	fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" fill="#667085" text-anchor="end">0</text>`,
		padLeft-8, zeroY+4)
	fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" font-weight="600" fill="#667085" text-anchor="end">-%s</text>`,
		padLeft-8, y(int64(yBot/1.12))+4, comma(int(bound)))

	// ---------- X 轴时间刻度（数量按宽度自适应） ----------
	nTicks := signMonitorTickCount(innerW, time.Duration(maxTS-minTS)*time.Millisecond)
	for i := 0; i <= nTicks; i++ {
		ts := minTS + int64(float64(maxTS-minTS)*float64(i)/float64(nTicks))
		label := time.UnixMilli(ts).Format("15:04")
		if maxTS-minTS > 12*3600*1000 {
			label = time.UnixMilli(ts).Format("01-02 15:04")
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" font-size="11" fill="#98a2b3" text-anchor="middle">%s</text>`,
			x(ts), padTop+innerH+18, label)
	}

	// ---------- 曲线 ----------
	colors := []string{"#2f6bff", "#e0484d", "#12a150", "#8a5cf5", "#f59e0b", "#0ea5e9", "#ec4899", "#64748b"}
	for i := range series {
		color := colors[i%len(colors)]
		var d strings.Builder
		// ★★ 阶梯线（step-after），不是斜线：采样每 5 分钟一次，
		//   两个采样点之间没有任何观测值。斜线会让人以为中间时刻也有
		//   连续变化的数据 —— 那是错觉；阶梯线明确表达
		//   「这个值一直有效到下一次采样」（用户 2026-10-07 20:47 指出）。
		for j := range stamps {
			if j == 0 {
				fmt.Fprintf(&d, "M%.1f,%.1f", x(stamps[j]), y(dev[i][j]))
				continue
			}
			// 先水平延伸到本时刻（保持上一个观测值），再垂直跳到新值
			fmt.Fprintf(&d, "L%.1f,%.1f", x(stamps[j]), y(dev[i][j-1]))
			fmt.Fprintf(&d, "L%.1f,%.1f", x(stamps[j]), y(dev[i][j]))
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="%s" stroke-width="2"/>`, d.String(), color)
	}

	// ---------- 右侧图例：名字 + 值（★ 数值在这里，线尾不标） ----------
	//
	// 正常态 → 一行「末点 ±N」
	// 异常态 → **每个异常时刻一行**，贴出该时刻**该人**的偏离；
	//   多个异常时刻全部列出（用户 17:01）。
	legendY := float64(padTop + 6)
	lx := padLeft + innerW + 16
	valColor := func(v int64) string {
		switch {
		case v > 0:
			return "#e0484d"
		case v < 0:
			return "#12a150"
		default:
			return "#98a2b3"
		}
	}
	for i := range series {
		color := colors[i%len(colors)]
		fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="10" height="10" fill="%s"/>`, lx, legendY, color)
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" fill="#475467">%s</text>`,
			lx+16, legendY+9, html.EscapeString(truncateStr(series[i].Name, 8)))

		// ★ 正常态：显示**末点累计偏离**（回答「谁比同伴多涨得多」）
		if len(anomalyIdx) == 0 {
			v := dev[i][len(dev[i])-1]
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#98a2b3">末点偏离</text>`,
				lx+16, legendY+21)
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="12" font-weight="700" fill="%s">%s%s</text>`,
				lx+70, legendY+21, valColor(v), signPrefix(v), comma(int(v)))
			legendY += 24
			continue
		}

		// ★ 异常态：**每个异常窗口一行**，显示该窗口的「半小时多涨量」
		//   （dev(t) - dev(t-halfHour)），不是累积偏离 ——
		//   累积偏离是「今天一共多涨多少」，与「突然多涨」是两件事。
		for k, j := range anomalyIdx {
			jump := dev[i][j] // dev 已是「单步增量 - 增量中位数」
			// 同时给出累计偏离，方便看「到这个时刻一共多涨多少」
			cum := dev[i][j]
			// ★ 每个异常时刻都完整写出「异常 HH:MM」
			//   （用户 17:01：「把这几个异常值的 4 个数据都贴出来」）。
			//   早先用空白占位 ⇒ 第二个异常时刻没有时间标签，等于漏报。
			tag := "异常"
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" font-weight="700" fill="#b42318">%s %s</text>`,
				lx+16, legendY+float64(k)*26+21, tag, time.UnixMilli(stamps[j]).Format("15:04"))
			// 第 1 行：半小时多涨量（判定依据）
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="11" font-weight="700" fill="%s">%s%s</text>`,
				lx+74, legendY+float64(k)*26+21, valColor(jump), signPrefix(jump), comma(int(jump)))
			// 第 2 行：累计偏离（背景参考，灰色不抢眼）
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-size="10" fill="#98a2b3">累计 %s%s</text>`,
				lx+74, legendY+float64(k)*26+33, signPrefix(cum), comma(int(cum)))
		}
		legendY += float64(len(anomalyIdx))*26 + 8
	}

	b.WriteString(`</svg>`)
	return b.String()
}
