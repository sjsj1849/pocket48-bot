package logic

import (
	"strings"
	"testing"
)

// zzSeriesLinear 造一条线性序列（起点 start，每步 step，共 n 点）。
func zzSeriesLinear(oid, name string, start int64, n int, step int64) signMonitorSeries {
	pts := make([][2]int64, 0, n)
	base := timeAt(0)
	for i := 0; i < n; i++ {
		pts = append(pts, [2]int64{base + int64(i)*300_000, start + int64(i)*step})
	}
	return signMonitorSeries{OID: oid, Name: name, Points: pts}
}

func timeAt(i int) int64 {
	return int64(1759804800000) + int64(i)*300_000 // 固定基准，避免依赖时区
}

// ★ 破万过滤后的成员数决定口径（用户 14:48 明确规则）：
//
//	≥3 人 → 偏离图，基线 = 组内中位数
//	 2 人 → **偏离图，基线 = 对方（差值口径）** ← 不是「不画图」
//	 1 人 → 不画图，但必须标注截止时间 + 原因
func TestValidCountAfterCrossoverDecidesMode(t *testing.T) {
	// 5 人，全未破万 ⇒ 5 人偏离图
	five := []signMonitorSeries{
		zzSeriesLinear("1", "A", 5000, 20, 50),
		zzSeriesLinear("2", "B", 5000, 20, 45),
		zzSeriesLinear("3", "C", 5000, 20, 40),
		zzSeriesLinear("4", "D", 5000, 20, 35),
		zzSeriesLinear("5", "E", 5000, 20, 30),
	}
	if svg := buildSignMonitorDeviationSVG(five, "T", "S", 760, 380, 800); svg == "" {
		t.Error("5 人应能画偏离图")
	}

	// 4 人，其中 1 个破万 ⇒ 剩 3 人 ⇒ 仍画偏离图
	threeValid := append(append([]signMonitorSeries{}, five[:3]...),
		zzSeriesLinear("x", "已破万", 10000, 20, 1))
	if got := filterBelowCrossover(threeValid); len(got) != 3 {
		t.Fatalf("应剩 3 个有效成员，实际 %d", len(got))
	}
	if svg := buildSignMonitorDeviationSVG(threeValid, "T", "S", 760, 380, 800); svg == "" {
		t.Error("破万 1 个后剩 3 人，应能画偏离图")
	}

	// 4 人，其中 2 个破万 ⇒ 剩 2 人 ⇒ **仍要画图**（差值口径）
	twoValid := append(append([]signMonitorSeries{}, five[:2]...),
		zzSeriesLinear("x", "破1", 10000, 20, 1),
		zzSeriesLinear("y", "破2", 10050, 20, 1))
	if got := filterBelowCrossover(twoValid); len(got) != 2 {
		t.Fatalf("应剩 2 个有效成员，实际 %d", len(got))
	}
	svg2 := buildSignMonitorDeviationSVG(twoValid, "T", "S", 760, 380, 800)
	if svg2 == "" {
		t.Fatal("★ 剩 2 人必须仍画偏离图（差值口径）—— 用户 14:48 明确「两个人的时候就要变成差值」")
	}
	// 差值口径下两条线互为相反数（基线各取对方）
	if b := pairDevBounds(filterBelowCrossover(twoValid)); b.Pos == 0 || b.Neg == 0 {
		t.Errorf("两人组 Pos/Neg 都应非零，实际 Pos=%d Neg=%d", b.Pos, b.Neg)
	}

	// 4 人，其中 3 个破万⇒ 剩 1 人 ⇒ 不画图
	oneValid := append([]signMonitorSeries{zzSeriesLinear("1", "唯一有效", 5000, 20, 50)},
		zzSeriesLinear("x", "破1", 10000, 20, 1),
		zzSeriesLinear("y", "破2", 10050, 20, 1),
		zzSeriesLinear("z", "破3", 10100, 20, 1))
	if got := filterBelowCrossover(oneValid); len(got) != 1 {
		t.Fatalf("应剩 1 个有效成员，实际 %d", len(got))
	}
	if svg := buildSignMonitorDeviationSVG(oneValid, "T", "S", 760, 380, 800); svg != "" {
		t.Error("★ 只剩 1 人时必须不画图（用户 14:48「只剩一个人了才不继续画图」）")
	}
}

// 1 人时的说明必须含**截止时间**和**原因**（用户 14:48 明确要求）。
func TestCutoffNoteHelperProvidesTime(t *testing.T) {
	// 直接验证 buildCrossoverCutoffNote 的输出（日报与面板共用同一套措辞）
	valid := []signMonitorSeries{zzSeriesLinear("a", "唯一", 5000, 20, 50)}
	note := buildCrossoverCutoffNote("第一组（万档）", valid, 3, "IAN", int64(1759804800000+300))
	if note == "" {
		t.Fatal("必须生成说明文案")
	}
	// 含组名
	if !strings.Contains(note, "第一组（万档）") {
		t.Errorf("说明应含组名，实际 %q", note)
	}
	// 含破万数量与名字
	if !strings.Contains(note, "3") || !strings.Contains(note, "IAN") {
		t.Errorf("说明应含破万数量与名字，实际 %q", note)
	}
	// 含截止时间（带日期或时间格式）
	if !strings.Contains(note, ":") {
		t.Errorf("说明应含截止时间，实际 %q", note)
	}
	t.Logf("说明文案：%s", note)
}
