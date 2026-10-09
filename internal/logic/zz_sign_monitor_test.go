package logic

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 超话签到监测（2026-10-07）。
//
// 关注点是「一天之内签到突然多了一截」——日报只有一天一个点，看不到这个；
// 监测的唯一价值就是把这个尖峰抓出来，所以涨幅检测必须准确。

func zzSamples(spikes ...signMonitorSample) []signMonitorSample {
	return spikes
}

func zzAt(minutesAgo int, sign int) signMonitorSample {
	return signMonitorSample{
		TS:   time.Now().Add(-time.Duration(minutesAgo) * time.Minute).UnixMilli(),
		OID:  "oid-1",
		Name: "ChoiJiwoo",
		Sign: sign,
	}
}

// 半小时涨 1500（用户实际观察到的现象）必须报出来。
func TestDetectSpikesCatchesSuddenJump(t *testing.T) {
	list := zzSamples(zzAt(60, 8200), zzAt(45, 8250), zzAt(30, 8300), zzAt(5, 9800))
	got := detectSpikes(list, 30, 800, 0.25)
	if len(got) != 1 {
		t.Fatalf("应报出 1 条异常，实际 %d 条：%+v", len(got), got)
	}
	// 基准是「窗口起点(35 分钟前)之前最后一个采样点」= 45 分钟前的 8250，
	// 不是窗口内的 8300 —— 这正是 detectSpikes 基准选取的语义。
	if got[0].From != 8250 || got[0].To != 9800 {
		t.Errorf("基准/终点不对：%d → %d", got[0].From, got[0].To)
	}
	if got[0].Delta != 1550 {
		t.Errorf("涨幅应为 1550，实际 %d", got[0].Delta)
	}
}

// 正常粉丝增长（半小时 +200）不该报。
func TestDetectSpikesIgnoresNormalGrowth(t *testing.T) {
	list := zzSamples(zzAt(60, 8200), zzAt(30, 8300), zzAt(5, 8500))
	if got := detectSpikes(list, 30, 800, 0.25); len(got) != 0 {
		t.Errorf("半小时 +200 属正常增长，不该报警：%+v", got)
	}
}

// 小基数超话按相对涨幅判：300 → 500（+67%）即使绝对值不到 800 也要报。
func TestDetectSpikesUsesRatioForSmallCounts(t *testing.T) {
	list := zzSamples(zzAt(60, 300), zzAt(20, 500))
	got := detectSpikes(list, 30, 800, 0.25)
	if len(got) != 1 {
		t.Fatalf("小基数高比例应报警，实际 %+v", got)
	}
	if got[0].Delta != 200 {
		t.Errorf("Delta 应为 200，实际 %d", got[0].Delta)
	}
}

// 下跌不报警（这是「水分」监测，不是「掉粉」监测）。
func TestDetectSpikesIgnoresDrop(t *testing.T) {
	list := zzSamples(zzAt(60, 9800), zzAt(5, 8200))
	if got := detectSpikes(list, 30, 800, 0.25); len(got) != 0 {
		t.Errorf("下跌不该报警：%+v", got)
	}
}

// 基准点取「窗口起点之前最后一个」，不能取窗口内最小值 ——
// 否则「先跌到 5000 再涨回 8300」会被误判成暴涨。
func TestDetectSpikesBaselineIsOutsideWindow(t *testing.T) {
	list := zzSamples(zzAt(120, 9000), zzAt(25, 5000), zzAt(5, 5200))
	if got := detectSpikes(list, 30, 800, 0.25); len(got) != 0 {
		t.Errorf("窗口内下跌后小反弹不该报警：%+v", got)
	}
}

// 窗口内没有更早的点时无从比较，放行。
func TestDetectSpikesNeedsBaseline(t *testing.T) {
	list := zzSamples(zzAt(10, 100), zzAt(5, 9000))
	if got := detectSpikes(list, 30, 800, 0.25); len(got) != 0 {
		t.Errorf("没有基准点时不该报警：%+v", got)
	}
}

func TestSignMonitorStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sign-monitor.jsonl")
	store := newSignMonitorStore(path, 72*time.Hour)
	base := time.Now().UnixMilli()
	store.append([]signMonitorSample{
		{TS: base - 60000, OID: "oid-1", Name: "ChoiJiwoo", Sign: 100},
		{TS: base - 30000, OID: "oid-1", Name: "ChoiJiwoo", Sign: 160},
		{TS: base - 30000, OID: "oid-2", Name: "stella", Sign: 900},
	})
	if len(store.samples) != 3 {
		t.Fatalf("内存应有 3 条，实际 %d", store.Len())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应落盘：%v", err)
	}

	// 重启后能读回来
	reloaded := newSignMonitorStore(path, 72*time.Hour)
	if err := reloaded.load(); err != nil {
		t.Fatalf("load 失败: %v", err)
	}
	if len(reloaded.samples) != 3 {
		t.Fatalf("重启后应有 3 条，实际 %d", reloaded.Len())
	}

	series, _ := reloaded.series(base - 60000)
	if len(series) != 2 {
		t.Fatalf("应有 2 条序列（按 oid 聚合），实际 %d", len(series))
	}
	// 名字取最新非空
	for _, s := range series {
		if s.Name == "" {
			t.Errorf("序列 %s 缺名字", s.OID)
		}
	}
}

// 超过保留期的采样要被裁掉，文件不会无限增长。
func TestSignMonitorStorePrunesOldSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sign-monitor.jsonl")
	store := newSignMonitorStore(path, time.Hour)
	old := time.Now().Add(-3 * time.Hour).UnixMilli()
	store.append([]signMonitorSample{
		{TS: old, OID: "oid-old", Name: "旧的", Sign: 1},
		{TS: time.Now().UnixMilli(), OID: "oid-new", Name: "新的", Sign: 2},
	})
	if len(store.samples) != 1 {
		t.Fatalf("过期样本应被裁掉，实际剩 %d 条", store.Len())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 && contains(string(body), "oid-old") {
		t.Error("落盘文件里也不该留过期样本")
	}
}

// 同一尖峰在冷却期内只告一次。
func TestSignMonitorStoreSpikeCooldown(t *testing.T) {
	store := newSignMonitorStore("", 72*time.Hour)
	spike := signMonitorSpike{OID: "oid-1", Name: "ChoiJiwoo", From: 8300, To: 9800, Delta: 1500, At: time.Now().UnixMilli()}
	if alert, upd := store.recordSpike(spike, time.Hour, 0); !alert || upd {
		t.Fatalf("首次应告警（且不是补发），got alert=%v update=%v", alert, upd)
	}
	if alert, _ := store.recordSpike(spike, time.Hour, 0); alert {
		t.Error("同一段没有任何新数据时不该再告")
	}
	// ★ 段被延长（又涨了一截并合并进来）⇒ 必须补发一次更新的表
	grown := spike
	grown.To = 10800
	grown.Delta = 2500
	grown.ToTS = spike.ToTS + 5*60*1000
	grown.Steps = 2
	if alert, upd := store.recordSpike(grown, time.Hour, 0); !alert || !upd {
		t.Errorf("同一段延长后应补发，got alert=%v update=%v", alert, upd)
	}
	// 换一段（不同起始时刻）应视为新异常
	down := spike
	down.FromTS = 10 * 60 * 1000
	down.ToTS = down.FromTS + 5*60*1000
	down.Delta = -900
	if alert, upd := store.recordSpike(down, time.Hour, 0); !alert || upd {
		t.Errorf("不同起始时刻应视为另一段异常，got alert=%v update=%v", alert, upd)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
