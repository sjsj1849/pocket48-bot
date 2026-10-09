package weverse

import (
	"testing"
	"time"
)

// TestDetailCacheHit 验证缓存的命中/未命中行为。
func TestDetailCacheHit(t *testing.T) {
	c := newDetailCache()
	c.put("post:1-2", Object{"body": "x"})
	if _, ok := c.get("post:1-2", time.Minute); !ok {
		t.Fatal("刚写入的条目应命中")
	}
	if _, ok := c.get("post:missing", time.Minute); ok {
		t.Fatal("不存在的键不应命中")
	}
}

// TestDetailCacheTTL 验证过期条目不再命中。
func TestDetailCacheTTL(t *testing.T) {
	c := newDetailCache()
	c.put("post:1", Object{"body": "x"})
	if _, ok := c.get("post:1", time.Nanosecond); ok {
		t.Fatal("TTL 过期的条目不应命中")
	}
}

// TestDetailCacheEvict 验证超出上限后按插入顺序淘汰最旧条目，避免无界增长。
func TestDetailCacheEvict(t *testing.T) {
	d := newDetailCache()
	for i := 0; i < detailCacheMax+10; i++ {
		d.put("post:"+string(rune('a'+i%26))+string(rune('a'+i/26)), Object{})
	}
	if len(d.entries) > detailCacheMax {
		t.Fatalf("缓存条目数 %d 超过上限 %d", len(d.entries), detailCacheMax)
	}
	if len(d.order) > detailCacheMax {
		t.Fatalf("淘汰队列长度 %d 超过上限 %d", len(d.order), detailCacheMax)
	}
}
