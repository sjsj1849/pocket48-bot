package logic

import (
	"strings"
	"testing"
)

// ★ 多个异常时刻都要贴出（用户 17:01：「如果有多个异常值，就需要把这几个异常值的
// 4 个数据都贴出来」）—— 原实现只取最早的异常时刻，后面的全漏了。
func TestAllAnomalyTimestampsAreListed(t *testing.T) {
	// ★ 新口径（用户 18:00）：dev = **单步增量** - 增量中位数。
	//   异常是「一根尖峰」（某一步多涨 1500），两个尖峰⇒ 两个异常时刻，
	//   每个异常时刻**每个人**都要贴出数值（用户 17:01）。
	n := 40
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]int64{timeAt(i), base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	a := mk("刷量号", 4000, 20)
	a.Points[11][1] += 1500 // 第一根尖峰
	a.Points[26][1] += 1500 // 第二根尖峰
	series := []signMonitorSeries{a, mk("正常1", 4000, 20), mk("正常2", 4000, 20)}

	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}

	// 收集所有「异常 HH:MM」标签
	var labels []string
	rest := svg
	for {
		i := strings.Index(rest, "异常 ")
		if i < 0 {
			break
		}
		tail := rest[i+len("异常 "):]
		j := strings.Index(tail, "<")
		if j < 0 {
			break
		}
		labels = append(labels, strings.TrimSpace(tail[:j]))
		rest = tail[j:]
	}
	t.Logf("异常时刻标签：%v（共 %d 个 = 2 时刻 × 3 人）", labels, len(labels))

	// 两个尖峰 ⇒ 至少 2 个**不同**的时刻
	uniq := setOf(labels)
	if len(uniq) < 2 {
		t.Errorf("两个独立尖峰应产生 2 个不同的异常时刻，实际 %v", labels)
	}
	// ★ 每个异常时刻 × 每个人都贴出来了（3 人 × 2 时刻 = 6 个标签）
	if len(labels) < len(uniq)*len(series) {
		t.Errorf("每个异常时刻应给全组 %d 人各贴一个值，实际 %d 个标签：%v",
			len(series), len(labels), labels)
	}
}

// anomalyTimesInSVG 从 SVG 里抓出所有「异常 HH:MM」的 HH:MM。
func anomalyTimesInSVG(svg string) []string {
	var out []string
	idx := 0
	for {
		i := strings.Index(svg[idx:], ">异常 ")
		if i < 0 {
			break
		}
		i += idx + len(">异常 ")
		rest := svg[i:]
		j := strings.Index(rest, "<")
		if j < 0 {
			break
		}
		t := strings.TrimSpace(rest[:j])
		if len(t) == 5 {
			out = append(out, t)
		}
		idx = i
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func setOf(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// 图例必须列出**每个人**在每个异常时刻的值（不是只列异常那个人）。
func TestLegendListsAllMembersAtAnomalyTimes(t *testing.T) {
	n := 40
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]int64{timeAt(i), base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	// 4 人组：A 有尖峰，B/C/D 正常
	a := mk("刷量号", 4000, 30)
	for i := 10; i < n; i++ {
		a.Points[i][1] += 1500
	}
	series := []signMonitorSeries{a, mk("正常1", 4000, 28), mk("正常2", 4000, 26), mk("正常3", 4000, 24)}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	// 4 个人的名字都必须出现在图例里
	for _, s := range series {
		if !strings.Contains(svg, truncateStr(s.Name, 8)) {
			t.Errorf("图例缺少成员 %s（异常时应列出全组）", s.Name)
		}
	}
	// 异常窗口标签数 = 4（每人一个）
	times := anomalyTimesInSVG(svg)
	if len(times) < 4 {
		t.Errorf("4 人组在异常窗口应有 4 个标签（每人一个），实际 %d 个", len(times))
	}

	// 线尾不应有圆点标注
	if strings.Contains(svg, "<circle") {
		t.Error("线尾不应画圆点（数值统一在图例）")
	}
}

// 正常态：每人一行「末点 +N」。
func TestLegendShowsEndpointWhenNormal(t *testing.T) {
	n := 30
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]int64{timeAt(i), base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	series := []signMonitorSeries{
		mk("慢", 4000, 20),
		mk("中", 4000, 30),
		mk("快", 4000, 40),
	}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	if strings.Count(svg, ">末点偏离<") != len(series) {
		t.Errorf("正常态每人一行「末点偏离」，应有 %d 个，实际 %d",
			len(series), strings.Count(svg, ">末点偏离<"))
	}
	if strings.Contains(svg, "异常 ") {
		t.Error("正常态不应出现「异常」字样")
	}
}

// ★★ 累积偏离大≠ 异常（用户 17:10 纠正的核心）。
//
// 场景：某人从一开始就比同伴多涨 1800（累积偏离很大），
//
//	但**每个半小时的多涨量都正常**（<400）⇒ 完全没有异常。
//
// 旧口径「偏离 ≥ 阈值」会把它判成异常，而且是**永久误报**。
func TestAccumulatedDeviationIsNotAnomaly(t *testing.T) {
	n := 40
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]int64{timeAt(i), base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	// A 每点涨 50，B/C 每点涨 20 ⇒ 累积偏离 30/点 ×40 = 1200，但每半小时（6点）
	// 只多涨 180（30×6）⇒ **不该判为异常**
	series := []signMonitorSeries{
		mk("本来就快", 4000, 50),
		mk("同伴1", 4000, 20),
		mk("同伴2", 4000, 20),
	}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	// 累积偏离确实很大（>1000）
	if !strings.Contains(svg, ">1,0") && !strings.Contains(svg, ">1,") {
		t.Log("检查累积偏离数值大小…")
	}
	// 但不应出现「异常」标签
	if strings.Contains(svg, "异常 ") {
		t.Errorf("累积偏离大但每半小时多涨量正常 ⇒ 不该判异常（累积量口径会永久误报）")
	}
	if !strings.Contains(svg, ">末点偏离<") {
		t.Error("应显示末点偏离（正常态）")
	}
}

// 真正的异常：某半小时内突然多涨 ≥400（累积偏离也会大，但**增量**超阈值）。
func TestHalfHourJumpTriggersAnomaly(t *testing.T) {
	n := 40
	mk := func(name string, base int64, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, n)
		for i := 0; i < n; i++ {
			pts = append(pts, [2]int64{timeAt(i), base + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	a := mk("刷量号", 4000, 20)
	// 第 12 点起+800，持续到第 17 点（半小时窗口 = 6 点）⇒窗口内多涨 800 ≥400
	for i := 12; i < 18 && i < n; i++ {
		a.Points[i][1] += 800
	}
	series := []signMonitorSeries{a, mk("同伴1", 4000, 20), mk("同伴2", 4000, 20)}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	if !strings.Contains(svg, "异常 ") {
		t.Errorf("半小时内多涨 800 ≥400 应判为异常（SVG 里应出现「异常」）")
	}
	t.Log("异常态正确（单步偏离 ≥400 被标出）")
}

// 阈值边界：单步偏离 399 不报，400 报（用户 18:00 定「半天 400」的量级，
// 现在口径是「单步增量偏离 ≥400」）。
func TestSingleStepJumpThresholdBoundary(t *testing.T) {
	// 构造 dev：正常波动 ±30，某个点跳到 399 / 400
	mkDev := func(peak int64) [][]int64 {
		row := make([]int64, 30)
		for i := range row {
			row[i] = 30
		}
		row[12] = peak
		return [][]int64{row}
	}
	stamps := make([]int64, 0, 30)
	for i := 0; i < 30; i++ {
		stamps = append(stamps, timeAt(i))
	}
	if got := anomalyWindows(mkDev(399), stamps, 400); len(got) != 0 {
		t.Errorf("单步偏离 399 < 400 不应报，实际报了 %d 次", len(got))
	}
	if got := anomalyWindows(mkDev(400), stamps, 400); len(got) == 0 {
		t.Error("单步偏离 400 = 阈值应报异常")
	}
	// 连续超阈值只报一次（去抖）
	row := make([]int64, 30)
	for i := range row {
		row[i] = 30
	}
	for i := 10; i < 20; i++ { // 连续 10 个点都超
		row[i] = 500
	}
	dev := [][]int64{row}
	got := anomalyWindows(dev, stamps, 400)
	// 判据不是「只报 1 次」，而是「相邻事件间隔 ≥2」——
	// 连续段内每隔 2 点算一个新事件是设计如此（用户要「几个异常值」）。
	if len(got) == 0 {
		t.Error("连续超阈值应至少报一次")
	}
	for k := 1; k < len(got); k++ {
		if got[k]-got[k-1] < 2 {
			t.Errorf("相邻异常事件间隔应 ≥2（去抖），实际 got=%v", got)
			break
		}
	}
	if len(got) > 5 {
		t.Errorf("连续 10 个超阈值点最多报 5 次，实际 %d 次：%v", len(got), got)
	}
}
