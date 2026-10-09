package logic

import (
	"testing"

	"pocket48-bot/internal/config"
)

// 组内对比判据（2026-10-07）。
//
// 用户反馈「绝对阈值不合理」：八小妹 8 个超话当天签到从 3271 到 12000，
// 同一个阈值对 3000 档是暴涨、对 12000 档只是日常波动。
// 真正刺眼的是某条明显高于同组其他人。

func zzDelta(name string, delta int) signMonitorDelta {
	return signMonitorDelta{
		OID: "oid-" + name, Name: name,
		From: 8000, To: 8000 + delta, Delta: delta,
		WindowMin: 30, At: 1791300000000,
	}
}

func zzGroups() []config.WeiboSignMonitorGroup {
	return []config.WeiboSignMonitorGroup{
		{"第一组", []string{"Ian", "Jiwoo", "Stella", "Yuha"}},
		{"第二组", []string{"Ana", "JUUN", "补充"}},
	}
}

// 复刻线上：Jiwoo 半小时 +1500，同组其他人都只有 +100~400。
func TestDetectGroupSpikesFlagsOutlier(t *testing.T) {
	deltas := map[string]signMonitorDelta{
		"Ian": zzDelta("Ian", 200), "Jiwoo": zzDelta("Jiwoo", 1500),
		"Stella": zzDelta("Stella", 300), "Yuha": zzDelta("Yuha", 100),
		"Ana": zzDelta("Ana", 50), "JUUN": zzDelta("JUUN", 60), "补充": zzDelta("补充", 40),
	}
	got := detectGroupSpikes(deltas, zzGroups(), 2, 300)
	if len(got) != 1 {
		t.Fatalf("只应报 Jiwoo 一条，实际 %d 条：%+v", len(got), got)
	}
	if got[0].Name != "Jiwoo" {
		t.Errorf("报错了对象：%s", got[0].Name)
	}
	if got[0].GroupName != "第一组" {
		t.Errorf("组名不对：%s", got[0].GroupName)
	}
	// 中位数 = (200+300)/2 = 250（含全部成员，不剔除疑似异常那条），
	// 阈值 = max(250*2, 300) = 500 ⇒ 1500 命中，300 不命中。
	if got[0].GroupMedian != 250 {
		t.Errorf("中位数应为 250，实际 %d", got[0].GroupMedian)
	}
	if got[0].GroupThreshold != 500 {
		t.Errorf("阈值应为 500，实际 %d", got[0].GroupThreshold)
	}
}

// 全组平稳时不报（哪怕绝对值都不小）。
func TestDetectGroupSpikesQuietWhenUniform(t *testing.T) {
	deltas := map[string]signMonitorDelta{
		"Ian": zzDelta("Ian", 300), "Jiwoo": zzDelta("Jiwoo", 320),
		"Stella": zzDelta("Stella", 280), "Yuha": zzDelta("Yuha", 310),
	}
	if got := detectGroupSpikes(deltas, zzGroups(), 2, 300); len(got) != 0 {
		t.Errorf("全组平稳不该报警：%+v", got)
	}
}

// 小组（<3 人）不做组内判据：中位数没有意义。
func TestDetectGroupSpikesSkipsTinyGroups(t *testing.T) {
	deltas := map[string]signMonitorDelta{
		"Ana": zzDelta("Ana", 50), "JUUN": zzDelta("JUUN", 5000),
	}
	groups := []config.WeiboSignMonitorGroup{{"第二组", []string{"Ana", "JUUN"}}}
	if got := detectGroupSpikes(deltas, groups, 2, 300); len(got) != 0 {
		t.Errorf("2 人组不该做组内判据：%+v", got)
	}
}

// 下跌不参与组内判据（中位数被跌的拉低会误报别人）。
func TestDetectGroupSpikesIgnoresDrop(t *testing.T) {
	deltas := map[string]signMonitorDelta{
		"Ian": zzDelta("Ian", -500), "Jiwoo": zzDelta("Jiwoo", 400),
		"Stella": zzDelta("Stella", 380), "Yuha": zzDelta("Yuha", -200),
	}
	got := detectGroupSpikes(deltas, zzGroups(), 2, 300)
	for _, spike := range got {
		if spike.Name == "Ian" || spike.Name == "Yuha" {
			t.Errorf("下跌不该被报出来：%+v", spike)
		}
	}
}

func TestMedianInt(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{5}, 5},
		{[]int{3, 1, 2}, 2},
		{[]int{4, 1, 3, 2}, 2}, // (2+3)/2 = 2（整除）
		{[]int{10, 10, 10}, 10},
	}
	for _, c := range cases {
		if got := medianInt(c.in); got != c.want {
			t.Errorf("medianInt(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// windowDelta 的基准必须在窗口外。
func TestWindowDeltaBaselineOutsideWindow(t *testing.T) {
	list := []signMonitorSample{
		zzAt(120, 9000), zzAt(25, 5000), zzAt(5, 5200),
	}
	base, latest, ok := windowDelta(list, 30)
	if !ok {
		t.Fatal("应能算出窗口涨幅")
	}
	if base.Sign != 9000 || latest.Sign != 5200 {
		t.Errorf("基准应取窗口外的 9000，实际 %d → %d", base.Sign, latest.Sign)
	}

	// 没有窗口外基准点时放弃（而不是拿窗口内第一个点当基准）
	short := []signMonitorSample{zzAt(10, 100), zzAt(5, 9000)}
	if _, _, ok := windowDelta(short, 30); ok {
		t.Error("没有窗口外基准时不该给出结论")
	}
}
