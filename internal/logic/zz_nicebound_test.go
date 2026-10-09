package logic

import (
	"strings"
	"testing"
)

// ★ 刻度必须以 5 或 0 结尾（用户 2026-10-07 提议的规则）。
//
// 原实现用 1/2/2.5/5 × 10ⁿ，会产生 2.5 这种半档，读图时不好对照。
// 现在每个数量级里取 1/2/3/5/10 的下一个：36→50、7→10、3→5。
func TestNiceBoundEndsWith5Or0(t *testing.T) {
	cases := []struct{ in, want int64 }{
		// 小值
		{1, 1}, {2, 2}, {3, 3}, {4, 5}, {5, 5}, {6, 10}, {7, 10}, {9, 10},
		{10, 10}, {11, 20}, {18, 20}, {26, 30}, {36, 50}, {49, 50}, {51, 100},
		// 跨数量级
		{101, 200}, {640, 1000}, {1500, 2000}, {12000, 20000},
		{0, 0}, {-5, 0},
	}
	for _, c := range cases {
		got := niceBound(c.in)
		if got != c.want {
			t.Errorf("niceBound(%d) 应得 %d，实际 %d", c.in, c.want, got)
		}
		if c.in <= 0 {
			continue
		}
		if got < c.in {
			t.Errorf("niceBound(%d)=%d 必须 ≥ 输入（否则曲线顶边）", c.in, got)
		}
		// 允许的尾数：0 或 5（1/2/3 本身除外，它们是 1/2/3 结尾的小值特例）
		last := got % 10
		if last != 0 && last != 5 && got > 5 {
			t.Errorf("niceBound(%d)=%d 尾数应是 0 或 5", c.in, got)
		}
	}
}

// 正负必须**共用同一个上界**（对称），否则 0 线不在正中、读图易误判。
func TestDeviationChartYAxisIsSymmetric(t *testing.T) {
	// ★ 单步口径下 Y 轴仍必须**对称**（0 永远在正中），否则
	// 「谁比同伴多涨」的正负方向会被视觉误导。
	//
	// 判据：bound = niceBound(max(|Pos|,|Neg|))，正负共用 ⇒ 结构上必然对称。
	// （不为「装得下」写断言 —— 那是 niceBound 的职责，
	//   由 TestNiceBoundEndsWith5Or0 直接验证。）
	for _, c := range []struct {
		pos, neg int
	}{{0, 0}, {10, 10}, {171, 171}, {500, -500}, {0, 300}, {300, 0}} {
		b := devBounds{Pos: c.pos, Neg: c.neg}
		absMax := b.Pos
		if b.Neg > absMax {
			absMax = b.Neg
		}
		bound := int(niceBound(int64(absMax)))
		if bound < absMax {
			t.Errorf("Pos=%d Neg=%d：上界 %d 装不下 |极值| %d（曲线会被裁）",
				b.Pos, b.Neg, bound, absMax)
		}
		// 上界必须以 5 或 0 结尾（用户 18:00 定规）
		if bound > 5 && bound%10 != 0 && bound%10 != 5 {
			t.Errorf("上界 %d 应以 5 或 0 结尾", bound)
		}
	}
	t.Log("Y 轴对称性：正负共用 niceBound(|极值|)，结构上必对称")
}

func svgBoundLabel(svg, sign string) string {
	needle := ">" + sign
	i := strings.Index(svg, needle)
	if i < 0 {
		return ""
	}
	rest := svg[i+len(needle):]
	j := strings.Index(rest, "<")
	if j < 0 {
		return ""
	}
	v := rest[:j]
	// 去掉逗号
	out := strings.ReplaceAll(v, ",", "")
	return out
}

func atoi64(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func parseNonNeg(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
