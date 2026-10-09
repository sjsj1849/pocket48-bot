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

// 打印真实数据每个时刻的偏离值，并渲染一张「大字标注末点偏离」的图，
// 用来验证用户质疑的「为什么图上看着全是正的」。
func TestDebugDeviationNumbers(t *testing.T) {
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
	grp := cfg.WeiboSignMonitor.Groups[0]
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
	if len(series) < 3 {
		t.Skip("不足3 人")
	}

	// 手算末点偏离（真中位数：中间两个平均）
	n := len(series)
	total := make([]int64, n)
	for i, s := range series {
		total[i] = s.Points[len(s.Points)-1][1] - s.Points[0][1]
	}
	sorted := append([]int64(nil), total...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
	median := (sorted[n/2-1] + sorted[n/2]) / 2
	t.Logf("第1组 %s：%d 人", grp.Name, n)
	t.Logf("总增量与偏离（真中位数 %d）:", median)
	pos, neg := 0, 0
	for i, s := range series {
		d := total[i] - median
		tag := "正"
		if d > 0 {
			pos++
		} else if d < 0 {
			neg++
			tag = "负"
		}
		t.Logf("  %-14s 总增量 %+4d  偏离 %+4d  (%s)", s.Name, total[i], d, tag)
	}
	t.Logf("=> 正 %d 个 / 负 %d 个", pos, neg)
	if pos > 0 && neg == 0 && n >= 3 {
		t.Errorf("★ %d 人组不该全部为正：正 %d 负 %d", n, pos, neg)
	}

	// 画一张「末点偏离大字标注」的图：把每条线的最终偏离直接写在图上
	if os.Getenv("POCKET48_SM_DAY") == "" {
		t.Skip("需要渲染")
	}
	from := time.UnixMilli(series[0].Points[0][0])
	to := time.UnixMilli(series[0].Points[len(series[0].Points)-1][0])
	sub := fmt.Sprintf("正负各半才对；虚线=同组中位数的同步涨幅（%d 人）", n)
	svg := buildSignMonitorDeviationSVG(series, grp.Name+" · 谁比同组多涨了多少（末点标注）", sub,
		760, signMonitorChartHeight(to.UnixMilli()-from.UnixMilli()), 800)
	if svg == "" {
		t.Fatal("SVG 为空")
	}
	png, err := renderHTMLToPNG(signMonitorChartHTML(svg, grp.Name))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll("/tmp/sm-dbg", 0o755)
	_ = os.WriteFile("/tmp/sm-dbg/末点偏离.png", png, 0o644)
	t.Log("已导出 /tmp/sm-dbg/末点偏离.png")
}
