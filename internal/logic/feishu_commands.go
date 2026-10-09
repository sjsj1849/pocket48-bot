package logic

import (
	"log"
	"strconv"
	"strings"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/outbound"
)

// 飞书指令桥（2026-10-08 新增）
//
// ── 为什么之前在飞书上敲 "bot login sms <手机号>" 毫无反应 ──
//
// handleFeishuIncoming 拿到飞书消息后只做一件事：抽链接。
// 抽不到链接就直接 return，**从来没有任何命令分派**。
// 也就是说 QQ 那套 CmdRegistry / handleCommand 从来没有迁移到飞书 ——
// 用户判断完全正确，不是配置问题，是根本没接。
//
// ── 这里的做法 ──
//
// 不复制第二份命令实现（必然随时间漂移），而是：
//  1. 把飞书消息包装成一个 napcat.Event（UserID 填该 open_id 对应的管理员 QQ 号）；
//  2. 执行期间把 b.reply 改道到飞书；
//  3. 命令结束后恢复。
// 于是 `bot login sms` / `bot code` / `bot help` / 全部既有命令在飞书都能用，
// 以后新增命令也不用再改飞书这边。

// feishuAdminIDByOpenID 用 FEISHU_PRIVATE_ROUTES 反查 open_id 对应的 QQ 号。
//
// 白名单不新增配置项：能私聊路由到飞书的管理员，本来就是 QQ 侧的管理员，
// 用同一份配置做准入，两个入口不会出现「A 是管理员 B 不是」的分叉。
func (b *Bot) feishuAdminIDByOpenID(openID string) (int64, bool) {
	openID = strings.TrimSpace(openID)
	if openID == "" || b.cfg == nil {
		return 0, false
	}
	for qqID, target := range b.cfg.FeishuPrivateRouteMap() {
		if strings.TrimSpace(target) != openID {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(qqID), 10, 64); err == nil && n != 0 {
			return n, true
		}
	}
	return 0, false
}

// parseFeishuCommand 判断一条飞书消息是不是命令，是则返回参数。
//
// 接受的写法（飞书输入通常没有 `/` 前缀）：
//
//	bot login sms 13800000000   ← 与 QQ 完全一致（用户最熟悉这个）
//	BOT LOGIN SMS 13800000000   ← 大小写不敏感
//	login sms 13800000000       ← 省略 bot
//
// 群里必须 @ 机器人才算（由调用方先判 MentionBot）。
func parseFeishuCommand(msg string) ([]string, bool) {
	trimmed := strings.TrimSpace(msg)
	if trimmed == "" {
		return nil, false
	}
	lower := strings.ToLower(trimmed)

	// 群聊里 @ 机器人会带一串占位符（形如 @_user_1 或 <at ...>），先剥掉。
	trimmed = stripFeishuMentions(trimmed)
	lower = strings.ToLower(trimmed)

	for _, p := range []string{"bot ", "bot：", "bot:", "/bot "} {
		if strings.HasPrefix(lower, p) {
			trimmed = strings.TrimSpace(trimmed[len(p):])
			lower = strings.ToLower(trimmed)
			break
		}
	}
	if trimmed == "" {
		return nil, false
	}

	// 纯链接 / 普通闲聊不进来。
	if isHelpWord(lower) {
		return []string{"help"}, true
	}
	if !isFeishuCommandWord(lower) {
		return nil, false
	}
	args := parseCommandArgs(trimmed)
	if len(args) == 0 {
		return nil, false
	}
	// 命令名统一小写，避免 BOT LOGIN SMS 查表失败。
	args[0] = strings.ToLower(args[0])
	// ★ 二级关键字也要小写：BOT LOGIN SMS 会解析出 args[1]=="SMS"，
	//   而 cmdLogin 判的是 args[1]=="sms" ⇒ 大写直接掉进「用法」分支。
	//   只小写**白名单里的关键字**，token / cookie / 验证码等值一律原样
	//   （无脑全小写会把 Cookie 和验证码改坏）。
	if len(args) > 1 && feishuSubcommands[strings.ToLower(args[1])] {
		args[1] = strings.ToLower(args[1])
	}
	return args, true
}

// feishuSubcommands 是可以安全小写化的二级关键字白名单。
//
// 取自 cmd_handlers.go 里真正用到的 args[1] 比较：
// sms / pwd / channels / super / superpost / cookie。
// 新增命令时若也用 args[1] 做分支，记得往这里补一条。
var feishuSubcommands = map[string]bool{
	"sms":       true,
	"pwd":       true,
	"channels":  true,
	"super":     true,
	"superpost": true,
	"cookie":    true,
}

func isHelpWord(lower string) bool {
	switch lower {
	case "help", "帮助", "?", "？", "bot help", "bot 帮助":
		return true
	}
	return false
}

// isFeishuCommandWord 只放行确实存在的命令名，
// 避免把「帮我看看这个」之类的话误判成命令后吞掉。
func isFeishuCommandWord(lower string) bool {
	name := lower
	if i := strings.IndexAny(name, " \t"); i > 0 {
		name = name[:i]
	}
	if _, ok := CmdRegistry[name]; ok {
		return true
	}
	return false
}

// stripFeishuMentions 去掉飞书群里 @ 机器人的占位符。
func stripFeishuMentions(s string) string {
	out := s
	// <at user_id="ou_xxx">名字</at> 形态
	for {
		i := strings.Index(out, "<at")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "</at>")
		if j < 0 {
			out = out[:i]
			break
		}
		out = out[:i] + out[i+j+len("</at>"):]
	}
	// <at id=...></at> / <at .../> 形态
	for {
		i := strings.Index(out, "<at")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], ">")
		if j < 0 {
			out = out[:i]
			break
		}
		out = out[:i] + out[i+j+1:]
	}
	// 飞书客户端有时会把 @ 渲染成 <at user_id="..." /> 之外的花括号占位
	out = strings.ReplaceAll(out, "@_user_1", " ")
	out = strings.ReplaceAll(out, "@_all", " ")
	return strings.TrimSpace(out)
}

// handleFeishuCommand 处理一条飞书命令消息，返回是否已处理。
func (b *Bot) handleFeishuCommand(ev outbound.FeishuIncomingEvent, msg string) bool {
	args, ok := parseFeishuCommand(msg)
	if !ok {
		return false
	}

	target, ok := feishuReplyTarget(ev)
	if !ok {
		return false
	}

	adminID, ok := b.feishuAdminIDByOpenID(ev.SenderID)
	if !ok {
		// 明确回一句，别让人对着空气排查。
		if b.outbound != nil {
			b.outbound.Send(target, message.Text("⛔️ 该飞书账号不在 Pocket48 管理员白名单里。\n管理员白名单取自 FEISHU_PRIVATE_ROUTES（QQ 号=open_id）。"))
		}
		log.Printf("[Feishu-Cmd] 非管理员 open_id=%s 试图下指令", ev.SenderID)
		return true
	}

	event := &napcat.Event{
		MessageType: "private",
		UserID:      adminID,
		Sender:      napcat.Sender{UserID: adminID},
		RawMessage:  msg,
	}

	// 改道 reply，让命令里所有 b.reply(...) 回到飞书这条消息。
	//
	// help / 单命令用法这类结构化长文本走卡片，其余（验证码回执、
	// 状态、报错）保持纯文本 —— 为一句话开张卡反而是噪音。
	b.pushFeishuReply(&feishuReplyCtx{
		target:   target,
		eventID:  ev.MessageID,
		markdown: wantsFeishuCard(args),
	})
	defer b.popFeishuReply()

	log.Printf("[Feishu-Cmd] 执行 %v（来自 open_id=%s）", args, ev.SenderID)
	b.handleCommand(event, args)
	return true
}

// wantsFeishuCard 判断这条命令的回复是否值得渲染成卡片。
func wantsFeishuCard(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "weibo", "douyin", "xiaohongshu", "welcome":
		// 这些命令输出的是分类清单/用法说明，纯文本读起来是一坨。
		return true
	default:
		return false
	}
}
