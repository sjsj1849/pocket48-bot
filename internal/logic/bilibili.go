package logic

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/napcat"
)

const bilibiliTick = 5 * time.Second

func bilibiliZone() *time.Location { return time.FixedZone("CST", 8*3600) }

func bilibiliUpName(sub bilibili.Subscription, fallback string) string {
	if name := strings.TrimSpace(sub.Name); name != "" {
		return name
	}
	if name := strings.TrimSpace(fallback); name != "" {
		return name
	}
	if uid := strings.TrimSpace(sub.UID); uid != "" {
		return "UID:" + uid
	}
	return "UP主"
}

// bilibiliKindLabel 返回消息里的类型小标签。
//
// 绝大多数情况下返回空串：UP 主名字已经在标题行里了，再写一句
// 「发布了新视频」纯属套话，用户明确要求去掉。只有转发这种类型自带
// 动作语义、且标题里看不出来的，才补一个短标签。
func bilibiliKindLabel(kind string) string {
	if kind == "forward" {
		return "转发"
	}
	return ""
}

func bilibiliTrim(text string, limit int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// bilibiliCleanTitle 清洗 B站 视频/专栏标题：
//  1. 去掉开头重复的「【UP主名】」前缀——UP 主名已经在消息头上了，
//     标题里再带一遍纯属噪音（用户明确要求删除）。
//  2. 把标题里的分隔竖线（含全角｜）换成换行，让长标题分段更易读。
//     B站 长视频标题普遍是「…舞台! ｜《ICONIC HEART》日本宣传活动」这种结构，
//     竖线两侧本就是两个语义单元，换行比竖线清楚得多。
func bilibiliCleanTitle(title, upName string) string {
	out := title
	// 去掉 【xxx】 开头（半角与全角都处理），允许多层如「【A】【B】标题」。
	for i := 0; i < 3; i++ {
		trimmed := strings.TrimSpace(out)
		if !strings.HasPrefix(trimmed, "【") {
			break
		}
		// 注意：strings.Index 返回的是**字节**下标，而 】 是 3 字节的 UTF-8 字符。
		// 早先写成 trimmed[end+1:] 只跳了 1 字节，切在字符中间，
		// 结果每条标题前面都残留一个乱码字符（\x80\x91）。
		// 这里改为按 rune 定位，并用 len(string(r)) 拿到该字符的真实字节长度。
		idx := strings.IndexRune(trimmed, '】')
		if idx < 0 {
			break
		}
		after := trimmed[idx:]
		_, size := utf8.DecodeRuneInString(after)
		out = strings.TrimSpace(after[size:])
	}
	// 竖线 → 换行。｜(U+FF5C) 与 |(U+007C) 都处理，连换行后不产生空行。
	parts := strings.FieldsFunc(out, func(r rune) bool {
		return r == '\uff5c' || r == '|' || r == '\u2502' || r == '\u2503'
	})
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			cleaned = append(cleaned, p)
		}
	}
	return strings.Join(cleaned, "\n")
}

// bilibiliDynamicGroups renders one dynamic: 标题 + 封面 + 链接 + 时间。
// 飞书会把裸链接抽成跳转按钮，并清掉残留的「B站链接：」标签。
func bilibiliDynamicGroups(sub bilibili.Subscription, d bilibili.Dynamic) [][]interface{} {
	name := bilibiliUpName(sub, d.Author)
	lines := []string{fmt.Sprintf("【%s|B站】", name)}
	if label := bilibiliKindLabel(d.Kind); label != "" {
		lines = append(lines, label)
	}
	if title := bilibiliTrim(bilibiliCleanTitle(d.Title, name), 200); title != "" {
		// 标题内部可能含换行，单独作为一段保留排版。
		for _, seg := range strings.Split(title, "\n") {
			if seg = strings.TrimSpace(seg); seg != "" {
				lines = append(lines, seg)
			}
		}
	}
	if text := bilibiliTrim(bilibiliCleanTitle(d.Text, name), 300); text != "" {
		body := strings.Join(strings.FieldsFunc(text, func(r rune) bool { return r == '\n' }), " ")
		if body != "" && body != strings.Join(strings.FieldsFunc(strings.TrimSpace(d.Title), func(r rune) bool {
			return r == '\n' || r == '\uff5c' || r == '|' || r == '\u2502' || r == '\u2503'
		}), " ") {
			lines = append(lines, body)
		}
	}
	// ★ 不再输出「时长 xxx」（2026-10-05 用户要求，此前已要求过一次）。
	//
	// 时长每条都在变、对「发了新动态」这个通知没有信息量，
	// 反而把标题挤下去。跨平台去重仍用时长比对（走 d.Length 内部字段），
	// 不依赖正文里写出来的这一行。

	card := []interface{}{}
	if sub.AtAll {
		card = append(card, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	card = append(card, napcat.TextSegment(strings.Join(lines, "\n")))
	if cover := strings.TrimSpace(d.Cover); strings.HasPrefix(cover, "http") {
		card = append(card, napcat.TextSegment("\n"), napcat.ImageSegment(cover))
	}
	footer := ""
	if link := strings.TrimSpace(d.URL); link != "" {
		footer = "B站链接：" + link
	}
	// opus 图文流不带发布时间，此时用抓取时间兜底，避免整条消息没有时间。
	stamp := time.Now().In(bilibiliZone())
	if d.Time > 0 {
		stamp = time.UnixMilli(d.Time).In(bilibiliZone())
	}
	if text := stamp.Format("2006-01-02 15:04:05"); footer != "" {
		footer += "\n\n" + text
	} else {
		footer = text
	}
	card = append(card, napcat.TextSegment("\n\n"+footer))
	return [][]interface{}{card}
}

// bilibiliLiveGroups renders 开播/下播 通知。
func bilibiliLiveGroups(sub bilibili.Subscription, room bilibili.LiveRoom, online bool) [][]interface{} {
	name := bilibiliUpName(sub, "")
	lines := []string{fmt.Sprintf("【%s|B站】", name)}
	if online {
		lines = append(lines, "🔴 开播了")
	} else {
		lines = append(lines, "⚫ 下播了")
	}
	if title := bilibiliTrim(room.Title, 200); title != "" {
		lines = append(lines, title)
	}

	card := []interface{}{}
	if sub.AtAll {
		card = append(card, napcat.AtSegment("all"), napcat.TextSegment("\n"))
	}
	card = append(card, napcat.TextSegment(strings.Join(lines, "\n")))
	if cover := strings.TrimSpace(room.Cover); strings.HasPrefix(cover, "http") {
		card = append(card, napcat.TextSegment("\n"), napcat.ImageSegment(cover))
	}
	footer := ""
	if link := strings.TrimSpace(room.URL()); link != "" {
		footer = "B站链接：" + link
	}
	stamp := time.Now().In(bilibiliZone()).Format("2006-01-02 15:04:05")
	if footer != "" {
		footer += "\n\n" + stamp
	} else {
		footer = stamp
	}
	card = append(card, napcat.TextSegment("\n\n"+footer))
	return [][]interface{}{card}
}

func bilibiliDynamicSeconds(cfg bilibili.Settings) int {
	if cfg.PollSeconds < bilibili.MinPollSeconds {
		return bilibili.DefaultPollSeconds
	}
	return cfg.PollSeconds
}

func bilibiliVideoSeconds(cfg bilibili.Settings) int {
	if cfg.VideoPollSeconds < bilibili.MinVideoPollSeconds {
		return bilibili.DefaultVideoPollSeconds
	}
	return cfg.VideoPollSeconds
}

func bilibiliLiveSeconds(cfg bilibili.Settings) int {
	if cfg.LivePollSeconds < bilibili.MinLivePollSeconds {
		return bilibili.DefaultLivePollSeconds
	}
	return cfg.LivePollSeconds
}

// bilibiliSendShortVideo 把短视频本体作为视频消息内嵌发出。
//
// 取直链走 x/web-interface/view（拿 cid）+ x/player/playurl
// （platform=html5&fnval=0 返回单文件 mp4）。任何一步失败都只记日志，
// 绝不影响主消息 —— 主消息已经把标题、封面、链接发出去了。
func (b *Bot) bilibiliSendShortVideo(ctx context.Context, client *bilibili.Client, sub bilibili.Subscription, d bilibili.Dynamic) {
	bvid := bilibiliBVID(d)
	if bvid == "" {
		return
	}
	source, err := client.VideoPlayURL(ctx, bvid)
	if err != nil {
		log.Printf("[Bilibili] 取视频直链失败 bvid=%s: %v", bvid, err)
		return
	}
	// 上限 80MB：短视频最长 10 分钟，长视频体积可达数十 MB。
	// 超出就只发链接，避免 IM 消息体超限。
	if source.Size > 0 && source.Size > 80<<20 {
		log.Printf("[Bilibili] 视频体积过大，跳过内嵌 bvid=%s size=%d", bvid, source.Size)
		return
	}

	// DASH 高画质路径已经合并好本地文件，直接用，不要再下载一遍。
	local := source.LocalPath()
	if local == "" {
		local, err = downloadMediaFile(source.URL)
		if err != nil {
			log.Printf("[Bilibili] 视频下载失败 bvid=%s: %v", bvid, err)
			return
		}
	}
	b.sendToTargetIDs(sub.TargetIDs, []napcat.MessageSegment{napcat.VideoSegment(local, "")})
	log.Printf("[Bilibili] 已内嵌短视频 bvid=%s 时长=%ds 体积=%d 分辨率=%dx%d 编码=%s",
		bvid, d.Seconds, source.Size, source.Width, source.Height, source.Codec)
}

// bilibiliBVID 从 Dynamic 里取出 BV 号。ID 形如 "av:BV1xx411c7mD"。
func bilibiliBVID(d bilibili.Dynamic) string {
	if v := strings.TrimSpace(d.URL); v != "" {
		if idx := strings.LastIndex(v, "/"); idx >= 0 && idx+1 < len(v) {
			candidate := v[idx+1:]
			if strings.HasPrefix(candidate, "BV") {
				return candidate
			}
		}
	}
	if v := strings.TrimSpace(d.ID); strings.HasPrefix(v, "av:") {
		return strings.TrimPrefix(v, "av:")
	}
	return ""
}

func (b *Bot) bilibiliSend(sub bilibili.Subscription, groups [][]interface{}) {
	if strings.EqualFold(b.cfg.MediaDelivery, "local") {
		b.localizeMessageGroups(groups)
	}
	for _, group := range groups {
		b.sendToTargetIDs(sub.TargetIDs, group)
	}
}

// bilibiliMerge sorts a batch oldest-first so notifications arrive in the order
// the UP 主 posted them. Items without a timestamp (opus feed) go last.
func bilibiliMerge(items []bilibili.Dynamic) []bilibili.Dynamic {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].Time, items[j].Time
		if left == 0 {
			left = 1 << 62
		}
		if right == 0 {
			right = 1 << 62
		}
		return left < right
	})
	return items
}

type bilibiliFetcher func(context.Context, *bilibili.Client, bilibili.Subscription) ([]bilibili.Dynamic, error)

// 包装成函数值，避免直接使用方法表达式（签名参数顺序不同）。
func bilibiliFetchOpus(ctx context.Context, client *bilibili.Client, sub bilibili.Subscription) ([]bilibili.Dynamic, error) {
	return client.SpaceOpus(ctx, sub.UID)
}
func bilibiliFetchArticles(ctx context.Context, client *bilibili.Client, sub bilibili.Subscription) ([]bilibili.Dynamic, error) {
	return client.SpaceArticles(ctx, sub.UID)
}

// bilibiliFetchVideos 抓投稿视频。
//
// 单靠合集会漏：实测 Hearts2Hearts 有 1335 条投稿，合集接口只覆盖 76 条，
// 2026-09-28/29/30 那几条就没进任何合集。因此这里用**双来源合并**：
//
//  1. seasons_series_list（合集）—— 匿名稳定，带标题/时长/播放量，团综主力来源。
//  2. wbi/arc/search（投稿列表）—— 覆盖合集外的投稿，但机房 IP 上不稳定，
//     时而返回 -403、时而只给 1 条，能拿到就合并，拿不到不报错。
//  3. feed/space（空间动态流）—— 投稿后的动态几乎实时生成，没有投稿列表的
//     索引延迟，是新投稿最早的可见入口。需登录 Cookie。
//
// 另加 navnum 兜底：它给出投稿总数且极其稳定。若「已见到的投稿数」明显少于
// 总数，说明两个来源都漏了，此时返回一条不含标题的提示（与原先降级行为一致），
// 至少不会静默丢掉「发了新长视频」这件事。
func bilibiliFetchVideos(ctx context.Context, client *bilibili.Client, sub bilibili.Subscription) ([]bilibili.Dynamic, error) {
	merged := map[string]bilibili.Dynamic{}
	var firstErr error

	// 来源 1：合集（匿名稳定，长视频主要来源）
	if items, err := client.SpaceSeasonVideos(ctx, sub.UID, 0); err != nil {
		if firstErr == nil {
			firstErr = err
		}
	} else {
		for _, d := range items {
			merged[d.ID] = d
		}
	}

	// 来源 2：投稿列表（覆盖合集外投稿，不稳定但能补漏）
	if items, err := client.SpaceVideos(ctx, sub.UID); err == nil {
		for _, d := range items {
			// 合集来源已有同一条时保留合集版本（时长更规范）；否则补入。
			if existing, ok := merged[d.ID]; ok {
				if existing.Length == "" {
					existing.Length = d.Length
					existing.Time = d.Time
				}
				merged[d.ID] = existing
				continue
			}
			// 这里**不再按时长丢弃**。原先用 MinVideoSeconds 一刀切掉短视频，
			// 等于假定「所有短视频都在抖音推过了」，但实际存在 B 站比抖音先发的情况，
			// 那些内容会被这道过滤器静默挡掉。短视频是否该推，改为在推送前
			// 用跨平台标题索引判断（见 bilibiliPollGroup）。
			merged[d.ID] = d
		}
	} else if firstErr == nil {
		firstErr = err
	}

	// 来源 3：空间动态流（feed/space）——投稿发布后几乎实时可见，用来消除投稿
	// 列表的索引延迟（实测新投稿要 15 分钟后才进 arc/search）。带 Cookie 时稳定；
	// 拿不到不影响前两个来源。
	if items, err := client.SpaceFeedVideos(ctx, sub.UID); err != nil {
		log.Printf("[Bilibili] 动态流来源失败 uid=%s: %v", sub.UID, err)
		if firstErr == nil {
			firstErr = err
		}
	} else {
		log.Printf("[Bilibili] 慢源补充：动态流 %d 条（用于补齐慢源漏掉的条目）", len(items))
		for _, d := range items {
			existing, ok := merged[d.ID]
			if !ok {
				merged[d.ID] = d
				continue
			}
			// 同一条投稿：其它来源缺时长/播放量/时间时用动态流补上。
			if existing.Length == "" {
				existing.Length = d.Length
				existing.Seconds = d.Seconds
			}
			if existing.View <= 0 {
				existing.View = d.View
			}
			if existing.Time == 0 {
				existing.Time = d.Time
			}
			merged[d.ID] = existing
		}
	}

	if len(merged) > 0 {
		// 两个来源都拿到内容时，不再因单个接口失败而报错。
		return values(merged), nil
	}

	// 兜底：都没拿到时用投稿计数探测，判断是否真的发了新投稿。
	if count, err := client.SpaceVideoCount(ctx, sub.UID); err == nil && count > 0 {
		return []bilibili.Dynamic{{
			ID:   fmt.Sprintf("nav:%d", count),
			Kind: "video",
			URL:  "https://space.bilibili.com/" + sub.UID + "/video",
			Text: "（B 站投稿接口暂时取不到详细列表，若刚发了新视频请点链接前往空间查看）",
		}}, nil
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// bilibiliFetchFeedVideos 只抓动态流一个源。
//
// 为什么要单独拆出来用更短的周期：三个来源里只有动态流是实时的
// （实测新投稿在 arc/search 里要等 15 分钟索引），另外两个慢源每秒都在
// 拉一遍纯属浪费配额。2026-10-06 实测「发布 -> 动态流可读」本身就要
// 3~4 分钟（B站自己转码/收录），我们能压缩的只有自己的轮询等待：
// 60 秒 -> 30 秒，平均少等 15 秒，而总请求量基本不变
// （慢源仍是 60 秒一次）。
func bilibiliFetchFeedVideos(ctx context.Context, client *bilibili.Client, sub bilibili.Subscription) ([]bilibili.Dynamic, error) {
	items, err := client.SpaceFeedVideos(ctx, sub.UID)
	if err != nil {
		return nil, err
	}
	log.Printf("[Bilibili] 动态流来源 uid=%s 拿到 %d 条投稿", sub.UID, len(items))
	return items, nil
}

// bilibiliFeedSeconds 是动态流快源的轮询间隔。
//
// 不做成配置项：动态流是唯一实时源，30 秒是「明显更快但远没到打脸 B站」的
// 保守值，真需要调时改这一个常量即可（面板上再加一项反而容易被人调到过激）。
const bilibiliFeedSeconds = 30

// values 把去重后的 map 转成切片，顺序不敏感（调用方会按时间排序）。
func values(m map[string]bilibili.Dynamic) []bilibili.Dynamic {
	out := make([]bilibili.Dynamic, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	return out
}

// bilibiliPollGroup runs one source group (图文+专栏 或 视频投稿) over every
// eligible subscription. A rate-limit response aborts the whole group for this
// cycle so we stop adding pressure on the shared IP.
func (b *Bot) bilibiliPollGroup(ctx context.Context, client *bilibili.Client, cfg bilibili.Settings, dir string, state *bilibili.Runtime, status *bilibili.Status, label string, eligible func(bilibili.Subscription) bool, fetchers []bilibiliFetcher) {
	for _, sub := range cfg.Subscriptions {
		if !sub.Enabled || !eligible(sub) || sub.UID == "" || ctx.Err() != nil {
			continue
		}
		var items []bilibili.Dynamic
		var err error
		for _, fetch := range fetchers {
			var batch []bilibili.Dynamic
			batch, err = fetch(ctx, client, sub)
			if err != nil {
				break
			}
			items = append(items, batch...)
		}
		if err != nil {
			status.Targets[sub.ID] = label + "：" + err.Error()
			if status.Error == "" {
				status.Error = err.Error()
			}
			if bilibili.IsRateLimited(err) {
				// 触到限流就整组退出，等下一个周期再慢慢来。
				log.Printf("[Bilibili] %s 被限流，本轮跳过其余订阅: %v", label, err)
				return
			}
			continue
		}
		status.Events += len(items)
		cursor := state.Subscriptions[sub.ID]
		if cursor.BaselineAt == 0 {
			// 首轮：建立基线，把此刻之前的历史投稿全部记入 Seen 但不推送。
			cursor.BaselineAt = time.Now().UnixMilli()
			log.Printf("[Bilibili] 订阅 %s 建立增量基线: %s（此刻前的历史投稿不再补推）", sub.UID, time.UnixMilli(cursor.BaselineAt).Format("2006-01-02 15:04:05"))
		}
		pending := bilibili.PendingDynamics(cursor, items)
		state.Subscriptions[sub.ID] = bilibili.Advance(cursor, sub.UID, items)
		if writeErr := bilibili.Write(dir, "state.json", *state); writeErr != nil {
			log.Printf("[Bilibili] 无法保存去重状态: %v", writeErr)
		}
		titleIndex := crossTitleIndex(b.cfg.ConfigPath())
		scanSeenAt := time.Now().UnixMilli()
		for _, d := range bilibiliMerge(pending) {
			if ctx.Err() != nil {
				return
			}
			// ★ 跨平台去重：**只跳视频本体**，文字与封面照发
			//   （用户 2026-10-05 口径：视频只发第一次，文字封面都发）。
			//
			// 原来是 `continue` 整条跳过 —— 那会把文字封面也吞掉。
			// 判定仍发生在**内嵌 mp4 之前**，所以命中时连下载都不会发生。
			//
			// 只有短视频参与判定：长视频（团综）别的平台不会发，不存在重复。
			skipVideo := bilibiliShouldSkipVideo(titleIndex, d, scanSeenAt)
			if skipVideo {
				log.Printf("[Bilibili] 跳过视频（%d 秒内有平台已发过同一条）: id=%s title=%s seconds=%d",
					dedupe.MatchWindowMillis/60000, d.ID, d.Title, d.Seconds)
			}
			b.bilibiliSend(sub, bilibiliDynamicGroups(sub, d))
			status.Forwarded++
			// 登记 B 侧发布时间与时长：若抖音/TikTok 稍后也发了同一条，
			// Record 会保留更早的时间戳，判定基准始终是「真正先发布的那一边」。
			// 时长一并登记，好让后续平台的二级判定（词重合 + 时长）能命中。
			if d.Kind == "video" && strings.TrimSpace(d.Title) != "" {
				// 登记的是**推送时刻**（scanSeenAt），不是 d.Time（作品发布时间）。
				// 判定方要比较的是「谁先把消息发出去」；拿发布时间登记会让
				// 早发稿的一侧永远显得更早，从而挡住真正先发出的那条。
				titleIndex.Record(d.Title, scanSeenAt, "bilibili", d.Seconds)
			}
			log.Printf("[Bilibili] %s 已入推送队列: %s uid=%s kind=%s id=%s", label, bilibiliUpName(sub, d.Author), sub.UID, d.Kind, d.ID)
			// 短视频额外内嵌 mp4；长视频只发链接，避免几十 MB 的团综占满消息体。
			// ★ 命中跨平台去重时跳过内嵌 —— 视频本体只发第一次。
			if bilibiliIsShortVideo(d) && !skipVideo {
				b.bilibiliSendShortVideo(ctx, client, sub, d)
			}
		}
		status.Targets[sub.ID] = "正常"
		// 订阅之间留间隔，降低触发限流的概率。
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// bilibiliPollLive batches the live-room query and notifies on 状态翻转。
func (b *Bot) bilibiliPollLive(ctx context.Context, client *bilibili.Client, cfg bilibili.Settings, dir string, state *bilibili.Runtime, status *bilibili.Status) {
	uids := cfg.UIDs()
	if len(uids) == 0 {
		return
	}
	rooms, err := client.LiveStatus(ctx, uids)
	if err != nil {
		if status.Error == "" {
			status.Error = err.Error()
		}
		return
	}
	changed := false
	for _, sub := range cfg.Subscriptions {
		if !sub.Enabled || !sub.Live || sub.UID == "" {
			continue
		}
		room, ok := rooms[sub.UID]
		if !ok || room.RoomID <= 0 {
			continue
		}
		cursor, event := bilibili.ObserveLive(state.Subscriptions[sub.ID], room.LiveStatus)
		state.Subscriptions[sub.ID] = cursor
		changed = true
		if event == "" {
			continue
		}
		b.bilibiliSend(sub, bilibiliLiveGroups(sub, room, event == "online"))
		status.Forwarded++
		log.Printf("[Bilibili] 直播状态变化已入推送队列: %s uid=%s room=%d event=%s", bilibiliUpName(sub, ""), sub.UID, room.RoomID, event)
	}
	if changed {
		if writeErr := bilibili.Write(dir, "state.json", *state); writeErr != nil {
			log.Printf("[Bilibili] 无法保存直播状态: %v", writeErr)
		}
	}
}

// runBilibiliLoop drives all three cadences from a single goroutine so the
// persisted cursors are only ever touched from here.
func (b *Bot) runBilibiliLoop(ctx context.Context) {
	dir := bilibili.Dir(b.cfg.ConfigPath())
	var state bilibili.Runtime
	if err := bilibili.Read(dir, "state.json", &state); err != nil {
		log.Print("[Bilibili] 无法读取去重状态，停止监控以避免重复推送")
		return
	}
	if state.Subscriptions == nil {
		state.Subscriptions = map[string]bilibili.Cursor{}
	}
	status := bilibili.Status{StartedAt: time.Now().Format(time.RFC3339), Targets: map[string]string{}}
	var nextLive, nextDynamics, nextVideo, nextFeed time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		cfg, err := bilibili.LoadSettings(dir)
		if err != nil {
			status.Error = "无法读取 B 站配置"
			status.LastCheck = time.Now().Format(time.RFC3339)
			_ = bilibili.Write(dir, "status.json", status)
		}
		if err == nil && cfg.Enabled {
			client := &bilibili.Client{Dir: dir, Cookie: cfg.Cookie}
			now := time.Now()
			status.Error = ""
			status.Events = 0
			status.Forwarded = 0
			status.Targets = map[string]string{}
			ran := false
			// 本轮跑了哪几组。视频组现在拆成快源（动态流 30 秒）与慢源
			// （合集+投稿列表 60 秒）两次调用，日志若还只写「扫描完成」，
			// 看日志时分不清刚才是哪个源扫的。
			var ranGroups []string
			if !now.Before(nextLive) {
				b.bilibiliPollLive(ctx, client, cfg, dir, &state, &status)
				nextLive = time.Now().Add(time.Duration(bilibiliLiveSeconds(cfg)) * time.Second)
				ranGroups = append(ranGroups, "直播")
				ran = true
			}
			if !now.Before(nextDynamics) {
				b.bilibiliPollGroup(ctx, client, cfg, dir, &state, &status, "动态",
					func(s bilibili.Subscription) bool { return s.Dynamics },
					[]bilibiliFetcher{bilibiliFetchOpus, bilibiliFetchArticles})
				nextDynamics = time.Now().Add(time.Duration(bilibiliDynamicSeconds(cfg)) * time.Second)
				ranGroups = append(ranGroups, "动态")
				ran = true
			}
			if !now.Before(nextFeed) {
				b.bilibiliPollGroup(ctx, client, cfg, dir, &state, &status, "视频投稿(动态流)",
					func(s bilibili.Subscription) bool { return s.Video },
					[]bilibiliFetcher{bilibiliFetchFeedVideos})
				nextFeed = time.Now().Add(time.Duration(bilibiliFeedSeconds) * time.Second)
				ranGroups = append(ranGroups, "视频投稿(动态流)")
				ran = true
			}
			if !now.Before(nextVideo) {
				b.bilibiliPollGroup(ctx, client, cfg, dir, &state, &status, "视频投稿",
					func(s bilibili.Subscription) bool { return s.Video },
					[]bilibiliFetcher{bilibiliFetchVideos})
				nextVideo = time.Now().Add(time.Duration(bilibiliVideoSeconds(cfg)) * time.Second)
				ranGroups = append(ranGroups, "视频投稿(合集+投稿列表)")
				ran = true
			}
			if ran {
				status.LastCheck = time.Now().Format(time.RFC3339)
				if status.Error == "" {
					status.LastSuccess = status.LastCheck
				}
				if writeErr := bilibili.Write(dir, "status.json", status); writeErr != nil {
					log.Printf("[Bilibili] 无法保存监控状态: %v", writeErr)
				} else if status.Error != "" {
					log.Printf("[Bilibili] 扫描异常: %s", status.Error)
				} else {
					log.Printf("[Bilibili] %s 扫描完成: %d 条内容，新内容 %d 条已入队",
						strings.Join(ranGroups, "+"), status.Events, status.Forwarded)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(bilibiliTick):
		}
	}
}
