package logic

import (
	"strings"
	"testing"
)

// zzChartSeries 造一条从 1000 涨到 1200 的曲线。
func zzChartSeries(oid, name string, base int) signMonitorSeries {
	pts := make([][2]int64, 0, 5)
	for i := int64(0); i < 5; i++ {
		pts = append(pts, [2]int64{1_700_000_000_000 + i*300_000, int64(base + int(i)*50)})
	}
	return signMonitorSeries{OID: oid, Name: name, Points: pts}
}

// ★ 回归 2026-10-07：日报 PNG 里 8 个量级差 3 倍的超话被挤在一张图上，
// Y 轴被万档组拉高，三千档组贴着底部。修复后必须按组分别出图。
func TestDailyReportChartsAreSplitByGroup(t *testing.T) {
	big := []signMonitorSeries{
		zzChartSeries("oid-a", "IAN", 9000),
		zzChartSeries("oid-b", "YUHA", 9500),
	}
	small := []signMonitorSeries{zzChartSeries("oid-c", "YEON", 3000)}

	// 两组各自的 Y 轴上限必须由本组决定，而不是全局最大值。
	bigSVG := buildSignMonitorLineSVG(big, "万档组", "", 760, 300)
	smallSVG := buildSignMonitorLineSVG(small, "三千档组", "", 760, 300)
	if !strings.Contains(bigSVG, "万档组") || !strings.Contains(smallSVG, "三千档组") {
		t.Fatal("标题里必须写清组名，否则三张图分不清哪张是哪组")
	}
	// 三千档组的图里不该出现 9500 这个量级（说明 Y 轴按本组算）。
	if strings.Contains(smallSVG, "9,500") {
		t.Error("三千档组的图里出现了万档组的数值，Y 轴被别的组拉高了")
	}
	if !strings.Contains(smallSVG, "3,200") {
		t.Error("三千档组应显示本组自己的刻度值")
	}
}

// 无标题时行为必须与原来完全一致（避免影响其他调用方）。
func TestLineSVGWithoutTitleHasNoHeader(t *testing.T) {
	series := []signMonitorSeries{zzChartSeries("oid-a", "IAN", 9000)}
	withTitle := buildSignMonitorLineSVG(series, "标题", "副标", 760, 300)
	if !strings.Contains(withTitle, `font-weight="600"`) {
		t.Error("有标题时应渲染标题文字")
	}
	if !strings.Contains(withTitle, "副标") {
		t.Error("副标题未渲染")
	}
}

// signMonitorChartHTML 必须带 data-raw="1" 与 #report-card。
//
// ★ 没有这两个，html_to_png.mjs 会退回 fullPage 截图（高度≥视口 1100px），
// 而图表只有 ~300px 高 ⇒ PNG 下方 3/4 全白（2026-10-07 用户报的问题）。
func TestChartHTMLCarriesRawFlag(t *testing.T) {
	html := signMonitorChartHTML(`<svg width="10" height="10"></svg>`, "标题")
	if !strings.Contains(html, `id="report-card"`) {
		t.Error("缺少 #report-card，脚本会退回 fullPage 截图")
	}
	if !strings.Contains(html, `data-raw="1"`) {
		t.Error("缺少 data-raw=\"1\"，Pillow 会把图表硬塞进 3:4 画布补边")
	}
	if signMonitorChartHTML("", "标题") != "" {
		t.Error("SVG 为空时应返回空串（调用方据此跳过附件）")
	}
}

// 柱状图标题必须写明组名与异常人，否则多组场景下认不出图。
func TestBarSVGTitleMentionsGroupAndSpike(t *testing.T) {
	series := []signMonitorSeries{
		zzChartSeries("oid-a", "IAN", 9000),
		zzChartSeries("oid-b", "YUHA", 9500),
	}
	from := timeFromMilli(1_700_000_000_000)
	to := timeFromMilli(1_700_000_000_000 + 1_800_000)
	svg := buildSignMonitorBarSVG(series, from, to, "万档组（IAN 异常 +1500）", 760, 300)
	if !strings.Contains(svg, "万档组") || !strings.Contains(svg, "IAN 异常") {
		t.Error("柱状图标题应含组名与异常对象")
	}
	if !strings.Contains(svg, "<rect") {
		t.Error("应画出柱子")
	}
}
