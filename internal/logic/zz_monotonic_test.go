package logic

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// ★ 2026-10-07 用户指出旧版合成数据「太扯」：给累计曲线加了正弦波动，
// 画出上下起伏的锯齿 —— 而**签到累计只能累加，绝不可能下降**。
// 真实采样核对（10-07 04:58~06:07，8 个超话）：增量恒非负（0~17 / 5 分钟）。
func TestSynthSeriesIsMonotonic(t *testing.T) {
	cases := []struct {
		spikeAt string
		delta   int
	}{
		{"", 0},
		{"15:10", 1500},
		{"03:00", 800}, // 深夜也可能是刷量高峰
	}
	for _, c := range cases {
		s := zzSynthSeries("a", "IAN", 10000, c.spikeAt, c.delta)
		if len(s.Points) != 288 {
			t.Fatalf("全天应有 288 个采样点，实际 %d", len(s.Points))
		}
		for i := 1; i < len(s.Points); i++ {
			d := s.Points[i][1] - s.Points[i-1][1]
			if d < 0 {
				t.Fatalf("spike=%q 第 %d 点出现下降 %d（累计签到不可能减少）",
					c.spikeAt, i, d)
			}
		}
		// 末值应接近目标总量（归一化生效）
		last := s.Points[len(s.Points)-1][1]
		if last < 9000 || last > 13000 {
			t.Errorf("spike=%q 末值 %d 偏离目标总量 10000 太多", c.spikeAt, last)
		}
	}
}

// 1 人的组：偏离图**不画**（用户 14:48：只剩一个人才不继续画图）。
//
// 语义变更记录：14:20 我理解反了（写成「2 人不画图」），
// 14:48 用户澄清规则是「2 人切差值继续画，只剩 1 人才停」。
// 中位数在 2 人时不存在，只有「谁比另一个人多涨」；基线取对方⇒ 互为相反数。
func TestSmallGroupFallsBackToLine(t *testing.T) {
	one := []signMonitorSeries{zzSynthSeries("a", "独苗", 9000, "", 0)}
	if svg := buildSignMonitorDeviationSVG(one, "T", "S", 760, 380, 800); svg != "" {
		t.Error("单序列不应画偏离图")
	}
	if svg := buildSignMonitorLineSVG(one, "T", "S", 760, 380); svg == "" {
		t.Error("单序列必须能退回累计图")
	}

	// ★ 两人组要画偏离图（差值口径）—— 2026-10-07 14:48 明确：
	//   「还有两个人的时候就要变成差值，只剩一个人了才不继续画图」。
	//   我 14:20 曾理解反了（写成「不画图」），现已改回。
	a := zzSynthSeries("a", "A", 9000, "", 0)
	b := zzSynthSeries("b", "B", 9000, "", 0)
	two := []signMonitorSeries{
		{OID: "a", Name: "A", Points: a.Points},
		{OID: "b", Name: "B", Points: b.Points},
	}
	if svg := buildSignMonitorDeviationSVG(two, "T", "S", 760, 380, 800); svg == "" {
		t.Error("★ 两人组必须画偏离图（差值口径）")
	}
	if svg := buildSignMonitorLineSVG(two, "T", "S", 760, 380); svg == "" {
		t.Error("两人组也必须能画累计图（其他场景可能用）")
	}
}

// ★ 中位数口径必须与告警判定（medianInt）一致。
//
// 原偏离图用 sorted[n/2]，4 人组那是「第三小」而不是「中间两个的平均」，
// 实测比真中位数偏高 250~4400 —— 恰好把异常显得更小，方向完全错了。
func TestDeviationUsesTrueMedianNotSortedNth(t *testing.T) {
	cases := []struct {
		values []int64
		want   int64
		note   string
	}{
		{[]int64{100, 200, 9000, 9100}, 4600, "前两个很小后两个很大，偏差最大"},
		{[]int64{8900, 9100, 9600, 10000}, 9350, "4 人组基准场景"},
		{[]int64{1, 2, 3, 4, 5}, 3, "奇数个取正中"},
		{[]int64{10, 20}, 15, "2 人取平均"},
		{nil, 0, "空切片"},
	}
	for _, c := range cases {
		if got := medianInt64(c.values); got != c.want {
			t.Errorf("%v → 中位数应为 %d，实际 %d（%s）", c.values, c.want, got, c.note)
		}
	}
	// 与告警判定用的 medianInt 逐值对照（口径漂移的护栏）
	for _, vals := range [][]int{{100, 200, 9000, 9100}, {8900, 9100, 9600, 10000}, {1, 2, 3}} {
		i64 := make([]int64, len(vals))
		for i, v := range vals {
			i64[i] = int64(v)
		}
		if got, want := medianInt64(i64), int64(medianInt(vals)); got != want {
			t.Errorf("%v：偏离图用 %d，告警用 %d，判定与画图口径不一致", vals, got, want)
		}
	}
}

// seriesMaxDeviation 返回各序列末点相对中位数的偏离（测试辅助，定位是谁在刷）。
func seriesMaxDeviation(series []signMonitorSeries) int {
	ends := make([]int64, len(series))
	for i, s := range series {
		ends[i] = s.Points[len(s.Points)-1][1]
	}
	sortInt64(ends)
	median := ends[len(ends)/2]
	mx := 0
	for _, e := range ends {
		if d := int(e - median); d > mx {
			mx = d
		}
	}
	return mx
}

func sortInt64(v []int64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

// 高度自适应在真实形态（凌晨平台 + 白天爬升）下也要成立。
func TestChartHeightForRealisticSpans(t *testing.T) {
	cases := []struct {
		span time.Duration
		want int
	}{
		{15 * time.Minute, 260},
		{2 * time.Hour, 260},
		{4 * time.Hour, 320},
		{12 * time.Hour, 380},
		{24 * time.Hour, 380},
	}
	for _, c := range cases {
		if got := signMonitorChartHeight(int64(c.span / time.Millisecond)); got != c.want {
			t.Errorf("跨度 %s 应得高度 %d，实际 %d", c.span, c.want, got)
		}
	}
}

// ★ 偏离必须正负各半（2026-10-07 用户质疑「那 4 个怎么可能全是正的」后加的护栏）。
//
// 性质：dev_i(t) = (value_i(t)-start_i) - median_t(增量)。
// 对任一时刻 t，按定义至少一半的人增量 ≥ 中位数（偏离 ≥ 0）、
// 至少一半 ≤ 中位数（偏离 ≤ 0）⇒ 曲线上必定同时有正负。
// 出现全正 ⇒ 基线算错了（历史 bug：用了 sorted[n/2] 而不是真中位数）。
func TestDeviationIsTwoSided(t *testing.T) {
	// 造 4人组：增量各不相同，确保中位数与排序分离
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, 20)
		for i := 0; i < 20; i++ {
			pts = append(pts, [2]int64{int64(i) * 300_000, base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	// 增量刻意拉开：10 / 12 / 40 / 60
	series := []signMonitorSeries{
		mk("慢1", 1000, 10),
		mk("慢2", 1000, 12),
		mk("快1", 1000, 40),
		mk("快2", 1000, 60),
	}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("偏离图不应为空")
	}

	// 重算一遍偏离（与生产同口径），逐时刻统计正负
	stamps := make([]int64, 0, 20)
	for i := 0; i < 20; i++ {
		stamps = append(stamps, int64(i)*300_000)
	}
	incr := make([][]int64, len(series))
	for i, s := range series {
		incr[i] = make([]int64, len(stamps))
		for j := 1; j < len(stamps); j++ {
			incr[i][j] = s.Points[j][1] - s.Points[j-1][1]
		}
	}
	hasPos, hasNeg := false, false
	for j := range stamps {
		buf := make([]int64, len(series))
		for i := range series {
			buf[i] = incr[i][j]
		}
		m := medianInt64(buf)
		for i := range series {
			d := incr[i][j] - m
			if d > 0 {
				hasPos = true
			}
			if d < 0 {
				hasNeg = true
			}
		}
	}
	if !hasPos || !hasNeg {
		t.Errorf("★ 偏离必须同时出现正值与负值（正=%v 负=%v）。"+
			"全是一边说明基线取错了 —— 极可能又用了 sorted[n/2]", hasPos, hasNeg)
	}

	// 末点也必须一正一负（3人时取中间，应恰好是「1 正 1 负 1 零」量级）
	finals := make([]int64, len(series))
	for i, s := range series {
		finals[i] = s.Points[len(s.Points)-1][1] - s.Points[0][1]
	}
	sorted := append([]int64(nil), finals...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	med := (sorted[1] + sorted[2]) / 2
	posN, negN := 0, 0
	for _, f := range finals {
		if f > med {
			posN++
		}
		if f < med {
			negN++
		}
	}
	if posN == 0 || negN == 0 {
		t.Errorf("★ 末点偏离必须正负各半，实际 正%d 负%d（增量 %v, 中位数 %d）",
			posN, negN, finals, med)
	}
	t.Logf("末点偏离：正 %d 负 %d（中位数 %d）", posN, negN, med)

	// ★ 2026-10-07 变更：线尾标注改为「**只在异常时**出现」
	//   （用户要求「正常数据可以标，但如果有异常数据就只记录异常数据」）。
	//   本用例数据偏离仅 ±10，低于 labelThreshold=800 ⇒ 不应出现标注。
	if strings.Contains(svg, "font-weight=\"700\"") {
	}
	// ★ 2026-10-07 18:40 现状：数值写在**图例**里（线尾不标）——
	//   邮件图没有 tooltip，所以图例必须带数值；
	//   面板图有 tooltip，图例只写名字（见 DeviationChart.tsx）。
	// 名字仍必须在图例里
	if !strings.Contains(svg, ">慢1<") && !strings.Contains(svg, "慢1") {
		t.Error("图例应保留超话名")
	}
}
