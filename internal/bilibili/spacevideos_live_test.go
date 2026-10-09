package bilibili

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// 登录态下的投稿列表必须能穿透风控。
//
// 背景：x/space/wbi/arc/search 在机房 IP 匿名态稳定返回 -352 / 412，
// 只有带上 SESSDATA 才拿得到合集之外的新投稿。而 WBI 签名密钥的获取有两条路：
// GenWebTicket（登录态下会报 -111）和 nav 接口（登录态 + buvid3 才返回 wbi_img）。
// 这个测试锁定「两条路都试一遍仍失败」时不会静默返回 0 条。
//
// 跑法：POCKET48_BILI_TEST_UID=xxx go test ./internal/bilibili/ -run TestSpaceVideosWithCookie -v
func TestSpaceVideosWithCookie(t *testing.T) {
	uid := os.Getenv("POCKET48_BILI_TEST_UID")
	if uid == "" {
		t.Skip("未设置 POCKET48_BILI_TEST_UID")
	}
	raw, err := os.ReadFile("../../storage/bilibili/settings.json")
	if err != nil {
		t.Skip("读不到 settings.json")
	}
	var cfg struct {
		Cookie string `json:"cookie"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Cookie == "" {
		t.Skip("未配置 Cookie")
	}

	c := &Client{Dir: t.TempDir(), Cookie: cfg.Cookie}
	// GenWebTicket 在登录态下必然失败，因此 nav 回退必须生效。
	if _, ticketErr := c.fetchWbiKeys(context.Background()); ticketErr == nil {
		t.Log("GenWebTicket 这次通了（不常见，但不阻塞）")
	}
	keys, navErr := c.fetchWbiKeysFromNav(context.Background())
	if navErr != nil {
		t.Fatalf("nav 回退取 wbi 密钥失败，投稿列表将完全不可用: %v", navErr)
	}
	if keys.ImgKey == "" || keys.SubKey == "" {
		t.Fatalf("nav 返回的 wbi 密钥不完整: img=%q sub=%q", keys.ImgKey, keys.SubKey)
	}

	items, err := c.SpaceVideos(context.Background(), uid)
	if err != nil {
		t.Fatalf("SpaceVideos 失败: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("登录态下应能拿到投稿列表，却返回 0 条")
	}
	for i, d := range items {
		if i >= 5 {
			break
		}
		t.Logf("  %s  %-8s %s", d.ID, d.Length, d.Title)
	}
}
