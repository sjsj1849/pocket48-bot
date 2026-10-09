package logic

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// zzSpanSeries 造一段「末值为 base、跨度 spanMS」的累计曲线。
func zzSpanSeries(oid, name string, base, spanMS int64, spikeAtMin int, spikeDelta int) signMonitorSeries {
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.FixedZone("CST", 8*3600)).UnixMilli()
	n := int(spanMS/300_000) + 1
	pts := make([][2]int64, 0, n)
	for i := 0; i < n; i++ {
		ts := start + int64(i)*300_000
		frac := float64(i) / float64(n-1)
		// S 形：前 40% 平，后 60% 爬
		s := frac
		if frac < 0.4 {
			s = frac * 0.15
		} else {
			s = 0.06 + (frac-0.4)/0.6*0.94
		}
		v := int64(float64(base) * s)
		if spikeAtMin >= 0 && i == spikeAtMin {
			v += int64(spikeDelta)
		}
		pts = append(pts, [2]int64{ts, v})
	}
	return signMonitorSeries{OID: oid, Name: name, Points: pts}
}

// 三个跨度（15 分钟 / 3 小时 / 全天）下的图，验证高度自适应是否合理。
func TestChartHeightAcrossSpans(t *testing.T) {
	cases := []struct {
		label  string
		spanMS int64
	}{
		{"15分钟", 15 * 60 * 1000},
		{"3小时", 3 * 3600 * 1000},
		{"全天", 24 * 3600 * 1000},
	}
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染")
	}
	if err := os.MkdirAll("/tmp/sm-span", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		spikeMin := -1
		if c.spanMS >= 3600*1000 {
			spikeMin = int(c.spanMS/300_000) * 2 / 3 // 全天图在 16:00 左右造尖峰
		}
		series := []signMonitorSeries{
			zzSpanSeries("a", "郑伊安IAN", 10000, c.spanMS, spikeMin, 1500),
			zzSpanSeries("b", "柳河岚YUHA", 9600, c.spanMS, -1, 0),
			zzSpanSeries("c", "ChoiJiwoo", 9100, c.spanMS, -1, 0),
		}
		h := signMonitorChartHeight(c.spanMS)
		ticks := signMonitorTickCount(760-56-150, time.Duration(c.spanMS)*time.Millisecond)
		sub := fmt.Sprintf("%s · 每 5 分钟采样 · 本组 3 个超话", c.label)
		svg := buildSignMonitorLineSVG(series, "第一组（万档） · 今日签到累计", sub, 760, h)
		png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组"))
		if err != nil {
			t.Errorf("%s 渲染失败: %v", c.label, err)
			continue
		}
		w, hh := pngDimensions(png)
		name := fmt.Sprintf("/tmp/sm-span/%s.png", c.label)
		_ = os.WriteFile(name, png, 0o644)
		t.Logf("跨度=%-8s 自适应高=%-4d 刻度数=%d → PNG %dx%d ratio=%.3f",
			c.label, h, ticks, w, hh, float64(w)/float64(h))
	}
	t.Log("已导出 /tmp/sm-span/")
}

// signMonitorChartHeight 的分档必须锁死：短跨度矮、长跨度高。
func TestChartHeightBuckets(t *testing.T) {
	cases := []struct {
		spanMS int64
		want   int
	}{
		{15 * 60 * 1000, 260},
		{2 * 3600 * 1000, 260},
		{3 * 3600 * 1000, 320},
		{6 * 3600 * 1000, 320},
		{7 * 3600 * 1000, 380},
		{24 * 3600 * 1000, 380},
	}
	for _, c := range cases {
		if got := signMonitorChartHeight(c.spanMS); got != c.want {
			t.Errorf("跨度 %d ms 应得高度 %d，实际 %d", c.spanMS, c.want, got)
		}
	}
}

// 刻度数：跨度小用短标签可以密一点，跨度大必须稀（否则长标签互相压字）。
func TestTickCountAvoidsOverlap(t *testing.T) {
	innerW := 760 - 56 - 150 // 554
	// 全天：11 字符标签约 78px，554/78 = 7
	if n := signMonitorTickCount(innerW, 24*time.Hour); n < 4 || n > 8 {
		t.Errorf("全天跨度刻度数 %d 不合理（长标签应稀疏，4~8）", n)
	}
	// 15 分钟：短标签 46px，可以密
	if n := signMonitorTickCount(innerW, 15*time.Minute); n <= 5 {
		t.Errorf("15 分钟跨度刻度数 %d 太少（短标签可以密一些）", n)
	}
	// 极窄：至少 2 个刻度，不能是 0 或 1
	if n := signMonitorTickCount(10, time.Minute); n < 2 {
		t.Errorf("极窄也要 %d 个刻度，实际 %d", 2, n)
	}
}

// Y 轴刻度必须落在「好看」的数上。
func TestNiceStep(t *testing.T) {
	cases := []struct{ maxV, want int }{
		{9997, 2500},
		{2697, 1000},
		// 858/4=214.5 → 落在 2.5×100 ⇒ 步长 250（刻度 0/250/500/750/1000）。
		// 步长 200 会让最顶刻度 1000 超出 maxV 留白过多，250 刚好收口。
		{858, 250},
		{100, 25},
		{7, 2},
		{1, 1},
	}
	for _, c := range cases {
		if got := signMonitorNiceStep(c.maxV, 4); got != c.want {
			t.Errorf("maxV=%d 应得步长 %d，实际 %d", c.maxV, c.want, got)
		}
	}
}

// signMonitorMaxDeviation 要能算出突增那个人的偏离量。
func TestMaxDeviationFindsSpiker(t *testing.T) {
	// ★ 用线性的 zzSeries（zzSpanSeries 是 S 形归一化的，末值=base，拉不开差距）。
	//   起点 5000（<10000，绝不破万），步长 150/60/55。
	//
	// ★★ 单步口径（2026-10-07 20:47 起，devBounds 与画图统一）：
	//   每一步的增量 = 150/ 60 / 55，中位数 = 60
	//   ⇒ 偏离 = +90 / 0 / -5
	//   旧口径算的是「累积增量之差」= 1710，那个值会随采样点数放大：
	//   采 20 个点是 1710，采 288 个点就是 24000 —— 无法设定稳定阈值。
	series := []signMonitorSeries{
		zzSeries("a", "刷量号", 5000, 20, 150),
		zzSeries("b", "正常1", 5000, 20, 60),
		zzSeries("c", "正常2", 5000, 20, 55),
	}
	got := signMonitorMaxDeviation(series)
	if got != 90 {
		t.Errorf("单步偏离应为 90（150 - 中位数 60），实际 %d", got)
	}
	b := groupDevBounds(series)
	if b.Pos != 90 {
		t.Errorf("正向单步偏离应为 90，实际 %d", b.Pos)
	}
	// 负向：55 比中位数 60 少5 ⇒ Neg = 5（非 0）
	if b.Neg != 5 {
		t.Errorf("负向单步偏离应为 5（60 - 55），实际 Pos=%d Neg=%d", b.Pos, b.Neg)
	}
	t.Logf("偏离极值 Pos=%d Neg=%d", b.Pos, b.Neg)
}
