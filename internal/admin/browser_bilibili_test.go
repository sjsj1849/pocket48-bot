package admin

import (
	"strings"
	"testing"
)

func TestValidBilibiliBrowserSession(t *testing.T) {
	const future = 1 << 40 // 远超当前时间戳，模拟未过期 cookie
	cases := []struct {
		name    string
		session bilibiliBrowserSession
		want    bool
	}{
		{"空会话不认", bilibiliBrowserSession{}, false},
		{"只有设备 cookie 不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "buvid3", Value: "x", Domain: ".bilibili.com", Expires: future},
		}}, false},
		{"只有 SESSDATA 认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: future},
		}}, true},
		{"SESSDATA + 配套 cookie 认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: future},
			{Name: "bili_jct", Value: "jct", Domain: ".bilibili.com", Expires: future},
			{Name: "DedeUserID", Value: "123", Domain: ".bilibili.com", Expires: future},
		}}, true},
		{"expires 为 -1（会话 cookie）认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: -1},
		}}, true},
		{"已过期不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: 1},
		}}, false},
		{"非 bilibili 域名不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".evil.com", Expires: future},
		}}, false},
		{"值里带分号不认（防注入）", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc; admin=1", Domain: ".bilibili.com", Expires: future},
		}}, false},
		{"值里带换行不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc\r\nX: 1", Domain: ".bilibili.com", Expires: future},
		}}, false},
		{"空值不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "", Domain: ".bilibili.com", Expires: future},
		}}, false},
		{"重复 cookie 名不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: future},
			{Name: "SESSDATA", Value: "def", Domain: ".bilibili.com", Expires: future},
		}}, false},
		{"白名单外的 cookie 不认", bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
			{Name: "SESSDATA", Value: "abc", Domain: ".bilibili.com", Expires: future},
			{Name: "evil", Value: "x", Domain: ".bilibili.com", Expires: future},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validBilibiliBrowserSession(tc.session); got != tc.want {
				t.Fatalf("validBilibiliBrowserSession() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBilibiliCookieFromSession(t *testing.T) {
	session := bilibiliBrowserSession{Cookies: []bilibiliBrowserCookie{
		{Name: "DedeUserID", Value: "123", Domain: ".bilibili.com", Expires: -1},
		{Name: "bili_jct", Value: "jct-value", Domain: ".bilibili.com", Expires: -1},
		{Name: "SESSDATA", Value: "sess-value", Domain: ".bilibili.com", Expires: -1},
	}}
	got := bilibiliCookieFromSession(session)
	want := "SESSDATA=sess-value; bili_jct=jct-value; DedeUserID=123"
	if got != want {
		t.Fatalf("bilibiliCookieFromSession() = %q, want %q", got, want)
	}
	// SESSDATA 必须在最前面：部分风控按顺序解析 cookie。
	if !strings.HasPrefix(got, "SESSDATA=") {
		t.Fatalf("SESSDATA 必须排在最前，实际 %q", got)
	}
}

func TestBilibiliCookieFromSessionEmpty(t *testing.T) {
	if got := bilibiliCookieFromSession(bilibiliBrowserSession{}); got != "" {
		t.Fatalf("空会话应得到空 Cookie，实际 %q", got)
	}
}
