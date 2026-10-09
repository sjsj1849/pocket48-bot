package logic

import (
	"strings"
)

// 飞书命令回复的排版（2026-10-09）
//
// 问题：bot help 在 QQ 上就是一坨纯文本，搬到飞书后仍然是一坨纯文本。
// 飞书本身能渲染 lark_md / 卡片 header，纯文本等于浪费这个能力。
//
// 为什么在命令侧做转换，而不是改 generateAutoHelp：
//  1. QQ 不认 markdown，`**加粗**` 到了 QQ 就是一堆星号 ——
//     generateAutoHelp 是 QQ/飞书共用的，改它必然伤到 QQ；
//  2. 帮助文本由 CmdRegistry 自动生成，**不能手写一份 markdown**，
//     那样新增命令就会漏（手写清单必然随时间漂移）。
// 所以做法是：保持 generateAutoHelp 的纯文本输出不变，
// 在送往飞书时按它**已有的固定形状**做一次无损排版。
//
// 输入形状（generateAutoHelp 产出，勿改）：
//
//	📖 可用命令：
//	【监控控制】
//	- bot live - 查看直播间状态
//	💡 也可以发 "帮助" 或 "help" 来获取此帮助

// feishuCardTitle 从纯文本首行取出卡片标题。
// 飞书 header 是 plain_text，会把整段塞进去，所以必须截短。
func feishuCardTitle(plain string) string {
	title := ""
	for _, line := range strings.Split(plain, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			title = line
			break
		}
	}
	if title == "" {
		return "Pocket48"
	}
	runes := []rune(strings.TrimLeft(title, "📖📘📗📕📙📚 "))
	if len(runes) > 20 {
		runes = runes[:20]
	}
	return string(runes)
}

// toFeishuMarkdown 把命令纯文本转成 lark_md。
//
// ★ 只用 lark_md 的安全子集：**加粗**、`代码`、普通换行、`▸` 引导符。
// 飞书 lark_md 不认 markdown 表格，也不认 `---` 横杠（会原样显示成三个减号），
// 所以这里一律不生成。
//
// 三类行的处理：
// 【分类】→ **分类**（加粗，去掉书名号）；`- bot x - 描述` → 行内代码 + 全角空格 + 描述；
// 💡 开头的提示 → 斜体。
func toFeishuMarkdown(plain string) string {
	lines := strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines)+2)
	firstNonEmpty := true

	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			// 分类之间保留一个空行做视觉分组；首行前的空行丢掉。
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}

		case strings.HasPrefix(trimmed, "【") && strings.HasSuffix(trimmed, "】"):
			// 首行是「📖 可用命令：」，已经进了卡片 header，正文不再重复。
			if firstNonEmpty {
				firstNonEmpty = false
				continue
			}
			category := strings.Trim(trimmed, "【】")
			if category == "" {
				continue
			}
			out = append(out, "**📂 "+category+"**")

		case strings.HasPrefix(trimmed, "- "):
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			// generateAutoHelp 的形状是 "bot <usage> - <help>"，
			// 拆成等宽命令 + 说明；拆不动就整行当正文。
			if cmd, desc, ok := splitHelpItem(item); ok {
				out = append(out, "▸ `"+cmd+"`　"+desc)
			} else {
				out = append(out, "▸ "+item)
			}
			firstNonEmpty = false

		case strings.HasPrefix(trimmed, "💡"), strings.HasPrefix(trimmed, "📎"):
			out = append(out, "*"+trimmed+"*")
			firstNonEmpty = false

		default:
			// 首个非空行是标题（generateAutoHelp 的「📖 可用命令：」），
			// 已经进了卡片 header，正文里必须丢掉。
			if firstNonEmpty {
				firstNonEmpty = false
				continue
			}
			out = append(out, trimmed)
		}
	}

	// 去掉尾部空行，避免卡片末尾出现一大片空白。
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

// splitHelpItem 把 "bot live - 查看直播间状态" 拆成命令与说明。
func splitHelpItem(item string) (cmd, desc string, ok bool) {
	idx := strings.Index(item, " - ")
	if idx <= 0 {
		return "", "", false
	}
	cmd = strings.TrimSpace(item[:idx])
	desc = strings.TrimSpace(item[idx+3:])
	if cmd == "" || desc == "" {
		return "", "", false
	}
	// 命令里不该有空格（`bot live` 是两个词，但形如 "bot x - y - z"
	// 的说明文会在第二个 " - " 被切错），只在第一次出现处切。
	return cmd, desc, true
}
