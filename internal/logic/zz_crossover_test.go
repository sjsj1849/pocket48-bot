package logic

import (
	"html"
	"testing"
	"time"
)

// zzSeries 造一条序列（起点 1000，每步 step）。
func zzSeries(oid, name string, start int64, n int, step int64) signMonitorSeries {
	pts := make([][2]int64, 0, n)
	for i := 0; i < n; i++ {
		pts = append(pts, [2]int64{
			time.Date(2026, 10, 7, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).
				Add(time.Duration(i) * 5 * time.Minute).UnixMilli(),
			start + int64(i)*step,
		})
	}
	return signMonitorSeries{OID: oid, Name: name, Points: pts}
}

// ★ 破万（≥10000）必须被剔除（2026-10-07 用户决策）。
//
// 实测破万后接口给 1000 粒度模糊值（10019→10000、11448→12000），
// 继续算偏移只是噪声 —— 分母分子都在按千位跳变。
func TestCrossoverSeriesAreFilteredOut(t *testing.T) {
	below := zzSeries("a", "IAN", 8000, 20, 50)   // 8000→8950（未破万）
	above := zzSeries("b", "YUHA", 10000, 20, 50) // 10000 起就破万

	got := filterBelowCrossover([]signMonitorSeries{below, above})
	if len(got) != 1 {
		t.Fatalf("应剔除破万的那条，实际剩 %d 条", len(got))
	}
	if got[0].OID != "a" {
		t.Errorf("应保留未破万的一条，实际保留了 %s", got[0].OID)
	}

	// 临界：9999 未破万、10000 破万
	if len(filterBelowCrossover([]signMonitorSeries{zzSeries("x", "临界", 9990, 10, 1)})) != 1 {
		t.Error("9999 应视为未破万")
	}
	if len(filterBelowCrossover([]signMonitorSeries{zzSeries("y", "临界", 10000, 10, 1)})) != 0 {
		t.Error("10000 应视为破万")
	}
}

// 破万时间要能被记录下来（用户在图上要看「什么时候破的」）。
func TestCrossoverTimeIsRecorded(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, loc)
	// 前 5 点未破万（8000..8400），第 6 点破万
	s := signMonitorSeries{OID: "a", Name: "ChoiJiwoo", Points: [][2]int64{}}
	for i := 0; i < 10; i++ {
		v := int64(8000 + i*400)
		s.Points = append(s.Points, [2]int64{base.Add(time.Duration(i) * 5 * time.Minute).UnixMilli(), v})
	}

	count, name, at := crossoverInfo([]signMonitorSeries{s})
	if count != 1 {
		t.Fatalf("应识别出 1 个破万，实际 %d", count)
	}
	if name != "ChoiJiwoo" {
		t.Errorf("破万超话名应为 ChoiJiwoo，实际 %s", name)
	}
	if at.IsZero() {
		t.Fatal("应记录破万时刻")
	}
	want := base.Add(5 * 5 * time.Minute) // 第 6 点（i=5）= 10000
	if at.UnixMilli() != want.UnixMilli() {
		t.Errorf("破万时刻应为 %s（首个 ≥10000 的点），实际 %s",
			want.Format("15:04"), at.Format("15:04"))
	}
}

// 剩 2 人时**必须**切差值口径（用户 14:48 明确：「两个人的时候就要变成差值」）。
//
// 差值口径回答「这两人谁涨得多」，破万判定回答「谁在刷量」；
// 剩 2 人时真实刷量者可能因同伴也涨而显示「差值正常」⇒ 误导。
func TestTwoValidSeriesUseGapSemantics(t *testing.T) {
	// 4 人组，其中 2 人已破万 ⇒ 有效成员剩 2 人
	// ★ 两条未破万曲线必须**增量不同**（50 vs 30），否则差值恒为 0
	//   （同增量 ⇒ 谁也没比另一个人多涨 ⇒ pairDevBounds 返回 0/0）。
	//   ★ 单步口径下差值 = 50 - 30 = **20**（每一步都是 20）。
	//     旧口径算的是累积差 (50-30)×19 = 380，那个值会随采样点数量放大 ——
	//     采一整天和采一小时会得到完全不同的"差值"，无法横向比较。
	group := []signMonitorSeries{
		zzSeries("a", "未破万1", 8000, 20, 50),
		zzSeries("b", "未破万2", 7000, 20, 30),
		zzSeries("c", "已破万1", 10000, 20, 10),
		zzSeries("d", "已破万2", 10500, 20, 10),
	}
	valid := filterBelowCrossover(group)
	if len(valid) != 2 {
		t.Fatalf("应剩 2 个有效成员，实际 %d", len(valid))
	}
	// ★ 语义已在 2026-10-07 14:48 改回：**2 人要画图**（差值口径）。
	//   用户原话：「还有两个人的时候就要变成差值，只剩一个人了才不继续画图」。
	svg := buildSignMonitorDeviationSVG(group, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("★ 剩 2 人必须画偏离图（差值口径），不是不画")
	}
	// 差值口径下两条线互为相反数（基线各取对方）。
	// ★ 单步口径：差值恒为 20，与采样点数量无关。
	if b := pairDevBounds(valid); b.Pos != 20 || b.Neg != 20 {
		t.Errorf("两人组单步差值应为 20（50-30），实际 Pos=%d Neg=%d", b.Pos, b.Neg)
	}
	// 说明里必须写清是差值口径
	if svg := buildSignMonitorDeviationSVG(valid, "两人的签到差距",
		"组内仅 2 人未破万，无中位数可依，图上=与另一个人的差距", 760, 380, 800); svg == "" {
		t.Error("两人组差值口径图应能画出")
	}
}

// 剩 3 人时正常画偏离图（4 人破 1 个的常见场景）。
func TestThreeValidSeriesStillDrawDeviation(t *testing.T) {
	group := []signMonitorSeries{
		zzSeries("a", "正常1", 8000, 20, 40),
		zzSeries("b", "正常2", 7500, 20, 35),
		zzSeries("c", "正常3", 7000, 20, 30),
		zzSeries("d", "已破万", 10000, 20, 5),
	}
	if svg := buildSignMonitorDeviationSVG(group, "T", "S", 760, 380, 800); svg == "" {
		t.Fatal("剩 3 人时应正常画偏离图（中位数取中间那个）")
	}
}

// 图例不再重复标数值（用户 14:09 指出：线尾标了、图例又标一遍）。
func TestLegendDoesNotDuplicateEndpointValue(t *testing.T) {
	series := []signMonitorSeries{
		zzSeries("a", "IAN", 8000, 20, 50),
		zzSeries("b", "YUHA", 7800, 20, 40),
		zzSeries("c", "Jiwoo", 7600, 20, 30),
	}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	// 图例里不应再有「末点」二字（数值由线尾标注承担）
	if html.UnescapeString(svg) != "" && containsStr(svg, ">末点<") {
		t.Error("图例不应再标「末点」—— 与线尾数值重复")
	}
	// 线尾标注：只在异常时才标 ⇒ 正常数据这里不该有 font-weight=700 的粗体数值
	if containsStr(svg, "<circle") {
		t.Error("线尾不应画圆点标注（数值统一在图例）")
	}
}

// 注入一个大偏离 ⇒ 线尾**应该**出现标注。
func TestInlineLabelOnlyForAbnormal(t *testing.T) {
	// ★ 基线用 5000（不是 8000）：8000 + 19*300 = 13700 会破万，
	//   而破万的序列会被 filterBelowCrossover 剔除 ⇒ 图上根本画不出来。
	//   5000 + 19*300 = 10700 仍破万，所以用 4000 → 9700，不破万。
	normal := []signMonitorSeries{
		zzSeries("a", "慢", 4000, 20, 20),
		zzSeries("b", "中", 4000, 20, 30),
		zzSeries("c", "快", 4000, 20, 40),
	}
	normalSVG := buildSignMonitorDeviationSVG(normal, "T", "S", 760, 380, 800)

	// 「刷量」末值 = 4000 + 19*300 = 9700（不破万），末点偏离 ≈ +4500，
	// 远超 labelThreshold=800 ⇒ 线尾必须标数值。
	abnormal := []signMonitorSeries{
		zzSeries("a", "慢", 4000, 20, 20),
		zzSeries("b", "中", 4000, 20, 30),
		zzSeries("c", "刷量", 4000, 20, 20),
	}
	// ★ 新口径：异常是**单步尖峰**（某一步多涨 ≥400），不是持续高。
	//   给「刷量」在第 8 步注入 +600（单步偏离 600 ≥ 阈值 400）。
	spiked := make([][2]int64, len(abnormal[2].Points))
	copy(spiked, abnormal[2].Points)
	if 8 < len(spiked) {
		spiked[8][1] += 600
	}
	abnormal[2].Points = spiked

	abnormalSVG := buildSignMonitorDeviationSVG(abnormal, "T", "S", 760, 380, 800)
	if abnormalSVG == "" {
		t.Fatal("异常组偏离图为空（检查是否误触破万过滤）")
	}

	// ★ 线尾【永远】不标数值（用户 16:46/18:00 规则）——
	//   数值统一写在右侧图例里。两种状态都不该有圆点标注。
	if containsStr(normalSVG, `<circle`) {
		t.Error("正常态线尾不应画圆点标注（数值在图例）")
	}
	if containsStr(abnormalSVG, `<circle`) {
		t.Error("异常态线尾不应画圆点标注（数值在图例）")
	}
	// 正常态标「末点偏离」，异常态标「异常 HH:MM」
	if !containsStr(normalSVG, ">末点偏离<") {
		t.Error("正常态图例应标「末点偏离」")
	}
	if containsStr(normalSVG, "异常 ") {
		t.Error("正常态不该出现「异常」字样")
	}
	if !containsStr(abnormalSVG, "异常 ") {
		t.Error("异常态图例应标「异常 HH:MM」")
	}
}
