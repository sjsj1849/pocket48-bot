package logic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/extract"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/outbound"
)

// 回归：飞书上的 bot help 曾经是一坨纯文本（2026-10-09 用户要求优化样式）。
//
// 输入是 generateAutoHelp 的真实形状。刻意不调用真实函数，是为了让断言聚焦
// 在「排版转换」这一层；形状与 generateAutoHelp 的输出格式保持一致，
// 改了那边这里要跟着改。
const sampleAutoHelp = `📖 可用命令：

【监控控制】
- bot live - 查看直播间状态
- bot gift - 礼物榜

【房间管理】
- bot list - 查看监控列表
- bot activity - 活跃度

💡 也可以发 "帮助" 或 "help" 来获取此帮助`

func TestToFeishuMarkdownDropsHeaderAndBoldsCategories(t *testing.T) {
	got := toFeishuMarkdown(sampleAutoHelp)

	if strings.Contains(got, "📖 可用命令") {
		t.Errorf("首行已进卡片 header，正文不该重复：\n%s", got)
	}
	if !strings.Contains(got, "**📂 监控控制**") {
		t.Errorf("分类没加粗：\n%s", got)
	}
	if !strings.Contains(got, "**📂 房间管理**") {
		t.Errorf("第二个分类缺失：\n%s", got)
	}
	if strings.Contains(got, "【") {
		t.Errorf("书名号没去掉：\n%s", got)
	}
}

func TestToFeishuMarkdownRendersCommandAsInlineCode(t *testing.T) {
	got := toFeishuMarkdown(sampleAutoHelp)
	if !strings.Contains(got, "▸ `bot live`　查看直播间状态") {
		t.Errorf("命令未渲染成行内代码：\n%s", got)
	}
	if strings.Contains(got, "- bot live") {
		t.Errorf("原始的「- bot x」列表符没转换：\n%s", got)
	}
}

func TestToFeishuMarkdownKeepsHintAndAvoidsHR(t *testing.T) {
	got := toFeishuMarkdown(sampleAutoHelp)
	if !strings.Contains(got, `*💡 也可以发 "帮助" 或 "help" 来获取此帮助*`) {
		t.Errorf("提示行没转成斜体：\n%s", got)
	}
	// lark_md 不认 `---`，生成了就会原样显示成三个减号。
	if strings.Contains(got, "---") {
		t.Errorf("出现了 lark_md 不认的横杠：\n%s", got)
	}
}

func TestToFeishuMarkdownNoTrailingBlankLines(t *testing.T) {
	got := toFeishuMarkdown(sampleAutoHelp)
	if strings.HasSuffix(got, "\n") || strings.HasSuffix(got, " ") {
		t.Errorf("尾部有多余空白：%q", got)
	}
}

func TestFeishuCardTitleShortensAndStripsEmoji(t *testing.T) {
	if got := feishuCardTitle(sampleAutoHelp); got != "可用命令：" {
		t.Errorf("标题 = %q，期望去掉 emoji 后的「可用命令：」", got)
	}
	long := strings.Repeat("长", 40)
	if got := feishuCardTitle("📖 " + long); len([]rune(got)) != 20 {
		t.Errorf("超长标题未截断：%q（%d 字）", got, len([]rune(got)))
	}
	if got := feishuCardTitle("   \n  "); got != "Pocket48" {
		t.Errorf("空文本标题 = %q", got)
	}
}

func TestSplitHelpItem(t *testing.T) {
	cmd, desc, ok := splitHelpItem("bot live - 查看直播间状态")
	if !ok || cmd != "bot live" || desc != "查看直播间状态" {
		t.Errorf("got %q/%q/%v", cmd, desc, ok)
	}
	// 说明里再出现 " - " 不能把命令名截断。
	cmd, desc, ok = splitHelpItem("bot weibo cookie set - 设置 Cookie")
	if !ok || cmd != "bot weibo cookie set" || desc != "设置 Cookie" {
		t.Errorf("含多段描述时拆错：%q/%q/%v", cmd, desc, ok)
	}
	if _, _, ok := splitHelpItem("没有分隔符"); ok {
		t.Error("没有 ' - ' 时不应判定为命令项")
	}
}

func TestWantsFeishuCardSelectsStructuredCommands(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"weibo"}, {"douyin"}, {"welcome"}, {"help", "weibo"}} {
		if !wantsFeishuCard(args) {
			t.Errorf("wantsFeishuCard(%v) = false，期望 true", args)
		}
	}
	// 短回执类不该开卡片。
	for _, args := range [][]string{{"login", "sms", "1"}, {"code", "123456"}, {"whoami"}, {}} {
		if wantsFeishuCard(args) {
			t.Errorf("wantsFeishuCard(%v) = true，期望 false", args)
		}
	}
}

// docRecordingSender 捕获 Document 类型的出站消息。
type docRecordingSender struct {
	texts []string
	docs  []message.Document
}

func (d *docRecordingSender) Send(_ outbound.Target, content interface{}) {
	switch c := content.(type) {
	case message.Document:
		d.docs = append(d.docs, c)
	case message.Segment:
		d.texts = append(d.texts, c.Data["text"])
	case []message.Segment:
		for _, seg := range c {
			d.texts = append(d.texts, seg.Data["text"])
		}
	}
}

func (d *docRecordingSender) QueueDepth() int { return 0 }

// markdown=true 必须发 Document（走 lark_md 渲染），不能是纯文本。
func TestFeishuMarkdownReplySendsDocument(t *testing.T) {
	sender := &docRecordingSender{}
	b := &Bot{cfg: &config.Config{}, outbound: sender}
	target := outbound.Target{Platform: "feishu", Kind: outbound.PrivateChat, Address: testOpenID}

	b.sendFeishuReply(feishuReplyCtx{target: target, markdown: true}, sampleAutoHelp)
	if len(sender.docs) != 1 {
		t.Fatalf("期望 1 张卡片，实际 %d 张（纯文本 %d 条）", len(sender.docs), len(sender.texts))
	}
	doc := sender.docs[0]
	if doc.Kind != "command_help" {
		t.Errorf("doc.Kind = %q", doc.Kind)
	}
	if doc.Title != "可用命令：" {
		t.Errorf("doc.Title = %q，期望卡片标题「可用命令：」", doc.Title)
	}
	if !strings.Contains(doc.Body, "**📂 监控控制**") {
		t.Errorf("卡片正文未排版：\n%s", doc.Body)
	}

	// markdown=false 必须走纯文本。
	b.sendFeishuReply(feishuReplyCtx{target: target}, "验证码已发送到 175****7570")
	if len(sender.texts) != 1 || !strings.Contains(sender.texts[0], "验证码") {
		t.Errorf("短回执应走纯文本，实际 docs=%d texts=%v", len(sender.docs), sender.texts)
	}
}

// ★ 2026-10-09 核心回归：6 图微博必须合成**一条**消息（正文 + 6 张图），
// 此前每张图各发一条，6 图刷出 7 条消息。
func TestExtractedImagesMergeIntoSingleDocument(t *testing.T) {
	dir := t.TempDir()
	paths := make([]string, 0, 6)
	items := make([]extract.Item, 0, 6)
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "pic"+string(rune('A'+i))+".jpg")
		if err := os.WriteFile(p, []byte("fake-jpeg-bytes"), 0o600); err != nil {
			t.Fatalf("写临时图失败: %v", err)
		}
		paths = append(paths, p)
		items = append(items, extract.Item{Kind: "image", URL: p, Cover: p})
	}

	post := &extract.Post{
		Platform: extract.PlatformWeibo,
		Author:   "Fraisesdesbois",
		Text:     "生日快乐我们的ian",
		URL:      "https://weibo.com/6694957538/5351887681618773",
		Cover:    paths[0],
		Items:    items,
	}

	sender := &docRecordingSender{}
	b := &Bot{cfg: &config.Config{}, outbound: sender}
	target := outbound.Target{Platform: "feishu", Kind: outbound.PrivateChat, Address: testOpenID}
	b.sendExtractedToFeishu(target, post, false)

	if len(sender.docs) != 1 {
		t.Fatalf("6 图微博应合成 1 条消息，实际 %d 条", len(sender.docs)+len(sender.texts))
	}
	doc := sender.docs[0]
	if len(doc.Media) != 6 {
		t.Fatalf("卡片内图片数 = %d，期望 6（合并全部图片）", len(doc.Media))
	}
	for i, m := range doc.Media {
		if m.Kind != "image" {
			t.Errorf("doc.Media[%d].Kind = %q", i, m.Kind)
		}
	}
	// 封面就是第一张图，不允许再额外追加一张造成重复。
	if doc.Media[0].Source != paths[0] {
		t.Errorf("封面顺序错位：%q 期望 %q", doc.Media[0].Source, paths[0])
	}
	if strings.TrimSpace(doc.Body) == "" {
		t.Error("正文丢了")
	}
}

// 视频帖仍要独立成条（飞书卡片内不能内嵌视频），且不能被当成图片合并。
func TestExtractedVideoStillSeparate(t *testing.T) {
	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.jpg")
	if err := os.WriteFile(cover, []byte("fake-jpeg"), 0o600); err != nil {
		t.Fatalf("写临时图失败: %v", err)
	}
	post := &extract.Post{
		Platform: extract.PlatformWeibo,
		Author:   "a",
		Text:     "hello",
		URL:      "https://weibo.com/1/1234567890123",
		Cover:    cover,
		// 视频走网络，测试里用一个不存在的本地文件让 resolveExtractMedia 快速失败。
		Items: []extract.Item{{Kind: "video", URL: filepath.Join(dir, "nope.mp4")}},
	}

	sender := &docRecordingSender{}
	b := &Bot{cfg: &config.Config{}, outbound: sender}
	target := outbound.Target{Platform: "feishu", Kind: outbound.PrivateChat, Address: testOpenID}
	b.sendExtractedToFeishu(target, post, false)

	if len(sender.docs) != 1 {
		t.Fatalf("应发 1 张卡片，实际 %d", len(sender.docs))
	}
	// 没有可用视频时，卡片仍应带上封面。
	if len(sender.docs[0].Media) != 1 {
		t.Errorf("视频帖无视频时卡片应带封面，实际图片数 %d", len(sender.docs[0].Media))
	}
}
