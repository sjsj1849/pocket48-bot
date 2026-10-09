package logic

import (
	"context"
	"testing"
	"time"

	"pocket48-bot/internal/weverse"
)

func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func contextTODO() context.Context { return context.Background() }

// ★ 按需同步策略（2026-10-08 用户定稿）：
//
//	有待解锁帖 → 每 20 小时打一次来源站
//	无待解锁帖 → 压根不打，只每 24 小时**本地**查一次
func TestZZPasswordSyncIdleVsActive(t *testing.T) {
	if weversePasswordSyncActiveInterval <= 0 || weversePasswordSyncIdleCheckInterval <= 0 {
		t.Fatal("两个间隔都必须为正")
	}
	// 高频间隔必须明显短于空闲检查间隔，否则「按需」没意义
	if weversePasswordSyncActiveInterval >= weversePasswordSyncIdleCheckInterval {
		t.Fatalf("高频间隔(%s)应短于空闲检查间隔(%s)",
			weversePasswordSyncActiveInterval, weversePasswordSyncIdleCheckInterval)
	}
	// 高频间隔应当「接近一天」而不是几小时：来源站更新频率未知，
	// 打太频繁没收益，打太少赶不上新帖。
	if d := weversePasswordSyncActiveInterval; d < 12*time.Hour || d > 24*time.Hour {
		t.Fatalf("高频间隔应在 12~24 小时之间，实际 %s", d)
	}
	t.Logf("高频=%s 空闲检查=%s 失败重试=%s",
		weversePasswordSyncActiveInterval, weversePasswordSyncIdleCheckInterval,
		weversePasswordSyncRetryInterval)
}

// ★「有没有待解锁帖」必须纯本地判断，绝不产生外部请求。
func TestZZPendingCountIsLocalOnly(t *testing.T) {
	dir := t.TempDir()
	// 目录里什么都没有 ⇒ 读不到社区/密码/历史 ⇒ 应返回 0 且不 panic
	if n := weversePendingLockedCount(dir); n != 0 {
		t.Fatalf("空目录应返回 0，实际 %d", n)
	}
}

// ★ sleepCtx 必须在 ctx 取消时立刻返回 false（否则重启会卡住 goroutine）。
func TestZZSleepCtxCancels(t *testing.T) {
	ctx, cancel := contextWithTimeout(50 * time.Millisecond)
	defer cancel()
	start := time.Now()
	if sleepCtx(ctx, 10*time.Second) {
		t.Fatal("ctx 取消后应返回 false")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("取消后应立即返回，实际耗时 %s", el)
	}
	// 正常路径：0 与正数都应立刻返回 true
	if !sleepCtx(contextTODO(), 0) {
		t.Fatal("d<=0 应返回 true")
	}
	if !sleepCtx(contextTODO(), time.Millisecond) {
		t.Fatal("正常等待应返回 true")
	}
	_ = weverse.LoadPostPasswords
}
