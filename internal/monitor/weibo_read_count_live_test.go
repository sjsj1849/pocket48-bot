package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"pocket48-bot/internal/config"
)

func TestLiveFetchSuperCountWithRead(t *testing.T) {
	if os.Getenv("LIVE_WEIBO_READ") != "1" {
		t.Skip("set LIVE_WEIBO_READ=1")
	}
	cfgPath := filepath.Join("..", "..", "config.json")
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	m := &WeiboMonitor{}
	m.SetCookie(cfg.WeiboCookie)
	m.SetMWeiboCookie(cfg.WeiboMWeiboCookie)
	n := 0
	for oid, topic := range cfg.WeiboSuperCountTopics {
		name := ""
		if topic != nil {
			name = topic.Name
		}
		res, err := m.FetchSuperCountByOID(oid, name)
		if err != nil {
			t.Logf("FAIL %s %s: %v", oid, name, err)
			continue
		}
		b, _ := json.Marshal(map[string]any{
			"name": res.Name, "sign": res.SignCount, "read": res.ReadCount,
			"posts": res.PostCount, "fans": res.FansCount, "level": res.LevelText,
			"source": res.Source,
		})
		t.Logf("OK %s", string(b))
		if res.ReadCount == "" {
			t.Errorf("%s missing read", name)
		}
		if res.PostCount == "" {
			t.Errorf("%s missing posts", name)
		}
		n++
		if n >= 3 {
			break
		}
	}
	if n == 0 {
		t.Fatal("no topics fetched")
	}
}
