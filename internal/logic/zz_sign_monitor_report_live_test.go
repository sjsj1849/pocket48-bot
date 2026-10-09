package logic

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

// 端到端验证：日报能不能真的发出去（HTML 正文 + 折线图 PNG + CSV 附件）。
//
// 跑法：POCKET48_SM_REPORT=1 go test ./internal/logic/ -run TestSignMonitorDailyReportLive -v
func TestSignMonitorDailyReportLive(t *testing.T) {
	if os.Getenv("POCKET48_SM_REPORT") == "" {
		t.Skip("未设置 POCKET48_SM_REPORT")
	}
	raw, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Skipf("读不到 config.json: %v", err)
	}
	cfg := &config.Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		t.Fatalf("配置解析失败: %v", err)
	}
	if !cfg.AlertEmailEnabled {
		t.Skip("ALERT_EMAIL_ENABLED 未开启")
	}
	store := newSignMonitorStore("../../storage/weibo/sign-monitor.jsonl", 72*time.Hour)
	if err := store.load(); err != nil {
		t.Fatalf("读取采样失败: %v", err)
	}
	if len(store.samples) == 0 {
		t.Skip("还没有采样数据")
	}
	t.Logf("载入 %d 个采样点", len(store.samples))

	b := &Bot{cfg: cfg, signMonitor: store}
	loc := time.FixedZone("CST", 8*3600)
	start := time.Now()
	b.sendSignMonitorDailyReport(time.Now().In(loc), loc)
	t.Logf("发送流程耗时 %s", time.Since(start).Round(time.Millisecond))

	// 顺带确认 HTML/CSV 都能生成（不依赖邮件是否真的投递）
	series, _ := store.series(time.Now().Add(-24 * time.Hour).UnixMilli())
	if svg := buildSignMonitorLineSVG(series, "全量", "回归", 760, 300); svg == "" {
		t.Error("折线图 SVG 为空")
	} else {
		t.Logf("折线图 SVG %d 字节", len(svg))
	}
	if csvData := signMonitorCSV(series); len(csvData) < 10 {
		t.Error("CSV 为空")
	} else {
		t.Logf("CSV %d 字节", len(csvData))
	}
	_ = context.Background()
}
