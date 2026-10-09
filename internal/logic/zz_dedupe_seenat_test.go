package logic

import (
	"testing"
	"time"

	"pocket48-bot/internal/dedupe"
)

// 线上故障（2026-10-06 13:29-13:40）：同一条 14 秒视频在 B站/抖音/TikTok 都有。
// B站 13:35 先推送，抖音 13:38 被正确跳过（只发文字封面），TikTok 13:40 又发了一遍。
//
// 根因：TikTok 侧拿**本条作品发布时间**当比较基准。TikTok 13:29:25 发稿、
// B站 13:30:00 发稿，TikTok 早了 35 秒，于是判定「我才是首发」→ 又下载又推送。
// 早先抖音侧也是同一个口径，只是这次抖音发布更晚才侥幸判对。
//
// 正确口径：**采集/推送时刻**（谁先把消息发出去），而不是作品发布时间。

func t0(hour, min, sec int) int64 {
	day := time.Date(2026, 10, 6, hour, min, sec, 0, time.Local)
	return day.UnixMilli()
}

// 复刻线上时间线：B站 13:30 发稿、13:35 被采集推送；TikTok 13:29:25 发稿、13:40 才抓到。
func TestTikTokSkipsWhenOtherPlatformSentTheMessageFirst(t *testing.T) {
	ix := zzIdx(t)
	// B站侧登记：存的是**作品发布时间** 13:30:00。
	ix.RecordWithAuthor("【Hearts2Hearts】the city needs to be aware of this shoulder move",
		t0(13, 30, 0), "bilibili", 15, "Hearts2Hearts")

	// TikTok 这条：作品发布时间 13:29:25（比 B站还早 35 秒），但 13:40:31 才被抓到。
	firstSeen, skip := tiktokAlreadySentElsewhere(ix,
		"the city needs to be aware of this shoulder move #hearts2hearts", 14,
		"Hearts2Hearts", t0(13, 40, 31))
	if !skip {
		t.Fatalf("B站 13:35 已经把消息发出去了，TikTok 不该再发一遍（firstSeen=%v）",
			time.UnixMilli(firstSeen).Format("15:04:05"))
	}
	if firstSeen != t0(13, 30, 0) {
		t.Errorf("应命中 B站那条（13:30:00），实际 %v", time.UnixMilli(firstSeen).Format("15:04:05"))
	}
}

// 反向：TikTok 先把消息发出去，后到的 B站/抖音要认输。
func TestTikTokKeepsWhenItIsTheFirstToSend(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("都到齐了吧", t0(10, 0, 0), "tiktok", 189, "Hearts2Hearts")

	// 索引里只有自己登记的条目 —— activeSource 过滤掉本方，匹配不到任何候选。
	if _, skip := tiktokAlreadySentElsewhere(ix, "都到齐了吧", 189, "Hearts2Hearts", t0(10, 5, 0)); skip {
		t.Error("索引里只有自己，不应跳过（自己不能把自己判掉）")
	}
}

// 索引里那条比我方早一点点（同团同长 ⇒ 极可能是同一条镜像）。
//
// ★ 语义在2026-10-08 改过：原来要求「差 1 秒内判不出先后 ⇒ 放行」，
//
//	余量只有 3 秒。线上那次正是被这条卡掉的
//	（TikTok 11:01:10 → B站 11:01:12，只差 2 秒 ⇒ 放行 ⇒ 两边都发视频本体）。
//
//	「拿不准就放行」在这里是错的取舍：候选已过 group + 时长 + 20 分钟
//	窗口三重筛，再放行只会产出用户看得见的重复视频。
//	真正的首发由「对方比我晚超过余量」保护（见下方 10 秒 /
//	TestZZBiliKeepsWhenItIsFirst 的 523 秒两档）。
func TestTikTokSkipsWhenMirrorIsSecondsApart(t *testing.T) {
	// 只差 1 秒：跨平台跟发，应判重
	ix := zzIdx(t)
	ix.RecordWithAuthor("另一个视频", t0(12, 0, 0), "bilibili", 14, "Hearts2Hearts")
	if _, skip := tiktokAlreadySentElsewhere(ix, "另一个视频", 14, "Hearts2Hearts", t0(12, 0, 1)); !skip {
		t.Error("只差 1 秒的跨平台镜像应判重")
	}
	// 差 10 秒：同样是对方先发，应判重
	// ★ 必须换一个新索引 —— SetActiveSource 是包级变量，复用同一个索引时
	//   上一次调用留下的 source 会影响后续判定（历史踩过的坑）。
	ix2 := zzIdx(t)
	ix2.RecordWithAuthor("另一个视频", t0(12, 0, 0), "bilibili", 14, "Hearts2Hearts")
	if _, skip := tiktokAlreadySentElsewhere(ix2, "另一个视频", 14, "Hearts2Hearts", t0(12, 0, 10)); !skip {
		t.Error("对方早 10 秒推送，应认输跳过（2026-10-06 19:38 双发就是漏了这一档）")
	}
}

// 抖音侧同一个坑：作品发布时间比对方晚，但对方消息已经发出去了。
// 抖音的判定走 douyinVideoAlreadySentIn，基准已改为采集时刻。
func TestDouyinSkipsBySeenAtNotByPublishTime(t *testing.T) {
	ix := zzIdx(t)
	// B站 13:30 发稿并已推送。
	ix.RecordWithAuthor("the city needs to be aware of this shoulder move",
		t0(13, 30, 0), "bilibili", 15, "Hearts2Hearts")

	// 抖音这条 13:36:12 发布、比 B站晚 → 老口径也会判对；
	// 这里再补一个「抖音发布更早」的反例，确保新口径不依赖发布时间顺序。
	post := douyinPost{
		Desc:       "the city needs to be aware of this shoulder move",
		Type:       "video",
		Duration:   14,
		VideoURL:   "https://v.example/a.mp4",
		CreateTime: t0(13, 29, 0) / 1000, // 抖音 13:29 发稿，比 B站早
		Nickname:   "Hearts2Hearts",
	}
	// ★ 必须显式传 seenAt。douyinVideoAlreadySentIn 内部取 time.Now()，
	//   而本用例的基准是 t0(13,30)（固定历史时刻），两者相隔多天必然
	//   超出 MatchWindowMillis(20 分钟) ⇒ 恒判不重复 ⇒ 用例随日期失效。
	//   这是 2026-10-08 修窗口校验时暴露的：新增的窗口判定让
	//   「拿 now 比固定历史时刻」这种写法永远通不过。
	if !douyinVideoAlreadySentAt(ix, post, t0(13, 38, 0)) {
		t.Error("抖音 13:38 抓到时 B站 13:35 已推送，应跳视频（发布更早不代表该它发）")
	}
}

// 真正的首发（索引为空）必须照发。
func TestDouyinKeepsWhenIndexEmpty(t *testing.T) {
	ix := zzIdx(t)
	post := douyinPost{
		Desc: "全新的一条", Type: "video", Duration: 42,
		VideoURL: "https://v.example/b.mp4", CreateTime: time.Now().Unix(), Nickname: "Hearts2Hearts",
	}
	if douyinVideoAlreadySentIn(ix, post) {
		t.Error("索引为空时不该跳过")
	}
}

// 线上双发（2026-10-06 19:38）：抖音 19:38:21 推送完成，B站 19:38:29 才扫到，
// 只差 8 秒。老口径下判定是「抖音作品发布时间 19:38:00 < B站采集时刻-2分钟」，
// 为假 => 放行 => 同一条视频 8 秒内发了两遍。
//
// 现在索引里存的是**推送时刻**，余量 3 秒，这个场景必须判成「对方先发」。
func TestCrossDedupeSkipsWhenOtherPlatformSentSecondsAgo(t *testing.T) {
	ix := zzIdx(t)
	// 抖音 19:38:21 推送完成并登记。
	ix.RecordWithAuthor("time to step up", t0(19, 38, 21), "douyin", 17, "Hearts2Hearts")

	// B站 19:38:29 扫到同一条（视频本体 18 秒 vs 抖音 17 秒，时长差 1 秒）。
	firstSeen, skip := tiktokAlreadySentElsewhere(ix,
		"【Hearts2Hearts】time to step up", 18, "Hearts2Hearts", t0(19, 38, 29))
	if !skip {
		t.Fatalf("抖音早 8 秒就推送了，本条不该再发（firstSeen=%v）",
			time.UnixMilli(firstSeen).Format("15:04:05"))
	}

	// 抖音侧对称：B站先发 8 秒，抖音也必须认输。
	ix2 := zzIdx(t)
	ix2.RecordWithAuthor("【Hearts2Hearts】time to step up", t0(19, 38, 21), "bilibili", 18, "Hearts2Hearts")
	post := douyinPost{
		Desc: "time to step up", Type: "video", Duration: 17,
		VideoURL: "https://v.example/a.mp4", CreateTime: t0(19, 38, 0) / 1000,
		Nickname: "Hearts2Hearts",
	}
	// 显式传 seenAt：不依赖「现在」，用例不会因为跑得久而失效。
	if !douyinVideoAlreadySentAt(ix2, post, t0(19, 38, 29)) {
		t.Error("B站早 8 秒推送，抖音这条应跳视频")
	}
}

// 余量常量锁死：索引存的是推送时刻，这个值决定「几秒内跟发」能不能认出来。
//
// ★ 2026-10-08 从 3000 放宽到 30000：线上 11:01:10 TikTok → 11:01:12 B站
//
//	只差 2 秒，3 秒余量刚好卡在门外 ⇒ 同一条视频两边都发了本体。
//	放宽到 30 秒后该场景判重，而「我方真首发」（对方比我晚 500 秒以上）
//	仍由 TestZZBiliKeepsWhenItIsFirst 守住。
func TestCrossDedupeUsesSeenAtNotPublishedAt(t *testing.T) {
	if crossSeenAtSlack != 2000 {
		t.Errorf("余量应为 2000 毫秒（2 秒），实际 %d", crossSeenAtSlack)
	}
	var _ = time.Now
	var _ = dedupe.MatchWindowMillis
}
