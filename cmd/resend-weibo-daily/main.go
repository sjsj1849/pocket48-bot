// One-shot: resend 超话日报 from stored snapshot (default yesterday 23:59 data).
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logic"
	"pocket48-bot/internal/monitor"
)

func main() {
	cfgPath := "/root/pocket48-bot/config.json"
	if v := strings.TrimSpace(os.Getenv("POCKET48_CONFIG")); v != "" {
		cfgPath = v
	}
	date := "2026-07-24"
	if len(os.Args) > 1 && strings.TrimSpace(os.Args[1]) != "" {
		date = strings.TrimSpace(os.Args[1])
	}
	cfg, err := config.LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	// Force fixed local SMTP even if file was overwritten by a running bot.
	cfg.AlertEmailSMTPHost = "127.0.0.1"
	cfg.AlertEmailSMTPPort = 25
	cfg.AlertEmailSMTPUser = ""
	cfg.AlertEmailSMTPPassword = ""
	cfg.AlertEmailEnabled = true

	snap := cfg.WeiboSuperCountDailySnapshotsV2[date]
	if len(snap) == 0 {
		log.Fatalf("no V2 snapshot for %s", date)
	}
	// Baseline = previous calendar day if present.
	t, err := time.ParseInLocation("2006-01-02", date, time.FixedZone("CST", 8*3600))
	if err != nil {
		log.Fatalf("bad date: %v", err)
	}
	prevDate := t.AddDate(0, 0, -1).Format("2006-01-02")
	var prev map[string]*config.WeiboSuperCountSnapshotItem
	if cfg.WeiboSuperCountDailySnapshotsV2 != nil {
		prev = cfg.WeiboSuperCountDailySnapshotsV2[prevDate]
	}

	results := make([]monitor.WeiboSuperCountResult, 0, len(snap))
	for oid, item := range snap {
		if item == nil {
			continue
		}
		results = append(results, monitor.WeiboSuperCountResult{
			OID:                oid,
			Name:               item.Name,
			SignCount:          item.SignCount,
			SuperLikeCount:     item.SuperLikeCount,
			Heat24h:            item.Heat24h,
			ReadCount:          item.ReadCount,
			PostCount:          item.PostCount,
			FansCount:          item.FansCount,
			LevelText:          item.LevelText,
			CreatorOfficerText: item.CreatorOfficerText,
			FanDiamondText:     item.FanDiamondText,
			DailyRankText:      item.DailyRankText,
			CheckinExpText:     item.CheckinExpText,
			CheckinStreakText:  item.CheckinStreakText,
			Source:             "snapshot-resend",
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SignCount != results[j].SignCount {
			return results[i].SignCount > results[j].SignCount
		}
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})

	signBase := map[string]int{}
	likeBase := map[string]int{}
	postBase := map[string]int{}
	for oid, item := range prev {
		if item == nil {
			continue
		}
		signBase[oid] = item.SignCount
		likeBase[oid] = item.SuperLikeCount
		if n, ok := monitor.ParseChineseNumber(item.PostCount); ok {
			postBase[oid] = n
		}
	}

	// Stamp as end-of-day for that report date.
	now := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 0, 0, t.Location())
	title := "[超话签到人数日报]"
	if err := logic.ResendWeiboSuperCountDailyEmail(cfg, results, nil, title, now, signBase, likeBase, postBase); err != nil {
		log.Fatalf("send: %v", err)
	}
	fmt.Printf("ok: resent %s daily report (%d topics) to %s via %s:%d\n",
		date, len(results), cfg.AlertEmailTo, cfg.AlertEmailSMTPHost, cfg.AlertEmailSMTPPort)
}
