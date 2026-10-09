package bilibili

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 探针：验证 B 站视频接口的**收录延迟**。
//
// 用户报「B 站这条 20:45 才发出来，明明 20:30 就投稿了，轮询 60 秒怎么这么慢」。
// 两种可能：
//
//	① 我们的轮询不够密
//	② **B 站接口本身晚收录**（投稿后过一段时间才出现在列表里）
//
// 若是 ②，压间隔完全无效 —— 必须是实测才能区分。
func TestZZProbePublishLatency(t *testing.T) {
	c := &Client{Dir: t.TempDir(), Cookie: os.Getenv("BILI_COOKIE")}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	const uid = "3546824314980440"
	const wantBVID = "BV1jHH46bExa"
	tz := time.FixedZone("CST", 8*3600)

	for round := 1; round <= 3; round++ {
		items, err := c.SpaceVideos(ctx, uid)
		if err != nil {
			t.Logf("round %d: SpaceVideos 失败: %v", round, err)
		} else {
			found := false
			newest := int64(0)
			for _, d := range items {
				if d.Time > newest {
					newest = d.Time
				}
				if strings.Contains(d.ID, wantBVID) || strings.Contains(d.URL, wantBVID) {
					found = true
					t.Logf("round %d: ★ 目标视频 title=%q time=%s seconds=%d id=%s",
						round, d.Title,
						time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.Seconds, d.ID)
				}
			}
			t.Logf("round %d: %d 条，最新一条发布于 %s，目标已出现=%v",
				round, len(items), time.UnixMilli(newest).In(tz).Format("15:04:05"), found)
		}
		if round < 3 {
			time.Sleep(20 * time.Second)
		}
	}
}
