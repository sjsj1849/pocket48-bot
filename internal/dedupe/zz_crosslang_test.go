// Regression tests for cross-platform dedupe.
//
// 2026-10-04 用户把方案整个定下来了：
//
//	「只要把 hearts to hearts 这个名字提取出来，以第一个拿到的视频时间为基准，
//	  往后推 15 分钟。在这 15 分钟内，如果不同平台来源于 hearts to hearts 的
//	  视频时长和它一致，视频本体我们就只发第一个，其他的都不发了。」
//
// 因此判据就是三件事：团名相交 + 时长一致 + 15 分钟窗口内候选唯一。
// 不再看标题正文 —— 四个平台的正文互不相同（中文/韩文/微博还差一个字），
// 靠正文比对必然漏判，这正是「同一条片子连推四条」的根因。
package dedupe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	// zzGroup 是四平台实测一致的团名。
	zzGroup = "Hearts2Hearts"

	// 真实四平台标题（2026-10-04 线上实测）。
	zzRealDy = "明天见嘻嘻 ♡ #Hearts2Hearts #H2H #ICONICHEART #Hearts2Hearts_ICONICHEART"
	zzRealTt = "내 봥 ㅋㅋ ♡ #Hearts2Hearts #하츠투하츠 #H2H #ICONICHEART #Hearts2Hearts_ICONICHEART"
	zzRealBl = "【Hearts2Hearts】明天见嘻嘻 ♡"
	zzRealWb = "明天见科科 ♡ #Hearts2Hearts #H2H #ICONICHEART #Hearts2Hearts_ICONICHEART"

	// 实测同一条片子跨平台发布的时间差约 15 分钟（TikTok 16:40 / 抖音 16:55）。
	zzRealGapMinutes = 15
)

func zzIdx(t *testing.T) *Index {
	t.Helper()
	return NewIndex(filepath.Join(t.TempDir(), "titles.json"), 72*time.Hour, 512)
}

// The core case: one clip posted to four platforms under four different
// titles, within the 15-minute window, with the same duration. Exactly one
// should survive.
func TestZZFourPlatformSameClipOnePush(t *testing.T) {
	for _, tc := range []struct {
		name   string
		title  string
		author string
		secs   int
	}{
		{"tiktok", zzRealTt, zzGroup, 21},
		{"bilibili", zzRealBl, zzGroup, 22},
		{"weibo", zzRealWb, zzGroup, 21},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := zzIdx(t)
			// Douyin posted first and registered the clip.
			ix.RecordWithAuthor(zzRealDy, zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)

			got, ok := ix.MatchWithAuthor(tc.title, tc.secs, tc.author, zzAgo(1))
			if !ok {
				t.Fatalf("%s 必须命中抖音已登记的同一条（正文不同，只有团名能对齐）", tc.name)
			}
			if got != zzAgo(zzRealGapMinutes) {
				t.Fatalf("应命中首发时间 %d，实际 %d", zzAgo(zzRealGapMinutes), got)
			}
		})
	}
}

// The reverse direction: TikTok is earliest, the other three arrive later.
func TestZZTikTokFirstSuppressesRest(t *testing.T) {
	ix := zzIdx(t)
	first := zzAgo(zzRealGapMinutes)
	ix.RecordWithAuthor(zzRealTt, first, "tiktok", 21, zzGroup)

	for _, tc := range []struct {
		name   string
		title  string
		author string
		secs   int
	}{
		{"bilibili", zzRealBl, zzGroup, 22},
		{"douyin", zzRealDy, zzGroup, 21},
		{"weibo", zzRealWb, zzGroup, 21},
	} {
		if got, ok := ix.MatchWithAuthor(tc.title, tc.secs, tc.author, zzAgo(1)); !ok {
			t.Errorf("%s 未命中 TikTok 首发记录", tc.name)
		} else if got != first {
			t.Errorf("%s 命中时间错误: got %d want %d", tc.name, got, first)
		}
	}
}

// Bilibili's title carries only the 【】prefix — the B站 API returns no tags
// at all (seasons_series_list and view both give tags=null, tname=""), so
// this prefix is the only anchor available on that platform.
func TestZZBilibiliPrefixIsTheAnchor(t *testing.T) {
	got := GroupKeys(zzRealBl, "")
	if len(got) != 1 || got[0] != "hearts2hearts" {
		t.Fatalf("B 站【】前缀必须提取为团名, got %v", got)
	}

	ix := zzIdx(t)
	ix.RecordWithAuthor(zzRealTt, zzAgo(zzRealGapMinutes), "tiktok", 21, zzGroup)
	if _, ok := ix.MatchWithAuthor(zzRealBl, 22, zzGroup, zzAgo(1)); !ok {
		t.Fatal("只有【】前缀的 B 站标题也应命中 TikTok 已登记的同一条")
	}
}

// 【Hearts2Hearts】 and Hearts2Hearts must normalise to the same key.
func TestZZGroupKeyNormalisation(t *testing.T) {
	fromTitle := GroupKeys(zzRealBl, "")
	fromAuthor := GroupKeys("", zzGroup)
	if len(fromTitle) != 1 || len(fromAuthor) != 1 {
		t.Fatalf("各应只得到 1 个团名: %v / %v", fromTitle, fromAuthor)
	}
	if fromTitle[0] != fromAuthor[0] {
		t.Fatalf("【】前缀与作者名归一化后应相同: %q vs %q", fromTitle[0], fromAuthor[0])
	}
}

// The 【】prefix must not swallow the following body text. Real title:
// 「【Hearts2Hearts】明天见嘻嘻 ♡」 — no space after the closing bracket.
func TestZZBracketStopsAtCloser(t *testing.T) {
	if got := GroupKeys("【Hearts2Hearts】明天见嘻嘻 ♡", ""); len(got) != 1 {
		t.Fatalf("必须只提取到 1 个团名（正文被吞进标签了）, got %v", got)
	}
}

// Same group, same duration, but outside the window: a genuinely new clip.
func TestZZOutsideWindowIsNew(t *testing.T) {
	ix := zzIdx(t)
	// 40 分钟前的内容，不在 15 分钟窗口内。
	ix.RecordWithAuthor(zzRealDy, zzAgo(40), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor(zzRealTt, 21, zzGroup, zzAgo(1)); ok {
		t.Fatal("超出 15 分钟窗口，不得判为同一条")
	}
}

// Different duration is decisive: same group, same window, still two clips.
func TestZZDifferentDurationIsNew(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("短版", zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor("长版", 180, zzGroup, zzAgo(1)); ok {
		t.Fatal("时长差 159 秒，绝不是同一条")
	}
}

// A different group must never be collapsed, even with identical duration.
func TestZZDifferentGroupNotMatched(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor(zzRealDy, zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor("完全不同的一条", 21, "别家组合", zzAgo(1)); ok {
		t.Fatal("不同团名不得判为同一条")
	}
}

// Same account posting several clips in a row: every one must push.
// This is why the "candidate must be unique" rule exists.
func TestZZBatchNotCollapsed(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("第一条", zzAgo(20), "douyin", 10, zzGroup)
	if _, ok := ix.MatchWithAuthor("第二条", 10, zzGroup, zzAgo(18)); ok {
		t.Error("同一次连发的第二条被误判为重复")
	}
	if _, ok := ix.MatchWithAuthor("第三条", 10, zzGroup, zzAgo(16)); ok {
		t.Error("同一次连发的第三条被误判为重复")
	}
}

// Two in-window candidates with the same group and duration are ambiguous.
func TestZZAmbiguousStaysSilent(t *testing.T) {
	ix := zzIdx(t)
	base := zzNow - 8*60*1000
	ix.RecordWithAuthor("第一个", base, "douyin", 21, zzGroup)
	ix.RecordWithAuthor("第二个", base+60_000, "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor("无法判断是哪一条", 21, zzGroup, zzSelf()); ok {
		t.Fatal("候选不唯一时必须放行 —— 误吞比多推严重")
	}
}

// Different lengths disambiguate, so matching works.
func TestZZDifferentLengthsDisambiguate(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor("长片", zzAgo(zzRealGapMinutes), "douyin", 180, zzGroup)
	ix.RecordWithAuthor("短片", zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor("新标题", 21, zzGroup, zzAgo(1)); !ok {
		t.Error("时长应能消歧，21 秒那条必须可匹配")
	}
	if _, ok := ix.MatchWithAuthor("另一个", 180, zzGroup, zzAgo(1)); !ok {
		t.Error("180 秒那条也必须保持可匹配")
	}
}

// Without publishedAt the caller cannot tell "who posted first", so stay silent.
func TestZZFallbackNeedsPublishedAt(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor(zzRealDy, zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor(zzRealTt, 21, zzGroup, 0); ok {
		t.Fatal("缺少本条发布时间时必须放行")
	}
}

// Records written by the previous binary have no group; they must not crash
// the matcher nor block newer records.
func TestZZLegacyEntryWithoutGroup(t *testing.T) {
	ix := zzIdx(t)
	ix.data.Titles = map[string]Entry{
		"legacy": {FirstSeen: zzAgo(zzRealGapMinutes), Source: "douyin", Seconds: 21, Title: "旧记录"},
	}
	ix.loaded = true

	if _, ok := ix.MatchWithAuthor(zzRealTt, 21, zzGroup, zzAgo(1)); ok {
		t.Fatal("旧格式条目没有团名，不能作为跨平台判据")
	}

	ix.RecordWithAuthor(zzRealDy, zzAgo(zzRealGapMinutes), "douyin", 21, zzGroup)
	if _, ok := ix.MatchWithAuthor(zzRealTt, 21, zzGroup, zzAgo(1)); !ok {
		t.Fatal("新格式记录应可正常参与判定")
	}
}

// A later record for the same fingerprint backfills the group on the earlier
// one, which then becomes usable as an anchor.
func TestZZRecordBackfillsGroup(t *testing.T) {
	ix := zzIdx(t)
	// 首发方标题带【】前缀但没传作者名（采集侧偶尔拿不到）。
	const first = "【Hearts2Hearts】明天见嘻嘻 ♡"
	ix.RecordWithAuthor(first, zzAgo(zzRealGapMinutes), "douyin", 21, "")

	key := Normalize(first)
	e, ok := ix.data.Titles[key]
	if !ok {
		t.Fatalf("记录应存在, key=%q", key)
	}
	// 【】前缀本身就能提取出团名，所以这里其实不为空 —— 改用纯正文验证回填。
	if len(e.Group) == 0 {
		t.Fatal("【】前缀应当能直接提取出团名")
	}

	// 换一个既没有【】也没作者的标题，回填逻辑才有意义。
	const bare = "纯正文没有任何前缀"
	ix.RecordWithAuthor(bare, zzAgo(zzRealGapMinutes), "douyin", 21, "")
	e = ix.data.Titles[Normalize(bare)]
	if len(e.Group) != 0 {
		t.Fatalf("无【】且无作者时团名应为空，实际 %v", e.Group)
	}

	// 同指纹、稍晚的一条带上了作者名 —— 必须回填到首发那条上。
	ix.RecordWithAuthor(bare, zzAgo(zzRealGapMinutes-2), "bilibili", 21, zzGroup)
	e = ix.data.Titles[Normalize(bare)]
	if len(e.Group) == 0 {
		t.Fatal("后到的记录必须回填团名")
	}
	if e.FirstSeen != zzAgo(zzRealGapMinutes) {
		t.Fatalf("回填不得改动首发时间: got %d want %d", e.FirstSeen, zzAgo(zzRealGapMinutes))
	}
	t.Logf("backfilled group = %v", e.Group)
}

// Exact fingerprint equality still wins immediately.
func TestZZExactFingerprintStillWorks(t *testing.T) {
	ix := zzIdx(t)
	ix.RecordWithAuthor(zzRealDy, zzAgo(10), "douyin", 21, zzGroup)

	if _, ok := ix.MatchWithAuthor(zzRealDy, 21, zzGroup, zzAgo(1)); !ok {
		t.Fatal("完全相同的指纹必须命中")
	}
}

// Multi-platform mirrors of the same clip MUST collapse even though there are
// several in-window candidates.
//
// ★ This is the case the old "exactly one candidate" rule got wrong (2026-10-04).
// The real index for one Hearts2Hearts clip held three rows at once:
//
//	tiktok 17:00 / douyin 17:02 / bilibili 17:07
//
// so when the fourth platform arrived the candidate count was 3 -> released ->
// the user still got the duplicate. Uniqueness cannot tell "three mirrors of one
// clip" from "one account posting three clips"; the source platform can.
func TestZZMultiPlatformMirrorsCollapse(t *testing.T) {
	ix := zzIdx(t)
	base := zzAgo(18)
	ix.RecordWithAuthor(zzRealTt, base, "tiktok", 21, zzGroup)
	ix.RecordWithAuthor(zzRealDy, base+2*60_000, "douyin", 21, zzGroup)
	ix.RecordWithAuthor(zzRealBl, base+7*60_000, "bilibili", 22, zzGroup)

	got, ok := ix.MatchWithAuthor(zzRealWb, 21, zzGroup, base+16*60_000)
	if !ok {
		t.Fatal("★ 三个平台镜像同一条内容，第四个平台必须被拦下")
	}
	if got != base {
		t.Errorf("应返回最早的 TikTok 首发 %d，实际 %d", base, got)
	}
}

// One account posting several clips in a row from a single platform stays open,
// even when a mirror of an earlier one also sits in the window.
//
// The mixed case ("douyin posted twice AND tiktok posted once") must NOT collapse:
// there the duration no longer pins down which clip is which.
func TestZZMixedSourcesStayOpen(t *testing.T) {
	ix := zzIdx(t)
	base := zzAgo(20)
	ix.RecordWithAuthor("douyin 连发第一条", base, "douyin", 10, zzGroup)
	ix.RecordWithAuthor("douyin 连发第二条", base+2*60_000, "douyin", 10, zzGroup)
	ix.RecordWithAuthor(zzRealTt, base+6*60_000, "tiktok", 10, zzGroup)

	if _, ok := ix.MatchWithAuthor("新的一条", 10, zzGroup, base+18*60_000); ok {
		t.Error("同平台出现多条候选时无法定位，必须放行 —— 误吞比多推严重")
	}
}

// Two mirrors far apart inside the window still collapse; taking the earliest
// is what makes "who pushed first" stable.
func TestZZMultiPlatformTakeEarliest(t *testing.T) {
	ix := zzIdx(t)
	base := zzAgo(19)
	ix.RecordWithAuthor(zzRealTt, base+8*60_000, "tiktok", 21, zzGroup)
	ix.RecordWithAuthor(zzRealDy, base, "douyin", 21, zzGroup)

	got, ok := ix.MatchWithAuthor("微博那条", 21, zzGroup, base+19*60_000)
	if !ok {
		t.Fatal("两条不同平台的候选必须能判为同一条")
	}
	if got != base {
		t.Errorf("应取最早的抖音 %d，实际 %d", base, got)
	}
}

// Records written before the group field existed must still act as anchors.
// Their Title carries the 【】prefix, so the group is recoverable on load.
//
// ★ Must go through the real load path (write the file, then let NewIndex
// load it). Assigning ix.data directly and setting loaded=true skips
// loadLocked entirely, so backfillGroupsLocked never runs — the test would be
// asserting against a path production never takes. That mistake already bit
// this suite once, hence the explicit warning.
func TestZZBackfillsGroupFromTitle(t *testing.T) {
	p := filepath.Join(t.TempDir(), "titles.json")
	legacy, err := json.Marshal(store{Titles: map[string]Entry{
		"legacy": {
			FirstSeen: zzAgo(16), Source: "bilibili", Seconds: 22,
			Title: zzRealBl, // 「【Hearts2Hearts】xxx」，Group deliberately empty
		},
	}})
	if err != nil {
		t.Fatalf("序列化存量索引失败：%v", err)
	}
	if err := os.WriteFile(p, legacy, 0o600); err != nil {
		t.Fatalf("写存量索引失败：%v", err)
	}

	ix := NewIndex(p, 72*time.Hour, 512)
	if _, ok := ix.MatchWithAuthor(zzRealTt, 22, zzGroup, zzAgo(1)); !ok {
		t.Fatal("★ 存量条目必须靠 Title 回填团名后仍能参与判定")
	}

	ix.mu.Lock()
	got := len(ix.data.Titles["legacy"].Group)
	ix.mu.Unlock()
	if got == 0 {
		t.Fatal("回填没生效：团名仍为空")
	}
}
