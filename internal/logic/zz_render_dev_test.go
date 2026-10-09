package logic

import (
	"fmt"
	"os"
	"testing"
)

// 用真实单调递增形态的数据渲染偏离图，供人工核对。
func TestRenderDeviationRealistic(t *testing.T) {
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染")
	}
	cases := []struct {
		name   string
		series []signMonitorSeries
	}{
		{"万档4人_1人下午突增1500", []signMonitorSeries{
			zzSynthSeries("a", "郑伊安IAN", 10000, "15:10", 1500),
			zzSynthSeries("b", "柳河岚YUHA", 9600, "", 0),
			zzSynthSeries("c", "ChoiJiwoo", 9100, "", 0),
			zzSynthSeries("d", "stella", 8900, "", 0),
		}},
		{"万档4人_无异常", []signMonitorSeries{
			zzSynthSeries("a", "郑伊安IAN", 10000, "", 0),
			zzSynthSeries("b", "柳河岚YUHA", 9600, "", 0),
			zzSynthSeries("c", "ChoiJiwoo", 9100, "", 0),
			zzSynthSeries("d", "stella", 8900, "", 0),
		}},
	}
	if err := os.MkdirAll("/tmp/sm-dev", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		dev := signMonitorMaxDeviation(c.series)
		sub := fmt.Sprintf("虚线=同组中位数；线在上方=比同伴多涨（最大 +%d 人）", dev)
		svg := buildSignMonitorDeviationSVG(c.series, "第一组（万档） · 谁比同组多涨了多少", sub, 760, 380, 800)
		if svg == "" {
			t.Errorf("%s 偏离图为空", c.name)
			continue
		}
		png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组"))
		if err != nil {
			t.Errorf("%s 渲染失败: %v", c.name, err)
			continue
		}
		w, h := pngDimensions(png)
		path := fmt.Sprintf("/tmp/sm-dev/%s.png", c.name)
		_ = os.WriteFile(path, png, 0o644)
		t.Logf("%-26s 偏离=%-5d PNG %dx%d → %s", c.name, dev, w, h, path)
	}

	// 对照：累计图（已从日报移除，但保留函数，画一张确认「看不出谁异常」）
	line := cases[0].series
	svg := buildSignMonitorLineSVG(line, "第一组（万档） · 今日签到累计", "对照用：已从日报移除", 760, 380)
	if png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组")); err == nil {
		w, h := pngDimensions(png)
		_ = os.WriteFile("/tmp/sm-dev/对照-累计图.png", png, 0o644)
		t.Logf("对照累计图 PNG %dx%d → /tmp/sm-dev/对照-累计图.png", w, h)
	}
	t.Log("已导出 /tmp/sm-dev/")
}
