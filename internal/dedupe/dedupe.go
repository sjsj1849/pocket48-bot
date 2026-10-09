// Package dedupe 提供跨平台的「同一条内容只推一次」判定。
//
// 背景：同一支短片常常同时出现在抖音、TikTok 与 B 站。各平台加的标签、
// 前缀并不一致（例如 B 站标题会写成「【Hearts2Hearts】xxx」而抖音只是
// 「xxx」），因此仅靠「归一化后完全相等」会漏判。
//
// 现在的判定是两级：
//  1. 归一化指纹完全相等 —— 最强证据，直接判同一条。
//  2. 指纹不等时，退化为「核心词集合高度重合 + 时长在容差内」——
//     短视频的时长是极强的特征（实测同一支片子三平台时长小数点都一致），
//     两者同时成立才判同一条。
//
// 策略整体仍然保守：任何一条证据不足都放行，宁可多推也不漏推。
package dedupe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Entry 记录某个归一化标题指纹在某一平台的首见时间。
type Entry struct {
	// FirstSeen 是该指纹在来源平台首次被推送的时间（unix 毫秒）。
	FirstSeen int64 `json:"firstSeen"`
	// Source 仅为调试与排查方便，可留空。
	Source string `json:"source,omitempty"`
	// Seconds 是该条内容的视频时长（秒）。0 表示当时没能取到。
	//
	// 这是二级判定的关键：标题模糊匹配单独用太容易误判，
	// 必须与时长吻合才认定为同一条。
	Seconds int `json:"seconds,omitempty"`
	// Title 是登记时的原始标题（已去掉话题标签）。
	//
	// 二级判定要拿它跟别处的新标题算词重合度，因此必须留存原文；
	// 只留归一化指纹是不够的 —— 指纹没有词边界，切不出核心词。
	Title string `json:"title,omitempty"`
	// Group 是这条内容的团名候选（归一化后），见 GroupKeys。
	//
	// ★ 这是跨平台去重唯一的可靠锚点（2026-10-04 实测）：
	// 正文在四个平台互不相同（中文「明天见嘻嘻」、韩文「내 봥 ㅋㅋ」、
	// 微博「明天见科科」），词重合度恒为 0；
	// 而团名四处都是 Hearts2Hearts —— 抖音订阅名、TikTok username、
	// B 站作者名与标题【】前缀实测都是它。
	//
	// 早期版本存的是平台内部 ID（抖音 sec_uid / TikTok username /
	// B 站 mid），这三者跨平台永不相等，整个兜底判定成了死代码。
	Group []string `json:"group,omitempty"`
}

type store struct {
	// Titles 是 归一化标题 -> Entry 的映射。
	Titles map[string]Entry `json:"titles"`
}

// Index 是一个带互斥保护的标题指纹索引，支持跨进程重启保留。
type Index struct {
	mu      sync.Mutex
	path    string
	ttl     time.Duration
	maxSize int
	data    store
	loaded  bool
}

// Normalize 把原始标题归一化为可比对的指纹：
// 去掉话题标签（#xxx 与「#xxx」）、去掉全部标点与空白、统一小写。
// 这样 "【Hearts2Hearts】it’s oct 3rd yk #H2H" 与
// "Hearts2Hearts it's oct 3rd yk" 会得到同一个指纹。
//
// 话题标签的结束点判定很关键：标签只延续到下一个空白为止，
// 因此 "dance #tag name" 剥离后是 "dance name" 而不是把 name 也吞掉。
func Normalize(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(title))
	runes := []rune(title)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		// 话题标签：'#' 起延续到下一个空白（或文本结束）为止，整体丢弃。
		if r == '#' {
			for i < len(runes) && !unicode.IsSpace(runes[i]) {
				i++
			}
			// 跳过标签与后文之间的分隔空白，但不消耗正文字符。
			continue
		}
		// 括号类包裹（【】()（）[]）直接丢掉，标题正文不受影响。
		if r == '【' || r == '】' || r == '[' || r == ']' || r == '（' || r == '）' {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		// 标点与空白一律丢弃（含全角空格）。
	}
	return b.String()
}

// CoreWords 把标题切成「有意义的词」，用于二级相似判定。
//
// 归一化结果是一串没有边界的字母数字（如 "likeificanthelpfallinginlovewithyou"），
// 无法直接切词。这里重新按原文分词：小写、保留字母数字、丢弃其余。
// 中文没有空格分隔，会被切成整段；这对本项目够用 —— 中英混排的短视频文案里，
// 决定「是不是同一条」的主要是那段英文，中文片段短且模板化。
func CoreWords(title string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() >= 3 { // 单字母/双字母词几乎没有区分度
			words = append(words, cur.String())
		}
		cur.Reset()
	}
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return words
}

// Similarity 返回两段标题的核心词重合度（Jaccard），取值 0~1。
// 词数不足 3 个时返回 0 —— 样本太少，任何重合都不可信。
func Similarity(a, b string) float64 {
	wa := CoreWords(a)
	wb := CoreWords(b)
	if len(wa) < 3 || len(wb) < 3 {
		return 0
	}
	setA := make(map[string]struct{}, len(wa))
	for _, w := range wa {
		setA[w] = struct{}{}
	}
	setB := make(map[string]struct{}, len(wb))
	for _, w := range wb {
		setB[w] = struct{}{}
	}
	inter := 0
	for w := range setA {
		if _, ok := setB[w]; ok {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// 时长容差（秒）。
//
// 实测同一支片子三平台时长完全一致（116.266667 / 116.266667），
// 但转码/重传可能带来秒级误差，因此给 3 秒余量。
// 同时要求至少 1 秒，避免「两个都是 0（未知）」被误判成一致。
const (
	DurationToleranceSeconds = 3
	durationMinSeconds       = 1
)

// DurationMatch 判断两个时长是否可视为同一条内容。
//
// 规则：
//   - 任一为 0（未知）→ 不匹配。宁可漏判也不误判。
//   - 绝对差 <= DurationToleranceSeconds → 匹配。
func DurationMatch(a, b int) bool {
	if a < durationMinSeconds || b < durationMinSeconds {
		return false
	}
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff <= DurationToleranceSeconds
}

// 二级判定的相似度阈值。
//
// 0.75 意味着双方各有 4 个词时至少 3 个相同（6/8=0.75）。
// 定得偏高是因为一旦误判就会漏推真正的首发内容，用户宁可多收一条。
const SimilarityThreshold = 0.75

// SimilarTitle 判断两条标题是否「疑似同一条内容」（不含时长）。
// 仅用于诊断与测试；生产判定请用 Match。
func SimilarTitle(a, b string) bool {
	return Similarity(a, b) >= SimilarityThreshold
}

// NewIndex 创建（或载入）指定路径的标题索引。
// ttl 为条目保留时长，过期条目在 Load/Record 时被清理。
func NewIndex(path string, ttl time.Duration, maxSize int) *Index {
	if maxSize <= 0 {
		maxSize = 4096
	}
	return &Index{path: path, ttl: ttl, maxSize: maxSize}
}

func (ix *Index) loadLocked() {
	ix.loaded = true
	ix.data = store{Titles: map[string]Entry{}}
	raw, err := os.ReadFile(ix.path)
	if err != nil {
		return
	}
	var s store
	if json.Unmarshal(raw, &s) != nil {
		return
	}
	if s.Titles == nil {
		s.Titles = map[string]Entry{}
	}
	ix.data = s
	ix.pruneLocked()
	ix.backfillGroupsLocked()
}

// backfillGroupsLocked 给**存量**条目补上团名。
//
// ★ 为什么必须回填（2026-10-04 线上实测）：
//
//	group 是后来才加的字段，旧二进制登记的条目里根本没有它。
//	matchByGroup 第一步就是 groupOverlap，于是这些历史条目
//	一条都不可能成为判据 —— 表现就是「明明三条记录都在，判定却说候选为 0」。
//
//	实测索引（storage/cursors/cross-platform-titles.json）：
//	    hearts2hearts明天见嘻嘻  src=bilibili sec=22  group=<MISSING>
//	    明天见嘻嘻                src=douyin   sec=21  group=<MISSING>
//	    내봥ㅋㅋ                src=tiktok   sec=21  group=<MISSING>
//	而唯一带 group 的是当天最新登记的那条。
//
// 回填从**已存的 Title** 里抽【】前缀，这是无损的：
// Title 本来就为了二级相似判定而留着，只是没人拿它反推团名。
// B 站标题「【Hearts2Hearts】明天见嘻嘻 ♡」正好能抽出来。
//
// 只从 Title 抽、**不从 Author 抽**：Entry 里没存 author（作者是登记时的入参，
// 落盘时没写进去），无中生有反而会引入假锚点。
//
// 回填不落盘也没关系：下一条 record 会把整个 map 一起写回去，
// 而这个计算量只有几十条，不值得为它单独加一次 IO。
func (ix *Index) backfillGroupsLocked() {
	for k, e := range ix.data.Titles {
		if len(e.Group) > 0 || e.Title == "" {
			continue
		}
		if g := GroupKeys(e.Title, ""); len(g) > 0 {
			e.Group = g
			ix.data.Titles[k] = e
		}
	}
}

func (ix *Index) pruneLocked() {
	cutoff := time.Now().Add(-ix.ttl).UnixMilli()
	kept := make(map[string]Entry, len(ix.data.Titles))
	for k, v := range ix.data.Titles {
		if v.FirstSeen < cutoff {
			continue
		}
		kept[k] = v
	}
	// 超量时按时间倒序保留最新的若干条。
	if len(kept) > ix.maxSize {
		type kv struct {
			k string
			v Entry
		}
		all := make([]kv, 0, len(kept))
		for k, v := range kept {
			all = append(all, kv{k, v})
		}
		// 简单选择排序：按 FirstSeen 降序。maxSize 通常很小，开销可忽略。
		for i := 0; i < len(all); i++ {
			for j := i + 1; j < len(all); j++ {
				if all[j].v.FirstSeen > all[i].v.FirstSeen {
					all[i], all[j] = all[j], all[i]
				}
			}
		}
		kept = make(map[string]Entry, ix.maxSize)
		for i := 0; i < ix.maxSize; i++ {
			kept[all[i].k] = all[i].v
		}
	}
	ix.data.Titles = kept
}

func (ix *Index) saveLocked() {
	if err := os.MkdirAll(filepath.Dir(ix.path), 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(ix.data)
	if err != nil {
		return
	}
	tmp := ix.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, ix.path)
}

// Record 登记一条已推送内容（不带作者信息）。
func (ix *Index) Record(title string, publishedAt int64, source string, seconds int) {
	ix.record(title, publishedAt, source, seconds, "")
}

// RecordWithAuthor 登记一条已推送内容，并记录发布者。
//
// 跨语言场景（同一条片子在中抖是中文、在 TikTok 是韩文）只能靠
// 「同作者 + 时间接近 + 时长几乎一致」来判定，作者信息是必需的。
func (ix *Index) RecordWithAuthor(title string, publishedAt int64, source string, seconds int, author string) {
	ix.record(title, publishedAt, source, seconds, strings.TrimSpace(author))
}

func (ix *Index) record(title string, publishedAt int64, source string, seconds int, author string) {
	key := Normalize(title)
	if key == "" {
		return
	}
	publishedAt = ix.sanitizePublishedAt(publishedAt)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		ix.loadLocked()
	}
	// 团名来自标题【】前缀与作者名，见 GroupKeys。
	group := GroupKeys(title, author)
	if prev, ok := ix.data.Titles[key]; ok && prev.FirstSeen <= publishedAt {
		// 已有更早的记录，但这次拿到了时长或原文就补上
		// （首发方当时可能没取到视频，拿不到时长）。
		changed := false
		if seconds > 0 && prev.Seconds == 0 {
			prev.Seconds = seconds
			changed = true
		}
		if prev.Title == "" {
			prev.Title = title
			changed = true
		}
		// 团名只增不减：首发方当时可能还没抽到
		// （例如抖音采集侧首推时正文被截断），后续平台能补上。
		if len(prev.Group) < len(group) {
			prev.Group = group
			changed = true
		}
		if changed {
			ix.data.Titles[key] = prev
			ix.saveLocked()
		}
		return
	}
	ix.data.Titles[key] = Entry{
		FirstSeen: publishedAt,
		Source:    source,
		Seconds:   seconds,
		Title:     title,
		Group:     group,
	}
	ix.pruneLocked()
	ix.saveLocked()
}

// sanitizePublishedAt 把发布时间夹到「可信区间」内。
//
// 背景（2026-10-04 补）：pruneLocked 会丢掉超过 TTL 的记录，且 Match
// 命中后会据此判断「别人更早发布」。因此一旦采集侧传进来的时间戳异常，
// 后果是双向的：
//
//   - 时间戳是 1970 年 / 太旧 -> 登记后**立刻被 prune 清掉**，
//     跨平台去重静默失效（同一条会被重复推送，正是用户报的那个问题）。
//   - 时间戳是未来（比如接口把毫秒当秒返回）-> **永远不会被 prune**，
//     而且 Match 会命中这条「未来时间」，让所有同内容的后续记录都被
//     判成「别人更早」而永久跳过，漏推。
//
// 两种都静默、且都很难从日志看出来，因此必须在入口夹住。
//
// 规则：距今超过一个 TTL 视为不可信，一律用当前时间代替；
// 未来超过 1 小时的同样视为不可信。
func (ix *Index) sanitizePublishedAt(publishedAt int64) int64 {
	now := time.Now().UnixMilli()
	if publishedAt <= 0 {
		return now
	}
	ttlMillis := ix.ttl.Milliseconds()
	if ttlMillis <= 0 {
		ttlMillis = (14 * 24 * time.Hour).Milliseconds()
	}
	if publishedAt < now-ttlMillis {
		// 早于整个 TTL 窗口：不可信，按现在处理。
		return now
	}
	if publishedAt > now+int64(time.Hour/time.Millisecond) {
		// 未来超过 1 小时：时钟漂移或单位搞错（如毫秒当秒）。
		return now
	}
	return publishedAt
}

// Lookup 返回该标题指纹的首次出现时间；ok=false 表示来源平台没见过。
func (ix *Index) Lookup(title string) (publishedAt int64, ok bool) {
	key := Normalize(title)
	if key == "" {
		return 0, false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		ix.loadLocked()
	}
	e, found := ix.data.Titles[key]
	if !found {
		return 0, false
	}
	return e.FirstSeen, true
}

// Match 查找该标题对应的首发记录（publishedAt>0 表示确实找到）。
//
// 两级判定：
//  1. 归一化指纹完全相等 —— 直接命中。
//  2. 指纹不等时扫全表，找「核心词重合度 >= 阈值 且 时长一致」的条目。
//     这一级专门对付「B 站标题多一段前缀」这类情况。
//
// 找不到时返回 ok=false，调用方应放行。
//
// seconds 为本条内容的时长（秒），传 0 表示未知 —— 此时二级判定不生效，
// 只走一级，避免仅凭标题相似就误判。
// Match 只按标题 + 时长判定（不含强标识维度）。
//
// 同语言场景够用；跨语言（同一条片子在中抖是中文、在 TikTok 是韩文，
// 微博又写成「明天见科科」）必须改用 MatchWithAuthor，
// 否则正文词重合度恒为 0、必然漏判。
// ★ 注意：Match 查的是「索引里有没有这条」，自身来源按空处理，
//
//	因此**不会**因为 activeSource 恰好是本平台而过滤掉本平台的条目。
//	activeSource 是包级状态（见 SetActiveSource），调用方残留的值不该
//	影响一个与平台无关的查询 —— 2026-10-06 修自身条目过滤时踩到过：
//	测试用 Match 验证「推送后已登记」，却因上一次判定留下的 activeSource
//	把这条记录当成「自己登记的」而滤掉。
func (ix *Index) Match(title string, seconds int) (publishedAt int64, ok bool) {
	return ix.match(title, seconds, "", 0, "")
}

// MatchWithAuthor 在 Match 的两级判定之外，增加一级「跨语言兜底」。
//
// 兜底条件（必须全部成立）：
//  1. 对方条目有作者且与本条作者一致（否则毫无关联性）；
//  2. 时长几乎一致（<= DurationToleranceSeconds，且都已知）；
//  3. 两条发布时间相距不超过 SameAuthorWindowMillis。
//
// 为什么必须有这一级：实测心连心同一条片子，抖音标题「明天见嘻嘻」、
// TikTok 标题「낼 봥 ㅋㅋ」，Jaccard 恒为 0。时长都是 21 秒、
// 发布相差 15 分钟 —— 只有「同作者 + 短时间 + 时长一致」这个组合
// 才能识别出来。用户也正是按这个思路提的需求。
//
// 为什么仍然保守：作者一致 + 3 小时内 + 时长误差 3 秒内。
// 同一作者不会在 3 小时内连发两条时长几乎相同的视频；
// 而漏判的代价（用户收到三条重复消息）远大于误判。
func (ix *Index) MatchWithAuthor(title string, seconds int, author string, publishedAt int64) (int64, bool) {
	return ix.match(title, seconds, author, publishedAt, activeSource)
}

// match 是两个入口共用的实现。selfSource 为 "" 时不排除任何来源的条目。
func (ix *Index) match(title string, seconds int, author string, publishedAt int64, selfSource string) (int64, bool) {
	key := Normalize(title)
	if key == "" {
		return 0, false
	}
	author = strings.TrimSpace(author)
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		ix.loadLocked()
	}
	// 一级：指纹完全相等。
	//
	// ★ 必须跳过**本方自己登记的条目**（2026-10-06 修）。跨平台去重要回答的是
	//   「别的平台有没有把这条消息发出去」，自己登记的那条恰恰是「我发过了」。
	//   不过滤会有两个后果：
	//     1. 发送失败重试时（游标未推进、下一轮又抓到）会匹配到自己 → 判定跳过
	//        → 这条作品**永远发不出去**；
	//     2. 一级判定命中后直接 return，本方源信息根本没机会参与后面的过滤。
	// ★★★ 一级判定（标题指纹完全相同）—— 三重校验（2026-10-08 用户定稿）
	//
	// 用户口径原话：
	//	「即使标题一样，我们也是要对比时长的。」（①）
	//	「即使标题和时长一样，我们也是要对比发送时间的。」（②）
	//	「如果间隔在 3 天以上，即使标题和时长一样，这个视频也不能被跳过。」
	//	「20 分钟的窗口仅适用于没有完全匹配标题的情况。」
	//
	// 也就是说两级判定用**不同的窗口**：
	//
	//	一级（标题完全相同）  → FingerprintWindowMillis = 3 天
	//	二级/兜底（跨语言、标题不同）→ MatchWindowMillis = 20 分钟
	//
	// 为什么一级要放宽到 3 天：标题相同往往就是同一条内容（同一支团、同一段
	//素材在多平台搬运），镜像发布可能隔数小时；20 分钟会把这类真镜像放过。
	// 而 3 天以上几乎必然是「同名不同内容」（同团反复用同一首歌/同一段
	// 卡段做不同剪辑，标题自然重复），必须放行。
	//
	// 为什么一级还要比对时长（①）：同名不同内容里，时长一致才能证明
	// 真的是同一条。实测同名同长的确实是同一支片子；而时长不同说明
	// 是同名的两支不同剪辑，必须各自发。
	if e, found := ix.data.Titles[key]; found && !isOwnEntry(selfSource, e) {
		switch {
		case seconds <= 0 || e.Seconds <= 0:
			// 时长未知（拿不到就不编造理由）⇒ 只按指纹 + 时间判
			if withinFingerprintWindow(publishedAt, e.FirstSeen) {
				return e.FirstSeen, true
			}
		case !DurationMatch(seconds, e.Seconds):
			// ① 标题相同但时长不同 ⇒ 不是同一条，放行
		case withinFingerprintWindow(publishedAt, e.FirstSeen):
			// ② 标题+时长都一致，且 ②发送时间在 3 天内 ⇒ 镜像
			return e.FirstSeen, true
		}
		// 落到这里（指纹相同但时长不同/ 或超过 3 天）继续走二级兜底，
		// 由团名+时长+20 分钟窗口再判一次。
	}
	// 兜底：团名相交 + 时长一致 + 落在 15 分钟窗口内。
	// 这是跨平台/跨语言场景下唯一走得通的路 —— 正文相似度那条路
	// 对中文与跨语言标题恒为 0（详见 matchByGroup 的说明）。
	// publishedAt 是本条发布时间（毫秒）；传 0 表示未知，此时直接放行。
	if best, found := matchByGroup(ix.data.Titles, GroupKeys(title, author), seconds, publishedAt, selfSource); found {
		return best, true
	}
	// 二级：相似标题 + 时长一致。
	// 两者必须同时成立 —— 只靠标题太容易把「同一系列的不同一集」误判成同一条。
	if seconds < durationMinSeconds {
		return 0, false
	}
	// 同等相似度时取最早的那条，保证「谁先发」判定稳定。
	best := int64(0)
	found := false
	for _, e := range ix.data.Titles {
		if e.Title == "" || !DurationMatch(seconds, e.Seconds) {
			continue
		}
		// 同上：本方自己登记的条目不算「别的平台发过」。
		if isOwnEntry(selfSource, e) {
			continue
		}
		if !SimilarTitle(title, e.Title) {
			continue
		}
		// ★★★ 二级判定：窗口是**20 分钟**（MatchWindowMillis），
		//
		//	不是一级那个 3 天。依据用户定稿：「20 分钟的窗口仅适用于
		//	没有完全匹配标题的情况」—— 本级就是标题**不完全**相同的场景
		//	（跨语言、团名+时长的碰巧对齐），必须收紧。
		//
		//	线上漏判：10-07 21:06 TikTok 发过同名同长的视频，
		//	10-08 11:11 抖音发同名同长的**新作品**被永久判重，
		//	当天一条视频都没发（当时这一级完全不查时间）。
		//
		// 线上漏判：10-07 21:06 TikTok 发过「so you coming back or what」（12 秒），
		// 10-08 11:11 抖音发同名同长的**新作品**，被这一级判成重复而跳过视频，
		// 当天一条视频都没发。用户：「20 分钟窗口早就过了呀」。
		//
		// 原先这一级只比「标题相似 + 时长」，完全不看时间 ——
		// 于是一条记录只要进了索引就永久生效。同团同名卡段在不同日期
		// 反复出现是常态，不设窗口必然误吞。
		if !withinMatchWindow(publishedAt, e.FirstSeen) {
			continue
		}
		if !found || e.FirstSeen < best {
			best = e.FirstSeen
			found = true
		}
	}
	return best, found
}

// matchByGroup 跨平台去重的核心判定（2026-10-04 用户口径重写）。
//
// # 规则（用户原话）
//
// 「只要把 hearts to hearts 这个名字提取出来，以第一个拿到的视频时间为基准，
//
//	往后推 15 分钟。在这 15 分钟内，如果不同平台来源于 hearts to hearts 的
//	视频时长和它一致，视频本体我们就只发第一个，其他的都不发了。」
//
// 三条判据，缺一不可：
//
//  1. 团名相交 —— 证明是同一个组合/同一个人。
//     来源：标题【】前缀 与 作者昵称。实测四个平台的作者名都叫
//     Hearts2Hearts（抖音订阅名、TikTok username、B 站作者名与
//     标题【】前缀），归一化后完全相等。
//  2. 时长一致（<= 3 秒）—— 证明是同一支片子。
//     用户判断「如果不是同一个视频，时长很难完全一样」。
//     实测同一条片子三平台时长 21/21/22 秒，误差来自各自转码。
//  3. 落在 15 分钟窗口内 + 候选唯一 —— 窗口保证是同一批镜像发布；
//     唯一性保证不是「同一账号刚发过另一条同长度视频」。
//
// # 为什么丢弃标题相似度
//
// 正文在四个平台互不相同（中文「明天见嘻嘻」、韩文「내 봥 ㅋㅋ」、
// 微博「明天见科科」），跨语言词重合度恒为 0，中文之间也差一个字。
// 靠正文比对只会漏判 —— 这就是「明明是同一个东西却连推四条」的根因。
// 归一化指纹完全相等那一级仍然保留，作为最省事的捷径。
// source 是本次判定方的来源平台（"x" / "douyin" / "bilibili" / "tiktok"）。
//
// ★ 用来区分两种「单一候选」：候选来自**别的平台**= 跨平台镜像，该拦；
//
//	候选来自**同一平台** = 同账号连发多条同长度视频，该放行。
//	不传这个信息就没法区分「X 17:00 发、抖音 17:01 跟发」
//	与「同账号一分钟内连发两条」—— 两者候选数都是 1。
func matchByGroup(entries map[string]Entry, group []string, seconds int, publishedAt int64, source string) (int64, bool) {
	if len(group) == 0 || seconds < durationMinSeconds {
		return 0, false
	}
	now := time.Now().UnixMilli()
	self := publishedAt
	if self <= 0 || self > now {
		// 拿不到本条发布时间就无法判断「谁先发」，直接放行。
		return 0, false
	}

	var candidates []Entry
	for _, e := range entries {
		// ★ 本方自己登记的条目不进候选（2026-10-06 修）。它证明的是
		//   「我已经发过这条」，而这里要判断的是「别人有没有发过」。
		//   不过滤会让「发送失败→下轮重试」的作品匹配到自己而被跳过，
		//   结果是那条作品永远发不出去。
		if isOwnEntry(source, e) {
			continue
		}
		if !groupOverlap(group, e.Group) {
			continue
		}
		if !DurationMatch(seconds, e.Seconds) {
			continue
		}
		if e.FirstSeen <= 0 || e.FirstSeen > now {
			continue
		}
		gap := self - e.FirstSeen
		if gap < 0 || gap > MatchWindowMillis {
			continue
		}
		candidates = append(candidates, e)
	}
	if len(candidates) == 0 {
		return 0, false
	}

	// ★ 候选必须按「来源平台」来分（2026-10-04 修正，根因见下）。
	//
	// 旧规则是「候选必须恰好 1 条」，理由是「多条 = 同账号连发多条同长度视频」。
	// 但这条规则把**跨平台镜像**一起拦掉了 —— 而那恰恰是我们要拦的情况：
	//
	//	同一条短片首发后，真实索引里躺着三条记录
	//	（bilibili 17:07 / douyin 17:02 / tiktok 17:00），
	//	第四个平台（微博 17:16）来的时候候选 = 3 -> 旧规则放行 -> 重复推送。
	//
	// 两种「多条候选」靠来源平台就能分开：
	//   - 来自 >=2 个不同平台、且每个平台恰好 1 条：跨平台镜像同一条内容，
	//     判同一条，取最早那条；
	//   - 其余（全部同平台，或某个平台出现多条）：同一次连发多条同长度内容，
	//     时长不足以定位到唯一一条，放行。
	//
	// 收紧到「每个平台恰好 1 条」是为了让「A 连发两条 + B 发一条」的混合情形
	// 也走放行 —— 保守优先，误吞（漏推）比多推严重。
	//
	// 这其实正是用户原话：「不同平台来源于 hearts to hearts 的视频时长和它一致，
	// 视频本体我们就只发第一个」。关键词是「不同平台」，之前实现漏掉了这一层。
	best := candidates[0].FirstSeen
	for _, e := range candidates[1:] {
		if e.FirstSeen < best {
			best = e.FirstSeen
		}
	}

	if perSource := dedupeBySource(candidates); len(perSource) >= 2 && len(perSource) == len(candidates) {
		// 跨平台镜像。这里**不套** MatchMinGapMillis：候选分属不同平台，
		// 相差很近恰恰说明是镜像发布，不是同一次连发。
		return best, true
	}

	// 0 条没有证据；>=2 条是「同一次连发的多条同长度内容」，
	// 时长不足以定位到唯一一条 —— 宁可多推也不要误吞。
	if len(candidates) != 1 {
		return 0, false
	}

	// ★ 恰好 1 条候选时，**它来自哪个平台**决定了要不要套 MinGap
	//   （2026-10-05 修正，这是最常见的镜像形态却一直漏判的地方）。
	//
	// 原先这里一律要求两条相差 >= MatchMinGapMillis（5 分钟），
	// 理由是「差得近 = 同一次连发」。但那只对**同平台**成立：
	//
	//	同平台连发（同账号一分钟内发两条 10 秒） -> 该放行
	//	跨平台跟发（X 17:00、抖音 17:01）       -> 该判为镜像
	//
	// 候选数都是 1，靠数量分不出来，只有 Entry.Source 能分。
	// 修正前「X 先发、抖音一分钟后到」会漏判 ⇒ 抖音重复下载并发送同一视频。
	if source != "" && candidateSource(entries, best, self) != source {
		return best, true
	}
	if self-best < MatchMinGapMillis {
		return 0, false
	}
	return best, true
}

// FingerprintWindowMillis 是**标题完全相同**时的时间窗（3 天）。
//
// ★ 2026-10-08 用户定稿：「如果间隔在 3 天以上，即使标题和时长一样，
//
//	这个视频也不能被跳过。」
//
//	完全匹配标题的那条多半是同一条内容跨平台搬运，镜像发布可能隔数小时，
//	20 分钟（MatchWindowMillis）会把这类真镜像放过 —— 所以一级放宽到 3 天。
//	而 3 天以上几乎必然是同名不同内容（同团反复用同一段素材做不同剪辑），
//	必须各自发出去。
const FingerprintWindowMillis = int64(3 * 24 * 60 * 60 * 1000)

// withinFingerprintWindow 用于一级（标题指纹完全相同）判定：窗口 3 天。
//
//	publishedAt <= 0 表示**时间未知**（如 Match() 不带时间基准）⇒ 返回 true。
//
//	这一点很要紧（2026-10-08 实测）：`Match()` 是「查索引里有没有这条」
//	的纯查询入口，传 publishedAt=0 表示「不参与时间判定」。
//	若返回 false，一级指纹判定会被整条跳过，于是「推送后应能在索引里查到」
//	（TestSentVideoRecordedForOtherPlatforms）直接失败 ——
//	时间校验把一个**不适用时间**的查询也否掉了。
//
//	publishedAt > 0 时按 gap 判：负 gap = 对方比我晚（我方首发）→ 放行；
//	超 3 天 = 同名不同内容 → 放行。
// 不拿 time.Now() 做上界，否则「按固定历史时刻构造的测试」会随日期失效。
func withinFingerprintWindow(publishedAt, other int64) bool {
	if publishedAt <= 0 {
		return true // 时间未知 ⇒ 不做窗口约束
	}
	if other <= 0 {
		return false
	}
	gap := publishedAt - other
	return gap >= 0 && gap <= FingerprintWindowMillis
}

// withinMatchWindow 用于二级/兜底判定（团名+时长，标题不完全相同）：
// 窗口仍是 20 分钟（MatchWindowMillis）。
//
// ★ 用户定稿：「20 分钟的窗口仅适用于没有完全匹配标题的情况」
//
//	跨语言场景（中文 vs 韩文）标题字面完全不同，只能靠团名+时长对齐，
//	这种「碰巧撞上」的匹配必须收紧窗口，否则同团隔天的两支同长视频
//	会被误判成同一条。
func withinMatchWindow(publishedAt, other int64) bool {
	if publishedAt <= 0 {
		return true
	}
	if other <= 0 {
		return false
	}
	gap := publishedAt - other
	return gap >= 0 && gap <= MatchWindowMillis
}

// candidateSource 返回 firstSeen == ts 的那条候选的来源平台。
//
// ★★ 2026-10-08 修：原先要求 `e.FirstSeen < self`，方向搞反了。
//
// 线上漏判（10-08 11:01）：TikTok 11:01:10 首发，B站 11:01:12 到，
// 只差 **2 秒**。此时 self(=B站 seenAt) 比候选(TikTok) **晚**，
// `e.FirstSeen < self` 成立本该找到…… 但 self 传的是**本条采集时刻**，
// 而候选的 FirstSeen 是**对方推送时刻**；两边时钟基准不同，
// 2 秒的差值让这条 `e.FirstSeen < self` 在部分顺序下不成立，
// 于是返回 "" ⇒ `"" != "bilibili"` 不成立 ⇒ 落到 MatchMinGapMillis(5 分钟)
// 分支 ⇒ gap 2 秒 < 5 分钟 ⇒ **放行** ⇒ 两边视频本体都发。
//
// 正确的语义是「找出这条候选来自哪个平台」，**不需要**和 self 比先后 ——
// 先后由上面 `gap := self - e.FirstSeen` 的窗口筛过了（gap 不能为负）。
// 这里再比一次纯属多余，且正是它把「跨平台紧邻跟发」误判成「同平台连发」。
func candidateSource(entries map[string]Entry, ts int64, _ int64) string {
	for _, e := range entries {
		if e.FirstSeen == ts {
			return e.Source
		}
	}
	return ""
}

// dedupeBySource 统计候选里每个来源平台各占几条，返回「平台 -> 条数」。
func dedupeBySource(candidates []Entry) map[string]int {
	out := make(map[string]int, len(candidates))
	for _, e := range candidates {
		out[e.Source]++
	}
	return out
}

// MatchByTitle 只按标题相似度匹配，不要求时长。
//
// 仅用于时长拿不到时的降级场景。风险高于 Match，
// 调用方应当只在「这条内容必然是短视频」时使用。
func (ix *Index) MatchByTitle(title string) (publishedAt int64, ok bool) {
	key := Normalize(title)
	if key == "" {
		return 0, false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		ix.loadLocked()
	}
	if e, found := ix.data.Titles[key]; found {
		return e.FirstSeen, true
	}
	best := int64(0)
	found := false
	for _, e := range ix.data.Titles {
		if e.Title == "" || !SimilarTitle(title, e.Title) {
			continue
		}
		if !found || e.FirstSeen < best {
			best = e.FirstSeen
			found = true
		}
	}
	return best, found
}

// Len 返回当前有效条目数，供诊断使用。
func (ix *Index) Len() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if !ix.loaded {
		ix.loadLocked()
	}
	return len(ix.data.Titles)
}

// activeSource 是当前判定方所属的平台，由各平台在调用 MatchWithAuthor 前设置。
//
// ★ 为什么要它：matchByGroup 需要区分「候选来自别的平台」（跨平台镜像，该拦）
//
//	与「候选来自同一平台」（同账号连发，该放行）。两种情况候选数都是 1，
//	数量分不出来。
//
// ★ 并发安全性：MatchWithAuthor 全程持有 ix.mu（见其实现），
//
//	所以设置与读取之间不会交错。
var activeSource string

// isOwnEntry 判断候选条目是否由判定方自己登记。
//
// source 为空表示调用方没有声明平台（老调用方），此时不过滤，保持原行为。
func isOwnEntry(source string, e Entry) bool {
	return source != "" && e.Source == source
}

// SetActiveSource 设置当前判定方的来源平台。
// 必须在调用 Index.MatchWithAuthor 之前设置。
func SetActiveSource(source string) { activeSource = source }
