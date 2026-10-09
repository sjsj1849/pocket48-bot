package logic

import (
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// zzSynthSeries 造一条**真实形态**的签到累计曲线。
//
// ★ 2026-10-07 用户指出旧版合成数据「太扯」：给累计曲线加了正弦波动，
//
//	画出来上下起伏的锯齿 —— 而签到累计**只能累加，绝不可能下降**。
//	用真实采样核对过（10-07 04:58~06:07，8 个超话各 16 点）：
//	增量全部非负（0~17 / 5 分钟），曲线单调递增。
//
// 现在：每 5 分钟增量 = 时段系数 × 抖动，**恒为非负**；
// 时段系数 深夜0.15 / 早高峰1.0 / 白天2.2 / 收尾0.4。
func zzSynthSeries(oid, name string, total int, spikeAt string, spikeDelta int) signMonitorSeries {
	base := time.Date(2026, 10, 7, 0, 0, 0, 0, time.FixedZone("CST", 8*3600))
	spikeT := time.Time{}
	if spikeAt != "" {
		spikeT, _ = time.ParseInLocation("15:04", spikeAt, time.FixedZone("CST", 8*3600))
	}
	pts := make([][2]int64, 0, 288)
	acc := 0.0
	for i := 0; i < 288; i++ {
		ts := base.Add(time.Duration(i) * 5 * time.Minute)
		h := ts.Hour()
		var factor float64
		switch {
		case h < 6:
			factor = 0.15
		case h < 9:
			factor = 1.0
		case h < 22:
			factor = 2.2
		default:
			factor = 0.4
		}
		// 抖动基数为正（1 + 0.3*sin ∈ [0.7,1.3]）⇒ 增量恒非负
		acc += factor * (1 + 0.3*math.Sin(float64(i)/7.0))
		if !spikeT.IsZero() && !ts.Before(spikeT) && ts.Before(spikeT.Add(5*time.Minute)) {
			acc += float64(spikeDelta)
		}
		pts = append(pts, [2]int64{ts.UnixMilli(), int64(acc)})
	}
	if total > 0 && acc > 0 {
		k := float64(total) / acc
		for i := range pts {
			pts[i][1] = int64(float64(pts[i][1]) * k)
		}
	}
	return signMonitorSeries{OID: oid, Name: name, Points: pts}
}

// 一天的数据画出来到底什么样？（2026-10-07 用户问：24h 后这张图还成立吗）
func TestChartLookAfterFullDay(t *testing.T) {
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染，设 POCKET48_SM_DAY=1")
	}
	loc := time.FixedZone("CST", 8*3600)
	_ = loc
	series := []signMonitorSeries{
		zzSynthSeries("a", "郑伊安IAN", 10000, "15:10", 1500),
		zzSynthSeries("b", "柳河岚YUHA", 9600, "", 0),
		zzSynthSeries("c", "ChoiJiwoo", 9100, "", 0),
		zzSynthSeries("d", "stella", 8900, "", 0),
	}
	subtitle := "10-07 00:00 至 10-07 23:55 · 每 5 分钟采样 · 本组 4 个超话（量级相近，可直接对比）"
	svg := buildSignMonitorLineSVG(series, "第一组（万档） · 今日签到累计", subtitle, 760, 300)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组"))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	w, h := pngDimensions(png)
	t.Logf("当前参数 760x300 → PNG %dx%d ratio=%.3f", w, h, float64(w)/float64(h))
	if err := os.MkdirAll("/tmp/sm-day", 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile("/tmp/sm-day/current-760x300.png", png, 0o644)

	// 对照：更高更宽的版本
	for _, dim := range [][2]int{{760, 420}, {900, 420}, {900, 340}} {
		svg2 := buildSignMonitorLineSVG(series, "第一组（万档） · 今日签到累计", subtitle, dim[0], dim[1])
		p2, err := renderHTMLToPNG(signMonitorChartHTML(svg2, "第一组"))
		if err != nil {
			t.Errorf("%dx%d 渲染失败: %v", dim[0], dim[1], err)
			continue
		}
		w2, h2 := pngDimensions(p2)
		name := fmt.Sprintf("/tmp/sm-day/cmp-%dx%d.png", dim[0], dim[1])
		_ = os.WriteFile(name, p2, 0o644)
		t.Logf("对照 %dx%d → PNG %dx%d ratio=%.3f → %s",
			dim[0], dim[1], w2, h2, float64(w2)/float64(h2), name)
	}
	t.Logf("已导出到 /tmp/sm-day/")
}
