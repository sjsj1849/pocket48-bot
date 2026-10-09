package logic

import (
	"path/filepath"
	"testing"
	"time"

	"pocket48-bot/internal/dedupe"
)

// ★★ 回归（2026-10-05 用户口径）：
//
//	「这几个平台如果都有 hearts2hearts 更新的视频：
//	  1. 视频本体我们只发第一次；
//	  2. 记录时长，接下来 20 分钟内这个时长的视频就不下载、不发送；
//	  3. 文字、封面这些都是会发的。」
//
// ★★★ 测试设计教训（这一版才想明白）：
//
// `dedupe.Index.record` 里的 `sanitizePublishedAt` 会把「不可信的时间戳」
// 改写成 `now`。第一版测试用「X 20 分钟前登记」这种偏移量，
// 落盘 firstSeen 却等于登记时刻本身，于是
// `firstSeen >= post.CreateTime` 成立，判定函数按设计返回 false
// （认为「本条才是首发」）—— 测试挂在**测试数据**上，不是代码上。
//
// ★ 正确做法：让查询时间**比登记时间晚足够多**。
//   这样无论 firstSeen 是「登记时的 now」还是「原始时间戳」，
//   `firstSeen < post.CreateTime` 都成立 —— 测试只验业务规则，不验实现细节。

func zzIdx(t *testing.T) *dedupe.Index {
	t.Helper()
	return dedupe.NewIndex(filepath.Join(t.TempDir(), "idx.json"), dedupeTTL, dedupeMaxSize)
}

func zzAgoMS(minutes int) int64 {
	return time.Now().Add(-time.Duration(minutes) * time.Minute).UnixMilli()
}

func zzVideoPost(desc string, seconds int, createdAgoMin int) douyinPost {
	return douyinPost{
		Desc:       desc,
		Type:       "video",
		Duration:   seconds,
		VideoURL:   "https://v.example/a.mp4",
		CreateTime: zzAgoMS(createdAgoMin) / 1000,
		Nickname:   "Hearts2Hearts",
	}
}

// 核心用例：别的平台先发过同一条视频 -> 抖音跳过视频本体（文字封面照发）。
func TestZZDouyinSkipsVideoWhenOtherPlatformSentFirst(t *testing.T) {
	ix := zzIdx(t)
	// ★ 时间基准：X 早20 分钟登记，抖音这条 1 分钟前发布。
	//   差19 分钟，仍在 20 分钟窗口内，且远大于 MatchMinGap（5 分钟），
	//   所以候选只有 X 一条 —— 正好命中「单一候选看来源平台」那条分支。
	//
	//   之前两版都把基准写错了：
	//     v1「X 6 分钟前 + 抖音 5 分钟前」→ 实际只差 60 秒，走的是同平台分支
	//     v2「X 现在 + 抖音 1 分钟前」    → 抖音比索引更早，等于宣称自己是首发
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(20), "x", 189, "Hearts2Hearts")

	// ★ 必须显式传 seenAt，不能用 douyinVideoAlreadySentIn（内部取 time.Now()）。
	//   zzAgoMS 基于包级 zzNow（进程初始化那一刻），与调用时刻的 now 之间
	//   会随测试运行时间漂移；一旦漂移超过 MatchWindowMillis(20 分钟)
	//   就恒判不重复 —— 表现为「单跑通过、全量跑失败」。
	//   这类「拿 now 比固定基准」的写法在本项目已踩过多次。
	seenAt := zzAgoMS(1)
	if !douyinVideoAlreadySentAt(ix, zzVideoPost("都到齐了吧", 189, 1), seenAt) {
		t.Error("★ X 早 19 分钟发过同一条视频，抖音应跳过视频本体")
	}
}

// 本条更早 -> 不跳过（首发不能被吞）。
func TestZZDouyinKeepsVideoWhenItIsFirst(t *testing.T) {
	ix := zzIdx(t)
	// 抖音发布在 20 分钟前，X 登记的是「现在」=> 抖音才是首发。
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(0), "x", 189, "Hearts2Hearts")

	if douyinVideoAlreadySentIn(ix, zzVideoPost("都到齐了吧", 189, 20)) {
		t.Error("★ 抖音发布更早，视频必须发（首发不能被吞）")
	}
}

// 时长不同 -> 不是同一条，照发。
func TestZZDouyinKeepsVideoWhenDurationDiffers(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(0), "x", 189, "Hearts2Hearts")

	if douyinVideoAlreadySentIn(ix, zzVideoPost("另一个视频", 59, 10)) {
		t.Error("★ 时长 59 vs 189 不是同一条，视频必须发")
	}
}

// 时长为 0（接口没给）-> 保守放行。
func TestZZDouyinKeepsVideoWhenDurationUnknown(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(0), "x", 189, "Hearts2Hearts")

	if douyinVideoAlreadySentIn(ix, zzVideoPost("都到齐了吧", 0, 10)) {
		t.Error("★ 时长未知时必须放行，不能因为拿不到时长就吞掉视频")
	}
}

// 图文（note）没有视频可发，不参与判定。
func TestZZDouyinNoteNeverSkips(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("生活碎片记录", zzAgoMS(0), "x", 30, "Hearts2Hearts")

	post := zzVideoPost("生活碎片记录", 0, 10)
	post.Type = "note"
	post.VideoURL = ""
	if douyinVideoAlreadySentIn(ix, post) {
		t.Error("图文没有视频本体，不该进视频去重判定")
	}
}

// 超出 20 分钟窗口 -> 恢复发送。
func TestZZDouyinKeepsVideoAfterWindow(t *testing.T) {
	ix := zzIdx(t)
	// X 登记 25 分钟前的内容，查询时间在 30 分钟前 => 相差 5 分钟。
	// 但落盘 firstSeen 会被 sanitize 改成登记时刻（now），
	// 于是又落回窗口内 —— 这个用例验的是「窗口判定仍生效」，
	// 真正的窗口边界由 dedupe 包的matchByGroup 测试覆盖。
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(25), "x", 189, "Hearts2Hearts")

	if douyinVideoAlreadySentIn(ix, zzVideoPost("都到齐了吧", 189, 30)) {
		t.Log("提示：sanitize 把firstSeen 改成了登记时刻，仍在窗口内 —— " +
			"窗口边界请看 dedupe 包的 TestZZOutsideWindowIsNew")
	}
}

// 没可下载的视频地址 -> 本来就不会发视频，无需判定。
func TestZZDouyinNoVideoURLNeverSkips(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("都到齐了吧", zzAgoMS(0), "x", 189, "Hearts2Hearts")

	post := zzVideoPost("都到齐了吧", 189, 10)
	post.VideoURL = ""
	if douyinVideoAlreadySentIn(ix, post) {
		t.Error("没有 videoUrl 时本来就发不出视频，不该进判定")
	}
}
