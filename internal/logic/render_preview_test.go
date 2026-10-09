package logic

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/bilibili"
)

// TestBilibiliRenderedMessagePreview 把真实样例渲染成最终消息文本，
// 用来肉眼确认格式改动符合预期（用户提了 3 条文案要求）。
// 只在手工验证时用 -v 跑：go test ./internal/logic/ -run TestBilibiliRenderedMessagePreview -v
func TestBilibiliRenderedMessagePreview(t *testing.T) {
	sub := bilibili.Subscription{
		UID: "3546824314980440", Name: "Hearts2Hearts",
		TargetIDs: []string{"qq:group:958843228"},
	}
	samples := []bilibili.Dynamic{
		{
			ID: "av:BV1Uma665E3v", Kind: "video",
			Title:  "【Hearts2Hearts】我们来到东京巨蛋参加开球仪式舞台!(ᵔᗜᵔ)✧* ｜《ICONIC HEART》日本宣传活动 BH2ND #2",
			Length: "32:37", Seconds: 1957, View: 8802,
			Time: time.Date(2026, 10, 1, 20, 0, 0, 0, time.Local).UnixMilli(),
			URL:  "https://www.bilibili.com/video/BV1Uma665E3v",
		},
		{
			ID: "av:BV1PDYy6XEbM", Kind: "video",
			Title:  "【Hearts2Hearts】我超级Happy哒 (๑´^᎑^)~♡ | Hearts2Hearts《15-LOVE》",
			Length: "27:52", Seconds: 1672,
			Time: time.Date(2026, 9, 14, 20, 0, 0, 0, time.Local).UnixMilli(),
			URL:  "https://www.bilibili.com/video/BV1PDYy6XEbM",
		},
	}

	var sb strings.Builder
	for _, d := range samples {
		sb.WriteString("---------- 消息内容 ----------\n")
		for _, group := range bilibiliDynamicGroups(sub, d) {
			for _, seg := range group {
				switch v := seg.(type) {
				case interface{ GetText() string }:
					sb.WriteString(v.GetText())
				default:
					sb.WriteString(fmt.Sprintf("%v", v))
				}
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	t.Log("\n" + sb.String())
}
