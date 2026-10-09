package bilibili

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 探针：第三个数据源 opus/feed（动态），我们已经在用它抓动态了。
// 如果视频发布也会进动态流，而动态流的收录更快，那就是答案。
func TestZZProbeOpus(t *testing.T) {
	c := &Client{Dir: t.TempDir(), Cookie: os.Getenv("BILI_COOKIE")}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	items, err := c.SpaceOpus(ctx, "3546824314980440")
	if err != nil {
		t.Logf("opus 失败: %v", err)
		return
	}
	tz := time.FixedZone("CST", 8*3600)
	t.Logf("opus 条数 = %d", len(items))
	for _, d := range items {
		if strings.Contains(d.ID, "BV1jHH46bExa") || strings.Contains(d.URL, "BV1jHH46bExa") ||
			strings.Contains(d.Text, "NIC") || strings.Contains(d.Title, "NIC") {
			t.Logf("★ opus 里找到：kind=%s time=%s title=%q id=%s",
				d.Kind, time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.Title, d.ID)
		}
	}
	// 打印前5 条看收录时间分布
	for i, d := range items {
		if i >= 5 {
			break
		}
		t.Logf("  [%d] kind=%-8s time=%s id=%s", i, d.Kind,
			time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.ID)
	}
}
