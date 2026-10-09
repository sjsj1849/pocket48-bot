package logic

import (
	"context"
	"testing"

	"pocket48-bot/internal/bilibili"
)

// 端到端验证视频投稿抓取：走真实的双来源合并路径。
//
// 这个测试原先断言「时长小于 1200 秒的投稿全部被丢弃」，那是旧策略
// —— 用 MinVideoSeconds 一刀切掉所有短视频，等于假定「所有短视频都在抖音
// 推过了」。但实际存在 B 站比抖音先发的情况，那一刀会静默丢掉真正的新内容。
//
// 现在短视频照常返回，是否推送改由 bilibiliPollGroup 里的跨平台标题索引
// 决定（抖音更早发过才跳过）。因此本测试改为断言：
//  1. 合集外投稿（arc/search 来源）的 Seconds 必须被正确解析，
//     否则后面「短视频内嵌」的时长判定会失效。
//  2. 短视频必须出现在结果里（放开过滤是这次改动的核心）。
func TestBilibiliFetchVideosIncludesShortAndParsesDuration(t *testing.T) {
	const uid = "3546824314980440"
	raw, err := readBiliSettingsCookie()
	if err != nil || raw == "" {
		t.Skip("未配置 B 站 Cookie")
	}
	client := &bilibili.Client{Cookie: raw}
	sub := bilibili.Subscription{
		UID:   uid,
		Video: true,
		// 仍保留旧阈值：这次改动不应静默改变订阅配置本身，
		// 只是让时长过滤不再一刀切丢弃短视频。
		MinVideoSeconds: 1200,
	}

	items, err := bilibiliFetchVideos(context.Background(), client, sub)
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	t.Logf("合并后共 %d 条", len(items))

	var short, long, unknown int
	for _, d := range items {
		switch {
		case d.Kind != "video":
			// 图文/专栏不参与时长判定
		case d.Seconds == 0:
			unknown++
			t.Logf("  [时长未知] %s  %s", d.ID, d.Title)
		case bilibiliIsShortVideo(d):
			short++
			t.Logf("  [短视频 %ds] %s  %s", d.Seconds, d.ID, d.Title)
		default:
			long++
			t.Logf("  [长视频 %ds] %s  %s", d.Seconds, d.ID, d.Title)
		}
	}
	if unknown > 0 {
		t.Fatalf("有 %d 条视频时长未知，说明 length 字符串没被解析成 Seconds", unknown)
	}
	if short == 0 {
		t.Fatal("短视频应出现在抓取结果里（是否推送交由跨平台去重判定）")
	}
	if long == 0 {
		t.Fatal("长视频（团综）不应消失")
	}
	t.Logf("短视频 %d 条 / 长视频 %d 条 / 时长未知 %d 条", short, long, unknown)
}
