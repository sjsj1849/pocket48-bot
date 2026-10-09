package logic

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/dedupe"
)

// resetCrossIndex 把全局单例重置到指向新路径，方便每个用例独立落盘。
func resetCrossIndex(t *testing.T, root string) *dedupe.Index {
	t.Helper()
	crossOnce = sync.Once{}
	crossIdx = nil
	return crossTitleIndex(filepath.Join(root, "config.json"))
}

func TestDouyinRecordThenBilibiliSkips(t *testing.T) {
	root := t.TempDir()
	ix := resetCrossIndex(t, root)

	now := time.Now()
	douyinAt := now.Add(-10 * time.Minute) // 抖音 10 分钟前就把视频本体发出去了

	// ★ 直接写索引来表达「抖音在 10 分钟前**推送**了」，不再走 douyinRecordTitle：
	//   2026-10-06 起索引里存的是推送时刻，而 douyinRecordTitle 内部取 now()，
	//   用它登记没法构造「抖音更早推送」这个前提。
	ix.RecordWithAuthor("【Hearts2Hearts】it's oct 3rd yk #H2H", douyinAt.UnixMilli(), "douyin", 0, "Hearts2Hearts")

	// B 站同一内容、发布更晚 → 应跳过
	late := bilibili.Dynamic{
		Kind: "video", Title: "Hearts2Hearts it's oct 3rd yk",
		Time: now.UnixMilli(), Seconds: 8,
	}
	if !bilibiliShouldSkipVideoLegacy(ix, late) {
		t.Fatal("抖音更早发布的同标题投稿应被跳过")
	}

	// B 站发布时间更早，但**它也是此刻才被扫到** ——
	// 而抖音 10 分钟前就已经把视频本体发出去过了。
	//
	// ★ 2026-10-05 语义变更：判定基准从「作品发布时间」改成「谁先推送」。
	//   线上症状（20:40 抖音先推、20:45 B 站后推，同一条视频两边都发）：
	//   B 站发布时间更早，旧逻辑据此判「B 站首发」放行，于是两边都发。
	//   真实该问的是「视频本体这条消息谁先发出去」。
	lateButScannedNow := bilibili.Dynamic{
		Kind: "video", Title: "Hearts2Hearts it's oct 3rd yk",
		Time: now.Add(-20 * time.Minute).UnixMilli(), Seconds: 8,
	}
	if !bilibiliShouldSkipVideo(ix, lateButScannedNow, now.UnixMilli()) {
		t.Fatal("★ 抖音 10 分钟前已发过视频本体，B 站此刻才扫到，应跳过")
	}

	// B 站在抖音之前就扫到并推送 → B 站首发，应发。
	beforeDouyin := bilibili.Dynamic{
		Kind: "video", Title: "Hearts2Hearts it's oct 3rd yk",
		Time: now.Add(-20 * time.Minute).UnixMilli(), Seconds: 8,
	}
	if bilibiliShouldSkipVideo(ix, beforeDouyin, now.Add(-15*time.Minute).UnixMilli()) {
		t.Fatal("★ B 站比抖音更早推送，不应跳过")
	}
}

func TestBilibiliUnknownTitleAlwaysPushes(t *testing.T) {
	root := t.TempDir()
	ix := resetCrossIndex(t, root)
	now := time.Now().UnixMilli()

	douyinRecordTitle(filepath.Join(root, "config.json"), "完全不同的一条内容", now-60000, "video", 0, "sec_douyin_test")

	// 标题对不上 → 宁可多推
	if bilibiliShouldSkipVideoLegacy(ix, bilibili.Dynamic{Kind: "video", Title: "另一支没在抖音发过的 MV", Time: now}) {
		t.Fatal("未登记的标题必须放行（宁可多推）")
	}
}

func TestDouyinNoteNotRecorded(t *testing.T) {
	root := t.TempDir()
	ix := resetCrossIndex(t, root)

	douyinRecordTitle(filepath.Join(root, "config.json"), "今天的穿搭分享", time.Now().Unix(), "note", 0, "sec_douyin_test")
	if ix.Len() != 0 {
		t.Fatalf("图文作品不应登记标题指纹，实际 %d 条", ix.Len())
	}
}
func TestShortVideoThreshold(t *testing.T) {
	cases := []struct {
		name string
		d    bilibili.Dynamic
		want bool
	}{
		{"8秒短视频", bilibili.Dynamic{Kind: "video", Seconds: 8}, true},
		{"84秒短视频", bilibili.Dynamic{Kind: "video", Seconds: 84}, true},
		{"正好10分钟", bilibili.Dynamic{Kind: "video", Seconds: bilibiliShortVideoMaxSeconds}, true},
		{"11分钟", bilibili.Dynamic{Kind: "video", Seconds: bilibiliShortVideoMaxSeconds + 1}, false},
		{"团综长视频", bilibili.Dynamic{Kind: "video", Seconds: 2400}, false},
		{"时长未知", bilibili.Dynamic{Kind: "video", Seconds: 0}, false},
		{"图文动态", bilibili.Dynamic{Kind: "draw", Seconds: 10}, false},
	}
	for _, c := range cases {
		if got := bilibiliIsShortVideo(c.d); got != c.want {
			t.Errorf("%s: bilibiliIsShortVideo = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestCrossIndexPersistsAcrossRecreate(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "config.json")
	resetCrossIndex(t, root)
	douyinRecordTitle(cfgPath, "persist cross me", time.Now().Unix(), "video", 0, "sec_douyin_test")

	// ★ 必须走 storageRootOf，不能自己拼 filepath.Join(root, dedupePath)。
	//   真实部署布局是 <root>/config.json + <root>/storage/，storage 是子目录；
	//   自己拼 root 会把索引写到项目根去，这正是 2026-10-04 线上踩的那个坑。
	path := filepath.Join(storageRootOf(cfgPath), dedupePath)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("索引应落盘到 %s: %v", path, err)
	}

	// 模拟重启：全新进程会重新 NewIndex
	fresh := dedupe.NewIndex(path, dedupeTTL, dedupeMaxSize)
	if _, ok := fresh.Lookup("persist cross me"); !ok {
		t.Fatal("重启后应仍能查到已登记标题")
	}
	// 换一种写法（加括号包裹与话题标签）仍应命中同一指纹
	if _, ok := fresh.Lookup("【persist cross me】#H2H"); !ok {
		t.Fatal("重启后归一化查询仍应命中")
	}
	// 内容不同则必须是不同指纹（否则会误判成同一条而漏推）
	if _, ok := fresh.Lookup("persist cross me 封面版"); ok {
		t.Fatal("多出正文的标题不应命中同一指纹")
	}
	// 「#」后面紧跟的词整体是标签，不能被当作正文参与匹配
	if _, ok := fresh.Lookup("#persist cross me"); ok {
		t.Fatal("标签词不应计入指纹")
	}
}
