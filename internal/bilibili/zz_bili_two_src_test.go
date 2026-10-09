package bilibili

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// 探针：分别测**合集接口**与**投稿接口**的收录速度。
//
// 用户质疑：「16 分钟延迟不正常，页面上明明能看见，
// 而且那条视频明明在合集里，为什么合集接口也没更快识别到？」
//
// 所以必须分两个来源单独实测，不能只看合并后的结果。
func TestZZProbeBothSources(t *testing.T) {
	c := &Client{Dir: t.TempDir(), Cookie: os.Getenv("BILI_COOKIE")}
	const uid = "3546824314980440"
	const wantBVID = "BV1jHH46bExa" // 20:30 发布的那条
	tz := time.FixedZone("CST", 8*3600)

	// 来源 1：合集
	ctx1, cancel1 := context.WithTimeout(context.Background(), 40*time.Second)
	seasons, err1 := c.SpaceSeasonVideos(ctx1, uid, 0)
	cancel1()
	if err1 != nil {
		t.Logf("合集接口失败: %v", err1)
	} else {
		found := false
		newest := int64(0)
		for _, d := range seasons {
			if d.Time > newest {
				newest = d.Time
			}
			if strings.Contains(d.ID, wantBVID) || strings.Contains(d.URL, wantBVID) {
				found = true
				t.Logf("★ 合集里找到：title=%q time=%s seconds=%d id=%s",
					d.Title, time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.Seconds, d.ID)
			}
		}
		t.Logf("合集：%d 条，最新发布 %s，目标存在=%v",
			len(seasons), time.UnixMilli(newest).In(tz).Format("15:04:05"), found)
	}

	// 来源 2：投稿列表
	ctx2, cancel2 := context.WithTimeout(context.Background(), 40*time.Second)
	posts, err2 := c.SpaceVideos(ctx2, uid)
	cancel2()
	if err2 != nil {
		t.Logf("投稿接口失败: %v", err2)
	} else {
		found := false
		newest := int64(0)
		for _, d := range posts {
			if d.Time > newest {
				newest = d.Time
			}
			if strings.Contains(d.ID, wantBVID) || strings.Contains(d.URL, wantBVID) {
				found = true
				t.Logf("★ 投稿列表里找到：title=%q time=%s seconds=%d id=%s",
					d.Title, time.UnixMilli(d.Time).In(tz).Format("15:04:05"), d.Seconds, d.ID)
			}
		}
		t.Logf("投稿列表：%d 条，最新发布 %s，目标存在=%v",
			len(posts), time.UnixMilli(newest).In(tz).Format("15:04:05"), found)
	}
}

// 测 navnum（投稿总数）：用它和实际列表条数的差可以判断是否漏收
func TestZZProbeNavnum(t *testing.T) {
	c := &Client{Dir: t.TempDir(), Cookie: os.Getenv("BILI_COOKIE")}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := c.SpaceVideoCount(ctx, "3546824314980440")
	t.Logf("navnum 投稿总数 = %d err=%v", n, err)
}
