package logic

// 导出异常邮件里的两张图到 /tmp/sm-spike/，用于和面板图对比。
// 由 POCKET48_SM_SPIKE=1 触发。
import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

func TestExportSpikeCharts(t *testing.T) {
	if os.Getenv("POCKET48_SM_SPIKE") == "" {
		t.Skip("未设置 POCKET48_SM_SPIKE")
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
	b := &Bot{cfg: cfg, signMonitor: store}
	series, _ := store.series(0)
	if len(series) == 0 {
		t.Skip("没有序列")
	}
	os.MkdirAll("/tmp/sm-spike", 0o755)

	windowMin := b.cfg.WeiboSignMonitor.SpikeWindow()
	t.Logf("SpikeWindow = %d 分钟", windowMin)

	// 找 22:14 的异常
	var at int64
	for _, s := range series {
		for _, p := range s.Points {
			ts := time.UnixMilli(p[0])
			if ts.Hour() == 22 && ts.Minute() == 14 {
				at = p[0]
			}
		}
	}
	if at == 0 {
		t.Skip("没找到 22:14")
	}

	// 复刻 sendSignMonitorSpikeReport 的窗口逻辑
	peak := at
	peakDelta := -1
	for _, s := range series {
		for i := 1; i < len(s.Points); i++ {
			if s.Points[i][0] > at {
				break
			}
			d := int(s.Points[i][1] - s.Points[i-1][1])
			if d > peakDelta {
				peakDelta = d
				peak = s.Points[i][0]
			}
		}
	}
	from := time.UnixMilli(peak - int64(windowMin/2)*60*1000)
	to := time.UnixMilli(peak + int64(windowMin/2)*60*1000)
	t.Logf("peak=%s 窗口 %s → %s（峰增量 %d）",
		time.UnixMilli(peak).Format("15:04"),
		from.Format("15:04"), to.Format("15:04"), peakDelta)

	// 找第一组
	var groupSeries []signMonitorSeries
	groupLabel := "全部超话"
	for _, grp := range b.signMonitorReportGroups(series) {
		found := false
		for _, s := range grp.Series {
			if s.Name == "ChoiJiwoo" {
				found = true
			}
		}
		if found {
			groupSeries = grp.Series
			groupLabel = grp.Name
			break
		}
	}
	t.Logf("组 %s，成员 %d", groupLabel, len(groupSeries))
	for _, s := range groupSeries {
		mx := int64(0)
		for _, p := range s.Points {
			if p[1] > mx {
				mx = p[1]
			}
		}
		crossed, atc := signMonitorCrossedAt(groupSeries, s.OID, 0)
		t.Logf("   %-14s 点数 %3d 末值 %5d 最大 %5d %s", s.Name, len(s.Points),
			s.Points[len(s.Points)-1][1], mx, crossoverMark(crossed, atc))
	}

	// 柱状图
	barTitle := "异常时段每 5 分钟增量 · " + groupLabel + "（ChoiJiwoo 异常 +561）"
	barSVG := buildSignMonitorBarSVG(groupSeries, from, to, barTitle, 760, 300)
	if png, err := renderHTMLToPNG(signMonitorChartHTML(barSVG, barTitle)); err == nil {
		os.WriteFile("/tmp/sm-spike/mail-bar.png", png, 0o600)
		t.Logf("已导出 /tmp/sm-spike/mail-bar.png")
	} else {
		t.Errorf("柱状图渲染失败: %v", err)
	}

	// 偏离图（用真实未破万成员）
	devTitle := "签到偏移（比同组多涨）· " + groupLabel + "（ChoiJiwoo 异常 +561）"
	devSVG := buildSignMonitorDeviationSVG(groupSeries, devTitle,
		"红线=比同组中位数多涨、绿线=比同组少涨（必定正负各半）；已破万者剔除，不计入偏移",
		760, signMonitorChartHeight(to.Sub(from).Milliseconds()), b.cfg.WeiboSignMonitor.SpikeAbs())
	if devSVG == "" {
		t.Logf("偏离图为空（有效成员 < 2）")
	} else if png, err := renderHTMLToPNG(signMonitorChartHTML(devSVG, devTitle)); err == nil {
		os.WriteFile("/tmp/sm-spike/mail-dev.png", png, 0o600)
		t.Logf("已导出 /tmp/sm-spike/mail-dev.png")
	} else {
		t.Errorf("偏离图渲染失败: %v", err)
	}
}

func crossoverMark(crossed bool, at int64) string {
	if !crossed {
		return ""
	}
	return "★已破万 " + time.UnixMilli(at).Format("15:04")
}
