package logic

import (
	"os"
	"testing"
)

// ★ Y 轴范围必须装得下所有曲线（2026-10-07 用户在第三组图上发现底部被裁）。
//
// 原实现：maxDev 只扫正向最大值，然后 yBot = -yTop 强行对称。
// 负向偏离大于正向时（很常见 —— 涨得少的人全程在下方），
// 超出 -yTop 的部分直接被画布切掉，图上是一条断线。
//
// 断言方式：直接从生成的 SVG 里核对Y 轴上下界标签，
// 确认「负向标签 ≥ 实际负向极值」且「正向标签 ≥ 实际正向极值」。
func TestChartYRangeCoversAllCurves(t *testing.T) {
	// 造 3 人组：一人一路少涨（负偏离大），一人正常，一人一路多涨（正偏离大）
	mk := func(name string, step int64) signMonitorSeries {
		pts := make([][2]int64, 0, 20)
		for i := 0; i < 20; i++ {
			pts = append(pts, [2]int64{int64(i) * 300_000, 1000 + int64(i)*step})
		}
		return signMonitorSeries{OID: name, Name: name, Points: pts}
	}
	// 增量 1 / 11 / 21 ⇒ 中位数 11 ⇒ 偏离 -10 / 0 / +10（对称，看不出问题）
	// 再加一个「负向远大于正向」的场景：
	series := []signMonitorSeries{
		mk("慢", 2), // 少涨
		mk("中", 11),
		mk("快", 20), // 多涨
	}
	b := groupDevBounds(series)
	if b.Neg <= 0 || b.Pos <= 0 {
		t.Fatalf("应同时有正负偏离，实际 Pos=%d Neg=%d", b.Pos, b.Neg)
	}
	t.Logf("偏离极值：Pos=%d Neg=%d", b.Pos, b.Neg)

	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要渲染")
	}
	svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	// SVG 里的 Y 轴上下界标签必须都不小于实际极值
	if !svgHasDevBound(svg, b.Pos) {
		t.Errorf("Y 轴上界标签未覆盖正向极值 +%d", b.Pos)
	}
	if !svgHasDevBound(svg, b.Neg) {
		t.Errorf("Y 轴下界标签未覆盖负向极值 -%d", b.Neg)
	}
	png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "T"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll("/tmp/sm-range", 0o755)
	_ = os.WriteFile("/tmp/sm-range/范围测试.png", png, 0o644)
	t.Log("已导出 /tmp/sm-range/范围测试.png")
}

// svgHasDevBound 检查 SVG 里是否存在覆盖 n 的刻度标签（含逗号格式）。
func svgHasDevBound(svg string, n int) bool {
	for _, cand := range []string{comma(n), "+" + comma(n), "-" + comma(n)} {
		if containsStr(svg, ">"+cand+"<") {
			return true
		}
	}
	return false
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 两人组：两条线必须互为相反数（否则其中一条恒为 0，看不出谁涨得多）。
//
// 历史 bug：原实现让两人共用同一个基线 ⇒ series[1] 的偏离恒为 0，
// 图上是一条压在零线上的平线。
func TestTwoPersonSeriesAreMirrored(t *testing.T) {
	aPts := make([][2]int64, 0, 20)
	bPts := make([][2]int64, 0, 20)
	for i := 0; i < 20; i++ {
		ts := int64(i) * 300_000
		aPts = append(aPts, [2]int64{ts, 1000 + int64(i)*20}) // 涨得多
		bPts = append(bPts, [2]int64{ts, 1000 + int64(i)*2})  // 涨得少
	}
	series := []signMonitorSeries{
		{OID: "a", Name: "A", Points: aPts},
		{OID: "b", Name: "B", Points: bPts},
	}
	// ★ 2026-10-07 14:48 语义：两人组**要画**偏离图（差值口径）。
	//   我 14:20 曾写成「不画图」，现已改回。
	if svg := buildSignMonitorDeviationSVG(series, "T", "S", 760, 380, 800); svg == "" {
		t.Error("两人组必须画偏离图（差值口径）")
	}
	if b := pairDevBounds(series); b.Pos == 0 || b.Neg == 0 {
		t.Errorf("pairDevBounds 应能算出差值，实际 Pos=%d Neg=%d", b.Pos, b.Neg)
	}
	if svg := buildSignMonitorLineSVG(series, "T", "S", 760, 380); svg == "" {
		t.Error("两人组必须能退回累计图")
	}
	// pairDevBounds 必须算出正负两个方向的差值（两人互为相反数，都不该是 0）
	b := pairDevBounds(series)
	if b.Pos == 0 || b.Neg == 0 {
		t.Errorf("pairDevBounds 正负都应非零，实际 Pos=%d Neg=%d（有人恒为 0 = 基线共用错了）",
			b.Pos, b.Neg)
	}
}
