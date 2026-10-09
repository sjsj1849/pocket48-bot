package dedupe

import (
	"sort"
	"strings"
	"unicode"
)

// 跨平台去重的强标识：团名 / 作者名。
//
// # 为什么只看「团名 + 时长 + 时间窗」（用户口径，2026-10-04）
//
// 用户原话：
//
//	「只要把 hearts to hearts 这个名字提取出来，以第一个拿到的视频时间为基准，
//	  往后推 15 分钟。在这 15 分钟内，如果不同平台来源于 hearts to hearts 的
//	  视频时长和它一致，视频本体我们就只发第一个，其他的都不发了。」
//
// 早期版本用的是「归一化标题 + 词重合度 + 强标识交集」三层判定，
// 实测根本跑不通，原因是特征选错了：
//
//	抖音  ：「明天见嘻嘻 ♡ #Hearts2Hearts #H2H #ICONICHEART」
//	TikTok：「내 봥 ㅋㅋ ♡ #Hearts2Hearts #하츠투하츠 #H2H #ICONICHEART」
//	B 站  ：「【Hearts2Hearts】明天见嘻嘻 ♡」
//	微博  ：「明天见科科 ♡ #Hearts2Hearts ...」
//
// 正文互不相同（跨语言词重合度恒为 0，中文之间也差字），
// 而**团名**在四个平台上都是 Hearts2Hearts —— 这是唯一稳定的锚点。
//
// # 时长为什么够用
//
// 短视频的时长是极强的单条特征。用户判断：
// 「如果不是同一个视频，时长应该很难完全一样」。
// 实测同一条片子三平台时长一致（21/21/22 秒，误差来自各自转码）。
//
// # 窗口的由来（2026-10-04 从 15 分钟放宽到 20 分钟）
//
// 最初实测同一条内容跨平台镜像发布的间隔是 TikTok 16:40 → 抖音 16:55，
// 正好 15 分钟，于是把窗口定成 15 分钟。
//
// ★ 结果当天就卡在门外了（2026-10-04 实测）：
//
//	TikTok  17:00:00  21s
//	抖音    17:02:23  21s
//	B站    17:07:15  22s
//	微博    17:16:10  21.479s   <- 距 TikTok 首发 16 分钟，超出 15 分钟窗口
//
//	于是「同一条短片四平台各推一遍」里微博那次仍然漏判，
//	用户还是在飞书里看到了重复内容。
//
// 结论：**15 分钟是拿单次观测当成了上限**。镜像发布间隔本来就没有
// 硬上限，不同批次的发布节奏不一样（手工搬运 vs 定时发布）。
//
// 放宽到 20 分钟的依据：
//   - 覆盖上面这条 16 分钟的真实样本，留 4 分钟余量；
//   - 真正的误判防护不靠这个窗口，而是靠下面两条 ——
//     「候选必须恰好 1 条」与「至少早 5 分钟」。放宽窗口只是把
//     候选集变大一点，这两条会继续把关。
//
// 真要再放宽就该改判据本身（同团名 + 时长几乎相同已经足够强），
// 而不是继续堆时间窗 —— 那样会明显提高误吞的风险。
const (
	// MatchWindowMillis 是同一条内容跨平台镜像发布的时间窗（毫秒）。
	//
	// 2026-10-04 从 15 分钟放宽到 20 分钟：实测存在 16 分钟的镜像间隔
	// （TikTok 17:00 → 微博 17:16），15 分钟会把后发的平台挡在门外，
	// 表现为「明明接了去重，用户还是收到重复内容」。
	MatchWindowMillis = int64(20 * 60 * 1000)

	// MatchMinGapMillis 是候选记录至少要比本条早多久。
	//
	// 实测同一账号连发多条短视频时，第二条会命中紧邻的那条被误吞
	// （测试「应推 3 条实际 1 条」）。镜像发布通常隔几分钟到十几分钟
	// （实测 TikTok 16:40 → 抖音 16:55）。
	//
	// ★ 取 5 分钟而不是更小：这条阈值拦的是「连发」，
	//   而连发的间隔通常在几分钟以内；如果取得太小（比如 2 分钟），
	//   连发间隔恰好 2~3 分钟时依然会被吞 —— 实测就是这个边界
	//   （gap=120000ms 恰好等于阈值，判成了同一条）。
	//   真正的兜底是下面「候选必须唯一」那条。
	MatchMinGapMillis = int64(5 * 60 * 1000)
)

// GroupKeys 从标题与作者名里提取「团名 / 作者名」候选。
//
// 两个来源：
//  1. 【】方括号前缀：【Hearts2Hearts】→ hearts2hearts
//     B 站标题固定带这个前缀，而且 B 站 API 根本不返回标签
//     （实测 seasons_series_list 与 view 的 tags 都是 null、
//     tname 是空串），所以这是 B 站唯一可用的锚点。
//  2. 作者昵称：四个平台的作者名实测都是 Hearts2Hearts。
//
// 归一化成小写去标点，因此【Hearts2Hearts】与 Hearts2Hearts
// 得到同一个键 hearts2hearts，可以互相匹配。
func GroupKeys(title, author string) []string {
	set := map[string]struct{}{}

	runes := []rune(title)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '【' && runes[i] != '［' && runes[i] != '[' {
			continue
		}
		closers := map[rune]bool{'】': true, '］': true, ']': true}
		var b strings.Builder
		// ★ 在**闭合括号**处停止，不能按空白停。
		//   实测踩坑：B 站「【Hearts2Hearts】明天见嘻嘻 ♡」
		//   闭合括号后紧跟正文且没有空格，按空白停会把「明天见嘻嘻」
		//   一起吞成一个假键，导致永远匹配不上。
		for i++; i < len(runes) && !closers[runes[i]]; i++ {
			b.WriteRune(runes[i])
		}
		if key := NormalizeGroup(b.String()); key != "" {
			set[key] = struct{}{}
		}
	}
	if key := NormalizeGroup(author); key != "" {
		set[key] = struct{}{}
	}

	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// 排序保证结果可复现 —— map 遍历顺序不定会让判定结果无法排查。
	sort.Strings(out)
	return out
}

// CanonicalGroup 是团名对外展示用的规范写法。
//
// ★ 为什么需要它（2026-10-04 用户要求）：
//
//	去重侧比对早就是大小写不敏感的（NormalizeGroup 内部 ToLower），
//	所以 TikTok 的 hearts2hearts 和抖音的 Hearts2Hearts 能匹配上 ——
//	**比对从来没问题**。有问题的是**给人看的字**：同一个人在消息里
//	一会儿小写一会儿大写，看起来像两个人。
//
//	规则：已知团名映射到规范写法，未知团名原样保留
//	（去掉多余空白即可）—— 不能反把所有团名都首字母大写，
//	那会把 SEVENTHSENSE、AKB48 这类全大写团名改成错的大小写。
var canonicalGroupNames = map[string]string{
	"hearts2hearts": "Hearts2Hearts",
}

// CanonicalGroupName 返回团名的展示用规范写法。
func CanonicalGroupName(s string) string {
	key := NormalizeGroup(s)
	if key == "" {
		return ""
	}
	if c, ok := canonicalGroupNames[key]; ok {
		return c
	}
	return strings.TrimSpace(s)
}

// NormalizeGroup 把团名归一化成可比较的键：只保留小写字母数字。
func NormalizeGroup(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// groupOverlap 判断两个团名集合是否相交。
func groupOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}
	for _, t := range a {
		if _, ok := setB[t]; ok {
			return true
		}
	}
	return false
}
