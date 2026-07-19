package monitor

import "testing"

func TestParseMWeiboPageInfoDescMoreUnits(t *testing.T) {
	read, posts, fans, level := parseMWeiboPageInfoDescMore([]string{
		"阅读9.6亿　帖子11.9万　粉丝4.9万",
		"等级LV.2中级粉丝　连续签到0天 >>",
	})
	if read != "9.6亿" {
		t.Fatalf("read=%q", read)
	}
	if posts != "11.9万" {
		t.Fatalf("posts=%q", posts)
	}
	if fans != "4.9万" {
		t.Fatalf("fans=%q", fans)
	}
	if level != "LV.2中级粉丝" {
		t.Fatalf("level=%q", level)
	}
	read, posts, fans, _ = parseMWeiboPageInfoDescMore([]string{"阅读1672.9万　帖子3780　粉丝5779"})
	if read != "1672.9万" || posts != "3780" || fans != "5779" {
		t.Fatalf("got read=%q posts=%q fans=%q", read, posts, fans)
	}
}
