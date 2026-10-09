package logic

import (
	"path/filepath"
	"testing"
	"time"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/dedupe"
)

// ★ 线上真实漏判（2026-10-05 20:40-20:46）：
//
//	20:40:55 抖音 推送「嘚瑟NICHEART」+ 视频 3.3MB
//	20:45:57 B 站 推送「嘚瑟NIC HEART」+ 视频 DASH 1080x1920
//	=> 同一条视频两边都发了
//
// 根因：判定只比 d.Time（作品发布时间），B 站永远更早 => 判「B站首发」放行。
// 修法：引入 seenAt（采集到并准备推送的时刻），按「谁先推出去」判。
const (
	// 线上索引里的抖音原文（含 hashtag），与 B 站标题形态完全不同，
	// 归一化后指纹不同 —— 只能靠团名 + 时长这一级判定对上。
	zzDyDesc = "嘚瑟NICHEART \n#Hearts2Hearts #H2H #ICONICHEART #Hearts2Hearts_ICONICHEART"
	// B 站作品发布时间 20:30:00（投稿时定下，永远比抖音早）
	zzBiliPubAt = int64(1791203400000)
	// 抖音登记时间20:39:43
	zzDyFirstSeen = int64(1791203983000)
)

func zzSeenAtIndex(t *testing.T) *dedupe.Index {
	t.Helper()
	return dedupe.NewIndex(filepath.Join(t.TempDir(), "i.json"), 14*24*time.Hour, 4096)
}

func zzBiliDynamic(seconds int) bilibili.Dynamic {
	return bilibili.Dynamic{
		Kind:    "video",
		Title:   "【Hearts2Hearts】嘚瑟NIC HEART",
		Author:  "Hearts2Hearts",
		Seconds: seconds,
		Time:    zzBiliPubAt,
	}
}

// 真实场景：抖音 20:40 先推视频，B 站 20:45 才扫到 —— B 站应跳视频
func TestZZBiliSkipsWhenOtherPlatformPushedFirst(t *testing.T) {
	ix := zzSeenAtIndex(t)
	ix.RecordWithAuthor(zzDyDesc, zzDyFirstSeen, "douyin", 19, "Hearts2Hearts")

	// B 站 20:45:57 才扫到（比抖音推送晚 5 分钟）
	if !bilibiliShouldSkipVideo(ix, zzBiliDynamic(19), 1791204357000) {
		t.Error("★ 抖音 20:40 已发过视频，B 站 20:45 才扫到，应跳过视频本体")
	}
}

// 反向：B 站先扫到并推送，抖音随后发 —— B 站应发
func TestZZBiliKeepsWhenItIsFirst(t *testing.T) {
	ix := zzSeenAtIndex(t)
	ix.RecordWithAuthor(zzDyDesc, zzDyFirstSeen, "douyin", 19, "Hearts2Hearts")

	// B 站 20:31 就扫到了（比对方登记的发布时间只差不到 1 分钟）
	if bilibiliShouldSkipVideo(ix, zzBiliDynamic(19), 1791203460000) {
		t.Error("★ B 站比对方更早推送，应保留视频本体")
	}
}

// 跨平台镜像只差几秒也要判重 —— 余量 30 秒（2026-10-08 线上漏判后调整）。
//
// ★ 原来这里守的是「差 1~2 秒拿不准就放行」，余量只有 3 秒。
//
//	线上就是被这个 3 秒漏掉的：11:01:10 TikTok 推送 → 11:01:12 B站扫到
//	同一条（18 秒），只差 2 秒 ⇒ 被判「拿不准」而放行 ⇒ 两边都下了视频本体，
//	用户在群里收到两条同一个视频。
//
//	「拿不准所以放行」在这里是**错的取舍**：候选已经过 MatchWithAuthor 的
//	group + 时长 + 20 分钟窗口三重筛，且确认来自别的平台；这种情形下
//	再放行只会产出重复视频，不会避免误吞首发（真正的首发由「对方比我晚
//	超过余量」那条保护，见 TestZZBiliKeepsWhenItIsFirst）。
//	重复视频可见度高，而首发被吞那条片子就永远发不出去了。
func TestZZBiliSkipsWhenMirrorIsSecondsApart(t *testing.T) {
	ix := zzSeenAtIndex(t)
	// 对方比我方采集早 2 秒（= 线上那次真实差值）
	ix.RecordWithAuthor(zzDyDesc, 1791203508000, "douyin", 19, "Hearts2Hearts")

	if !bilibiliShouldSkipVideo(ix, zzBiliDynamic(19), 1791203510000) {
		t.Error("★ 只差 2 秒的跨平台镜像应判重（B站应跳过视频本体）")
	}
	// 早 10 分钟：对方确实先发出去了，必须认输。
	ix2 := zzSeenAtIndex(t)
	ix2.RecordWithAuthor(zzDyDesc, 1791202910000, "douyin", 19, "Hearts2Hearts")
	if !bilibiliShouldSkipVideo(ix2, zzBiliDynamic(19), 1791203510000) {
		t.Error("★ 对方早 10 分钟推送，应跳过视频本体")
	}
}

// 图文 / 非视频不参与判定
func TestZZBiliNonVideoNeverSkips(t *testing.T) {
	ix := zzSeenAtIndex(t)
	ix.RecordWithAuthor("某篇专栏", zzBiliPubAt, "douyin", 0, "Hearts2Hearts")
	d := bilibili.Dynamic{Kind: "article", Title: "某篇专栏", Author: "Hearts2Hearts"}
	if bilibiliShouldSkipVideo(ix, d, 1791204357000) {
		t.Error("★ 非视频投稿不该被视频去重拦")
	}
}

// 时长不同不该拦（不同剪辑）
func TestZZBiliKeepsWhenDurationDiffers(t *testing.T) {
	ix := zzSeenAtIndex(t)
	ix.RecordWithAuthor(zzDyDesc, zzDyFirstSeen, "douyin", 59, "Hearts2Hearts")

	if bilibiliShouldSkipVideo(ix, zzBiliDynamic(189), 1791204357000) {
		t.Error("★ 时长不同（59s vs 189s，不同剪辑），不应拦")
	}
}

// 索引里没有这条 —— 任何时刻都该发
func TestZZBiliSendsWhenNothingInIndex(t *testing.T) {
	ix := zzSeenAtIndex(t)
	if bilibiliShouldSkipVideo(ix, zzBiliDynamic(19), 1791204357000) {
		t.Error("★ 索引为空，不该拦")
	}
}
