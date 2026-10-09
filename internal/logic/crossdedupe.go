package logic

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/bilibili"
	"pocket48-bot/internal/dedupe"
)

// 抖音与 B 站的跨平台去重。
//
// 同一支短片常常两边都发，标题往往一致。原做法是「B 站只推长视频」
// （minVideoSeconds=1200），但有些内容 B 站反而比抖音先发，
// 于是真正的新内容被这个粗过滤器挡掉了。
//
// 现在的规则（用户口径）：
//   - 抖音：**全部推送**，同时把标题指纹与发布时间登记进索引。
//   - B 站：推送前查索引。若同一标题抖音更早发布，则跳过（抖音已经推过了）；
//     否则推送，并登记自己的发布时间。
//   - 标题匹配不上时**一律放行**，宁可多推也不漏推。
//
// 索引本身跨进程重启保留，因此「谁先发」这个判定不会因为重启而漂移。

const (
	// dedupePath 是标题指纹索引的落盘位置（相对 storage 根目录）。
	// 放在 cursors 目录下，与其它平台的游标文件同属一类「运行态状态」。
	dedupePath = "cursors/cross-platform-titles.json"
	// dedupeTTL 保留 14 天。短片的热度窗口远超一周，再久的记录没有参考价值，
	// 还会让索引无限膨胀。
	dedupeTTL = 14 * 24 * time.Hour
	// dedupeMaxSize 上限。心跳2Hearts 一天也就十几条，4096 足够覆盖两个月。
	dedupeMaxSize = 4096
)

var (
	crossOnce sync.Once
	crossIdx  *dedupe.Index
)

// storageRootOf 从 config.json 路径推出 **storage 目录**的绝对路径。
//
// ★ 目录关系（2026-10-04 线上踩坑）：部署布局是
//
//	/root/pocket48-bot/
//	  ├─ config.json
//	  └─ storage/
//	       ├─ douyin/  weverse/  x/  tiktok/  cursors/ ...
//
// 即 config.json 与 storage/ **同级**，storage 是它的子目录。
//
// 之前的注释写的是「config.json 位于 <storage>/config.json，因此上跳一级」——
// 这个前提是错的，于是 filepath.Dir() 只上跳到项目根，
// 索引被写到了 /root/pocket48-bot/cursors/ 而不是
// /root/pocket48-bot/storage/cursors/。TikTok 那边的 state.json 同样
// 跑到了 /root/pocket48-bot/tiktok/。
//
// 后果不只是「目录多一层」：storage/cursors/ 里已经躺着其它平台的游标文件，
// 索引混在项目根会让人误判「去重没生效」（实际是文件不在预期位置），
// 而且备份/清理脚本按 storage/ 扫时会整片漏掉。
//
// 全项目**只有这一处**允许推导 storage 根，其它地方一律调它，
// 免得同一个约定在三个文件里各写一遍、各错一遍。
func storageRootOf(configPath string) string {
	if configPath == "" {
		return "storage"
	}
	return filepath.Join(filepath.Dir(configPath), "storage")
}

// crossTitleIndex 返回全局标题指纹索引（首次调用时惰性创建）。
func crossTitleIndex(configPath string) *dedupe.Index {
	crossOnce.Do(func() {
		crossIdx = dedupe.NewIndex(
			filepath.Join(storageRootOf(configPath), dedupePath),
			dedupeTTL, dedupeMaxSize)
	})
	return crossIdx
}

// douyinRecordTitle 登记抖音作品的标题指纹、发布时间与时长。
//
// 只登记「视频」作品：图文（note）与 B 站不会同时发，登记了也没有对照价值，
// 反而会挤占索引容量。
//
// seconds 为视频时长（秒），传 0 表示未知（此时只参与一级指纹判定）。
func douyinRecordTitle(configPath, desc string, createTime int64, kind string, seconds int, author string) {
	if configPath == "" || kind == "note" {
		return
	}
	if createTime > 0 {
		createTime *= 1000 // 抖音用秒，B 站与索引统一用毫秒
	}
	// ★ 索引里的时间戳是**推送时刻**，不是 createTime（作品发布时间）。
	//   跨语言标题（中抖中文 / TikTok 韩文）词重合度恒为 0，只能靠
	//   「同作者 + 时长一致 + 时间接近」识别同一条；而「时间接近」比较的
	//   必须是消息发出的先后，用作品发布时间会得出相反结论
	//   （抖音 19:38 发布、B站 19:30 发布，但 B站的消息 19:38:29 才发出，
	//   真正先发出去的是抖音 —— 用发布时间比会判反）。
	//   调用点在视频推送**之后**，因此这里取 now 就是准确的推送时刻。
	crossTitleIndex(configPath).RecordWithAuthor(desc, time.Now().UnixMilli(), "douyin", seconds, author)
}

// bilibiliShouldSkipVideo 判断这条 B 站投稿是否应当跳过（抖音已先推过同一条）。
//
// 返回 false 时调用方照常推送。判定分两步：
//  1. 用 Match 找到同一条内容在索引里的首发时间。该方法内部会先试
//     「归一化指纹完全相等」，失败再试「核心词高度重合 + 时长在容差内」。
//     ★ 第二级是必需的：B 站标题常带「【Hearts2Hearts】」前缀，
//     2026-10-04 线上那次重复推送就是因为只比指纹导致漏判。
//  2. 索引里的首发时间**严格早于**本条发布时间，才认为「抖音已经推过了」。
//
// 注意 Seconds <= 0（时长未知）也参与判定：无法证明它是长视频，
// 就按短视频处理，宁可多推一条也不要漏。此时 Match 只会走一级指纹判定。
// bilibiliShouldSkipVideo 判断这条投稿的视频本体是否已被别的平台发过。
//
// seenAt 是**本条内容被 B 站采集到、准备推送的时刻**（毫秒）。
//
// ★★ 为什么必须区分 d.Time 与 seenAt（2026-10-05 线上实测踩出来的）：
//
// 索引里的 firstSeen 是各平台的**作品发布时间**，不是「谁先把消息发出去」。
// 而去重要回答的是「**视频本体这条消息，谁先发出去**」。
//
// 线上症状（20:40:55 抖音先推、20:45:57 B 站后推，同一条视频两边都发了）：
//
//	B 站 d.Time    = 20:30:00（作品发布时间，投稿时就定了，永远更早）
//	抖音 firstSeen = 20:39:43（作品发布时间，比 B 站晚 9 分钟）
//	B 站 seenAt    = 20:45:57（B 站接口晚收录，扫到时已经晚 5 分钟）
//
// 原代码只比 d.Time：`if firstSeen >= d.Time { return false }`
// B 站发布时间更早 => 判定「B 站首发」=> 放行=> 两边都发视频。
// 判定基准必须换成「谁先推出去」，seenAt 就是为此存在的。
//
// 抖音 / TikTok 侧同样只比作品发布时间，但它们的采集是 sidecar 主动推送、
// 延迟在秒级，暂未暴露；B 站是「投稿后接口晚收录」，最容易踩这个坑。
// crossSeenAtSlack 是「对方确实比我方先发出去」的判定余量（毫秒）。
//
// ★ 索引里的 firstSeen 现在存的是**我们把这条消息推送出去的时刻**
//
//	（不是作品发布时间），所以两边在同一个量纲上，余量只需要覆盖
//	「同一次处理里的先后顺序」与毫秒级抖动，3 秒足够。
//
// ★★ 为什么从 2 分钟降到 3 秒：2026-10-06 19:38 线上双发就是这个值造成的 ——
//
//	抖音 19:38:21 推送完成（登记 19:38:00 作品发布时间），B站 19:38:29 才扫到，
//	判定写成 firstSeen < seenAt-2分钟 => 19:38:00 < 19:36:29 为假 => 放行，
//	于是同一条视频在 8 秒内发了两遍。2 分钟余量只在「拿作品发布时间当推送
//	时刻」时才必要；改成记录真实推送时刻后它反而成了漏判的来源。
//
// ★ 这个常量原先是 bilibiliShouldSkipVideo 里的局部变量，2026-10-06 才知道
// 抖音与 TikTok 侧还在拿**作品发布时间**当基准，于是同一条片子会重演两次：
//
//	13:29:25  TikTok 作品发布
//	13:30:00  B站作品发布
//	13:35:24  B站采集到 → 推送（firstSeen 登记为 13:30:00）
//	13:36:12  抖音作品发布
//	13:38:24  抖音采集 → firstSeen 13:30:00 < 13:36:12 → 正确跳过
//	13:40:31  TikTok 采集 → firstSeen 13:30:00 < 13:29:25 为**假** → 误判自己是首发
//	          → 又下载又推送了一次（用户收到两条同一视频）
//
// 换成采集时刻后，TikTok 拿 13:40:31 - 2 分钟去比 13:30:00 → 认输跳过。
// 余量从 3 秒放宽到 30 秒（2026-10-08）。
//
//	线上漏判：11:01:10 TikTok 推送 → 11:01:12 B站扫到同一条，只差 2 秒，
//	3 秒余量刚好把它卡在门外 ⇒ 两边都下了视频本体。
//
//	为什么放宽是安全的（不会吞首发）：
//	  MatchWithAuthor 已经保证候选**来自别的平台**，且已在窗口内
//	  （MatchWindowMillis = 20 分钟）通过了 group + 时长 + 窗口三重筛。
//	  放宽的只是最后「对方算不算先发」的余量，不是匹配范围。
//	  唯一被牺牲的是「对方比我晚 0~30 秒」这种情况 —— 但那种情况
//	  本质是两边几乎同时处理同一条镜像，谁先谁后无关紧要，
//	  **吞掉的后果只是少下一次重复视频，不是首发丢失**。
//	  TestZZBiliKeepsWhenItIsFirst（对方登记比我方晚 523 秒 = 我方真首发）
//	  仍然放行，因为 523 秒远超这个余量。
//
// 余量 2 秒：只用来吸收同一毫秒/调度抖动，**不参与判定先后**。
//
// ★ 2026-10-08 定稿（前面两次都错，教训记在这）：
//
//	① 最初 3s：线上 11:01:10 TikTok → 11:01:12 B站（gap=2s）卡在门外漏判。
//	② 改 30s：方向性 bug 修好后，发现 19 分钟、8 秒这些真镜像
//	   也被「对方早得超过 30s ⇒ 基准不可信」挡掉，误伤更多。
//	③ 现状 2s + 正确方向（otherSentFirst）：
//	   判定 = 「对方比我早」且「差值 ≤ 2s」。slack 只防抖动，
//	   gap=2s 那次能认出，19 分钟 / 8 秒也不受影响。
//
// crossSeenAtSlack **不再参与判定**（见 otherSentFirst 的定稿说明）。
// 保留常量仅为兼容既有引用；判定逻辑不读它。
const crossSeenAtSlack = int64(2 * time.Second / time.Millisecond) // = 2000 毫秒

// otherSentFirst 报告「对方确实先发出去」。
//
// ★★★ 定稿（2026-10-08）。这个函数错了三版，教训是
//
//	**「对方比我早」本身就是完整判据，不存在任何需要额外余量的理由。**
//
//	错误演进：
//	  v1 （原代码）
//	     减法方向反了：slack 越大越难认输。gap=8s/slack=30s 实算为假。
//	  v2 （我第一版改法）
//	     过宽：会吞「我方其实更早」的首发。
//	  v3 （我第二版改法）
//	     仍然错：把 slack 当成了「基准可信度上限」，
//	     于是 gap=8s、19min 这些**最像真镜像**的场景反被排除。
//
//	正确且唯一的语义：
//
//	  MatchWithAuthor 返回 true 时，已经确认候选**来自别的平台**
//	  且通过了 group + 时长 + MatchWindowMillis(20分钟) 三重筛。
//	  在这个前提下，对方登记时刻早于我方采集时刻 ⇒ 就是对方先发 ⇒ 我方认输。
//	  gap 越大越像镜像，没有任何理由因为「差得多」就放弃。
//	  我方是否首发，由「对方比我晚」这一条自然保护（firstSeen >= seenAt）。
func otherSentFirst(firstSeen, seenAt int64) bool {
	return firstSeen < seenAt
}

func bilibiliShouldSkipVideo(ix *dedupe.Index, d bilibili.Dynamic, seenAt int64) bool {
	if ix == nil || d.Kind != "video" {
		return false
	}
	// 告诉去重器本方是B站：候选来自其它平台时才算跨平台镜像。
	dedupe.SetActiveSource("bilibili")
	if seenAt <= 0 {
		seenAt = time.Now().UnixMilli()
	}
	// ★★ publishedAt 必须传 **seenAt**，不能传 d.Time（2026-10-05 实测）。
	//
	// MatchWithAuthor 里的 matchByGroup 有这么一段：
	//
	//	gap := self - e.FirstSeen
	//	if gap < 0 || gap > MatchWindowMillis { continue }
	//
	// 传 d.Time 时：B 站作品发布时间 20:30 **比索引里抖音那条 20:39 更早**，
	// gap = -583000 < 0 => **候选被直接排除** => 匹配不上 => 放行 => 两边都发视频。
	//
	// 这正是线上那次漏判的直接原因（B 站 20:45 才扫到，却在 20:30 就想赢）。
	// 传 seenAt（20:45:57）时 gap = +374000，落在 20 分钟窗口内 => 正确匹配。
	firstSeen, ok := ix.MatchWithAuthor(d.Title, d.Seconds, bilibiliAuthorKey(d), seenAt)
	if !ok {
		return false
	}
	// 索引里有同一条：谁先把**视频本体**发出去，谁赢。
	//
	// firstSeen 是对方登记的**作品发布时间**，没有对方的推送时刻可比。
	// 保守做法：只在「对方发布时间明显早于我方采集时刻」时才认输，
	// 给 2 分钟余量 —— 双方发布时间本来就常常只差一两分钟。
	// 差值不足就放行：宁可多发也不要误吞（漏推比多推严重）。
	// ★ seenAt 是**毫秒**（time.Now().UnixMilli()），而 2*time.Minute
	//   是纳秒（1.2e11），直接相减会把 5 分钟的差值吃掉 120000 倍 ——
	//   判定永远为 false，等于去重完全失效。
	//   这就是第一版修复「看着对但没生效」的原因。
	// 判定保持「对方**确实更早**才算对方先发」（2026-10-08 改过一次又改回）。
	//
	// 线上漏判（11:01：TikTok 11:01:10 → B站 11:01:12，只差 2 秒）的正确修法
	// 在 dedupe.candidateSource —— 它原先多了一个与 self 的比较，把
	// 「跨平台跟发」误判成「同平台连发」而落到 MatchMinGap 分支上。
	//
	// ★★★ 跨平台镜像要能认出「只差 2~3 秒跟发」（2026-10-08 线上漏判）。
	//
	// 线上：11:01:10 TikTok 推送 → 11:01:12 B站扫到同一条（18 秒），
	// 旧判定 `firstSeen < seenAt-3000` 要求差 3 秒以上，2 秒卡在门外
	// ⇒ 放行 ⇒ 同一条视频两边都下了本体。
	//
	// 先后怎么判才既不漏判、又不吞首发：
	//   对方来自**别的平台**（MatchWithAuthor 已保证）⇒ 同一条内容
	//   不可能两个平台各自首发，索引里那条就是对方先发的证据；
	//   但仍要给「我方其实更早」留出口 —— TestZZBiliKeepsWhenItIsFirst
	//   （对方登记比我方 seenAt 晚 523 秒 = 我方首发）必须放行，
	//   **首发被吞 ⇒ 那条片子永远发不出去，比多发严重得多**。
	//   所以：**对方不比我晚 3 秒以上 ⇒ 认输**；比我方明显晚 ⇒ 我方首发，放行。
	// ★★★ 方向性 bug（2026-10-08 修）：原写法 `firstSeen < seenAt-slack`
	//
	//	语义应是「对方比我早、且早得不算多」，而减法把门槛推向了**更早**。
	//	探针实测（slack=30s、线上 gap=8s）：
	//	  19:38:21 < 19:38:29 - 30s(=19:37:59) → 假 ⇒ 不认输 ⇒ 放行
	//	也就是 slack 越大越难认输，与本意完全相反。
	//	（历史上把 2 分钟改成 3 秒「看起来更合理」，只是因为 8 秒 > 3 秒
	//	  恰好绕过了这个坑，判定逻辑本身从来没对过。）
	//
	// 顺带说明为什么不改成「firstSeen <= seenAt+1秒」（试过，更糟）：
	//	那会让「我方其实更早、对方登记在扫描缓存里」也被吞 ——
	//	TestZZDouyinKeepsVideoWhenItIsFirst 守着的就是这条，
	//	**首发被吞 ⇒ 那条片子永远发不出去，比多发严重得多**。
	return otherSentFirst(firstSeen, seenAt)
}

// bilibiliShouldSkipVideoLegacy 是旧签名（不带 seenAt），保留给既有测试。
// 它用当前时刻当 seenAt，等价于「本条现在才被看到」。
func bilibiliShouldSkipVideoLegacy(ix *dedupe.Index, d bilibili.Dynamic) bool {
	return bilibiliShouldSkipVideo(ix, d, 0)
}

// bilibiliAuthorKey 取 B 站投稿的作者标识。
//
// ★ 必须用**昵称/团名**（Author），不能用 mid（2026-10-04 实测修正）：
// mid 是 B 站内部 ID，与抖音 sec_uid / TikTok username 跨平台永不相等。
// 实测该UP 主的昵称就是 "Hearts2Hearts"，与抖音订阅名、TikTok username
// 归一化后完全一致，这才是能跨平台对齐的那一维。
//
// 另外实测确认：B 站**取不到标签** —— seasons_series_list 与
// x/web-interface/view 两个接口的 tags 都是 null、tname 是空串，
// 因为该 UP 主没有使用 B 站的标签功能。所以 B 站侧的跨平台对齐
// 完全依赖昵称与标题里的【Hearts2Hearts】前缀，不要指望 tags。
//
// 昵称可变，改名会被当成两个人而漏判；同一批偶像账号极少变动，
// 且这个维度只在「强标识交集 + 时长一致 + 6 小时内 + 候选唯一」时才生效。
func bilibiliAuthorKey(d bilibili.Dynamic) string {
	return strings.TrimSpace(d.Author)
}

// bilibiliIsShortVideo 判断投稿是否属于「短视频」，即值得内嵌的时长区间。
//
// 长视频（团综）动辄几十分钟，内嵌既慢又可能超出 IM 消息体积限制，
// 因此只对短片取 mp4 直链。阈值取 10 分钟：实测 Hearts2Hearts 的短视频
// 集中在 8~200 秒，长视频从 20 分钟起，两者中间没有模糊地带。
const bilibiliShortVideoMaxSeconds = 600

func bilibiliIsShortVideo(d bilibili.Dynamic) bool {
	return d.Kind == "video" && d.Seconds > 0 && d.Seconds <= bilibiliShortVideoMaxSeconds
}

// probeLocalVideoSeconds 从已下载的本地视频里读时长（秒）。
//
// 为什么不单独请求接口拿时长：抖音作品推送时本来就要把视频下到本地
// （localizeDouyinVideo），顺手 ffprobe 一下即可，不额外产生网络开销。
//
// 拿不到时返回 0，调用方据此关闭二级判定（只按标题指纹判），保证保守。
func probeLocalVideoSeconds(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	).Output()
	if err != nil {
		return 0
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" || raw == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return int(math.Round(f))
}

// douyinVideoAlreadySent 判断这条抖音作品的**视频本体**是否已被别的平台发过。
//
// ★ 口径（用户 2026-10-05 明确）：
//
//	「视频本体我们只发第一次；20 分钟内这个时长的视频我们就不下载、不发送；
//	  文字、封面这些都是会发的。」
//
// 所以这里返回 true 时**只跳视频**，调用方照常发文字与封面 ——
// 与 bilibiliShouldSkipVideo 那种「整条跳过」是不同语义，别混用。
//
// ★ 依赖 post.Duration：抖音接口的 video.duration 已经在返回里（毫秒），
//
//	必须一路带到 Go 侧（douyinPost.Duration）才能在下载前判定。
//	时长为 0（接口没给）时保守放行 —— 宁可多发一次视频，
//	也不能因为拿不到时长就把该发的内容吞掉。
func douyinVideoAlreadySent(configPath string, post douyinPost) bool {
	if configPath == "" {
		return false
	}
	return douyinVideoAlreadySentIn(crossTitleIndex(configPath), post)
}

// douyinVideoAlreadySentIn 是实际判定，**只依赖 Index**。
//
// ★ 为什么要拆成两层：crossTitleIndex 是 sync.Once 单例
//
//	（记忆里记过：重置单例测路径行不通，crossOnce 重置后 crossIdx
//	仍指向旧实例）。生产入口固定走全局索引，
//	而测试要能塞进临时索引 —— 所以判定本身必须能注入 Index。
func douyinVideoAlreadySentIn(ix *dedupe.Index, post douyinPost) bool {
	return douyinVideoAlreadySentAt(ix, post, time.Now().UnixMilli())
}

// douyinVideoAlreadySentAt 是实际判定。seenAt 是「本条被采集到、准备推送的时刻」。
//
// 单独暴露这个入口是为了可测：MatchWithAuthor 里的「时间接近」窗口只有
// 20 分钟，用例若把时间写成固定历史时刻，跑得越久离 now 越远就越会失效
// （这种"一开始能过、跑半小时就挂"的测试最费时间）。
// 生产入口仍走 douyinVideoAlreadySentIn，内部取当前时刻。
func douyinVideoAlreadySentAt(ix *dedupe.Index, post douyinPost, seenAt int64) bool {
	if ix == nil || post.Type == "note" {
		return false
	}
	// 没有可下载的视频地址，本来就不会发视频，无需判定。
	if !strings.HasPrefix(post.VideoURL, "http") {
		return false
	}
	if post.Duration <= 0 {
		return false
	}
	// ★ 必须声明本方是抖音：activeSource 是包级变量，不设就会沿用
	//   上一次判定留下的值（其它平台或上一次调用），结果不可预期。
	//   候选来自别的平台时才算跨平台镜像（2026-10-05 新增的判定维度）。
	dedupe.SetActiveSource("douyin")
	// seenAt 是**本条被采集到、准备推送的时刻**，不是它的发布时间。
	// 用发布时间比较会误判「我发布得更早所以该我发」，而实际上别的平台
	// 可能已经把这条消息发出去了（TikTok 侧 2026-10-06 就是这么重发的）。
	if seenAt <= 0 {
		seenAt = time.Now().UnixMilli()
	}
	firstSeen, ok := ix.MatchWithAuthor(
		post.Desc, post.Duration, douyinWorksAuthorKey(post), seenAt)
	if !ok {
		return false
	}
	// 对方**不比我晚**就算对方先发出去，跳视频本体。
	//
	// ★ 与 B站侧同一口径：必须保持「对方确实更早」+ 余量。
	//   放宽成「不比我晚」会让「抖音比索引更早 = 抖音是首发」被误吞 ——
	//   TestZZDouyinKeepsVideoWhenItIsFirst 就是守着这条的。
	//   首发被吞 ⇒ 那条片子永远发不出去，比多发严重得多。
	//   2 秒镜像的漏判修在 dedupe.candidateSource，不在这里。
	return otherSentFirst(firstSeen, seenAt)
}

// douyinWorksAuthorKey 取抖音作品的作者标识（团名/昵称），不是 sec_uid。
//
// ★ 与 bilibiliAuthorKey / tiktokAuthorName 同一个道理：
//
//	跨平台比对的锚点是**团名**，三者归一化后必须相等；
//	内部ID（sec_uid / mid / username）跨平台永不相等，会让兜底判定成死代码。
func douyinWorksAuthorKey(post douyinPost) string {
	if nick := strings.TrimSpace(post.Nickname); nick != "" {
		return dedupe.CanonicalGroupName(nick)
	}
	return ""
}
