package logic

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 复现行不通的两件事：
//
//	① 同一段异常被延长后必须补发（用户：第二次要把两次都囊括进去）
//	② 重启后不能把历史异常再发一遍
func TestZZRecordSpikeFlow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sign-monitor.jsonl")
	store := newSignMonitorStore(path, 72*time.Hour)
	if store.alertsPath == "" {
		t.Fatalf("alertsPath empty")
	}
	if err := store.loadAlerts(); err != nil {
		t.Fatal(err)
	}
	base := signMonitorSpike{
		OID: "oid-1", Name: "ChoiJiwoo", From: 7883, To: 8274, Delta: 391,
		FromTS: 1760000000000, ToTS: 1760000000000 + 5*60*1000, Steps: 1,
	}

	alert, upd := store.recordSpike(base, 60*time.Minute, 0)
	fmt.Printf("step1: alert=%v update=%v (期望 true/false)\n", alert, upd)
	if !alert || upd {
		t.Fatalf("首轮应当告警且不是补发")
	}

	// 第二轮：22:14 那一步被合并进来 —— 必须补发，且窗口要覆盖整段
	grown := base
	grown.To = 8888
	grown.Delta = 1005
	grown.ToTS = base.ToTS + 5*60*1000
	grown.Steps = 2
	alert, upd = store.recordSpike(grown, 60*time.Minute, 0)
	fmt.Printf("step2: alert=%v update=%v (期望 true/true)\n", alert, upd)
	if !alert || !upd {
		t.Fatalf("段被延长后必须补发")
	}

	// 第三轮：没有任何变化 —— 不该再发
	alert, upd = store.recordSpike(grown, 60*time.Minute, 0)
	fmt.Printf("step3: alert=%v update=%v (期望 false/false)\n", alert, upd)
	if alert {
		t.Fatalf("没有新数据就不该再推")
	}

	// 上限：连续多次延长也不能无限推
	pushed := 1
	for i := 0; i < 10; i++ {
		g2 := grown
		g2.To += 10
		g2.Delta += 10
		g2.Steps++
		g2.ToTS += 5 * 60 * 1000
		a, _ := store.recordSpike(g2, 60*time.Minute, 0)
		if a {
			pushed++
		}
		grown = g2
	}
	fmt.Printf("补发上限验证：总计推送 %d 次（含首轮，上限=%d）\n", pushed, maxAlertsPerSpike)
	if pushed > maxAlertsPerSpike {
		t.Fatalf("突破补发上限")
	}

	// ---- 重启模拟：新建 store + loadAlerts，历史异常必须被认出来 ----
	store2 := newSignMonitorStore(path, 72*time.Hour)
	if err := store2.loadAlerts(); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("重启后读到已告警异常 %d 条\n", len(store2.alerts))
	if !store2.hasAlerted("oid-1", base.FromTS) {
		t.Fatalf("重启后应当认出这条已经发过的异常")
	}
	if store2.hasAlerted("oid-1", base.FromTS+1) {
		t.Fatalf("不同起始时刻不该被认成同一条")
	}
	if _, err := os.Stat(store2.alertsPath); err != nil {
		t.Fatalf("告警索引没有落盘: %v", err)
	}
	raw, _ := os.ReadFile(store2.alertsPath)
	fmt.Printf("索引文件内容 %d 字节\n", len(raw))
}
