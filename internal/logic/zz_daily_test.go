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

func zzDailySamples(t *testing.T) []signstat.Sample {
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

// 渲染 10-07 日报邮件正文，核对：① 小时表进正文 ② 8 人按年龄顺序 ③ 无那张无意义表
func TestZZDailyMailBody(t *testing.T) {
	all := zzDailySamples(t)
	loc := time.FixedZone("CST", 8*3600)
	day := time.Date(2026, 10, 7, 23, 55, 0, 0, loc)

	tab, err := computeHourlyTable(all, day)
	if err != nil {
		t.Fatalf("computeHourlyTable: %v", err)
	}
	t.Logf("年龄顺序：")
	for i, oid := range tab.OIDs {
		t.Logf("  %d. %s", i+1, tab.Names[oid])
	}

	in := signMonitorReportInput{
		Title:      "超话签到日报 2026-10-07",
		Subtitle:   "0:00 至 23:55 · 采样间隔 5 分钟",
		HourlyHTML: signMonitorHourlyTableHTML(tab, day),
	}
	body := signMonitorReportHTML(in)
	if err := os.WriteFile("/tmp/daily_body.html", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// ① 小时表必须进正文
	if !strings.Contains(body, "每小时增量") {
		t.Error("❌ 正文缺小时表")
	}
	if !strings.Contains(body, "当日合计") {
		t.Error("❌ 正文缺当日合计行")
	}
	// ② 用户要求删掉的表不能回来
	for _, bad := range []string{"区间最低", "区间最高"} {
		if strings.Contains(body, bad) {
			t.Errorf("❌ 正文又出现 %s", bad)
		}
	}
	// ③ 列顺序 = 年龄顺序
	want := []string{"Carmen", "ChoiJiwoo", "柳河岚YUHA", "stella",
		"JUUN", "ANA卢惟那", "郑伊安IAN", "YEON金奈延"}
	head := body
	if i := strings.Index(head, "每小时增量"); i >= 0 {
		head = head[i:]
	}
	last := -1
	for _, n := range want {
		j := strings.Index(head, n)
		if j < 0 {
			t.Fatalf("❌ 表头缺少 %s", n)
		}
		if j < last {
			t.Errorf("❌ %s 出现在后面，顺序不对", n)
		}
		last = j
	}
	t.Log("✅ 8 人顺序正确")
}
