// 微博接入跨平台去重的 logic 层接线。
//
// 2026-10-04 新增。用户在飞书里看到「同一个视频又在微博和 B 站发了」，
// 排查发现微博是唯一没接 crossTitleIndex 的平台 —— 抖音、B站、TikTok
// 三家都接了，微博这条链路完全独立，所以同一条内容必然重复推送。
//
// 这里只提供两个闭包，实际判定逻辑复用既有的 dedupe.Index，
// 判定口径与其它平台完全一致（作者名 + 时长差 <= 3s，两级判定）。

package logic

import (
	"log"
	"strings"
	"time"

	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/xmonitor"
)

// weiboCrossGate 返回「别的平台是否更早推过同一条」的判定闭包。
//
// 与 bilibiliShouldSkipVideo / tiktok_monitor.go 的判定完全同构：
//  1. 用 MatchWithAuthor 找到同一条内容在索引里的首发时间。
//     该方法内部先试「归一化指纹完全相等」，失败再试
//     「作者名一致 + 时长差在容差内」。
//  2. 索引里的首发时间**严格早于**本条发布时间，才认为该跳过。
//     时间相同或更晚说明微博才是首发，必须推。
//
// publishedAtMS <= 0（created_at 解析失败）时保守放行：
// 无法证明别人先发，就不拦。
func (b *Bot) weiboCrossGate() monitor.CrossDedupeGate {
	return func(title, author string, seconds int, publishedAtMS int64) bool {
		if strings.TrimSpace(title) == "" {
			return false
		}
		if publishedAtMS <= 0 {
			// 时间都拿不到，不参与判定（宁可多推）。
			return false
		}
		// 告诉去重器本方是微博：候选来自其它平台时才算跨平台镜像。
		dedupe.SetActiveSource("weibo")
		firstSeen, ok := crossTitleIndex(b.cfg.ConfigPath()).
			MatchWithAuthor(title, seconds, author, publishedAtMS)
		if !ok || firstSeen >= publishedAtMS {
			return false
		}
		log.Printf("[Weibo] 去重命中：%q（作者=%s %d秒）索引里 %s 首发，本条 %s 发布",
			truncate(title, 40), author, seconds,
			formatMS(firstSeen), formatMS(publishedAtMS))
		return true
	}
}

// weiboCrossRec 返回「登记微博首发」的闭包，供抖音/B站/TikTok 反向比对。
//
// 只登记**视频**微博：图文微博不会同时出现在 B 站/抖音，
// 登记了没有对照价值，反而挤占索引容量。
// 判定「是不是视频」交给调用侧（monitor 侧只在有 PageInfo 时才带上时长），
// 这里用 seconds > 0 作为「有视频」的近似信号 —— 图文微博取不到时长。
func (b *Bot) weiboCrossRec() monitor.CrossDedupeRecorder {
	return func(title, author string, seconds int, publishedAtMS int64) {
		if strings.TrimSpace(title) == "" || publishedAtMS <= 0 {
			return
		}
		if seconds <= 0 {
			// 图文微博：不登记，避免和视频内容抢指纹。
			return
		}
		// 登记**推送时刻**（与其它平台同一口径），publishedAtMS 只是作品
		// 发布时间，登记它会让「先发稿」被误当成「先发出消息」。
		crossTitleIndex(b.cfg.ConfigPath()).
			RecordWithAuthor(title, time.Now().UnixMilli(), "weibo", seconds, author)
		log.Printf("[Weibo] 已登记首发：%q（作者=%s %d秒）",
			truncate(title, 40), author, seconds)
	}
}

// wireWeiboCrossDedupe 把去重钩子装到微博监控上。
//
// 装配遗漏的最坏后果只是「多推」（未注入时 gate 为 nil，一律放行），
// 不会静默变成漏推 —— 这是刻意的兜底方向。
func (b *Bot) wireWeiboCrossDedupe() {
	if b.weiboMonitor == nil {
		return
	}
	b.weiboMonitor.SetCrossDedupe(b.weiboCrossGate(), b.weiboCrossRec())
	log.Printf("[Weibo] 跨平台去重已接线（与抖音/B站/TikTok 共用同一索引）")
}

// formatMS 把毫秒时间戳格式化成月-日 时:分，用于去重日志。
func formatMS(ms int64) string {
	if ms <= 0 {
		return "未知时间"
	}
	return time.UnixMilli(ms).Format("01-02 15:04")
}

// xVideoAlreadySent 判断 X 这条推文里的视频本体是否已被别的平台发过。
//
// ★ 用户 2026-10-05 口径：「视频本体只发第一次；20 分钟内这个时长的视频
//
//	就不下载、不发送；文字、封面这些都是会发的。」
//
// 时长取 X API 的 duration_ms（xmonitor.Media.DurationMS），
// **不需要下载**即可判定。
// 转推（Reposted）里的视频同样算 —— 那也是视频本体。
//
// 取不到时长（纯图片推文 / API 没给）时返回 false：没有视频可跳，
// 或者宁可多发一次也不能因为信息缺失就吞掉内容。
func xVideoAlreadySent(configPath string, event xmonitor.Event) bool {
	if configPath == "" {
		return false
	}
	seconds := xLongestVideoSeconds(event)
	if seconds <= 0 {
		return false
	}
	// 告诉去重器本方是 X：候选来自其它平台时才算跨平台镜像。
	dedupe.SetActiveSource("x")
	firstSeen, ok := crossTitleIndex(configPath).MatchWithAuthor(
		xDedupeTitle(event), seconds, xAuthorName(event), event.Time*1000)
	if !ok {
		return false
	}
	if event.Time <= 0 || firstSeen >= event.Time*1000 {
		return false
	}
	return true
}

// xLongestVideoSeconds 取这条推文（含转推）里最长的视频时长（秒）。
func xLongestVideoSeconds(event xmonitor.Event) int {
	best := 0
	collect := func(ms []xmonitor.Media) {
		for _, m := range ms {
			if m.Kind != "video" && m.Kind != "gif" && m.Kind != "animated" {
				continue
			}
			if s := int(m.DurationMS / 1000); s > best {
				best = s
			}
		}
	}
	collect(event.Media)
	if event.Reposted != nil {
		collect(event.Reposted.Media)
	}
	return best
}

// xDedupeTitle 取去重用的标题：转推用原帖标题（转推与原视频是同一条内容）。
func xDedupeTitle(event xmonitor.Event) string {
	if event.Reposted != nil {
		if t := strings.TrimSpace(event.Reposted.Body); t != "" {
			return t
		}
	}
	return strings.TrimSpace(event.Body)
}

// xAuthorName 取作者标识：跟其它平台一样用**显示昵称**，跨平台才对得齐。
func xAuthorName(event xmonitor.Event) string {
	name := strings.TrimSpace(event.Author.Name)
	if event.Reposted != nil && event.Reposted.Author.Name != "" && name == "" {
		name = strings.TrimSpace(event.Reposted.Author.Name)
	}
	return dedupe.CanonicalGroupName(name)
}
