package extract

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDetectIsDeterministic 是一条关键回归：
//
// Detect 遍历 c.resolvers（一个 map）来决定平台，而 Go 的 **map 遍历顺序是随机的**。
// 如果两个解析器的 Matches 都能匹配同一条链接，每次调用可能路由到不同平台。
// 匹配范围只要有重叠，这个不确定性就会变成线上偶发的「同一条链接时灵时不灵」。
func TestDetectIsDeterministic(t *testing.T) {
	cases := []string{
		"https://www.douyin.com/video/7675209447762131172",
		"https://weibo.com/6694957538/5350064312812042",
		"https://www.bilibili.com/video/BV1UMYx6BEP8",
	}

	// 只做平台识别，不真正联网解析。
	noop := func(p Platform) Resolver { return nil }

	for _, raw := range cases {
		seen := map[Platform]int{}
		for i := 0; i < 200; i++ {
			_ = noop
			client := New(Options{Timeout: time.Second},
				&BilibiliResolver{},
				&XResolver{},
				&DouyinResolver{},
				&WeiboResolver{},
			)
			platform, _, err := client.Detect(context.Background(), raw)
			if err != nil {
				t.Fatalf("%s 第 %d 次识别报错: %v", raw, i, err)
			}
			seen[platform]++
		}
		if len(seen) != 1 {
			t.Fatalf("%s 被识别成多个平台 %v —— map 遍历顺序不确定，必然偶发错路由", raw, seen)
		}
	}
}

// TestMatchesRejectsCrossContamination 验证不会把别的平台的链接误判过来。
func TestMatchesRejectsCrossContamination(t *testing.T) {
	dy := &DouyinResolver{}
	wb := &WeiboResolver{}
	bl := &BilibiliResolver{}
	xr := &XResolver{}

	cases := []struct {
		name       string
		resolver   Resolver
		hostPath   string
		shouldFail bool
	}{
		{"抖音认抖音", dy, "douyin.com/video/123", false},
		{"抖音不认微博", dy, "weibo.com/6694957538/5350064312812042", true},
		{"抖音不认B站", dy, "bilibili.com/video/BV1x", true},
		{"微博认微博", wb, "weibo.com/6694957538/5350064312812042", false},
		{"微博不认抖音", wb, "douyin.com/video/123", true},
		{"微博不认B站", wb, "bilibili.com/video/BV1x", true},
		{"B站认B站", bl, "bilibili.com/video/BV1x", false},
		{"B站不认微博", bl, "weibo.com/6694957538/1", true},
		{"X认X", xr, "x.com/user/status/123", false},
		{"X不认抖音", xr, "douyin.com/video/123", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := tc.resolver.(interface{ Matches(string) bool })
			if !ok {
				t.Fatalf("%T 实现了 Matches 接口", tc.resolver)
			}
			got := m.Matches(tc.hostPath)
			if tc.shouldFail && got {
				t.Fatalf("%T 不该匹配 %q，却匹配了", tc.resolver, tc.hostPath)
			}
			if !tc.shouldFail && !got {
				t.Fatalf("%T 应该匹配 %q，却没匹配", tc.resolver, tc.hostPath)
			}
		})
	}
}

// TestDetectShortLinkAndNormalize 覆盖短链与 www 前缀的归一化。
func TestDetectNormalizesHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := New(Options{Timeout: 5 * time.Second},
		&BilibiliResolver{}, &XResolver{}, &DouyinResolver{}, &WeiboResolver{})

	// www. 前缀应被剥掉后再匹配
	platform, _, err := client.Detect(context.Background(), "https://www.douyin.com/video/123")
	if err != nil {
		t.Fatalf("识别失败: %v", err)
	}
	if platform != PlatformDouyin {
		t.Fatalf("www.douyin.com 应识别为抖音，实际 %q", platform)
	}
}
