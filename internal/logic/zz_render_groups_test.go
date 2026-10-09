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

// 用真实采样渲染 4 人组（万档）与 2 人组（第二/三组），供人工核对。
func TestRenderRealGroups(t *testing.T) {
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要真实渲染")
	}
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
	cfgRaw, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Skip(err)
	}
	var cfg config.Config
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Skip(err)
	}
	if err := os.MkdirAll("/tmp/sm-grp", 0o755); err != nil {
		t.Fatal(err)
	}

	for gi, grp := range cfg.WeiboSignMonitor.Groups {
		var series []signMonitorSeries
		for _, member := range grp.Members {
			list, ok := byOID[member]
			if !ok {
				continue
			}
			sort.Slice(list, func(a, b int) bool { return list[a].TS < list[b].TS })
			pts := make([][2]int64, 0, len(list))
			for _, o := range list {
				pts = append(pts, [2]int64{o.TS, int64(o.Sign)})
			}
			series = append(series, signMonitorSeries{OID: member, Name: list[0].Name, Points: pts})
		}
		if len(series) < 2 {
			t.Logf("第 %d 组「%s」只有 %d 条序列，跳过", gi+1, grp.Name, len(series))
			continue
		}
		from := time.UnixMilli(series[0].Points[0][0])
		to := time.UnixMilli(series[0].Points[len(series[0].Points)-1][0])
		dev := signMonitorMaxDeviation(series)

		var title, sub string
		if len(series) >= 3 {
			title = grp.Name + " · 谁比同组多涨了多少"
			sub = fmt.Sprintf("已扣除各自起始值与同组中位数同步涨幅，图上 = 比同组中位数多涨（最大 +%d 人）", dev)
		} else {
			title = grp.Name + " · 两人的签到差距"
			sub = fmt.Sprintf("本组 2 人无中位数可依，图上 = 与另一个人的差距（最大 %d 人）", dev)
		}
		svg := buildSignMonitorDeviationSVG(series, title, sub, 760,
			signMonitorChartHeight(to.UnixMilli()-from.UnixMilli()), 800)
		if svg == "" {
			t.Errorf("第 %d 组偏离图为空", gi+1)
			continue
		}
		png, err := renderHTMLToPNG(signMonitorChartHTML(svg, grp.Name))
		if err != nil {
			t.Errorf("第 %d 组渲染失败: %v", gi+1, err)
			continue
		}
		w, h := pngDimensions(png)
		path := fmt.Sprintf("/tmp/sm-grp/%d-%s.png", gi+1, grp.Name)
		_ = os.WriteFile(path, png, 0o644)
		t.Logf("第%d组 %-14s %d人 偏离+%-5d PNG %dx%d → %s", gi+1, grp.Name, len(series), dev, w, h, path)
	}
}
