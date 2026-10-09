package logic

import (
	"testing"
	"time"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/dedupe"
)

// ★★ 用 zzIdx(t) 而不是 crossTitleIndex(...)：后者是**全局单例**
//   （crossOnce 只会执行一次，传什么 configPath 都返回同一个索引），
//   测试里用它会写进生产索引文件并与其它用例互相污染 ——
//   表现为「单跑通过、全量跑失败」（2026-10-08 实际踩到）。
//   记忆里也记着这条：重置单例测路径行不通，判定必须能注入 Index。

// 线上漏判一（2026-10-08 11:01）：TikTok 11:01:10 推送，B站 11:01:12 扫到
// 同一条（18 秒），只差 2 秒 ⇒ 旧判定 `firstSeen < seenAt-3000` 要求差
// 3 秒以上 ⇒ 放行 ⇒ 同一条视频两边都下了本体。
func TestZZCrossPlatform2sApartMustSkip(t *testing.T) {
	ix := zzIdx(t)

	tiktokTitle := "*anthem of the week* #Hearts2Hearts #하츠투하츠   #STELLA #A_NA #YE_ON"
	biliTitle := "【Hearts2Hearts】*anthem of the week*"

	if g := dedupe.GroupKeys(biliTitle, "Hearts2Hearts"); len(g) == 0 {
		t.Fatal("B站标题应能提取出团名")
	}

	seenAt := time.Date(2026, 10, 8, 11, 1, 10, 0, time.Local).UnixMilli()
	dedupe.SetActiveSource("tiktok")
	ix.RecordWithAuthor(tiktokTitle, seenAt, "tiktok", 18, "Hearts2Hearts")

	// B站 11:01:12 扫到
	biliSeenAt := seenAt + 2000
	d := bilibili.Dynamic{Kind: "video", Title: biliTitle, Seconds: 18, Author: "Hearts2Hearts"}
	if !bilibiliShouldSkipVideo(ix, d, biliSeenAt) {
		t.Error("❌ 只差 2 秒的跨平台镜像应被判重（B站应跳过视频本体）")
	}
}

// 同一标题+时长、隔 14 小时 ⇒ 一级窗口（3 天）内，**应判重**。
//
// 最初这个用例断言「必须放行」，那是「一级也用 20 分钟窗口」时的语义；
// 用户 2026-10-08 改了口径：「如果间隔太短，比如一天（甚至不到 24 小时），
// 那都还好」⇒ 一级窗口放宽到 3 天。
func TestZZFingerprintWithinThreeDaysShouldDedupe(t *testing.T) {
	ix := zzIdx(t)

	desc := "so you coming back or what \n#Hearts2Hearts #하츠투하츠 #H2H #JUUN #IAN"
	// 昨天 21:06 TikTok 登记
	old := time.Date(2026, 10, 7, 21, 6, 39, 0, time.Local).UnixMilli()
	dedupe.SetActiveSource("tiktok")
	ix.RecordWithAuthor(desc, old, "tiktok", 12, "Hearts2Hearts")

	dedupe.SetActiveSource("douyin") // activeSource 是包级变量，必须显式声明
	post := zzMakeDyPost(desc, 12, time.Date(2026, 10, 8, 11, 10, 25, 0, time.Local))
	if !douyinVideoAlreadySentAt(ix, post, time.Now().UnixMilli()) {
		t.Error("❌ 隔 14 小时、标题+时长完全一致 ⇒ 应判重（一级窗口 3 天）")
	}
}

// 窗口内的同名同长**仍然**要去重（别把一级判定改废了）。
func TestZZFreshFingerprintStillDedupes(t *testing.T) {
	ix := zzIdx(t)

	desc := "so you coming back or what \n#Hearts2Hearts #H2H #JUUN #IAN"
	// ★ seenAt 必须**晚于**登记时刻，不能两次都取 time.Now()。
	//   otherSentFirst 的语义是「对方登记时刻 < 我方采集时刻」；
	//   两次 time.Now() 会撞成同一毫秒（gap=0）⇒ 判成我方首发。
	now := time.Now()
	old := now.Add(-3 * time.Minute).UnixMilli()
	dedupe.SetActiveSource("tiktok")
	ix.RecordWithAuthor(desc, old, "tiktok", 12, "Hearts2Hearts")

	// ★ 必须显式声明本方是 douyin —— dedupe.activeSource 是**包级变量**，
	//   同一个进程里跑完别的平台用例后会残留上次的值
	//   （历史踩过：单跑这个用例通过、全量跑失败）。
	dedupe.SetActiveSource("douyin")
	post := zzMakeDyPost(desc, 12, now)
	if !douyinVideoAlreadySentAt(ix, post, now.Add(time.Second).UnixMilli()) {
		t.Error("❌ 3 分钟前的同名同长镜像应被判重")
	}
}

func zzMakeDyPost(desc string, dur int, ct time.Time) douyinPost {
	p := douyinPost{
		Desc:       desc,
		Duration:   dur,
		VideoURL:   "https://example.com/v.mp4",
		Type:       "video",
		CreateTime: ct.Unix() / 1000,
		Nickname:   "Hearts2Hearts",
	}
	return p
}

// ★ 间隔超过 3 天 ⇒ 即使标题和时长完全一样也**不能**跳过（用户 2026-10-08 定稿）。
//
// 用户原话：「如果间隔在 3 天以上，即使标题和时长一样，这个视频也不能被跳过。」
func TestZZFingerprintOlderThanThreeDaysMustNotDedupe(t *testing.T) {
	ix := zzIdx(t)
	desc := "so you coming back or what \n#Hearts2Hearts #하츠투하츠 #H2H #JUUN #IAN"
	old := time.Now().Add(-4 * 24 * time.Hour).UnixMilli()
	dedupe.SetActiveSource("tiktok")
	ix.RecordWithAuthor(desc, old, "tiktok", 12, "Hearts2Hearts")

	dedupe.SetActiveSource("douyin")
	now := time.Now()
	post := zzMakeDyPost(desc, 12, now)
	if douyinVideoAlreadySentAt(ix, post, now.Add(time.Second).UnixMilli()) {
		t.Error("❌ 隔 4 天、标题+时长完全一致 ⇒ 不是同一条，必须发视频本体")
	}
}

// ★ 标题相同但**时长不同** ⇒ 不是同一条（用户定稿第①条：「即使标题一样，
//
//	我们也是要对比时长的」）。
func TestZZSameTitleDifferentDurationMustNotDedupe(t *testing.T) {
	ix := zzIdx(t)
	desc := "so you coming back or what \n#Hearts2Hearts #하츠투하츠 #H2H #JUUN #IAN"
	now := time.Now()
	dedupe.SetActiveSource("tiktok")
	ix.RecordWithAuthor(desc, now.UnixMilli(), "tiktok", 12, "Hearts2Hearts")

	dedupe.SetActiveSource("douyin")
	post := zzMakeDyPost(desc, 25, now) // 时长 25 秒 ≠ 对方 12 秒
	if douyinVideoAlreadySentAt(ix, post, now.Add(time.Second).UnixMilli()) {
		t.Error("❌ 标题相同但时长不同（12s vs 25s）⇒ 不是同一条，必须发视频本体")
	}
}
