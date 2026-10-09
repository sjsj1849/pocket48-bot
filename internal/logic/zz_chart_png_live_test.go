package logic

import (
	"os"
	"testing"
	"time"
)

// 真实渲染一张图表 PNG，验证尺寸比例（2026-10-07 用户报「只占最上面一块」）。
//
// 判据：PNG 的宽高比必须接近 SVG 的 760x300（含 padding），
// 即 height/width < 0.5。此前因为走 fullPage + 3:4 补边，高度是宽度的 4/3。
func TestSignMonitorChartPNGRatioLive(t *testing.T) {
	if os.Getenv("POCKET48_SM_PNG") == "" {
		t.Skip("需要真实浏览器渲染，设 POCKET48_SM_PNG=1")
	}
	base := time.Now().Add(-6 * time.Hour).UnixMilli()
	series := []signMonitorSeries{
		zzChartSeries("oid-a", "IAN", 8200),
		zzChartSeries("oid-b", "YUHA", 8600),
	}
	// 稍微真实一点：多点、有起伏
	for i := range series {
		pts := make([][2]int64, 0, 40)
		for i2 := 0; i2 < 40; i2++ {
			pts = append(pts, [2]int64{base + int64(i2)*300_000,
				int64(8000 + i2*30 + (i2%5)*40)})
		}
		series[i].Points = pts
	}
	svg := buildSignMonitorLineSVG(series, "万档组 · 今日签到累计", "回归测试", 760, 300)
	png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "万档组"))
	if err != nil {
		t.Fatalf("渲染失败: %v", err)
	}
	w, h := pngDimensions(png)
	t.Logf("PNG 实际尺寸 %dx%d ratio=w/h=%.3f 字节=%d", w, h, float64(w)/float64(h), len(png))
	if float64(h)/float64(w) > 0.6 {
		t.Errorf("PNG 过高（h/w=%.3f），说明底部有大片空白", float64(h)/float64(w))
	}
	if float64(w)/float64(h) < 1.8 {
		t.Errorf("PNG 偏窄（w/h=%.3f），可能被横向压扁", float64(w)/float64(h))
	}
	if err := os.WriteFile("/tmp/sm-chart-check.png", png, 0o644); err == nil {
		t.Logf("已保存 /tmp/sm-chart-check.png")
	}
}
