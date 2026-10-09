package logic

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/tiktokmonitor"
)

// TikTok（洋抖）监控 —— 只做动态，不做 IM。
//
// 用户口径（2026-10-04）：
//  1. 只监控「心连心」（Hearts2Hearts）
//  2. 沿用现有时间线与跨平台去重：**洋抖/中抖/B站发同一支片子，只推最早的那个**
//
// 因此本文件的职责是「三平台最早者胜」里的第三方：
//   - TikTok 发现新作品 → 先查索引，若抖音或 B站 更早发过就跳过
//   - 否则推送，并把 TikTok 的发布时间与时长登记进同一个索引
//   - TikTok 本身也走 douyinRecordTitle 同一套登记逻辑，保证三方可比

const (
	// tiktokScanInterval 轮询间隔。
	//
	// 为什么是 15 分钟而不是更短：**TikTok 的 item_list 接口连请求 5-6 次
	// 就会持续返回空响应，且 5 分钟内不恢复**。间隔低于 10 分钟很容易把
	// 限流窗口拉长到一直好不了。心连心更新本身也不密，15 分钟足够。
	tiktokScanInterval = 15 * time.Minute

	// tiktokAlertFailThreshold 连续失败多少次才告警。
	// 单次失败多半是限流，不该打扰用户。
	tiktokAlertFailThreshold = 3

	// tiktokStorageDir 采集数据目录（相对 storage 根）。
	tiktokStorageDir = "tiktok"
)

// TiktokClient 是采集能力接口。
//
// 为什么要抽出来：tiktokmonitor.Client 是拉起 Python 子进程的具体实现，
// 测试时无法替换成假实现。抽成接口后，
// scan/Start 的全部编排逻辑都能脱离网络做单测 —— 而这套逻辑恰恰是
// 最容易出错的地方（游标推进、去重判定、失败重试）。
type TiktokClient interface {
	Timeline(ctx context.Context, username string, limit int) ([]tiktokmonitor.Video, error)
	Download(ctx context.Context, videoID string) (*tiktokmonitor.Downloaded, error)
}

// TiktokMonitor 是 TikTok 作品监控器。
type TiktokMonitor struct {
	cfg   ConfigProvider
	send  TiktokSender
	store string // storage/tiktok 目录

	// newClient 构造采集客户端。测试里替换成假实现。
	newClient func(dir string) TiktokClient

	// onAlert 把告警文案交给 Bot 的管理员通知通道。
	// 抽成字段是为了让 logic 内部不依赖 Bot 的具体实现。
	onAlert func(text string)

	once     sync.Once
	failures map[string]int  // username -> 连续失败次数
	alerted  map[string]bool // 是否已就本次连续失败告警过

	mu    sync.Mutex
	state tiktokmonitor.State
}

// NewTiktokMonitor 创建监控器。storageRoot 是 storage 目录的绝对路径。
// tiktokAuthorName 取 TikTok 视频作者名，用于跨平台去重。
//
// ★ 传昵称而不是 username（2026-10-04 用户口径）：
// 「不能用我们现在传的这个东西，需要真的拿到他们的昵称才可以」。
// 实测 hearts2hearts 这个账号 username=hearts2hearts、
// authorNickname 也是 Hearts2Hearts，与抖音订阅名、B 站作者名同源。
// 按 AuthorNick -> AuthorName -> 空串 顺序回落。
func tiktokAuthorName(v tiktokmonitor.Video) string {
	if n := strings.TrimSpace(v.AuthorNick); n != "" {
		return n
	}
	return strings.TrimSpace(v.AuthorName)
}

func NewTiktokMonitor(cfg ConfigProvider, send TiktokSender, storageRoot string) *TiktokMonitor {
	store := filepath.Join(storageRoot, tiktokStorageDir)
	return &TiktokMonitor{
		cfg:   cfg,
		send:  send,
		store: store,
		newClient: func(dir string) TiktokClient {
			return tiktokmonitor.Client{Dir: dir}
		},
		failures: map[string]int{},
		alerted:  map[string]bool{},
		onAlert:  func(string) {},
	}
}

// client 惰性构造采集客户端。
func (m *TiktokMonitor) client() TiktokClient {
	return m.newClient(m.store)
}

// ConfigProvider 是监控器需要的配置能力（用接口而非具体类型，便于测试）。
type ConfigProvider interface {
	ConfigPath() string
}

// TiktokSender 负责把作品推送到飞书。
//
// 告警方法返回待发送的文案而不是自己发，是为了让 sender 不必知道
// Bot 的管理员通知方式（目前是 notifyAdmins(string)，将来可能变）。
type TiktokSender interface {
	SendTiktokVideo(ctx context.Context, v tiktokmonitor.Video, localPath string, seconds int) error
	// AlertText 生成告警文案；不需要告警时返回空串。
	AlertText(username string, failures int, cause error) string
}

// Start 启动后台轮询。ctx 取消时停止。
//
// 首次运行只建立游标、不推送历史作品（见 tiktokmonitor.Pending），
// 所以上线的第一轮不会有任何消息发出，这是预期行为。
func (m *TiktokMonitor) Start(ctx context.Context, username string) {
	if username == "" {
		username = tiktokmonitor.DefaultUser
	}
	if err := tiktokmonitor.ValidateUserName(username); err != nil {
		log.Printf("[TikTok] 用户名不合法，已停止监控: %v", err)
		return
	}

	m.once.Do(func() {
		m.mu.Lock()
		m.state = tiktokmonitor.LoadState(m.store)
		m.mu.Unlock()

		log.Printf("[TikTok] 监控已启动 @%s，轮询间隔 %v（首轮仅建立基线，不推送历史）",
			username, tiktokScanInterval)
	})

	go func() {
		// 首轮稍微延后，避开服务启动高峰，也给限流留出冷却。
		timer := time.NewTimer(90 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		m.scan(ctx, username)
		ticker := time.NewTicker(tiktokScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("[TikTok] 监控已停止")
				return
			case <-ticker.C:
				m.scan(ctx, username)
			}
		}
	}()
}

// ensureState 懒加载状态并保证 map 非 nil。
//
// 为什么需要：State.Cursors 是 map，零值是 nil，直接赋值会 panic。
// Start 里通过 LoadState 初始化，但 scan 若被单独调用（测试、或将来加手动
// 触发端点）就会踩到。这里兜住，比在每个调用点判断可靠。
func (m *TiktokMonitor) ensureState() {
	if m.state.Cursors == nil {
		m.state = tiktokmonitor.LoadState(m.store)
	}
	if m.state.Cursors == nil {
		m.state.Cursors = map[string]tiktokmonitor.Cursor{}
	}
}

// scan 执行一轮采集与推送。
func (m *TiktokMonitor) scan(ctx context.Context, username string) {
	m.mu.Lock()
	m.ensureState()
	m.mu.Unlock()

	client := m.client()
	videos, err := client.Timeline(ctx, username, tiktokmonitor.DefaultLimit)
	if err != nil {
		m.noteFailure(ctx, username, err)
		return
	}
	log.Printf("[TikTok] 拉到 @%s 最新 %d 条作品", username, len(videos))

	m.mu.Lock()
	cursor := m.state.Cursors[username]
	m.mu.Unlock()

	pending := tiktokmonitor.Pending(cursor, username, videos)
	if len(pending) == 0 {
		// 首轮，或本轮无新内容。
		if !cursor.Ready {
			m.mu.Lock()
			all := videos
			c := tiktokmonitor.Advance(cursor, username, all, allIDs(all))
			c.UserID = ""
			m.state.Cursors[username] = c
			m.state.Save(m.store)
			m.mu.Unlock()
			log.Printf("[TikTok] 已建立基线（%d 条），后续只推新作品", len(all))
		} else {
			m.mu.Lock()
			c := cursor
			c.LastScan = time.Now()
			m.state.Cursors[username] = c
			m.state.Save(m.store)
			m.mu.Unlock()
		}
		m.clearFailure(username)
		return
	}

	// 跨平台去重：谁最早发就只推谁。
	ix := m.titleIndex()
	// 本轮采集时刻。去重比较必须用它而不是作品发布时间：作品发布时间只说明
	// 「谁先发稿」，而我们要回答「这条消息谁先发出去」。同一条片子 TikTok 完全
	// 可能先发稿、却因为 sidecar 晚几轮才抓到 —— 2026-10-06 线上就是这样，
	// B站 13:35 已经推送，TikTok 13:40 才抓到，因为比的是 13:29 < 13:30
	// 而把同一条视频又发了一遍。
	scanSeenAt := time.Now().UnixMilli()
	var processed []string
	// 本轮失败次数。>0 时不清零连续失败计数，否则告警永远触发不了。
	failed := 0
	for _, v := range pending {
		seconds := tiktokmonitor.Seconds(v)
		link := tiktokmonitor.Link(v)

		// 索引里已有更早的同一条内容 -> 别人先发了，跳过。
		// 作者标识用 username（TikTok 唯一 handle），跨语言去重的关键维度：
		// 同一条片子在 TikTok 是韩文标题、在中抖是中文，词重合度恒为 0，
		// 只能靠「同作者 + 时长一致 + 时间接近」判定。
		// ★ 作者维度传**昵称/团名**而不是 username（2026-10-04 修正）：
		// 实测 TikTok username=hearts2hearts，与抖音订阅名、B 站作者名
		// 归一化后完全一致；但 AuthorNick/AuthorName 更贴近「人看到的名字」，
		// 两者按顺序取第一个非空。
		// 告诉去重器本方是 TikTok。
		if firstSeen, skip := tiktokAlreadySentElsewhere(ix, v.Desc, seconds, tiktokAuthorName(v), scanSeenAt); skip {
			log.Printf("[TikTok] 跳过 %s：%s 已在 %s 被别的平台先发出（不下载）",
				v.ID, truncate(v.Desc, 40), time.UnixMilli(firstSeen).Format("01-02 15:04"))
			processed = append(processed, v.ID)
			continue
		}

		localPath, err := m.download(ctx, client, v.ID)
		if err != nil {
			// 下载失败不推进游标，下轮重试。
			log.Printf("[TikTok] 下载失败 %s: %v", v.ID, err)
			m.noteFailure(ctx, username, err)
			failed++
			continue
		}
		if seconds == 0 {
			// 列表接口没给时长时，从下载好的本地文件 ffprobe 一次
			// （与抖音侧 probeLocalVideoSeconds 同一套口径）。
			seconds = probeLocalVideoSeconds(localPath)
		}

		if err := m.send.SendTiktokVideo(ctx, v, localPath, seconds); err != nil {
			log.Printf("[TikTok] 发送失败 %s: %v（不推进游标，下轮重试）", v.ID, err)
			m.noteFailure(ctx, username, err)
			failed++
			continue
		}

		// 登记进跨平台索引：TikTok 这条也是「某平台已发」的凭据，
		// B站/抖音侧比对时会用到。
		// 登记推送时刻（此刻刚发送成功），不是 v.CreateTime（作品发布时间）。
		ix.RecordWithAuthor(v.Desc, time.Now().UnixMilli(), "tiktok", seconds, tiktokAuthorName(v))
		processed = append(processed, v.ID)
		log.Printf("[TikTok] 已推送 %s（%d 秒）%s", v.ID, seconds, link)
	}

	// 只把**成功处理过**的并进游标（含被跳过的）。
	m.mu.Lock()
	c := tiktokmonitor.Advance(cursor, username, videos, processed)
	m.state.Cursors[username] = c
	m.state.Save(m.store)
	m.mu.Unlock()

	// 有作品处理失败时不清零计数。
	// ★ 这里原先是无条件 clearFailure，导致「下载失败 -> 计数 +1 ->
	//   循环结束立刻归零」，告警阈值永远达不到，功能形同虚设。
	if failed == 0 {
		m.clearFailure(username)
	}
}

// download 下载作品视频。
func (m *TiktokMonitor) download(ctx context.Context, client TiktokClient, id string) (string, error) {
	d, err := client.Download(ctx, id)
	if err != nil {
		return "", err
	}
	if d.Path == "" {
		return "", fmt.Errorf("TikTok 作品 %s 未返回本地路径", id)
	}
	return d.Path, nil
}

// tiktokAlreadySentElsewhere 判断这条 TikTok 作品是否**已经被别的平台先把消息发出去**。
//
// seenAt 是本条被采集到、准备推送的时刻（毫秒）。比较基准必须是它而不是
// v.CreateTime：作品发布时间只说明「谁先发稿」，而去重要回答的是「谁先把消息
// 发出去」。同一条片子 TikTok 完全可能先发稿、却晚几轮才被抓到。
//
// 线上症状（2026-10-06 13:29-13:40，Hearts2Hearts 同一条 14 秒视频）：
//
//	13:29:25  TikTok 作品发布
//	13:30:00  B站作品发布
//	13:35:24  B站采集到 → 推送视频（索引登记 firstSeen=13:30:00）
//	13:36:12  抖音作品发布
//	13:38:24  抖音采集 → 13:30:00 < 13:36:12 → 正确跳过视频
//	13:40:31  TikTok 采集 → 13:30:00 < 13:29:25 为**假** → 判定「我首发」
//	          → 又下载又推送了一遍，用户收到两条同一视频
//
// 改成拿 scanSeenAt 比之后：13:30:00 < 13:40:31-2min → 认输跳过，且**在下载前**
// 就返回，媒体文件根本不会落地。
func tiktokAlreadySentElsewhere(ix *dedupe.Index, desc string, seconds int, author string, seenAt int64) (int64, bool) {
	if ix == nil {
		return 0, false
	}
	// 声明本方是 TikTok：activeSource 是包级变量，不设会沿用上一次判定的值。
	// 候选只从其它平台里找，因此自己登记过的条目不会把自己判掉。
	dedupe.SetActiveSource("tiktok")
	if seenAt <= 0 {
		seenAt = time.Now().UnixMilli()
	}
	firstSeen, ok := ix.MatchWithAuthor(desc, seconds, author, seenAt)
	if !ok {
		return 0, false
	}
	// 与 B站 / 抖音**同一判据**（2026-10-08 三处统一，见 crossdedupe.otherSentFirst）。
	// 原先这里是 ，与另外两处一样带方向性 bug。
	return firstSeen, otherSentFirst(firstSeen, seenAt)
}

// titleIndex 拿跨平台去重索引。TikTok 与抖音/B站共用同一个文件。
func (m *TiktokMonitor) titleIndex() *dedupe.Index {
	return crossTitleIndex(m.cfg.ConfigPath())
}

func (m *TiktokMonitor) noteFailure(ctx context.Context, username string, err error) {
	m.mu.Lock()
	m.failures[username]++
	n := m.failures[username]
	c := tiktokmonitor.NoteFailure(m.state.Cursors[username], username, err.Error())
	m.state.Cursors[username] = c
	m.state.Save(m.store)
	m.mu.Unlock()

	log.Printf("[TikTok] 第 %d 次连续失败: %v", n, err)

	if n >= tiktokAlertFailThreshold && !m.alerted[username] {
		m.alerted[username] = true
		if text := m.send.AlertText(username, n, err); text != "" {
			log.Printf("[TikTok] 告警: %s", strings.SplitN(text, "\n", 2)[0])
			m.onAlert(text)
		}
	}
}

func (m *TiktokMonitor) clearFailure(username string) {
	m.mu.Lock()
	if m.failures[username] > 0 {
		m.failures[username] = 0
	}
	m.alerted[username] = false
	m.mu.Unlock()
}

func allIDs(videos []tiktokmonitor.Video) []string {
	out := make([]string, 0, len(videos))
	for _, v := range videos {
		if v.ID != "" {
			out = append(out, v.ID)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
