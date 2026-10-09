package logic

//用**真实采样数据**发一封异常邮件，验证 22:30 的三项改动：
//  1. 异常邮件附带偏离图（原来只有柱状图 + CSV）
//  2. 新增「异常时段 5 分钟粒度明细」表
//  3. 破万者的 +0 注明原因（郑伊安IAN 已破万，不是真的没涨）
//
// 由 POCKET48_SM_SPIKE=1 触发。
import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"pocket48-bot/internal/config"
)

func TestSendRealSpikeReport(t *testing.T) {
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

	b := &Bot{cfg: cfg, signMonitor: store}

	series, _ := store.series(0)
	if len(series) == 0 {
		t.Skip("没有序列")
	}
	t.Logf("载入 %d 序列", len(series))

	// ★ 手工构造指向真实异常时刻的 spike。
	//   store.series() 返回的 spikes 是「未报过的新异常」，今天 22:15 已报过 ⇒ 为空。
	//   这里直接用 22:14 那个真实异常（ChoiJiwoo +561）复现。
	var at int64
	names := map[string]string{}
	for _, s := range series {
		for _, p := range s.Points {
			ts := time.UnixMilli(p[0])
			if ts.Hour() == 22 && ts.Minute() == 14 {
				at = p[0]
			}
		}
		if s.Name != "" {
			names[s.OID] = s.Name
		}
	}
	if at == 0 {
		t.Skip("没找到 22:14 的采样点")
	}
	targetName := ""
	for _, n := range names {
		if n == "ChoiJiwoo" {
			targetName = n
		}
	}
	spike := signMonitorSpike{At: at, OID: "", Name: targetName, Delta: 561}
	for oid, n := range names {
		if n == targetName {
			spike.OID = oid
		}
	}
	if spike.GroupName == "" {
		for _, grp := range b.signMonitorReportGroups(series) {
			for _, s := range grp.Series {
				if s.Name == targetName {
					spike.GroupName = grp.Name
				}
			}
		}
	}
	t.Logf("构造异常：%s +%d @ %s 组=%s", spike.Name, spike.Delta,
		time.UnixMilli(spike.At).Format("15:04"), spike.GroupName)

	b.sendSignMonitorSpikeReport(spike, false)

	// ★ 顺带验证明细表直接产出（不发信），打印前1200 字符人工核对
	// ★ 明细表现在只认 signstat.DetailWindow（面板/PNG/邮件同一份数据）
	toTS := spike.ToTS
	if toTS == 0 {
		toTS = spike.At
	}
	dw, ok := b.spikeDetailWindow(spike)
	if !ok {
		t.Fatal("拿不到明细窗口")
	}
	dw.OID, dw.Name = spike.OID, spike.Name
	tbl := signMonitorSpikeDetailTable(&dw, spike.OID, spike.GroupThreshold, int(cfg.WeiboSignMonitor.Interval().Minutes()))
	_ = toTS
	if tbl == "" {
		t.Error("★ 明细表为空")
	} else {
		t.Logf("明细表长度 %d 字符", len(tbl))
		os.WriteFile("/tmp/spike_detail.html", []byte(tbl), 0o600)
		t.Logf("已写出 /tmp/spike_detail.html")
	}
}
