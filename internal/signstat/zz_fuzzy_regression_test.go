package signstat

import (
	"testing"
	"time"
)

// 2026-10-07 郑伊安真实序列的形状（逐点缩时到 5 分钟间隔）。
// 22:04 破万（9993→10000，只 +7），随后卡在 10000 整整 41 分钟，
// 22:45 突然整千跳到 11000，之后 75 分钟不动。
//   跳变速度 = 1000 / (41/60) 小时 ≈ 1463 人/小时。
func zzIANSamples() []Sample {
	base := time.Date(2026, 10, 7, 20, 0, 0, 0, time.FixedZone("CST", 8*3600)).UnixMilli()
	step := int64(5 * 60 * 1000)
	// 20:00 ~ 21:35 每 5 分钟 +40（末值 9960）⇒ 破万前 2 小时基线 480/人时
	vals := []int{9200}
	for i := 0; i < 23; i++ {
		vals = append(vals, vals[len(vals)-1]+40)
	}
	// 21:40 破万那一刻：9960 → 10000（只 +40）
	vals = append(vals, 10000)
	// 破万后立刻卡住不动（真实数据里卡了 41 分钟，这里 8 个点）
	for i := 0; i < 8; i++ {
		vals = append(vals, 10000)
	}
	// 整千跳变 10000 → 11000
	vals = append(vals, 11000)
	// 之后长时间不动
	for i := 0; i < 12; i++ {
		vals = append(vals, 11000)
	}
	out := make([]Sample, 0, len(vals))
	for i, v := range vals {
		out = append(out, Sample{
			TS: base + int64(i)*step, OID: "oid-ian", Name: "郑伊安IAN", Sign: v,
		})
	}
	return out
}

// 破万后「不到一小时涨 1000」必须判成疑似 —— 只看相对基线会漏掉。
//
// ★ 这条锁的是 2026-10-08 的真实漏判：原实现基线只取破万前最后 30 分钟，
//   那段正好是冲榜（562/人时），跳变 ratio 只有 2.66 卡在阈值 3 以下，
//   一次都没报。改成绝对速度为主判据后才抓出来。
func TestSuspectedCrossoverAbsoluteRate(t *testing.T) {
	res := AnalyzeSamples(zzIANSamples(), nil, Options{})

	var sus []Anomaly
	for _, a := range res.Anomalies {
		if a.Severity == SeveritySuspected {
			sus = append(sus, a)
		}
	}
	if len(sus) != 1 {
		t.Fatalf("应恰好判出 1 条疑似，实际 %d 条", len(sus))
	}
	a := sus[0]
	if a.OID != "oid-ian" {
		t.Fatalf("判到了别人: %s", a.Name)
	}
	if a.To-a.From != 1000 {
		t.Errorf("跳变量应为 1000，实际 %d", a.To-a.From)
	}
	if a.RatePerHour < 1200 {
		t.Errorf("速度应约 1400+/小时，实际 %.0f", a.RatePerHour)
	}
	if a.Severity == SeverityConfirmed {
		t.Error("破万者只能是疑似，不能是确认异常")
	}
}

// 速度正常（卡很久才涨 1000）时完全不报 —— 用户明确要求「拉得正常就别判」。
func TestSuspectedCrossoverNormalRateNoReport(t *testing.T) {
	base := time.Date(2026, 10, 7, 20, 0, 0, 0, time.FixedZone("CST", 8*3600)).UnixMilli()
	step := int64(5 * 60 * 1000)
	vals := []int{9200}
	for i := 0; i < 20; i++ {
		vals = append(vals, vals[len(vals)-1]+40)
	}
	vals = append(vals, 10000) // 破万
	// 卡 4 小时（共 48 个采样点）才涨 1000 ⇒ 约 250 人/小时，正常
	for i := 0; i < 48; i++ {
		vals = append(vals, 10000)
	}
	vals = append(vals, 11000)
	out := make([]Sample, 0, len(vals))
	for i, v := range vals {
		out = append(out, Sample{TS: base + int64(i)*step, OID: "oid-slow", Name: "慢的", Sign: v})
	}
	res := AnalyzeSamples(out, nil, Options{})
	for _, a := range res.Anomalies {
		if a.Severity == SeveritySuspected {
			t.Fatalf("速度正常不该报疑似，实际报了: %s", a.Reason)
		}
	}
}

// ★★ 破万后的步是「已知的下界」，不是未知 —— 那 1000 必须计入。
//
// 2026-10-08 用户纠正：旧实现遇到破万后的步直接 continue，
// 于是 22:45 那一步 10000→11000 的 1000 被丢掉，
// 小时表只显示「+7」，而实际已知下限是 7 + 1000 = 1007。
func TestWindowDetailCountsKnownFuzzyStep(t *testing.T) {
	all := zzIANSamples()
	series := BuildSeries(all)
	var s Series
	for _, x := range series {
		if x.OID == "oid-ian" {
			s = x
		}
	}
	if !s.Crossed() {
		t.Fatal("测试序列应破万")
	}
	// 窗口与真实告警一致：异常时刻往前 1 小时（22:45 往前到 21:45）
	jumpTS := int64(0)
	for i, p := range s.Points {
		if i > 0 && p.Sign == 11000 && s.Points[i-1].Sign == 10000 {
			jumpTS = p.TS
			break
		}
	}
	dw := WindowDetail(all, nil, jumpTS-60*60*1000, jumpTS)

	var row *DetailRow
	for i := range dw.Rows {
		if dw.Rows[i].OID == "oid-ian" {
			row = &dw.Rows[i]
		}
	}
	if row == nil {
		t.Fatal("找不到该超话的明细行")
	}
	if !row.Fuzzy {
		t.Error("破万者应标记 Fuzzy")
	}

	// 找出观测到 +1000 的那一格
	saw1000 := false
	for ci, d := range row.Deltas {
		if d != nil && *d == 1000 {
			saw1000 = true
			if ci >= len(row.Marks) || row.Marks[ci] != "crossover" {
				t.Errorf("那一步应带 crossover 标记（渲染成 +1000*），实际 %q", row.Marks[ci])
			}
		}
	}
	if !saw1000 {
		t.Fatal("❌ 10000→11000 那一步的 +1000 没有被计入（旧 bug）")
	}
	// 净增必须包含这 1000
	if row.Net == nil || *row.Net < 1000 {
		t.Errorf("❌ 净增应≥1000，实际 %v", row.Net)
	}
}
