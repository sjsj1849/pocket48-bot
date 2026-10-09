package dedupe

import (
	"testing"
)

// 2026-10-06 线上修复：跨平台去重的所有判定都必须排除**本方自己登记的条目**。
//
// 不过滤的后果比"多发一条"严重得多：发送/下载失败时游标不推进、下一轮又抓到
// 同一条作品 → 匹配到自己登记的记录 → 判定"别的平台已发过" → 跳过 →
// 这条作品**永远发不出去**（静默漏推，且没有任何报错）。

// ★ 时间基准见 zz_testhelpers_test.go 的 zzSelf()（2026-10-08 由30 分钟
//	改为 5 分钟）：原来这里用 zzSelf()(=30分钟前) 作「本条发布时间」，
//	而条目登记在 zzAgo(10)，gap = 20 分钟，正好落在 MatchWindowMillis
//	的**边界之外**。新增的「一级指纹判定也要受窗口约束」上线后这些用例
//	全被判不重复 —— 那是真实回归（20 分钟前的同名作品确实不该算重复），
//	不是测试写错。改成 5 分钟后 gap 在窗口内，同时保留 zzAgo(20)
//	那个「超出窗口就不算重复」的用例，边界两侧都有覆盖。

// 一级判定（指纹完全相等）：自己登记的不能匹配到自己。
func TestMatchWithAuthorIgnoresOwnEntryFingerprint(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("都到齐了吧", zzAgo(10), "tiktok", 189, "Hearts2Hearts")

	SetActiveSource("tiktok")
	if _, ok := ix.MatchWithAuthor("都到齐了吧", 189, "Hearts2Hearts", zzSelf()); ok {
		t.Error("一级判定匹配到了自己登记的条目 -> 重试时会把自己跳掉")
	}

	// 换成别的平台来问，就应该能查到。
	SetActiveSource("bilibili")
	if _, ok := ix.MatchWithAuthor("都到齐了吧", 189, "Hearts2Hearts", zzSelf()); !ok {
		t.Error("别的平台应该能查到这条")
	}
}

// 二级判定（相似标题 + 时长一致）：同样要排除自己。
func TestMatchWithAuthorIgnoresOwnEntrySimilar(t *testing.T) {
	ix := zzIdx(t)
	// 标题不同但高度相似，用于走 SimilarTitle 分支。
	ix.RecordWithAuthor("都到齐了吧？～", zzAgo(10), "douyin", 189, "Hearts2Hearts")

	SetActiveSource("douyin")
	if _, ok := ix.MatchWithAuthor("都到齐了吧", 189, "Hearts2Hearts", zzSelf()); ok {
		t.Error("二级判定匹配到了自己登记的条目")
	}
}

// 兜底判定（团名 + 时长 + 窗口）：同样要排除自己。
func TestMatchWithGroupIgnoresOwnEntry(t *testing.T) {
	ix := zzIdx(t)
	// 跨语言标题：正文毫无重合度，只能靠「同团名 + 同时长 + 时间接近」。
	ix.RecordWithAuthor("언니가 더 예뿌", zzAgo(10), "tiktok", 21, "Hearts2Hearts")

	SetActiveSource("tiktok")
	if _, ok := ix.MatchWithAuthor("姐姐更漂亮", 21, "Hearts2Hearts", zzSelf()); ok {
		t.Error("兜底判定匹配到了自己登记的条目")
	}

	// 换成别的平台来问，且本条采集时刻**晚于**对方（gap > 0）——
	// matchByGroup 要求「对方先、本条后」，这也是新口径的语义。
	SetActiveSource("bilibili")
	if _, ok := ix.MatchWithAuthor("姐姐更漂亮", 21, "Hearts2Hearts", zzAgo(5)); !ok {
		t.Error("别的平台应该能通过团名+时长匹配到")
	}
	// 本条比对方还早 => 对方不可能是「先发出去的那条」，应放行。
	if _, ok := ix.MatchWithAuthor("姐姐更漂亮", 21, "Hearts2Hearts", zzAgo(20)); ok {
		t.Error("本条采集时刻早于对方，不该被判成已发过")
	}
}

// Match 是「与平台无关」的查询入口，不该被上一次残留的 activeSource 影响。
func TestMatchIgnoresActiveSource(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("shared clip", zzAgo(1), "tiktok", 42, "Hearts2Hearts")

	SetActiveSource("tiktok") // 上一次判定残留的状态
	if _, ok := ix.Match("shared clip", 42); !ok {
		t.Error("Match 用来确认「是否已登记」，不该被 activeSource 过滤")
	}
}
