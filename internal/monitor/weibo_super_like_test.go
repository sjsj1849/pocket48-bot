package monitor

import (
	"os"
	"pocket48-bot/internal/config"
	"testing"
)

func TestParseMWeiboSuperLike(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		valid      bool
	}{
		{"without rank suffix", `{"ok":1,"data":{"cards":[{"card_group":[{"itemid":"badge_chaolike","desc":"超LIKE(290人)"}]}]}}`, 290, true},
		{"nested zero without suffix", `{"ok":1,"data":{"cards":[{"card_group":[{"itemid":"badge_chaolike","desc":"超LIKE(0人)"}]}]}}`, 0, true},
		{"count", `{"ok":1,"data":{"cards":[{"itemid":"badge_chaolike","desc":"超LIKE榜(295人)"}]}}`, 295, true},
		{"comma", `{"ok":1,"data":{"cards":[{"desc":"超LIKE榜（1,295人）"}]}}`, 1295, true},
		{"empty", `{"ok":1,"data":{"cards":[{"desc":"绚羽超话超LIKE榜"},{"card_group":[{"itemid":"badge_chaolike_record_empty"}]}]}}`, 0, true},
		{"explicit zero", `{"ok":1,"data":{"cards":[{"desc":"超LIKE榜(0人)"}]}}`, 0, true},
		{"missing", `{"ok":1,"data":{"cards":[{"desc":"超LIKE榜"}]}}`, 0, false},
		{"expired", `{"ok":0,"data":{"cards":[{"desc":"超LIKE榜(295人)"}]}}`, 0, false},
		{"invalid", `<html>login</html>`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, err := parseMWeiboSuperLike([]byte(tc.body))
			if (err == nil) != tc.valid || n != tc.want {
				t.Fatalf("got %d, %v", n, err)
			}
		})
	}
}

// Read-only integration check; never signs in or sends a report.
func TestLiveMWeiboSuperLike(t *testing.T) {
	if os.Getenv("LIVE_WEIBO_LIKE") != "1" {
		t.Skip("set LIVE_WEIBO_LIKE=1")
	}
	cfg, err := config.LoadConfig("../../config.json")
	if err != nil {
		t.Fatal(err)
	}
	m := &WeiboMonitor{}
	m.SetCookie(cfg.WeiboCookie)
	m.SetMWeiboCookie(cfg.WeiboMWeiboCookie)
	for _, oid := range []string{"1008083e042947a55a5273bc0ee55c623ca829", "1008080e1356953b659905e124dfa701d5a422", "1008088558d16cd2543e8b9c52d1a619fa65c6", "10080881f7468de44dae1addeb8455c864014d", "100808430244540aa38ff181545cb0d539f038", "1008087641b5b1c7276c07e11f9cb5b74ddfc9"} {
		res := &WeiboSuperCountResult{}
		m.enrichSuperLikeFromMWeibo(res, oid)
		if !res.SuperLikeKnown {
			t.Errorf("%s missing count", oid)
		}
		t.Logf("oid=%s known=%t count=%d", oid, res.SuperLikeKnown, res.SuperLikeCount)
	}
}
