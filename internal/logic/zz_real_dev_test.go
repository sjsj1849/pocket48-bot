package logic

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"testing"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/signstat"
)

// splitLines 把 jsonl 拆成逐行字节切片。
//
// ★ jsonl 是「一连串顶层 JSON 对象」，没有数组包裹；
//
//	顶层调用 Decoder.More() 行为未定义（实测直接返回空）。
func splitLines(b []byte) [][]byte {
	out := [][]byte{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		out = append(out, cp)
	}
	return out
}

// ★ 2026-10-08 重写。
//
// 旧版TestDeviationPointsAtInjectedSpike 测的是
// signMonitorMaxDeviation + filterBelowCrossover 那条老链路，
// 而 filterBelowCrossover 的语义（破万 ⇒ 整条剔除）**本身就是错的**，
// 用户 2026-10-08 明确要求改成「曲线截断到破万点、只是不参与后续偏移计算」。
//
// 而且那个测试**在改动之前就已经是红的**（注入前峰值就打印 +0）：
// 真实数据里郑伊安 10-07 22:04破万、柳河岚 23:50 破万，
// 第一组 4 人里 2 人被 filterBelowCrossover 整条剔除 ⇒ 只剩 2 人，
// 注入到第 3 个人身上时它已经被剔掉了 ⇒ after == before == 0。
// 这不是代码坏了，是测试在验证一个错误的设计。
//
// 现在改为直接验证新口径（internal/signstat），并且**顺手验证真实数据**：
//  1. 破万者不被剔除，只是标注为 excluded（曲线仍在）
//  2. 跨日归零步不进统计
//  3. 连续尖峰合并成一次
func loadRealSignSamples(t *testing.T) []signstat.Sample {
	t.Helper()
	raw, err := os.ReadFile("../../storage/weibo/sign-monitor.jsonl")
	if err != nil {
		t.Skipf("读不到采样文件: %v", err)
	}
	out := []signstat.Sample{}
	// jsonl 是「一连串顶层 JSON 对象」，没有数组包裹 ⇒ 循环 Decode 到 EOF。
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var s signstat.Sample
		if err := dec.Decode(&s); err != nil {
			break
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		t.Skip("采样文件为空")
	}
	return out
}

func loadRealSignGroups(t *testing.T) []signstat.Group {
	t.Helper()
	raw, err := os.ReadFile("../../config.json")
	if err != nil {
		t.Skipf("读不到 config.json: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Skipf("配置解析失败: %v", err)
	}
	out := make([]signstat.Group, 0, len(cfg.WeiboSignMonitor.Groups))
	for _, g := range cfg.WeiboSignMonitor.Groups {
		out = append(out, signstat.Group{Name: g.Name, Members: g.Members})
	}
	if len(out) == 0 {
		t.Skip("没配分组")
	}
	return out
}

// 真实数据上跑一遍新口径，检查那些「只有真实数据才会暴露」的性质。
func TestSignStatOnRealSamples(t *testing.T) {
	samples := loadRealSignSamples(t)
	groups := loadRealSignGroups(t)

	res := signstat.AnalyzeSamples(samples, groups, signstat.Options{
		Threshold: signstat.DefaultThreshold,
	})

	if len(res.Groups) == 0 {
		t.Fatal("真实数据应至少出一组偏离")
	}

	// ★ 破万者必须**保留在序列里**（只是不进统计），不能整条消失。
	seriesByOID := map[string]signstat.Series{}
	for _, s := range res.Series {
		seriesByOID[s.OID] = s
	}
	crossedCount := 0
	for _, s := range res.Series {
		if !s.Crossed() {
			continue
		}
		crossedCount++
		if len(s.Points) == 0 {
			t.Fatalf("%s 已破万但整条序列被清空了", s.Name)
		}
		// 破万点必须在序列里（曲线画到破万点为止）
		found := false
		for _, p := range s.Points {
			if p.TS == s.CrossoverAt {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 的破万点不在序列里，曲线画不到破万时刻", s.Name)
		}
		// 参与统计的应是破万**之前**的精确段
		if len(s.AnalysisPoints()) >= len(s.Points) {
			t.Fatalf("%s 破万后仍参与统计（%d/%d 点）", s.Name, len(s.AnalysisPoints()), len(s.Points))
		}
	}
	t.Logf("真实数据中破万者%d 人，均已保留曲线且不参与后续偏移统计", crossedCount)

	// ★ 跨日归零步绝不能进统计。
	//
	// 实测每天 00:05 前后 8 个超话同时暴跌 3000~10500（签到数按天归零），
	// 这是数据固有特征不是异常。若不剔除，选「近 24 小时」及以上时
	// 这一步会以 -10000 量级把 Y 轴撑爆（正常波动只有 ±14）。
	for _, g := range res.Groups {
		for oid, pts := range g.Series {
			for _, p := range pts {
				if p.Dev < -2000 {
					t.Fatalf("%s/%s 出现 %d 的偏离 —— 跨日归零步没被剔除，Y 轴会被撑爆",
						g.GroupName, g.Names[oid], p.Dev)
				}
			}
		}
	}

	// ★ 偏离必定正负各半（基线算错的最好用红灯）。
	for _, g := range res.Groups {
		pos, neg := 0, 0
		for _, pts := range g.Series {
			for _, p := range pts {
				if p.Dev > 0 {
					pos++
				} else if p.Dev < 0 {
					neg++
				}
			}
		}
		if pos == 0 || neg == 0 {
			t.Fatalf("%s 偏离必须正负各半，实际 pos=%d neg=%d ⇒ 基线算错了",
				g.GroupName, pos, neg)
		}
		t.Logf("%s: %d 个采样点, 峰值 +%d/-%d", g.GroupName, len(g.Stamps), g.Bounds.Pos, g.Bounds.Neg)
	}
}

// 在真实数据上注入连续尖峰，验证「能被检出」且「连续点合并成一次」。
//
// ★ 断言用绝对值而非差值：真实数据本来就有真实异常（实测当天抓到 +564），
//
//	拿「真实数据必须干净」当基准必然不稳定（这个坑踩过）。
func TestSignStatDetectsInjectedSpikeOnRealData(t *testing.T) {
	samples := loadRealSignSamples(t)
	groups := loadRealSignGroups(t)

	before := signstat.AnalyzeSamples(samples, groups, signstat.Options{Threshold: signstat.DefaultThreshold})

	// ★ 注入目标必须**未破万**且在第一组：破万者不参与偏移统计，
	//   注到它身上等于没注入（破万过滤是设计行为）。
	first := groups[0]
	byOID := map[string]signstat.Series{}
	for _, s := range before.Series {
		byOID[s.OID] = s
	}
	targetOID := ""
	targetName := ""
	for _, oid := range first.Members {
		s, ok := byOID[oid]
		if ok && !s.Crossed() && len(s.Points) > 10 {
			targetOID, targetName = oid, s.Name
			break
		}
	}
	if targetOID == "" {
		t.Skip("第一组里没有未破万且数据足够的成员")
	}
	t.Logf("注入目标：%s（未破万）", targetName)

	// ★ 连续三步注入：刷量的真实形态是「连续几步冲高」，不是孤立单点。
	//   逐级累加（不是各点独立加同一个数）—— 否则第二步会比第一步矮，
	//   那不是台阶，是噪声。
	tsOf := map[int64]int{}
	for _, p := range byOID[targetOID].Points {
		tsOf[p.TS] = p.Sign
	}
	stamps := make([]int64, 0, len(tsOf))
	for ts := range tsOf {
		stamps = append(stamps, ts)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })
	mid := len(stamps) / 2
	// 找到 mid 之后连续 3 个采样时刻
	if mid+3 >= len(stamps) {
		t.Skip("采样点不足以注入")
	}
	// ★ 关键：连续刷量的正确建模是「**抬高原值，之后保持**」，
	//   不是在每一点独立加同一个数。
	//
	//   逐点独立加会互相抵消：给第 k 点加d，则它之后的每一步增量都变成
	//   「原增量 − d」。实测注入 400/700/300 变成 step=420/329/−380，
	//   第三步直接翻负 —— 断言方向全反。
	//
	//   真实刷量是「冲高之后维持在高位」（实测 ChoiJiwoo 22:09→22:24
	//   从 7883 一路抬到 9042），所以正确做法是给**前缀**累加，
	//   使每一步的增量都真的多涨了 d。
	deltas := []int{400, 700, 300}
	// ★ 注入点必须让注入后**仍不被判为破万**（2026-10-08 修）。
	//
	// 用例原本在 len(stamps)/2 处注入，而真实数据持续增长：
	// ChoiJiwoo 中段实测已 9532，注入 400+700+300 前缀累加后变成 10932
	// ⇒ 越过10000 被判为**已破万** ⇒ 破万序列被排除出组内检测
	// ⇒「注入的尖峰没被检出」。**这是真实数据的演进，不是生产逻辑回归**
	//（破万者本来就不参与组内离群检测）。
	//
	// 修法：往前找一个注入点，要求「注入点及其后**所有**被抬升的采样点
	// 都仍 < 破万线」。
	//   ★ 每个点抬升的是**前缀和**（400 / 1100 / 1400），不是总量 1400——
	//     算错这一点会把本该安全的点判成不安全。
	//   ★ 只检查注入点那一个是错的：前缀累加会把后面几个点一起抬高。
	injectAt := -1
	for cand := mid; cand >= 3; cand -= 3 {
		safe := true
		running := 0
		for k := 0; k < len(deltas) && cand+k < len(stamps); k++ {
			running += deltas[k]
			if tsOf[stamps[cand+k]]+running >= signstat.CrossoverSign {
				safe = false
				break
			}
		}
		if safe {
			injectAt = cand
			break
		}
	}
	if injectAt < 0 {
		t.Skipf("真实数据里找不到不会越过破万线的注入点（%s 全段已接近 %d）",
			targetName, signstat.CrossoverSign)
	}
	mid = injectAt
	t.Logf("注入点：index=%d 原值=%d 注入后=%d（破万线 %d，余量 %d）",
		mid, tsOf[stamps[mid]], tsOf[stamps[mid]]+sumInts(deltas),
		signstat.CrossoverSign, signstat.CrossoverSign-tsOf[stamps[mid]]-sumInts(deltas))

	firstTS := stamps[mid]
	running := 0
	for k, d := range deltas {
		running += d
		ts := stamps[mid+k]
		for i := range samples {
			if samples[i].OID == targetOID && samples[i].TS == ts {
				samples[i].Sign += running
			}
		}
	}

	after := signstat.AnalyzeSamples(samples, groups, signstat.Options{Threshold: signstat.DefaultThreshold})

	var hit *signstat.Anomaly
	for i := range after.Anomalies {
		a := after.Anomalies[i]
		if a.OID == targetOID && a.FromTS <= firstTS && a.ToTS >= firstTS {
			hit = &after.Anomalies[i]
			break
		}
	}
	if hit == nil {
		for _, a := range after.Anomalies {
			t.Logf("got: %s peak=%d total=%d steps=%d %d~%d",
				a.Name, a.PeakDelta, a.TotalDelta, a.Steps, a.FromTS, a.ToTS)
		}
		t.Fatalf("注入的连续尖峰（%s %d~%d）没被检出", targetName, firstTS, stamps[mid+2])
	}
	t.Logf("检出：%s peak=%d total=%d steps=%d severity=%s from=%d to=%d fromTs=%d toTs=%d",
		hit.Name, hit.PeakDelta, hit.TotalDelta, hit.Steps, hit.Severity,
		hit.From, hit.To, hit.FromTS, hit.ToTS)
	t.Logf("注入的三个时刻: %d %d %d (逐点增量 400/700/300)", stamps[mid], stamps[mid+1], stamps[mid+2])

	// 峰值必须覆盖注入的最大那一步（700）—— 用绝对值，不用差值
	if hit.PeakDelta < 600 {
		t.Errorf("峰值偏离应覆盖注入的最大单步(700)，实际 %d", hit.PeakDelta)
	}
	// ★ 连续三点必须合并成一次（这正是用户 22:09/22:14/22:19 那个场景）
	if hit.Steps < 3 {
		t.Errorf("连续 3 个采样点应合并为一次异常，实际 steps=%d（未合并）", hit.Steps)
	}
	// 累计增量应覆盖三步之和（400+700+300=1400）
	if hit.TotalDelta < 1200 {
		t.Errorf("累计增量应覆盖三步之和(1400)，实际 %d", hit.TotalDelta)
	}
	if hit.Severity != signstat.SeverityConfirmed {
		t.Errorf("精确数据上的离群应标为 confirmed，实际 %s", hit.Severity)
	}

	// ★ 破万者不该产生「确定异常」
	for _, a := range after.Anomalies {
		if byOID[a.OID].Crossed() && a.Severity == signstat.SeverityConfirmed {
			t.Errorf("%s 已破万却被判为确定异常：%+v", byOID[a.OID].Name, a)
		}
	}
}

func sumInts(v []int) int {
	n := 0
	for _, x := range v {
		n += x
	}
	return n
}
