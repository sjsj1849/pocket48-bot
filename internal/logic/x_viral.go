package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/xmonitor"
	"regexp"
	"sort"
	"strings"
	"time"
)

const xViralSearchLimit = 500

func xViralExpressions(keywords []string) []string {
	result := []string{`("Hearts2Hearts" OR "하츠투하츠" OR @Hearts2Hearts)`, `(#CARMEN OR #카르멘 OR #JIWOO OR #지우 OR #YUHA OR #유하 OR #STELLA OR #스텔라 OR #JUUN OR #주은 OR #A_NA OR #에이나 OR #IAN OR #이안 OR #YE_ON OR #예온)`}
	clean := []string{}
	for _, keyword := range keywords {
		keyword = strings.TrimSpace(strings.ReplaceAll(keyword, `"`, ""))
		if keyword == "" {
			continue
		}
		if !strings.HasPrefix(keyword, "#") && !strings.HasPrefix(keyword, "@") {
			keyword = `"` + keyword + `"`
		}
		clean = append(clean, keyword)
	}
	for start := 0; start < len(clean); start += 12 {
		end := start + 12
		if end > len(clean) {
			end = len(clean)
		}
		result = append(result, "("+strings.Join(clean[start:end], " OR ")+")")
	}
	return result
}

func xViralPeriod(cfg xmonitor.Settings, now time.Time) (time.Time, time.Time, error) {
	month := cfg.ViralTargetMonth
	if month == "" {
		month = now.AddDate(0, -1, 0).Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, time.FixedZone("CST", 8*3600))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, start.AddDate(0, 1, 0), nil
}

func xViralTail(start, end time.Time) string {
	return fmt.Sprintf(" -filter:nativeretweets since:%s until:%s", start.Format("2006-01-02"), end.Format("2006-01-02"))
}

func relevantXViralPost(event xmonitor.Event) bool {
	body := strings.ToLower(event.Body)
	return strings.Contains(body, "hearts2hearts") || strings.Contains(body, "하츠투하츠") || len(xmonitor.SuggestViralMembers(body)) > 0
}

func filterXViralEvents(events []xmonitor.Event, cfg xmonitor.Settings, start, end time.Time, requireRelevant bool) []xmonitor.Event {
	excluded := map[string]bool{"hearts2hearts": true}
	for _, username := range cfg.ViralExcludeUsers {
		name, err := xmonitor.Username(username)
		if err == nil {
			excluded[strings.ToLower(name)] = true
		}
	}
	seen, result := map[string]bool{}, []xmonitor.Event{}
	for _, event := range events {
		if seen[event.ID] || excluded[strings.ToLower(event.Author.Username)] || event.Kind == "repost" || event.Time < start.UnixMilli() || event.Time >= end.UnixMilli() || (requireRelevant && !relevantXViralPost(event)) {
			continue
		}
		if event.LikeCount < cfg.ViralMinLikes && event.RepostCount < cfg.ViralMinReposts {
			continue
		}
		seen[event.ID] = true
		result = append(result, event)
	}
	return result
}

var xViralTagRE = regexp.MustCompile(`(?i)#[\p{L}\p{N}_-]+`)

func discoveredXViralTags(events []xmonitor.Event) []string {
	counts := map[string]int{}
	for _, event := range events {
		if relevantXViralPost(event) {
			for _, tag := range xViralTagRE.FindAllString(event.Body, -1) {
				counts[tag]++
			}
		}
	}
	tags := make([]string, 0, len(counts))
	for tag := range counts {
		tags = append(tags, tag)
	}
	sort.Slice(tags, func(i, j int) bool {
		return counts[tags[i]] > counts[tags[j]] || (counts[tags[i]] == counts[tags[j]] && tags[i] < tags[j])
	})
	if len(tags) > 40 {
		tags = tags[:40]
	}
	return tags
}

func runXViralScan(ctx context.Context, dir string, cfg xmonitor.Settings) (xmonitor.ViralStatus, error) {
	now := time.Now()
	status := xmonitor.ViralStatus{LastCheck: now.Format(time.RFC3339)}
	start, end, err := xViralPeriod(cfg, now)
	if err != nil {
		return status, err
	}
	client := xmonitor.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
	seedEvents := []xmonitor.Event{}
	tail := xViralTail(start, end)
	for _, expression := range xViralExpressions(cfg.ViralKeywords) {
		for _, threshold := range []string{"min_faves:1000", "min_retweets:500"} {
			events, searchErr := client.SearchPosts(ctx, expression+" "+threshold+tail, xViralSearchLimit)
			if searchErr != nil {
				return status, searchErr
			}
			seedEvents = append(seedEvents, events...)
		}
	}
	status.DiscoveredTags = discoveredXViralTags(seedEvents)
	authorSet := map[string]bool{}
	for _, event := range seedEvents {
		if relevantXViralPost(event) && event.Author.Username != "" {
			authorSet[event.Author.Username] = true
		}
	}
	for _, sub := range cfg.Subscriptions {
		if sub.Username != "" {
			authorSet[sub.Username] = true
		}
	}
	authors := make([]string, 0, len(authorSet))
	for author := range authorSet {
		if !strings.EqualFold(author, "Hearts2Hearts") {
			authors = append(authors, author)
		}
	}
	sort.Strings(authors)
	if len(authors) > 40 {
		authors = authors[:40]
	}
	status.AuthorsScanned = len(authors)
	authorEvents := []xmonitor.Event{}
	for offset := 0; offset < len(authors); offset += 8 {
		last := offset + 8
		if last > len(authors) {
			last = len(authors)
		}
		parts := make([]string, 0, last-offset)
		for _, author := range authors[offset:last] {
			parts = append(parts, "from:"+author)
		}
		expression := "(" + strings.Join(parts, " OR ") + ")"
		for _, threshold := range []string{fmt.Sprintf("min_faves:%d", cfg.ViralMinLikes), fmt.Sprintf("min_retweets:%d", cfg.ViralMinReposts)} {
			events, searchErr := client.SearchPosts(ctx, expression+" "+threshold+tail, xViralSearchLimit)
			if searchErr != nil {
				return status, searchErr
			}
			authorEvents = append(authorEvents, events...)
		}
	}
	direct := filterXViralEvents(seedEvents, cfg, start, end, true)
	fromAuthors := filterXViralEvents(authorEvents, cfg, start, end, false)
	candidates := filterXViralEvents(append(direct, fromAuthors...), cfg, start, end, false)
	status.Candidates = len(candidates)
	store, err := xmonitor.OpenViralStore(dir)
	if err != nil {
		return status, err
	}
	defer store.Close()
	status.Stored, err = store.Upsert(candidates, cfg.ViralMinLikes, cfg.ViralMinReposts, now)
	if err != nil {
		return status, err
	}
	all, err := store.List(xmonitor.ViralFilter{Status: "all", Limit: 2000})
	if err != nil {
		return status, err
	}
	ids := map[string]bool{}
	for _, event := range candidates {
		ids[event.ID] = true
	}
	render := []xmonitor.ViralPost{}
	for _, post := range all {
		if ids[post.ID] {
			render = append(render, post)
		}
	}
	if err := xmonitor.RenderViralSnapshots(dir, render); err != nil {
		return status, err
	}
	status.LastSuccess = status.LastCheck
	return status, nil
}

func (b *Bot) runXViralLoop(ctx context.Context) {
	dir := xmonitor.Dir(b.cfg.ConfigPath())
	for {
		cfg, err := xmonitor.LoadSettings(dir)
		wait := 10 * time.Minute
		if err != nil {
			log.Printf("[X高热] 无法读取配置: %v", err)
		} else if cfg.Enabled && cfg.ViralEnabled {
			wait = time.Duration(cfg.ViralPollHours) * time.Hour
			status, scanErr := runXViralScan(ctx, dir, cfg)
			if scanErr != nil {
				status.Error = scanErr.Error()
				log.Printf("[X高热] 扫描失败: %v", scanErr)
			} else {
				log.Printf("[X高热] 扫描完成: %d 条候选，反查 %d 个相关作者，%d 条记录已更新", status.Candidates, status.AuthorsScanned, status.Stored)
			}
			_ = xmonitor.Write(dir, "viral-status.json", status)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}
