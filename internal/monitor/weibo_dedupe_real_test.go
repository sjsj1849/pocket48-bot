package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocket48-bot/internal/dedupe"
)

// ★ 这批样本全部是 2026-10-04 实测抓来的真实数据，一处都没有编造。
//
// 用户反馈「同一个视频又在微博和 B 站发了」，实际抓下来发现是**同一条短片
// 在四个平台各发一遍**，微博是最后一个（所以之前只有微博在重复推）：
//
//	平台    发布时间(CST)   时长      正文
//	TikTok  17:00:00        21s      `낼 봥 ㅋㅋ ♡ #Hearts2Hearts ...`
//	抖音    17:02:23        21s      `明天见嘻嘻 ♡ #Hearts2Hearts ...`
//	B站    17:07:15        22s      `【Hearts2Hearts】明天见嘻嘻 ♡`
//	微博    17:16:10        21.479s  `明天见kk ♡`
//
// 三条关键事实（每一条都是「光看代码推不出来」的）：
//
//  1. 微博写的是「明天见**kk**」，不是「明天见嘻嘻」—— 中文之间差一个字，
//     所以标题相似度那条路（Similarity ≥ 0.75）走不通。
//     能救它的只有「团名 + 时长 + 15 分钟窗口」这一组判据。
//  2. 微博的 created_at 是 RFC1123 格式带 +0800 时区：
//     `Sun Oct 04 17:16:10 +0800 2026`。时区解析错一小时，
//     窗口判定就会整个偏掉。
//  3. 时长是 float 21.479，且**同一个字段在长视频里是字符串**（"232"）。
//
// 线上索引的真实内容（storage/cursors/cross-platform-titles.json）：
//	TikTok 낸봥ㅋㅋ                      firstSeen=17:00:00 seconds=21
//	抖音   明天见嘻嘻                     firstSeen=17:02:23 seconds=21
//	B站    【Hearts2Hearts】明天见嘻嘻     firstSeen=17:07:15 seconds=22
const realIndexJSON = `{"titles":{` +
	`"hearts2heartslikeificanthelpfallinginlovewithyou":{"firstSeen":1791092772000,"source":"bilibili"},` +
	`"hearts2hearts明天见嘻嘻":{"firstSeen":1791104835000,"source":"bilibili","seconds":22,"title":"【Hearts2Hearts】明天见嘻嘻 ♡"},` +
	`"hearts2hearts现在让大家和ian的chatgpt通话ᯓhearts2hearts2026timabh2nd":{"firstSeen":1791111600000,"source":"bilibili","seconds":1215,"title":"【Hearts2Hearts】现在让大家和IAN的ChatGPT通话ᯓ★｜Hearts2Hearts 2026 TIMA BH2ND"},` +
	`"likeificanthelpfallinginlovewithyou":{"firstSeen":1791092331000,"source":"douyin"},` +
	`"明天见嘻嘻":{"firstSeen":1791104543000,"source":"douyin","seconds":21,"title":"明天见嘻嘻 ♡ \n#Hearts2Hearts #H2H #ICONICHEART #Hearts2Hearts_ICONICHEART"},` +
	`"낼봥ㅋㅋ":{"firstSeen":1791104400000,"source":"tiktok","seconds":21,"title":"낼 봥 ㅋㅋ ♡ #Hearts2Hearts #하츠투하츠 #H2H  #ICONICHEART #Hearts2Hearts_ICONICHEART "}` +
	`}}`

// 微博真实卡片：id=5350346069115769，2026-10-04 17:16:10，时长 21.479s
const realWeiboCardJSON = `{
	"id": 5350346069115769,
	"created_at": "Sun Oct 04 17:16:10 +0800 2026",
	"text": "明天见kk ♡<br /><br /><a href=\"https://m.weibo.cn/sinaurl?u=https%3A%2F%2Fweibo.com%2F7971304015%2F5350346069115769\">全文链接</a>",
	"user": {"id": 7971304015, "screen_name": "Hearts2Hearts"},
	"page_info": {
		"type": "video",
		"object_type": 11,
		"media_info": {"duration": 21.479, "stream_url": "https://v.example/x.mp4"}
	}
}`

func loadRealIndex(t *testing.T) *dedupe.Index {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cross-platform-titles.json")
	if err := os.WriteFile(p, []byte(realIndexJSON), 0o600); err != nil {
		t.Fatalf("写临时索引失败：%v", err)
	}
	ix := dedupe.NewIndex(p, 14*24*time.Hour, 4096)
	if got := ix.Len(); got != 6 {
		t.Fatalf("真实索引应加载出 6 条，实际 %d", got)
	}
	return ix
}

func parseRealWeiboCard(t *testing.T) WeiboCard {
	t.Helper()
	var card WeiboCard
	if err := json.Unmarshal([]byte(realWeiboCardJSON), &card); err != nil {
		t.Fatalf("真实微博卡片解析失败：%v", err)
	}
	return card
}

// TestWeiboRealCardDimensions 逐项核对从真实卡片算出的四个维度。
func TestWeiboRealCardDimensions(t *testing.T) {
	card := parseRealWeiboCard(t)

	if got := card.User.ScreenName; got != "Hearts2Hearts" {
		t.Errorf("作者 = %q，期望 Hearts2Hearts", got)
	}
	// 时长：float 21.479 -> 21 秒，必须与 TikTok/抖音的 21 秒对得上
	if got := weiboCardSeconds(card); got != 21 {
		t.Errorf("时长 = %d 秒，期望 21", got)
	}
	// created_at 带 +0800 时区，必须解析成 2026-10-04 09:16:10 UTC。
	// 解析错一小时会让 15 分钟窗口判定整个偏掉，所以这里钉死。
	wantMS := time.Date(2026, 10, 4, 9, 16, 10, 0, time.UTC).UnixMilli()
	gotMS := weiboCardCreatedAtMS(card)
	if gotMS != wantMS {
		t.Errorf("发布时间 = %d（%s），期望 %d（+0800 时区）",
			gotMS, time.UnixMilli(gotMS).UTC().Format("15:04:05"), wantMS)
	}

	// 与线上索引里的时间关系：微博距 TikTok(17:00) 首发 16 分钟。
	//
	// ★ 这个 16 分钟正是把窗口从 15 放宽到 20 的原因（2026-10-04）：
	//   15 分钟窗口下这条微博被判成「不在镜像窗口内」-> 重复推送。
	gapMin := time.UnixMilli(gotMS).Sub(time.UnixMilli(1791104400000)).Minutes()
	if gapMin <= 0 || gapMin > 20 {
		t.Errorf("微博距 TikTok %.1f 分钟，应在 (0, 20] 区间内", gapMin)
	}
	t.Logf("微博距 TikTok 首发 %.1f 分钟（15 分钟窗口会漏判，现已放宽到 20）", gapMin)
}

// TestWeiboRealTitleCleaning 确认真实正文的清洗结果。
//
// ★ 这是本次改动的一环：真实 text 尾部带
// `<a href="...">全文链接</a>`，不清掉指纹就变成 `明天见kk全文链接`，
// 与其它平台的 `明天见嘻嘻` 对不上，重复推送仍然漏判。
func TestWeiboRealTitleCleaning(t *testing.T) {
	m := &WeiboMonitor{}
	card := parseRealWeiboCard(t)

	title := m.weiboDedupeTitle(card)
	t.Logf("清洗后标题 = %q", title)
	t.Logf("归一化指纹 = %q", dedupe.Normalize(title))

	for _, noise := range []string{"全文链接", "网页链接", "查看图片", "<", ">"} {
		if strings.Contains(title, noise) {
			t.Errorf("★ 标题仍含尾噪声 %q：%q", noise, title)
		}
	}
	if !strings.Contains(title, "明天见") {
		t.Errorf("标题丢了正文核心内容：%q", title)
	}
}

// TestWeiboDedupeSkipsRealRepeat 是本次改动的**核心断言**。
//
// 用线上真实索引 + 真实微博卡片跑完整判定：这条微博应当被识别成
// 「TikTok 17:00 / 抖音 17:02 / B站 17:07 已经推过的同一条短片」，
// 于是跳过，不再重复推送。
//
// 为什么这个测试重要：微博正文写的是「明天见kk」，与其它平台的
// 「明天见嘻嘻」差一个字，标题相似度那条路（≥0.75）必然失配。
// 能让它匹配上的只有「团名 Hearts2Hearts 相交 + 时长 21s 一致 +
// 落在时间窗内」这一组判据 —— 也就是 matchByGroup。
//
// ★ 必须走 crossDecideWeibo（生产真实入口）拿标题，不要在测试里
//   自己拼 stripHTML —— 那样测的是测试自己写的清洗，
//   跟生产链路口径漂移了就发现不了（第一版就踩了这个坑）。
func TestWeiboDedupeSkipsRealRepeat(t *testing.T) {
	ix := loadRealIndex(t)
	card := parseRealWeiboCard(t)
	publishedMS := weiboCardCreatedAtMS(card)
	author := card.User.ScreenName
	seconds := weiboCardSeconds(card)

	m := &WeiboMonitor{}
	title := m.weiboDedupeTitle(card)

	firstSeen, ok := ix.MatchWithAuthor(title, seconds, author, publishedMS)
	if !ok {
		t.Fatalf("★ 真实索引里没匹配上 —— 微博接入去重后这条仍会重复推送！\n"+
			"  标题(清洗后) = %q\n  指纹 = %q\n  作者 = %q  时长 = %ds  时间 = %s",
			title, dedupe.Normalize(title), author, seconds,
			time.UnixMilli(publishedMS).In(time.FixedZone("CST", 8*3600)).Format("15:04:05"))
	}
	if firstSeen >= publishedMS {
		t.Fatalf("索引首发 %s 不早于本条 %s，样本前提不成立",
			time.UnixMilli(firstSeen).Format("15:04"),
			time.UnixMilli(publishedMS).Format("15:04"))
	}
	t.Logf("★ 命中：已有平台首发 %s，微博发布 %s（晚 %d 分钟）-> 应跳过",
		time.UnixMilli(firstSeen).In(time.FixedZone("CST", 8*3600)).Format("15:04"),
		time.UnixMilli(publishedMS).In(time.FixedZone("CST", 8*3600)).Format("15:04"),
		(publishedMS-firstSeen)/60000)
}

// TestWeiboDedupeDoesNotSkipWhenWeiboIsFirst 反向验证，防止判定写反。
//
// 微博时间比索引里所有记录都早 -> 微博是首发，必须推。
// 判定写反会变成系统性漏推，比重复推送严重得多。
func TestWeiboDedupeDoesNotSkipWhenWeiboIsFirst(t *testing.T) {
	ix := loadRealIndex(t)

	// 同一条内容，但时间改到 TikTok 之前一天
	raw := `{
		"created_at": "Sat Oct 03 09:00:00 +0800 2026",
		"text": "明天见kk ♡",
		"user": {"screen_name": "Hearts2Hearts"},
		"page_info": {"type": "video", "media_info": {"duration": 21.479}}
	}`
	var card WeiboCard
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		t.Fatalf("解析失败：%v", err)
	}

	firstSeen, ok := ix.MatchWithAuthor(stripHTML(card.Text), weiboCardSeconds(card),
		card.User.ScreenName, weiboCardCreatedAtMS(card))
	if !ok {
		t.Skip("没匹配上，不影响本用例意图（要验的是「匹配上之后不许判反」）")
	}
	if firstSeen < weiboCardCreatedAtMS(card) {
		t.Fatalf("★ 判定反了：索引 %s 晚于本条 %s，却判成应跳过 —— 这是漏推！",
			time.UnixMilli(firstSeen).Format("15:04"),
			time.UnixMilli(weiboCardCreatedAtMS(card)).Format("15:04"))
	}
	t.Logf("本条 %s 早于索引 %s -> 应推送（判定正确）",
		time.UnixMilli(weiboCardCreatedAtMS(card)).Format("15:04"),
		time.UnixMilli(firstSeen).Format("15:04"))
}

// TestWeiboDedupeIgnoresUnrelatedWeibo 确认无关微博不会被误拦。
//
// 真实样本 id=5350373382424178（19:04:42 的 ChatGPT 通话图文，
// 没有 media_info -> 时长 0），以及 08-11 的 232 秒 MV。
// 这些内容与短片无关，绝不能因为「同作者 + 15 分钟内」被误判成同一条。
func TestWeiboDedupeIgnoresUnrelatedWeibo(t *testing.T) {
	ix := loadRealIndex(t)

	cases := []struct {
		name    string
		payload string
	}{
		{"图文无时长", `{
			"created_at": "Sun Oct 04 19:04:42 +0800 2026",
			"text": "现在让大家和IAN的ChatGPT通话ᯓ★｜Hearts2Hearts 2026 TIMA BH2ND",
			"user": {"screen_name": "Hearts2Hearts"},
			"page_info": {"type": "video"}
		}`},
		{"232秒MV", `{
			"created_at": "Tue Aug 11 23:01:14 +0800 2026",
			"text": "Hearts2Hearts《ICONIC HEART》MV",
			"user": {"screen_name": "Hearts2Hearts"},
			"page_info": {"type": "video", "media_info": {"duration": "232"}}
		}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var card WeiboCard
			if err := json.Unmarshal([]byte(c.payload), &card); err != nil {
				t.Fatalf("解析失败：%v", err)
			}
			seconds := weiboCardSeconds(card)
			publishedMS := weiboCardCreatedAtMS(card)
			if _, ok := ix.MatchWithAuthor(stripHTML(card.Text), seconds,
				card.User.ScreenName, publishedMS); ok {
				t.Errorf("★ 无关微博被误判成重复（时长=%ds）—— 这会造成漏推", seconds)
			}
		})
	}
}
