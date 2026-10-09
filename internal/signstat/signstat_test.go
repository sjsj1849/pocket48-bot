package signstat

import (
	"testing"
	"time"
)

var testLoc = time.FixedZone("CST", 8*3600)

func ts(t *testing.T, y int, mo int, d int, h int, mi int) int64 {
	t.Helper()
	return time.Date(y, time.Month(mo), d, h, mi, 0, 0, testLoc).UnixMilli()
}

// buildSamples 把「分钟偏移 → 签到数」转成 samples。
// dayStart 是当天 00:00，之后每 5 分钟一个点。
func buildSamples(dayStart int64, spec map[string][]int) []Sample {
	out := []Sample{}
	for name, values := range spec {
		for i, v := range values {
			out = append(out, Sample{
				TS:   dayStart + int64(i)*5*60*1000,
				OID:  "oid-" + name,
				Name: name,
				Sign: v,
			})
		}
	}
	return out
}

func groupOf(names ...string) Group {
	g := Group{Name: "G"}
	for _, n := range names {
		g.Members = append(g.Members, "oid-"+n)
	}
	return g
}

// ---------------------------------------------------------------------------
// 测试 1：跨日归零必须被剔除
// ---------------------------------------------------------------------------

func TestCrossMidnightStepIsDropped(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	d2 := ts(t, 2026, 10, 8, 0, 0)
	// 10-07 23:55 是 9000，10-08 00:05 变成 300（归零）
	samples := buildSamples(d1, map[string][]int{
		"A": {9000, 9001},
		"B": {8000, 8001},
	})
	// 手动补跨日点
	samples = append(samples,
		Sample{TS: d2 + 5*60*1000, OID: "oid-A", Name: "A", Sign: 300},
		Sample{TS: d2 + 5*60*1000, OID: "oid-B", Name: "B", Sign: 280},
	)
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")}, Options{Threshold: 150})

	// 关键：不应有 -8700 级别的异常
	for _, a := range res.Anomalies {
		if a.PeakDelta < -1000 || a.TotalDelta < -1000 {
			t.Fatalf("跨日归零步被当成了异常: %+v", a)
		}
	}
	// 也不应有接近 -8700 的偏离点
	for _, g := range res.Groups {
		for _, pts := range g.Series {
			for _, p := range pts {
				if p.Dev < -1000 {
					t.Fatalf("图上出现跨日巨量偏离 %d，Y 轴会被撑爆", p.Dev)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 测试 2：正常波动不应误报（阈值必须真的有效）
// ---------------------------------------------------------------------------

func TestNormalFluctuationDoesNotTrigger(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	// 4 人组，每步增量在 40~65 之间小幅抖动（贴近真实 p50=4 / p90=14）
	var A, B, C, D []int
	for i := 0; i < 60; i++ {
		a := 1000 + i*45 + jitter(i, 8)
		A = append(A, a)
		B = append(B, 1100+i*44+jitter(i+1, 8))
		C = append(C, 1200+i*46+jitter(i+2, 8))
		D = append(D, 1300+i*43+jitter(i+3, 8))
	}
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B, "C": C, "D": D})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B", "C", "D")},
		Options{Threshold: DefaultThreshold})
	if len(res.Anomalies) != 0 {
		t.Fatalf("正常波动被误报 %d 次: %+v", len(res.Anomalies), res.Anomalies[0])
	}
}

func jitter(i int, amp int) int {
	// 确定性的小抖动，避免随机
	v := (i*7)%11 - 5
	return v * amp / 5
}

// ---------------------------------------------------------------------------
// 测试 3：单点刷量必须被抓到
// ---------------------------------------------------------------------------

func TestSinglePointSpikeIsCaught(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B, C, D []int
	for i := 0; i < 40; i++ {
		A = append(A, 1000+i*45)
		B = append(B, 1100+i*45)
		C = append(C, 1200+i*45)
		D = append(D, 1300+i*45)
	}
	// 单点刷量：抬高一个点。下一采样点会回落到趋势线，
	// 那一步是个**独立的**负偏离（确实是另一件事），所以这里只断言
	//「+600 那次被抓到」，不断言只有一条。
	A[20] += 600
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B, "C": C, "D": D})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B", "C", "D")},
		Options{Threshold: DefaultThreshold})

	found := false
	for _, a := range res.Anomalies {
		if a.Name == "A" && a.PeakDelta >= 500 && a.PeakDelta <= 700 {
			found = true
		}
	}
	if !found {
		for _, a := range res.Anomalies {
			t.Logf("got: %s peak=%d from=%d to=%d steps=%d", a.Name, a.PeakDelta, a.FromTS, a.ToTS, a.Steps)
		}
		t.Fatal("单点 +600 尖峰没被抓到")
	}
}

// ---------------------------------------------------------------------------
// 测试 4：★ 连续三点刷量必须合并成一次（用户 22:09/22:14/22:19 的场景）
// ---------------------------------------------------------------------------

func TestConsecutiveSpikesMergeIntoOne(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B []int
	for i := 0; i < 40; i++ {
		A = append(A, 1000+i*45)
		B = append(B, 1100+i*45)
	}
	// ★ 连续刷量的正确建模：**连续多个采样点都超阈值**。
	//   注意不能"抬高单点再回落" —— 那会让下一步变成暴跌，
	//   而暴跌确实是另一件事，不该被合并（实测刷量后的回落是缓坡，不是断崖）。
	//   实测 ChoiJiwoo 22:09/22:14/22:19 偏离 +351/+564/+71，
	//   是连续三点都偏离同组，不是单点尖峰。
	A[20] += 350
	A[21] += 350 + 600
	A[22] += 350 + 600 + 200
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")},
		Options{Threshold: DefaultThreshold})

	// 找到「A 的那条正向异常」：必须恰好 1 条，且已把 3 个采样点合并进来。
	//
	// ★ 注意：抬升之后回落到趋势线的那一步是**反向**的（-1150），
	//   它确实是另一件事，会单独成为一条 —— 这是正确行为，不是 bug。
	//   单点刷量留下"上去再下来"的痕迹，正是判断瞬时刷量的线索。
	var pos *Anomaly
	negCount := 0
	for i := range res.Anomalies {
		a := res.Anomalies[i]
		if a.Name != "A" {
			continue
		}
		if a.PeakDelta > 0 {
			if pos != nil {
				t.Fatalf("同一人的正向异常应合并成1 条，实际出现多条")
			}
			pos = &res.Anomalies[i]
		} else {
			negCount++
		}
	}
	if pos == nil {
		for _, a := range res.Anomalies {
			t.Logf("got: %s peak=%d steps=%d", a.Name, a.PeakDelta, a.Steps)
		}
		t.Fatal("没有抓到 A 的正向异常")
	}
	if pos.Steps != 3 {
		t.Fatalf("应合并 3 个连续采样点，实际 %d（未合并）", pos.Steps)
	}
	if pos.PeakDelta < 500 || pos.PeakDelta > 700 {
		t.Fatalf("峰值偏离应≈600，实际 %d", pos.PeakDelta)
	}
	if pos.TotalDelta < 1000 {
		t.Fatalf("累计增量应≈1150，实际 %d", pos.TotalDelta)
	}
	// ★ 2 人组不能报成两条（A 多涨 / B 少涨是同一件事）
	for _, x := range res.Anomalies {
		if x.Name == "B" && x.PeakDelta < 0 {
			t.Fatal("2 人组不应把「少涨」也报成一条独立异常")
		}
	}
	_ = negCount
}

// ---------------------------------------------------------------------------
// 测试 5：偏离必定正负各半（基线算错的最好用红灯）
// ---------------------------------------------------------------------------

func TestDeviationHasBothSigns(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B, C, D []int
	for i := 0; i < 50; i++ {
		A = append(A, 1000+i*45+jitter(i, 20))
		B = append(B, 1100+i*45+jitter(i+1, 20))
		C = append(C, 1200+i*45+jitter(i+2, 20))
		D = append(D, 1300+i*45+jitter(i+3, 20))
	}
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B, "C": C, "D": D})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B", "C", "D")},
		Options{Threshold: DefaultThreshold})

	pos, neg := 0, 0
	for _, g := range res.Groups {
		for _, pts := range g.Series {
			for _, p := range pts {
				if p.Dev > 0 {
					pos++
				} else if p.Dev < 0 {
					neg++
				}
			}
		}
	}
	if pos == 0 || neg == 0 {
		t.Fatalf("偏离必须正负各半，实际 pos=%d neg=%d ⇒ 基线算错了", pos, neg)
	}
}

// ---------------------------------------------------------------------------
// 测试 6：★ 破万是「曲线截断」不是「整条消失」
// ---------------------------------------------------------------------------

func TestCrossoverTruncatesNotRemoves(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	// 要让 A 精确段够长又不在中途破万：起点 8000，每点 +45，20 点后到 8900
	var A, B, C []int
	for i := 0; i < 20; i++ {
		A = append(A, 8000+i*45)
		B = append(B, 1100+i*45)
		C = append(C, 1200+i*45)
	}
	exactLen := len(A) // 20
	// A 破万：继续给模糊值
	A = append(A, 10000, 10000, 10000, 11000, 11000)
	var Bext, Cext []int
	Bext = append(Bext, B...)
	Bext = append(Bext, 1935, 1980, 2025, 2070, 2115)
	Cext = append(Cext, C...)
	Cext = append(Cext, 2035, 2080, 2125, 2170, 2215)

	samples := buildSamples(d1, map[string][]int{"A": A, "B": Bext, "C": Cext})
	series := BuildSeries(samples)

	var sa *Series
	for i := range series {
		if series[i].OID == "oid-A" {
			sa = &series[i]
		}
	}
	if sa == nil {
		t.Fatal("找不到 A 的序列")
	}
	if sa.CrossoverAt == 0 {
		t.Fatal("应识别出破万时刻")
	}
	// ★ 关键：整条序列必须还在（前缀 + 模糊段），不是被丢掉
	if len(sa.Points) != len(A) {
		t.Fatalf("破万后整条序列被删了：剩 %d 点，应有 %d 点", len(sa.Points), len(A))
	}
	if sa.Points[len(A)-1].Sign != 11000 {
		t.Fatal("曲线应画到破万/模糊段末尾")
	}
	// 统计只用破万前的精确段
	an := sa.AnalysisPoints()
	if len(an) != exactLen {
		t.Fatalf("参与统计的精确段应为 %d 点，实际 %d", exactLen, len(an))
	}

	// 组里仍然要能算偏离（用 B、C 做参照）
	res := AnalyzeSeries(series, []Group{groupOf("A", "B", "C")}, Options{Threshold: DefaultThreshold})
	if len(res.Groups) == 0 {
		t.Fatal("破万者所在组应仍能出图（用未破万成员做基线）")
	}
	found := false
	for _, e := range res.Groups[0].Excluded {
		if e.OID == "oid-A" {
			found = true
		}
	}
	if !found {
		t.Fatal("破万者应出现在 Excluded 里（说明为何不参与），而不是凭空消失")
	}
}

// ---------------------------------------------------------------------------
// 测试 7：★ 破万者不产生「确定异常」
// ---------------------------------------------------------------------------

func TestCrossoverDoesNotMakeConfirmedAnomaly(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B []int
	for i := 0; i < 30; i++ {
		A = append(A, 9000+i*45)
		B = append(B, 1100+i*45)
	}
	A = append(A, 10000, 10000, 10000, 11000, 11000)
	B = append(B, 1335, 1380, 1425)
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")}, Options{Threshold: 150})
	for _, a := range res.Anomalies {
		if a.Severity == SeverityConfirmed && a.Name == "A" {
			t.Fatalf("破万者的模糊跳变被当成了确定异常: %+v", a)
		}
	}
}

// ---------------------------------------------------------------------------
// 测试 8：★ 破万者速度异常 → 疑似；速度正常 → 完全不判
// ---------------------------------------------------------------------------

func TestSuspectedOnlyWhenRateAbnormal(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A []int
	for i := 0; i < 30; i++ {
		// ★ 最后精确值贴近门槛（真实是 9993 → 10000，只 +7），
		//   这样「跨越破万门槛那一步」不会被误当成模糊期跳变
		A = append(A, 9900+i*5)
	}
	// 破万后 5 分钟就跳 +2000（极快）⇒ 应判疑似
	A = append(A, 10000, 12000)
	samples := buildSamples(d1, map[string][]int{"A": A})
	res := AnalyzeSeries(BuildSeries(samples), nil, Options{Threshold: 150})
	if len(res.Anomalies) == 0 {
		t.Fatal("极快的模糊跳变应判疑似异常")
	}
	if res.Anomalies[0].Severity != SeveritySuspected {
		t.Fatalf("应标为疑似，实际 %s", res.Anomalies[0].Severity)
	}
	if res.Anomalies[0].RatePerHour <= res.Anomalies[0].BaselinePerHour*3 {
		t.Fatalf("速度比应超 3 倍: rate=%.0f base=%.0f",
			res.Anomalies[0].RatePerHour, res.Anomalies[0].BaselinePerHour)
	}
}

func TestNormalCrossoverRateNotFlagged(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A []int
	for i := 0; i < 30; i++ {
		A = append(A, 9900+i*5)
	}
	// 破万后先卡住 6 小时（72 个点全是 10000），然后才涨到 11000
	// ⇒ 隐含速度 ≈ 167/小时，远低于自身基线 ⇒ **不该判疑似**
	for i := 0; i < 72; i++ {
		A = append(A, 10000)
	}
	A = append(A, 11000)
	samples := buildSamples(d1, map[string][]int{"A": A})
	res := AnalyzeSeries(BuildSeries(samples), nil, Options{Threshold: 150})
	for _, a := range res.Anomalies {
		if a.Severity == SeveritySuspected {
			t.Fatalf("速度正常却被判疑似: rate=%.0f base=%.0f", a.RatePerHour, a.BaselinePerHour)
		}
	}
}

// ---------------------------------------------------------------------------
// 测试 9：破万时刻取第一次（正序扫）
// ---------------------------------------------------------------------------

func TestCrossoverAtIsFirstOccurrence(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	A := []int{9000, 9100, 10000, 10500, 11000}
	samples := buildSamples(d1, map[string][]int{"A": A})
	series := BuildSeries(samples)
	want := d1 + 2*5*60*1000
	if series[0].CrossoverAt != want {
		t.Fatalf("破万时刻应取第一次(%d)，实际 %d", want, series[0].CrossoverAt)
	}
	if series[0].CrossoverFrom != 9100 {
		t.Fatalf("破万前最后精确值应为 9100，实际 %d", series[0].CrossoverFrom)
	}
}

// ---------------------------------------------------------------------------
// 测试 10：2 人组必定一正一负
// ---------------------------------------------------------------------------

func TestTwoMemberAlwaysOppositeSigns(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B []int
	for i := 0; i < 30; i++ {
		A = append(A, 1000+i*45)
		B = append(B, 1100+i*45)
	}
	A[15] += 400
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")}, Options{Threshold: 150})
	for _, g := range res.Groups {
		va := g.Series["oid-A"]
		vb := g.Series["oid-B"]
		if len(va) != len(vb) {
			t.Fatalf("两人点数应相同: %d vs %d", len(va), len(vb))
		}
		for i := range va {
			if va[i].Dev != -vb[i].Dev {
				t.Fatalf("2 人组必须互为相反数: A=%d B=%d @%d", va[i].Dev, vb[i].Dev, i)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 测试 11：单步偏离公式（手算对照）
// ---------------------------------------------------------------------------

func TestDeviationFormulaHandChecked(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	// 手算：3 人，一步增量 40 / 50 / 60 ⇒ 中位数 50 ⇒ 偏离 -10 / 0 / +10
	A := []int{1000, 1040}
	B := []int{2000, 2050}
	C := []int{3000, 3060}
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B, "C": C})
	res := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B", "C")}, Options{Threshold: 100000})
	if len(res.Groups) != 1 {
		t.Fatal("应出一组")
	}
	got := map[string]int{}
	for _, p := range res.Groups[0].Series["oid-A"] {
		got["A"] = p.Dev
	}
	for _, p := range res.Groups[0].Series["oid-B"] {
		got["B"] = p.Dev
	}
	for _, p := range res.Groups[0].Series["oid-C"] {
		got["C"] = p.Dev
	}
	if got["A"] != -10 || got["B"] != 0 || got["C"] != 10 {
		t.Fatalf("手算应为 A=-10 B=0 C=10，实际 %+v", got)
	}
}

// ---------------------------------------------------------------------------
// 测试 12：小时汇总不把跨日归零算成增量
// ---------------------------------------------------------------------------

func TestHourlyIgnoresMidnightReset(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	d2 := ts(t, 2026, 10, 8, 0, 0)
	samples := []Sample{
		{TS: d1 + 55*60*1000, OID: "oid-A", Name: "A", Sign: 9000},
		{TS: d2 + 5*60*1000, OID: "oid-A", Name: "A", Sign: 300},
		{TS: d2 + 10*60*1000, OID: "oid-A", Name: "A", Sign: 400},
	}
	h := Hourly(BuildSeries(samples), testLoc)
	if len(h) != 1 {
		t.Fatalf("应只有 10-08 00:00 一个桶，实际 %d 个: %+v", len(h), h)
	}
	if h[0].Total != 100 {
		t.Fatalf("00:00 桶应只算 +100（归零那步不计），实际 %+d", h[0].Total)
	}
}

// ---------------------------------------------------------------------------
// 测试 13：阈值可配置
// ---------------------------------------------------------------------------

func TestThresholdIsConfigurable(t *testing.T) {
	d1 := ts(t, 2026, 10, 7, 0, 0)
	var A, B []int
	for i := 0; i < 30; i++ {
		A = append(A, 1000+i*45)
		B = append(B, 1100+i*45)
	}
	A[15] += 120 // 小幅偏离
	samples := buildSamples(d1, map[string][]int{"A": A, "B": B})

	lo := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")}, Options{Threshold: 50})
	if len(lo.Anomalies) == 0 {
		t.Fatal("阈值 50 应能抓到 +120")
	}
	hi := AnalyzeSeries(BuildSeries(samples), []Group{groupOf("A", "B")}, Options{Threshold: 800})
	if len(hi.Anomalies) != 0 {
		t.Fatal("阈值 800 不该抓到 +120")
	}
}