package extract

import (
	"context"
	"testing"
)

// TikTok 链接识别测试（2026-10-04）。
//
// 重点验证三件事：
//  1. 正常作品链接能识别
//  2. **相似域名不能误命中**（nottiktok.com 这类）—— 这是记忆里
//     反复踩过的坑：裸 HasSuffix 会把 notdouyinvod.com 匹配成 douyinvod.com
//  3. 非视频页不该被认成作品链接（否则主页链接会走进 resolver 却拿不到 ID）
func TestZZTiktokMatches(t *testing.T) {
	r := &TiktokResolver{}
	cases := []struct {
		hostPath string
		want     bool
		reason   string
	}{
		{"tiktok.com/@hearts2hearts/video/7688182139010977025", true, "标准作品链接"},
		{"www.tiktok.com/@user/video/7688182139010977025", true, "带 www 前缀"},
		{"tiktok.com/@user/video/7688182139010977025/", true, "结尾斜杠"},
		{"vm.tiktok.com/ZMabcdef/", true, "短链（需先跟随跳转）"},
		{"tiktok.com/@user", false, "主页不是作品页"},
		{"tiktok.com/", false, "裸首页"},
		// ★ 反直觉的坑：域名相似但不是 TikTok。
		// vm.tiktok.com 是真短链域名，但要靠 tiktok.com 精确后缀命中，
		// 而 nottiktok.com / faketiktok.com 必须被排除。
		{"nottiktok.com/@user/video/1234567890123456", false, "nottiktok.com 不该命中"},
		{"faketiktok.com/@user/video/1234567890123456", false, "faketiktok.com 不该命中"},
		{"tiktok.com.evil.com/@user/video/1234567890123456", false, "后缀在攻击者域名上"},
		{"notiktok.com/@user/video/1234567890123456", false, "少一个 t 的域名"},
	}
	for _, c := range cases {
		if got := r.Matches(c.hostPath); got != c.want {
			t.Errorf("Matches(%q) = %v，期望 %v（%s）", c.hostPath, got, c.want, c.reason)
		}
	}
}

func TestZZTiktokPlatformIsTiktok(t *testing.T) {
	var r Resolver = &TiktokResolver{}
	if r.Platform() != PlatformTiktok {
		t.Fatalf("Platform() = %q，期望 %q", r.Platform(), PlatformTiktok)
	}
}

// TestZZTiktokIDExtraction 验证作品 ID 与用户名的提取。
//
// 真实 ID 是 19 位雪花算法数字（实测 7688182139010977025）。
// 用户名来自 /@xxx/ 这一段。
func TestZZTiktokIDExtraction(t *testing.T) {
	const raw = "https://www.tiktok.com/@hearts2hearts/video/7688182139010977025"

	id := tiktokVideoID.FindString(raw)
	if id != "7688182139010977025" {
		t.Fatalf("作品 ID 提取错误：得到 %q，期望 7688182139010977025", id)
	}

	m := tiktokUserFromPath.FindStringSubmatch(raw)
	if len(m) != 2 || m[1] != "hearts2hearts" {
		t.Fatalf("用户名提取错误：得到 %v，期望 [hearts2hearts]", m)
	}
}

// TestZZTiktokDetect 注册后的端到端识别：Detect 必须能认出 TikTok。
//
// 这一层才是真正有意义的 —— 记忆里的教训是
// 「handler 写好 ≠ 路由注册」，同理「resolver 写好 ≠ Detect 能认出它」。
func TestZZTiktokDetect(t *testing.T) {
	client := New(Options{Timeout: 1}, &TiktokResolver{Dir: "/tmp"})

	cases := []struct {
		url  string
		want Platform
	}{
		{"https://www.tiktok.com/@hearts2hearts/video/7688182139010977025", PlatformTiktok},
		{"https://m.tiktok.com/@user/video/7688182139010977025", PlatformTiktok},
		// 别的平台不能被 TikTok resolver 抢走
		{"https://www.bilibili.com/video/BV1UbSoBYEYt", PlatformUnknown},
		{"https://x.com/user/status/123", PlatformUnknown},
	}
	for _, c := range cases {
		got, _, err := client.Detect(context.Background(), c.url)
		if err != nil {
			t.Errorf("Detect(%q) 返回错误：%v", c.url, err)
			continue
		}
		if got != c.want {
			t.Errorf("Detect(%q) = %q，期望 %q", c.url, got, c.want)
		}
	}
}

// TestZZTiktokResolveRejectsNonVideo 确认非 TikTok 链接不会被误处理。
func TestZZTiktokResolveRejectsNonVideo(t *testing.T) {
	r := &TiktokResolver{Dir: "/tmp"}
	if _, err := r.Resolve(context.Background(), "https://www.bilibili.com/video/BV1UbSoBYEYt"); err == nil {
		t.Fatal("B 站链接不该被 TikTok resolver 解析成功")
	}
}
