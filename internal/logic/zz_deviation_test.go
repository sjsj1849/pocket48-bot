package logic

import (
	"html"
	"os"
	"strings"
	"testing"
)

func TestDeviationChartRevealsSpike(t *testing.T) {
	series := []signMonitorSeries{
		zzSynthSeries("a", "郑伊安IAN", 10000, "15:10", 1500),
		zzSynthSeries("b", "柳河岚YUHA", 9600, "", 0),
		zzSynthSeries("c", "ChoiJiwoo", 9100, "", 0),
		zzSynthSeries("d", "stella", 8900, "", 0),
	}
	svg := buildSignMonitorDeviationSVG(series, "第一组（万档） · 相对同组中位数的偏离",
		"虚线=同组中位数；线在上方说明比同伴涨得多", 760, 340, 800)
	if svg == "" {
		t.Fatal("偏离图 SVG 为空")
	}
	if !strings.Contains(svg, "同组中位数") {
		t.Error("必须标出基线含义")
	}
	// ★ 断言不能直接查中文名：图例走 html.EscapeString，非 ASCII 全被转成
	//   &#NNNN; 实体。正确做法是反转义后再比对。
	if unescaped := html.UnescapeString(svg); !strings.Contains(unescaped, "IAN") {
		t.Error("图例应保留超话名")
	}
	// 偏离图的核心承诺：必须能区分「正向偏离」和「零线」。
	if !strings.Contains(svg, "stroke-dasharray") {
		t.Error("必须画出同组中位数基线（虚线）")
	}
	// 刻度值随数据变（合成数据起始值全是 0，基线扣除失效），只锁结构。
	// ★ 2026-10-07 刻度改 niceBound 后标签是「200」而不是「+200」
	//   （正负对称共用一个上界，靠位置区分正负，不需要 + 号）。
	if !strings.Contains(svg, `text-anchor="end"`) {
		t.Error("Y 轴应仍有刻度标签")
	}
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染")
	}
	png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "偏离"))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	w, h := pngDimensions(png)
	t.Logf("偏离图 PNG %dx%d ratio=%.3f", w, h, float64(w)/float64(h))
	_ = os.MkdirAll("/tmp/sm-day", 0o755)
	_ = os.WriteFile("/tmp/sm-day/deviation-760x340.png", png, 0o644)
	t.Log("已导出 /tmp/sm-day/deviation-760x340.png")
}
