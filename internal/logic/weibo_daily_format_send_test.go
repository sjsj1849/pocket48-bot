package logic

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
)

// TestSendCurrentSuperCountFormatEmail sends one real email with the latest
// template (2026-07-19 snapshot) so the user can check mobile rendering.
// Run: SEND_DAILY_FORMAT_TEST=1 go test ./internal/logic/ -count=1 -run TestSendCurrentSuperCountFormatEmail -v
func TestSendCurrentSuperCountFormatEmail(t *testing.T) {
	if os.Getenv("SEND_DAILY_FORMAT_TEST") != "1" {
		t.Skip("set SEND_DAILY_FORMAT_TEST=1 to send")
	}
	cfgPath := filepath.Join("..", "..", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		cfgPath = "config.json"
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	day, prevDay := "2026-07-19", "2026-07-18"
	v2 := cfg.WeiboSuperCountDailySnapshotsV2
	if v2 == nil || v2[day] == nil {
		t.Fatalf("no snapshot for %s", day)
	}
	results := make([]monitor.WeiboSuperCountResult, 0, len(v2[day]))
	for oid, item := range v2[day] {
		if item == nil {
			continue
		}
		results = append(results, monitor.WeiboSuperCountResult{
			OID:            oid,
			Name:           item.Name,
			SignCount:      item.SignCount,
			SuperLikeCount: item.SuperLikeCount,
			Heat24h:        item.Heat24h,
			ReadCount:      item.ReadCount,
			PostCount:      item.PostCount,
			FansCount:      item.FansCount,
			LevelText:      item.LevelText,
			DailyRankText:  item.DailyRankText,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SignCount != results[j].SignCount {
			return results[i].SignCount > results[j].SignCount
		}
		return results[i].Name < results[j].Name
	})
	var signBaseline, likeBaseline, postBaseline map[string]int
	if v2[prevDay] != nil {
		signBaseline = buildSignBaselineFromSnapshotV2(v2[prevDay])
		likeBaseline = buildLikeBaselineFromSnapshotV2(v2[prevDay])
		postBaseline = buildPostBaselineFromSnapshotV2(v2[prevDay])
	}
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 7, 19, 23, 59, 32, 0, loc)

	bot := &Bot{cfg: cfg}
	title := "[超话签到人数日报 · 格式测试]"
	report := formatWeiboSuperCountDualRanking(results, nil, title, now, signBaseline, likeBaseline, postBaseline)
	sections := bot.buildWeiboSuperCountEmailSections(results)
	t.Logf("to=%s sections=%d topics=%d", cfg.AlertEmailTo, len(sections), len(results))
	for _, s := range sections {
		t.Logf("  section=%s topics=%d", s.Title, len(s.Results))
	}
	bot.sendWeiboSuperCountDailyEmail(report, title, results, nil, now, signBaseline, likeBaseline, postBaseline)
	t.Logf("sent format-test email topics=%d", len(results))
}
