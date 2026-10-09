package logic

// 临时验证：用真实采样数据生成「异常推送」的 PNG（不发消息）。
// 跑法：go test ./internal/logic/ -run TestGenPushPreview -v
//
// ★ 2026-10-08：明细表口径收敛到 signstat.WindowDetail
//   （面板、飞书/QQ 告警、邮件附件是同一份数据），这里同步更新。

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/signstat"
)

func loadSamplesFromJSONL(t *testing.T) []signstat.Sample {
	t.Helper()
	raw, err := os.ReadFile("/root/pocket48-bot/storage/weibo/sign-monitor.jsonl")
	if err != nil {
		t.Skipf("no jsonl: %v", err)
	}
	out := []signstat.Sample{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var s struct {
			TS   int64  `json:"ts"`
			OID  string `json:"oid"`
			Name string `json:"name"`
			Sign int    `json:"sign"`
		}
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			continue
		}
		if s.OID == "" || s.TS == 0 {
			continue
		}
		out = append(out, signstat.Sample{TS: s.TS, OID: s.OID, Name: s.Name, Sign: s.Sign})
	}
	return out
}

func TestGenPushPreview(t *testing.T) {
	samples := loadSamplesFromJSONL(t)
	if len(samples) == 0 {
		t.Skip("no samples")
	}
	// 找 ChoiJiwoo 昨晚 22:00-22:30 的真实异常时段
	target := "100808c35fb628604a4ed52a30bf6a3b345c03"
	loc := time.FixedZone("CST", 8*3600)
	winFrom := time.Date(2026, 10, 7, 22, 0, 0, 0, loc).UnixMilli()
	winTo := time.Date(2026, 10, 7, 22, 30, 0, 0, loc).UnixMilli()
	var fromTS, toTS int64
	name := ""
	for _, s := range samples {
		if s.OID != target {
			continue
		}
		name = s.Name
		if s.TS >= winFrom && s.TS <= winTo {
			if fromTS == 0 || s.TS < fromTS {
				fromTS = s.TS
			}
			if s.TS > toTS {
				toTS = s.TS
			}
		}
	}
	if fromTS == 0 {
		t.Skip("no spike window in data")
	}
	spike := signMonitorSpike{
		OID: target, Name: name,
		FromTS: fromTS, ToTS: toTS,
		From: 7883, To: 8888, Delta: 1005,
		PeakDelta: 567, GroupMedian: 43, GroupThreshold: 150,
		GroupName: "第一组（万档）", Steps: 2, Severity: "confirmed",
	}

	// ★ 粒度与告警一致：异常时刻往前 1 小时，异常时刻在最后一列
	from := toTS - spikeDetailWindowMin*60*1000
	to := toTS

	// ① 明细表
	dw := signstat.WindowDetail(samples, nil, from, to)
	dw.OID = target
	dw.Name = name
	html := buildSignMonitorDetailHTML(dw, spike, 1)
	if html != "" {
		if png, err := renderHTMLToPNG(html); err == nil {
			os.WriteFile("/tmp/push_detail.png", png, 0o644)
			t.Logf("detail png %d bytes", len(png))
		} else {
			t.Errorf("detail render: %v", err)
		}
	}

	// ② 每小时表
	if png, err := buildHourlyTablePNG(samples, time.Now()); err == nil {
		os.WriteFile("/tmp/push_hourly.png", png, 0o644)
		t.Logf("hourly png %d bytes", len(png))
	} else {
		t.Errorf("hourly: %v", err)
	}
}
