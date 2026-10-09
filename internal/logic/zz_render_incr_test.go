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

// 渲染真实数据（新的「单步增量偏离」口径）+ 注入异常。
func TestRenderIncrementalDeviation(t *testing.T) {
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
	sub := fmt.Sprintf("%s 至 %s · 纵轴=每5 分钟增量相对同组增量中位数的偏离", from.Format("01-02 15:04"), to.Format("15:04"))

	if err := os.MkdirAll("/tmp/sm-inc", 0o755); err != nil {
		t.Fatal(err)
	}

	// 正常态
	svg := buildSignMonitorDeviationSVG(base, "第一组（万档） · 单步增量偏离（真实数据）", sub, 760, h, 800)
	if png, err := renderHTMLToPNG(signMonitorChartHTML(svg, "第一组")); err == nil {
		w, hh := pngDimensions(png)
		_ = os.WriteFile("/tmp/sm-inc/真实-正常态.png", png, 0o644)
		t.Logf("真实数据 PNG %dx%d → /tmp/sm-inc/真实-正常态.png", w, hh)
	}

	// 异常态：给第一个人注入两次单点突增（模拟刷量）
	spiked := make([]signMonitorSeries, len(base))
	copy(spiked, base)
	pts := make([][2]int64, len(base[0].Points))
	copy(pts, base[0].Points)
	n := len(pts)
	for _, at := range []int{n / 3, n * 2 / 3} {
		if at+1 < n {
			pts[at+1][1] += 600 // 单步 +600 ≥ 阈值400
		}
	}
	spiked[0].Points = pts
	if svg2 := buildSignMonitorDeviationSVG(spiked, "第一组（万档） · 注入 2 次单点突增（各+600）",
		"验证多个异常点都能被标出", 760, h, 800); svg2 != "" {
		if png, err := renderHTMLToPNG(signMonitorChartHTML(svg2, "第一组")); err == nil {
			w, hh := pngDimensions(png)
			_ = os.WriteFile("/tmp/sm-inc/真实-异常态.png", png, 0o644)
			t.Logf("异常态 PNG %dx%d → /tmp/sm-inc/真实-异常态.png", w, hh)
		}
	} else {
		t.Error("异常态 SVG 为空")
	}
}
