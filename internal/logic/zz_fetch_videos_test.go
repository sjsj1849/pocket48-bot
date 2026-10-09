package logic

import (
	"context"
	"os"
	"testing"
	"time"

	"pocket48-bot/internal/bilibili"
)

// 探针：直接调生产函数 bilibiliFetchVideos，看它到底返回多少条。
//
// 日志里视频组显示「9 条」「0 条」，而分开测两个接口是 76 + 50 = 126 条。
// 差了一个数量级 —— 必须确认是不是合并逻辑把数据丢了。
func TestZZProbeFetchVideos(t *testing.T) {
	client := &bilibili.Client{Dir: t.TempDir(), Cookie: os.Getenv("BILI_COOKIE")}
	sub := bilibili.Subscription{UID: "3546824314980440", Name: "Hearts2Hearts"}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	items, err := bilibiliFetchVideos(ctx, client, sub)
	tz := time.FixedZone("CST", 8*3600)
	if err != nil {
		t.Logf("bilibiliFetchVideos 失败: %v", err)
	} else {
		t.Logf("★ bilibiliFetchVideos 返回 %d 条", len(items))
		newest := int64(0)
		for _, d := range items {
			if d.Time > newest {
				newest = d.Time
			}
		}
		t.Logf("  最新一条发布于 %s", time.UnixMilli(newest).In(tz).Format("15:04:05"))
		for i, d := range items {
			if i >= 5 {
				break
			}
			t.Logf("  [%d] kind=%-8s sec=%-5d time=%s id=%s", i, d.Kind, d.Seconds,
				time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.ID)
		}
	}
}
