package mediafetch

import (
	"net/http"
	"testing"
)

func TestRefererFor(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		// 抖音：实测不带就是 403
		{"抖音 CDN", "https://v26-webf.douyinvod.com/abc/video/tos/cn/x.mp4", "https://www.douyin.com/"},
		{"抖音 CDN 带端口", "https://v26-webf.douyinvod.com:443/abc.mp4", "https://www.douyin.com/"},
		{"抖音图片 CDN", "https://p3-pc-sign.douyinpic.com/image-cut-tos-priv/abc.jpeg", "https://www.douyin.com/"},
		{"抖音主站", "https://www.douyin.com/video/123", "https://www.douyin.com/"},
		{"抖音分享站", "https://www.iesdouyin.com/share/video/123/", "https://www.douyin.com/"},

		// B站：playurl.go 自己设，这里只是兜底
		{"B站 CDN", "https://cn-gotcha01.bilivideo.com/video.m4s", "https://www.bilibili.com/"},
		{"B站主站", "https://www.bilibili.com/video/BV1x", "https://www.bilibili.com/"},

		// 不需要 Referer 的平台：必须保持空，不能乱加
		{"X", "https://video.twimg.com/ext_tw_video/123/pu/pl/abc.mp4", ""},
		{"微博", "https://f.video.weibocdn.com/o0/abc.mp4", ""},

		// 异常输入
		{"空串", "", ""},
		{"非法 URL", "://not a url", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := refererFor(tc.url); got != tc.want {
				t.Fatalf("refererFor(%q) = %q, 期望 %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestRefererSuffixNotFooledByLookalikeHost 是防误匹配的关键一条。
// 攻击/误配场景：xxx-douyinvod.com.evil.com 绝不能被当成抖音 CDN。
func TestRefererSuffixNotFooledByLookalikeHost(t *testing.T) {
	bad := []string{
		"https://douyinvod.com.evil.com/a.mp4",
		"https://notdouyinvod.com/a.mp4",
		"https://evil.com/?x=douyinvod.com",
	}
	for _, raw := range bad {
		if got := refererFor(raw); got != "" {
			t.Fatalf("refererFor(%q) 不应匹配到抖音，却返回 %q", raw, got)
		}
	}
}

func TestApplyRefererSetsHeader(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://v26-webf.douyinvod.com/abc.mp4", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if req.Header.Get("Referer") != "" {
		t.Fatal("初始不应有 Referer")
	}
	ApplyReferer(req, "https://v26-webf.douyinvod.com/abc.mp4")
	if got := req.Header.Get("Referer"); got != "https://www.douyin.com/" {
		t.Fatalf("Referer 未被设置，实际 %q", got)
	}
}

// TestApplyRefererLeavesOthersAlone 保证不认识的目标不被污染。
func TestApplyRefererLeavesOthersAlone(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/a.mp4", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	ApplyReferer(req, "https://example.com/a.mp4")
	if got := req.Header.Get("Referer"); got != "" {
		t.Fatalf("不该设置 Referer，实际 %q", got)
	}
}

// TestApplyRefererDoesNotOverrideExisting 已有 Referer 时不覆盖：
// 采集链路可能已经根据具体稿件设了更精确的 Referer。
func TestApplyRefererDoesNotOverrideExisting(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://v26-webf.douyinvod.com/abc.mp4", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Referer", "https://www.douyin.com/user/xyz")
	ApplyReferer(req, "https://v26-webf.douyinvod.com/abc.mp4")
	if got := req.Header.Get("Referer"); got != "https://www.douyin.com/user/xyz" {
		t.Fatalf("已有 Referer 被覆盖了，实际 %q", got)
	}
}
