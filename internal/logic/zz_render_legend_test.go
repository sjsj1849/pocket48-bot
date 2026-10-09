package logic

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

// 渲染真实数据 + 注入异常，验证新的图例标注（用户 16:46 规则）。
func TestRenderLegendValues(t *testing.T) {
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染")
	}
	load := func() []signMonitorSeries {
		raw, err := os.ReadFile("../../storage/weibo/sign-monitor.jsonl")
		if err != nil {
			t.Skip(err)
		}
		type sm struct {
			TS   int64  `json:"ts"`
			OID  string `json:"oid"`
			Name string `json:"name"`
			Sign int    `json:"sign"`
		}
		byOID := map[string][]sm{}
		for _, line := range splitLines(raw) {
			var o sm
			if err := json.Unmarshal(line, &o); err != nil {
				continue
			}
			byOID[o.OID] = append(byOID[o.OID], o)
		}
		cfgRaw, _ := os.ReadFile("../../config.json")
		var cfg config.Config
		if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
			t.Skip(err)
		}
		var out []signMonitorSeries
		for _, member := range cfg.WeiboSignMonitor.Groups[0].Members {
			list, ok := byOID[member]
			if !ok {
				continue
			}
			sort.Slice(list, func(a, b int) bool { return list[a].TS < list[b].TS })
			pts := make([][2]int64, 0, len(list))
			for _, o := range list {
				pts = append(pts, [2]int64{o.TS, int64(o.Sign)})
			}
			out = append(out, signMonitorSeries{OID: member, Name: list[0].Name, Points: pts})
		}
		return out
	}

	base := filterBelowCrossover(load())
	if len(base) < 3 {
		t.Skip("有效成员不足 3")
	}
	from := time.UnixMilli(base[0].Points[0][0])
	to := time.UnixMilli(base[0].Points[len(base[0].Points)-1][0])
	h := signMonitorChartHeight(to.UnixMilli() - from.UnixMilli())
	sub := fmt.Sprintf("%s 至 %s · 偏离图例会写每个人的末点数值", from.Format("01-02 15:04"), to.Format("15:04"))

	if err := os.MkdirAll("/tmp/sm-lg", 0o755); err != nil {
		t.Fatal(err)
	}

	// 正常态
	svg := buildSignMonitorDeviationSVG(base, "第一组（万档） · 正常态（图例写末点数值）", sub, 760, h, 800)
	if png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组")); err == nil {
		w, hh := pngDimensions(png)
		_ = os.WriteFile("/tmp/sm-lg/正常态.png", png, 0o644)
		t.Logf("正常态 PNG %dx%d → /tmp/sm-lg/正常态.png", w, hh)
	}

	// 异常态：给第一个人在中段注入 +1800
	spiked := make([]signMonitorSeries, len(base))
	copy(spiked, base)
	pts := make([][2]int64, len(base[0].Points))
	copy(pts, base[0].Points)
	mid := len(pts) / 2
	for i := mid; i < len(pts); i++ {
		pts[i][1] += 1800
	}
	spiked[0].Points = pts
	if svg2 := buildSignMonitorDeviationSVG(spiked, "第一组（万档） · 异常态（图例写「异常时刻」+ 全组值）",
		"注入 +1800 验证异常标注", 760, h, 800); svg2 != "" {
		if png, err := renderHTMLToPNG(signMonitorChartHTML(svg2, "第一组")); err == nil {
			w, hh := pngDimensions(png)
			_ = os.WriteFile("/tmp/sm-lg/异常态.png", png, 0o644)
			t.Logf("异常态 PNG %dx%d → /tmp/sm-lg/异常态.png", w, hh)
		}
	} else {
		t.Error("异常态 SVG 为空")
	}
}
