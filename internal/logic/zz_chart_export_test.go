package logic

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

// 导出日报的三张分组图到 /tmp/sm-charts/，用于人工核对画面占比与分组。
//
// 跑法：POCKET48_SM_EXPORT=1 go test ./internal/logic/ -run TestSignMonitorExportGroupCharts -v
func TestSignMonitorExportGroupCharts(t *testing.T) {
	if os.Getenv("POCKET48_SM_EXPORT") == "" {
		t.Skip("未设置 POCKET48_SM_EXPORT")
	}
	raw, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Skipf("读不到 config.json: %v", err)
	}
	cfg := &config.Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		t.Fatalf("配置解析失败: %v", err)
	}
	store := newSignMonitorStore("../../storage/weibo/sign-monitor.jsonl", 72*time.Hour)
	if err := store.load(); err != nil {
		t.Fatalf("读取采样失败: %v", err)
	}
	loc := time.FixedZone("CST", 8*3600)
	now := time.Now().In(loc)
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).UnixMilli()
	series, _ := store.series(since)
	b := &Bot{cfg: cfg, signMonitor: store}

	outDir := "/tmp/sm-charts"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	groups := b.signMonitorReportGroups(series)
	for _, g := range groups {
		sub := "05:00 至今 · 每 5 分钟采样 · 本组 " + strconv.Itoa(len(g.Series)) + " 个超话"
		svg := buildSignMonitorLineSVG(g.Series, g.Name+" · 今日签到累计", sub, 760, 300)
		if svg == "" {
			t.Logf("组 %s 没有可画的数据", g.Name)
			continue
		}
		png, err := renderHTMLToPNG(signMonitorChartHTML(svg, g.Name))
		if err != nil {
			t.Errorf("组 %s 渲染失败: %v", g.Name, err)
			continue
		}
		w, h := pngDimensions(png)
		name := filepath.Join(outDir, fmt.Sprintf("%s.png", g.Name))
		if err := os.WriteFile(name, png, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("组 %-10s %d 人  PNG %dx%d ratio=%.3f → %s",
			g.Name, len(g.Series), w, h, float64(w)/float64(h), name)
	}
	t.Logf("共 %d 组", len(groups))
}
