package logic

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/signstat"
)

func zzMailSamples(t *testing.T) []signstat.Sample {
	t.Helper()
	root := os.Getenv("POCKET48_ROOT")
	if root == "" {
		root = "/root/pocket48-bot"
	}
	f, err := os.Open(filepath.Join(root, "storage", "weibo", "sign-monitor.jsonl"))
	if err != nil {
		t.Skipf("no samples: %v", err)
	}
	defer f.Close()
	out := []signstat.Sample{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var sm signstat.Sample
		if err := json.Unmarshal(line, &sm); err != nil {
			continue
		}
		out = append(out, sm)
	}
	return out
}

// ★★★ 邮件正文表格曾经有第二套独立实现，用户连着投诉三次（2026-10-08）。
// 这条测试钉住四个坑：
//
//	① 正文不得再出现「区间最低/区间最高/采样点」那张分组统计表
//	② 明细表窗口必须是**异常自己的时段**，最后一列 = 异常时刻
//	③ 未破万的人（前几列）不得被误标破万 —— 柳河岚 23:50 才破万
//	④ 破万后的真实增量（10000→11000 的 1000）必须出现在表里
func TestMailBodyUsesAnomalyOwnWindow(t *testing.T) {
	all := zzMailSamples(t)
	loc := time.FixedZone("CST", 8*3600)
	groups := []signstat.Group{
		{Name: "第一组（万档）", Members: []string{
			"100808a13fc3473ab904866d82c69e8fcb57d4",
			"100808c35fb628604a4ed52a30bf6a3b345c03",
			"100808430244540aa38ff181545cb0d539f038",
			"1008086caedd6c0e0da530739e493e819ba68e",
		}},
	}

	res := signstat.AnalyzeSamples(all, groups, signstat.Options{})
	var sus *signstat.Anomaly
	for i := range res.Anomalies {
		if res.Anomalies[i].Severity == signstat.SeveritySuspected {
			sus = &res.Anomalies[i]
			break
		}
	}
	if sus == nil {
		t.Skip("当前数据里没有疑似异常")
	}

	dw := signstat.WindowDetail(all, groups,
		sus.ToTS-spikeDetailWindowMin*60*1000, sus.ToTS)
	dw.OID, dw.Name = sus.OID, sus.Name

	in := signMonitorReportInput{
		Title:           "超话签到异常：" + sus.Name,
		Subtitle:        "异常时段",
		Spikes:          []signMonitorSpike{anomalyToSpike(*sus)},
		IntervalMinutes: 5,
		Detail:          &dw,
		AnomOID:         sus.OID,
		DetailThreshold: sus.Threshold,
	}
	body := signMonitorReportHTML(in)

	// ① 用户明确要求删掉的分组统计表
	for _, bad := range []string{"区间最低", "区间最高"} {
		if strings.Contains(body, bad) {
			t.Errorf("❌ 正文仍含要求删除的分组统计表列：%s", bad)
		}
	}
	if strings.Contains(body, "solid #e4e7ec\">采样点") {
		t.Error("❌ 正文仍含「采样点」表头")
	}
	if strings.Contains(body, "已破万（10-07 23:50") {
		t.Error("❌ 仍用「累计破万时刻」的整行说明")
	}

	// ② 窗口必须是这条异常自己的时段，最后一列 = 异常时刻
	lastCol := time.UnixMilli(dw.Stamps[len(dw.Stamps)-1]).In(loc).Format("15:04")
	wantCol := time.UnixMilli(sus.ToTS).In(loc).Format("15:04")
	if lastCol != wantCol {
		t.Errorf("❌ 最后一列应是异常时刻 %s，实际 %s", wantCol, lastCol)
	}

	// ③ 未破万者前几列不得被误标
	early := map[int]bool{}
	for i := 0; i < 4 && i < len(dw.Stamps); i++ {
		early[i] = true
	}
	for _, r := range dw.Rows {
		if r.Name != "柳河岚YUHA" {
			continue
		}
		for ci := range r.Marks {
			if !early[ci] {
				continue
			}
			if r.Marks[ci] == "crossover" {
				t.Errorf("❌ 柳河岚第 %d 列被误标破万（她 23:50 才破万）", ci)
			}
			if r.Deltas[ci] != nil && *r.Deltas[ci] <= 0 {
				t.Errorf("❌ 柳河岚第 %d 列增量应>0，实际 %v", ci, *r.Deltas[ci])
			}
		}
	}

	// ④ 破万后的真实增量必须保留
	saw1000 := false
	for _, r := range dw.Rows {
		if r.Name != sus.Name {
			continue
		}
		for ci, d := range r.Deltas {
			if d != nil && *d == 1000 {
				saw1000 = true
				if r.Marks[ci] != "crossover" {
					t.Errorf("那一步应带 crossover 标记（渲染 +1000*），实际 %q", r.Marks[ci])
				}
			}
		}
	}
	if !saw1000 {
		t.Error("❌ 10000→11000 的 +1000 没出现在表里（旧 bug）")
	}
}
