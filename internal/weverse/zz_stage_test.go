package weverse

import (
	"context"
	"testing"
	"time"
)

// TestZZStage 临时：测量 Events 各阶段真实耗时（含 LiveEndEvents）。验证完即删。
func TestZZStage(t *testing.T) {
	dir := Dir("/root/pocket48-bot/config.json")
	cfg, _ := LoadSettings(dir)
	c := NewClient(dir, cfg.ProxyURL)
	defer c.HTTP.CloseIdleConnections()
	// 复用同一 client，模拟生产的缓存效果
	_, _ = c.Members(context.Background(), 235)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var st Status
	_ = Read(dir, "status.json", &st)
	since, _ := time.Parse(time.RFC3339, st.LastSuccess)
	if !since.IsZero() {
		since = since.Add(-5 * time.Minute)
	}

	t0 := time.Now()
	events, err := c.Events(ctx)
	t.Logf("Events 总耗时 = %v  事件=%d err=%v", time.Since(t0).Round(time.Millisecond), len(events), err)

	var rt Runtime
	_ = Read(dir, "state.json", &rt)
	t1 := time.Now()
	ends, err := c.LiveEndEvents(ctx, &rt, cfg, events, time.Now())
	t.Logf("LiveEndEvents 耗时 = %v  新增=%d err=%v", time.Since(t1).Round(time.Millisecond), len(ends), err)

	t2 := time.Now()
	m2, _ := c.Members(ctx, 235)
	t.Logf("Members(缓存命中) 耗时 = %v 成员=%d", time.Since(t2).Round(time.Millisecond), len(m2))
}
