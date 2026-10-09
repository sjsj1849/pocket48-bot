package xmonitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPruneUserCacheDropsExpiredAndEmpty(t *testing.T) {
	now := time.Now()
	cache := map[string]UserCacheEntry{
		"alive":   {User: User{ID: "1", Username: "alive"}, ExpiresAt: now.Add(time.Hour)},
		"expired": {User: User{ID: "2", Username: "expired"}, ExpiresAt: now.Add(-time.Minute)},
		"noid":    {User: User{Username: "noid"}, ExpiresAt: now.Add(time.Hour)},
		"zeroexp": {User: User{ID: "4", Username: "zeroexp"}},
	}
	got := PruneUserCache(cache, 0)
	if _, ok := got["expired"]; ok {
		t.Fatal("过期条目必须被丢弃")
	}
	if _, ok := got["noid"]; ok {
		t.Fatal("缺少 ID 的条目必须被丢弃")
	}
	if _, ok := got["zeroexp"]; !ok {
		t.Fatal("ExpiresAt 为零值表示不过期，应保留")
	}
	if _, ok := got["alive"]; !ok {
		t.Fatal("未过期条目必须保留")
	}
}

func TestPruneUserCacheRespectsLimit(t *testing.T) {
	now := time.Now()
	cache := map[string]UserCacheEntry{}
	for i := 0; i < 10; i++ {
		key := string(rune('a' + i))
		cache[key] = UserCacheEntry{
			User:      User{ID: key, Username: key},
			ExpiresAt: now.Add(time.Duration(i) * time.Minute),
		}
	}
	got := PruneUserCache(cache, 3)
	if len(got) != 3 {
		t.Fatalf("期望保留 3 条，实际 %d", len(got))
	}
	// 保留的是 ExpiresAt 最晚的 3 条：i = 9,8,7
	for _, k := range []string{"j", "i", "h"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("应保留最近写入的 %s", k)
		}
	}
	if _, ok := got["a"]; ok {
		t.Fatal("最旧的条目必须被裁掉")
	}
}

func TestPruneUserCacheReturnsNilWhenEmpty(t *testing.T) {
	if got := PruneUserCache(nil, 10); got != nil {
		t.Fatal("空输入应返回 nil，配合 omitempty 省略该键")
	}
	if got := PruneUserCache(map[string]UserCacheEntry{}, 10); got != nil {
		t.Fatal("空 map 应返回 nil")
	}
}

// 老版本 state.json 没有 userCache 键，必须能正常读出且不报错。
func TestRuntimeReadsLegacyStateWithoutUserCache(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"subscriptions":{"a":{"username":"x","userId":"1","ready":true,"seen":["2"],"highWaterId":"2"}}}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	var rt Runtime
	if err := Read(dir, "state.json", &rt); err != nil {
		t.Fatalf("老版本 state.json 读取失败: %v", err)
	}
	if rt.UserCache != nil {
		t.Fatal("老版本没有 userCache，应为 nil")
	}
	if len(rt.Subscriptions) != 1 {
		t.Fatalf("订阅数据丢失: %+v", rt.Subscriptions)
	}
}

// 核心回归：缓存写入 state.json 后重启能被复用，即不再调用 UserByScreenName。
func TestUserCacheSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().Add(time.Hour)
	rt := Runtime{
		Subscriptions: map[string]Cursor{},
		UserCache: map[string]UserCacheEntry{
			"nekomo_st": {
				User:      User{ID: "1968494047598886912", Username: "nekomo_st"},
				ExpiresAt: future,
			},
		},
	}
	if err := Write(dir, "state.json", rt); err != nil {
		t.Fatal(err)
	}

	// 模拟进程重启
	var again Runtime
	if err := Read(dir, "state.json", &again); err != nil {
		t.Fatal(err)
	}
	got, ok := again.UserCache["nekomo_st"]
	if !ok {
		t.Fatalf("重启后缓存丢失: %+v", again.UserCache)
	}
	if got.User.ID != "1968494047598886912" {
		t.Fatalf("缓存的 user ID 不正确: %q", got.User.ID)
	}
	if time.Until(got.ExpiresAt) <= 0 {
		t.Fatal("过期时间未保留")
	}
}

// 空缓存不应写出 userCache 键，避免污染既有 state.json。
func TestRuntimeOmitsEmptyUserCache(t *testing.T) {
	dir := t.TempDir()
	rt := Runtime{Subscriptions: map[string]Cursor{}}
	rt.UserCache = PruneUserCache(nil, 64)
	if err := Write(dir, "state.json", rt); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "userCache") {
		t.Fatalf("空缓存不应写入 userCache 键: %s", raw)
	}
}
