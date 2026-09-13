package logic

import (
	"context"
	"fmt"
	"log"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/weverse"
	"time"
)

func aiSubscription(cfg weverse.Settings, job *weverse.AIBatch) (weverse.Subscription, bool) {
	if cfg.Enabled {
		for _, s := range cfg.Subscriptions {
			if job.MatchesSubscription(s) {
				return s, true
			}
		}
	}
	return weverse.Subscription{}, false
}

func weverseAISegments(s weverse.Subscription, job *weverse.AIBatch) [][]interface{} {
	text := []rune(fmt.Sprintf("【%s|Weverse】\nAI 聊天整理（%d 条回复）\n\n%s", job.Author, len(job.Entries), job.Result))
	var messages [][]interface{}
	for len(text) > 0 {
		n := len(text)
		if n > 1800 {
			n = 1800
		}
		segments := []interface{}{}
		mentionAll := false
		for _, id := range job.ActorIDs() {
			if s.MentionsAll(id) {
				mentionAll = true
			}
		}
		if len(messages) == 0 && mentionAll {
			segments = append(segments, napcat.AtSegment("all"), napcat.TextSegment("\n"))
		}
		segments = append(segments, napcat.TextSegment(string(text[:n])))
		messages = append(messages, segments)
		text = text[n:]
	}
	return messages
}

func (b *Bot) runWeverseAISummaryLoop(ctx context.Context, dir string) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		ai, err := weverse.LoadAISettings(dir)
		if err != nil || !ai.Enabled {
			continue
		}
		cfg, err := weverse.LoadSettings(dir)
		if err != nil {
			continue
		}
		var status weverse.Status
		_ = weverse.Read(dir, "status.json", &status)
		last, parseErr := time.Parse(time.RFC3339, status.LastSuccess)
		freshness := time.Duration(cfg.PollSeconds)*time.Second + 4*time.Minute
		fresh := parseErr == nil && status.Error == "" && time.Since(last) < freshness
		job, err := weverse.ClaimAIJob(dir, cfg, ai, time.Now(), fresh)
		if err != nil {
			log.Printf("[Weverse AI] 队列读取失败: %v", err)
			continue
		}
		if job == nil {
			continue
		}
		if job.Result == "" {
			callCtx, cancel := context.WithTimeout(ctx, ai.RequestTimeout())
			job.Result, err = weverse.SummarizeAI(callCtx, ai, *job)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = weverse.SaveAIResult(dir, job.ID, job.Result)
			}
			if err != nil {
				_ = weverse.FinishAIJob(dir, job.ID, err)
				log.Printf("[Weverse AI] 整理失败，将重试: %v", err)
				continue
			}
		}
		// Recheck current recipients and switches after the potentially slow AI request.
		cfg, err = weverse.LoadSettings(dir)
		if err != nil {
			continue
		}
		ai, err = weverse.LoadAISettings(dir)
		if err != nil || !ai.Enabled {
			continue
		}
		s, allowed := aiSubscription(cfg, job)
		if allowed {
			for _, segments := range weverseAISegments(s, job) {
				b.napcat.SendGroupMessage(s.GroupID, segments)
			}
			log.Printf("[Weverse AI] 已转发 %s 的聊天整理（%d 条回复）", job.Author, len(job.Entries))
		}
		if err := weverse.FinishAIJob(dir, job.ID, nil); err != nil {
			log.Printf("[Weverse AI] 保存完成状态失败: %v", err)
		}
	}
}
