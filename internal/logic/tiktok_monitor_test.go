package logic

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pocket48-bot/internal/tiktokmonitor"
)

// ------------------------------------------------------------ 测试替身

type fakeCfg struct{ path string }

func (f fakeCfg) ConfigPath() string { return f.path }

type sentVideo struct {
	video tiktokmonitor.Video
	secs  int
	path  string
}

type fakeSender struct {
	mu         sync.Mutex
	sent       []sentVideo
	failSend   bool     // 模拟飞书发送失败
	alerts     []string // 告警触发的账号
	alertTexts []string // 实际发出的告警文案
}

func (f *fakeSender) SendTiktokVideo(_ context.Context, v tiktokmonitor.Video, path string, secs int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSend {
		return errors.New("飞书发送失败（测试注入）")
	}
	f.sent = append(f.sent, sentVideo{video: v, secs: secs, path: path})
	return nil
}

func (f *fakeSender) AlertText(user string, n int, cause error) string {
	if n <= 0 {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alerts = append(f.alerts, user)
	return "TikTok 告警文案"
}

// stubClient 替换真实 sidecar，让测试完全脱离网络。
//
// videos 是**可变**的：模拟真实轮询 —— 首轮只有历史作品，
// 之后作者发了新的，列表变多。
//
// 为什么不用「固定返回一个闭包」：那样首轮就把新作品吃进基线了，
// 第二轮无新内容可推，测试语义含糊（第一版 4 个用例正是这么失败的）。
type stubClient struct {
	videos      []tiktokmonitor.Video
	timelineErr error
	downloadErr error
	dlPath      string
	dlSeconds   int
}

func (s *stubClient) Timeline(context.Context, string, int) ([]tiktokmonitor.Video, error) {
	if s.timelineErr != nil {
		return nil, s.timelineErr
	}
	out := make([]tiktokmonitor.Video, len(s.videos))
	copy(out, s.videos)
	return out, nil
}

func (s *stubClient) Download(context.Context, string) (*tiktokmonitor.Downloaded, error) {
	if s.downloadErr != nil {
		return nil, s.downloadErr
	}
	return &tiktokmonitor.Downloaded{Path: s.dlPath, Duration: float64(s.dlSeconds)}, nil
}

// publish 追加新作品（新的在前面，与接口返回的倒序一致）。
func (s *stubClient) publish(v ...tiktokmonitor.Video) {
	s.videos = append(append([]tiktokmonitor.Video{}, v...), s.videos...)
}

// ------------------------------------------------------------ 辅助

func newTestMonitor(t *testing.T, s *fakeSender) *TiktokMonitor {
	t.Helper()
	root := t.TempDir()
	m := NewTiktokMonitor(fakeCfg{path: filepath.Join(root, "config.json")}, s, root)
	m.store = filepath.Join(root, "tiktok")
	// 去重索引是包级单例，每个测试必须重置，否则跨测试污染。
	crossOnce = sync.Once{}
	crossIdx = nil
	m.newClient = func(string) TiktokClient { return &stubClient{} }
	m.onAlert = func(text string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if text != "" {
			s.alertTexts = append(s.alertTexts, text)
		}
	}
	return m
}

// tt 构造测试作品。
//
// secsAgo 是「多少秒前发布」，而不是绝对时间戳。**必须这样写**：
// dedupe.Index 的 pruneLocked 会丢掉超过 TTL（14 天）的记录，
// 用 1970 年的时间戳会让登记的记录当场被清掉，测试断言随之失效 ——
// 那是数据不真实，不是代码有 bug。
func tt(id string, secsAgo int64, dur float64, desc string) tiktokmonitor.Video {
	return tiktokmonitor.Video{
		ID:         id,
		CreateTime: time.Now().Unix() - secsAgo,
		Duration:   dur,
		Desc:       desc, AuthorName: "hearts2hearts",
	}
}

func user() string { return tiktokmonitor.DefaultUser }

func sentCount(s *fakeSender) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// ------------------------------------------------------------ 用例

// 用户报告的核心问题：B站和抖音推了同一条视频。
// 三个平台共用一个去重索引，所以 TikTok 侧必须能识别「别人已推过」。
func TestSkipsWhenOtherPlatformPublishedEarlier(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	// 模拟抖音早 7 分钟发了这条（与线上那次的时间差一致）
	// 抖音比 TikTok 早 30 秒发这条。间隔要拉开：判定是严格小于，
	// 毫秒级相等会在边界上抖动，测试就不稳定了。
	douyinAt := time.Now().Add(-450 * time.Second).UnixMilli()
	m.titleIndex().Record("like if I can't help falling in love with you?",
		douyinAt, "douyin", 116)

	stub := &stubClient{dlPath: "/tmp/x.mp4", dlSeconds: 116}
	defer swapClient(t, m, stub)()

	// 首轮建基线
	stub.publish(tt("7001", 900, 60, "older clip"))
	m.scan(context.Background(), user())

	// 第二轮：TikTok 收到那条抖音已先发的内容
	stub.publish(tt("7002", 420, 116, "like if I can't help falling in love with you?"))
	m.scan(context.Background(), user())

	if got := sentCount(s); got != 0 {
		t.Fatalf("抖音已先发布，TikTok 不该再推，实际发了 %d 条", got)
	}
}

// 用户要的规则：谁最早发就只发谁。TikTok 更早 -> 必须推。
func TestSendsWhenTikTokIsEarliest(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	// 抖音晚 5 分钟才发（这条 TikTok 更早，应当推送）
	douyinAt := time.Now().Add(5 * time.Minute).UnixMilli()
	m.titleIndex().Record("some song", douyinAt, "douyin", 116)

	stub := &stubClient{dlPath: "/tmp/y.mp4", dlSeconds: 116}
	defer swapClient(t, m, stub)()

	stub.publish(tt("7001", 1791091000, 60, "older clip"))
	m.scan(context.Background(), user())

	stub.publish(tt("7002", 120, 116, "some song"))
	m.scan(context.Background(), user())

	if got := sentCount(s); got != 1 {
		t.Fatalf("TikTok 更早应推送 1 条，实际 %d", got)
	}
	s.mu.Lock()
	secs := s.sent[0].secs
	s.mu.Unlock()
	if secs != 116 {
		t.Errorf("时长应为 116 秒，实际 %d", secs)
	}
}

// 上线当天不该刷屏：首轮只建基线，绝不推历史作品。
func TestFirstScanDoesNotPushHistory(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		dlPath:    "/tmp/z.mp4",
		dlSeconds: 10,
		videos: []tiktokmonitor.Video{
			tt("3", 600, 10, "c"), tt("2", 700, 10, "b"), tt("1", 800, 10, "a"),
		},
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())

	if got := sentCount(s); got != 0 {
		t.Fatalf("首轮应只建基线，实际推了 %d 条", got)
	}
	m.mu.Lock()
	c := m.state.Cursors[user()]
	m.mu.Unlock()
	if !c.Ready || len(c.Seen) != 3 {
		t.Fatalf("基线未正确建立: ready=%v seen=%d", c.Ready, len(c.Seen))
	}
	if c.HighWater <= 0 {
		t.Fatalf("水位线应已建立，实际 %d", c.HighWater)
	}
}

// 发送失败必须不推进游标，否则永久丢失 —— 监控最常见的漏推原因。
func TestSendFailureDoesNotAdvanceCursor(t *testing.T) {
	s := &fakeSender{failSend: true}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:    []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		dlPath:    "/tmp/r.mp4",
		dlSeconds: 30,
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user()) // 建基线

	// 发送会失败的新作品
	stub.publish(tt("7003", 60, 30, "retry me"))
	m.scan(context.Background(), user())

	m.mu.Lock()
	c := m.state.Cursors[user()]
	m.mu.Unlock()
	// 失败的作品是 60 秒前发布的；若水位线推进到了它，说明 Advance 收紧了。
	newAt := time.Now().Add(-60 * time.Second).UnixMilli()
	if c.HighWater >= newAt {
		t.Fatalf("发送失败不应推进水位线（应 < %d），实际 %d", newAt, c.HighWater)
	}

	// 恢复后必须补发
	s.mu.Lock()
	s.failSend = false
	s.mu.Unlock()
	m.scan(context.Background(), user())
	if got := sentCount(s); got != 1 {
		t.Fatalf("恢复后应补发 1 条，实际 %d", got)
	}
}

// 下载失败同样不推进，且要计入连续失败。
func TestDownloadFailureDoesNotAdvanceCursor(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:      []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		downloadErr: errors.New("TikTok 视频下载失败：CDN 拒绝"),
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())

	stub.publish(tt("7004", 60, 30, "dl fail"))
	for i := 0; i < 4; i++ {
		m.scan(context.Background(), user())
	}

	if got := sentCount(s); got != 0 {
		t.Fatalf("下载失败不该发送，实际 %d", got)
	}
	m.mu.Lock()
	c := m.state.Cursors[user()]
	m.mu.Unlock()
	newAt := time.Now().Add(-60 * time.Second).UnixMilli()
	if c.HighWater >= newAt {
		t.Fatalf("下载失败不应推进水位线（应 < %d），实际 %d", newAt, c.HighWater)
	}
	if len(s.alerts) == 0 {
		t.Fatal("连续失败达阈值应告警")
	}
}

// 单次限流不打扰用户，达阈值才告警，且不重复告警。
func TestAlertOnlyAfterThreshold(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{timelineErr: &tiktokmonitor.Error{Code: "rate_limited"}}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())
	m.scan(context.Background(), user())
	if len(s.alerts) != 0 {
		t.Fatalf("失败 2 次不该告警，实际 %d 次", len(s.alerts))
	}
	m.scan(context.Background(), user())
	if len(s.alerts) != 1 {
		t.Fatalf("失败 3 次应告警 1 次，实际 %d", len(s.alerts))
	}
	m.scan(context.Background(), user())
	m.scan(context.Background(), user())
	if len(s.alerts) != 1 {
		t.Fatalf("同一次连续失败不应重复告警，实际 %d", len(s.alerts))
	}
}

// 恢复成功后应清零计数，下次再出问题才能重新告警。
func TestAlertResetsAfterRecovery(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:    []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		dlPath:    "/tmp/a.mp4",
		dlSeconds: 10,
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user()) // 成功一次

	stub.timelineErr = &tiktokmonitor.Error{Code: "rate_limited"}
	for i := 0; i < 3; i++ {
		m.scan(context.Background(), user())
	}
	if len(s.alerts) != 1 {
		t.Fatalf("应告警 1 次，实际 %d", len(s.alerts))
	}

	// 恢复正常
	stub.timelineErr = nil
	stub.publish(tt("2", 120, 10, "new one"))
	m.scan(context.Background(), user())

	// 再次连续失败，应能重新告警
	stub.timelineErr = &tiktokmonitor.Error{Code: "rate_limited"}
	for i := 0; i < 3; i++ {
		m.scan(context.Background(), user())
	}
	if len(s.alerts) != 2 {
		t.Fatalf("恢复后再次失败应能重新告警，实际 %d", len(s.alerts))
	}
}

// 推送成功后必须登记进索引，让 B站/抖音侧识别「TikTok 已推过」。
func TestSentVideoRecordedForOtherPlatforms(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:    []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		dlPath:    "/tmp/s.mp4",
		dlSeconds: 42,
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())
	want := tt("7005", 60, 42, "shared clip")
	stub.publish(want)
	m.scan(context.Background(), user())

	ix := m.titleIndex()
	got, ok := ix.Match("shared clip", 42)
	if !ok {
		t.Fatal("推送后应能在索引里查到")
	}
	// 2026-10-06 起索引里存的是**推送时刻**（time.Now()），不是作品发布时间。
	// 判定方要比较的是「谁先把消息发出去」，登记发布时间会得出相反结论。
	if delta := got - time.Now().UnixMilli(); delta > 5000 || delta < -5000 {
		t.Fatalf("登记的应是推送时刻（与现在相差 <5s），实际偏差 %d ms", delta)
	}
}

// 重复轮询同一批内容不应重复推送（幂等）。
func TestRescanSameContentIsIdempotent(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:    []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		dlPath:    "/tmp/i.mp4",
		dlSeconds: 10,
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())
	stub.publish(tt("2", 120, 10, "brand new"))
	m.scan(context.Background(), user())

	if got := sentCount(s); got != 1 {
		t.Fatalf("应只推 1 条，实际 %d", got)
	}
	// 再扫 5 轮，列表内容不变
	for i := 0; i < 5; i++ {
		m.scan(context.Background(), user())
	}
	if got := sentCount(s); got != 1 {
		t.Fatalf("重复轮询不该重复推送，实际累计 %d 条", got)
	}
}

// 多条新作品应按时间正序推送（先发老的）。
func TestPushesInChronologicalOrder(t *testing.T) {
	s := &fakeSender{}
	m := newTestMonitor(t, s)

	stub := &stubClient{
		videos:    []tiktokmonitor.Video{tt("1", 600, 10, "old")},
		dlPath:    "/tmp/o.mp4",
		dlSeconds: 10,
	}
	defer swapClient(t, m, stub)()

	m.scan(context.Background(), user())

	// 接口是倒序返回的（新的在前）
	stub.publish(
		tt("c", 60, 10, "third"),
		tt("b", 120, 10, "second"),
		tt("a", 180, 10, "first"),
	)
	m.scan(context.Background(), user())

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) != 3 {
		t.Fatalf("应推 3 条，实际 %d", len(s.sent))
	}
	want := []string{"first", "second", "third"}
	for i, w := range want {
		if s.sent[i].video.Desc != w {
			t.Fatalf("第 %d 条应为 %q，实际 %q", i+1, w, s.sent[i].video.Desc)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("短文本不该截断，实际 %q", got)
	}
	got := truncate("这是一个很长的标题需要被截断处理", 5)
	if len([]rune(got)) != 6 { // 5 个字 + 省略号
		t.Errorf("应截断为 5 字加省略号，实际 %q (%d runes)", got, len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("应以省略号结尾，实际 %q", got)
	}
}

// swapClient 把监控器的采集客户端换成假实现。
func swapClient(t *testing.T, m *TiktokMonitor, stub *stubClient) func() {
	t.Helper()
	orig := m.newClient
	m.newClient = func(string) TiktokClient { return stub }
	return func() { m.newClient = orig }
}
