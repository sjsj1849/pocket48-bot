package logic

import (
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"strings"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/napcat"
)

func shouldTreatLastPrivateArgAsGroupID(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "login", "code":
		return false
	default:
		return true
	}
}

func (b *Bot) handlePrivateMessage(event *napcat.Event) {
	// Access Control: Only allow admins
	if !b.cfg.IsAdmin(event.UserID) {
		return
	}

	// Check for commands
	msg := strings.TrimSpace(event.RawMessage)
	prefix := b.cfg.CommandPrefix
	lowerMsg := strings.ToLower(strings.TrimSpace(msg))
	if lowerMsg == "帮助" || lowerMsg == "help" || lowerMsg == "bot 帮助" || lowerMsg == "bot help" {
		b.reply(event, b.generateBotHelp())
		return
	}

	if strings.HasPrefix(msg, prefix) {
		args := parseCommandArgs(msg[len(prefix):])
		if len(args) > 0 {
			// Super admin can specify target group as last arg: bot <cmd> <args> <groupID>
			if event.UserID == b.cfg.SuperAdmin && shouldTreatLastPrivateArgAsGroupID(args) {
				lastArg := args[len(args)-1]
				if gid, err := strconv.ParseInt(lastArg, 10, 64); err == nil && gid > 100000 {
					event.GroupID = gid
					args = args[:len(args)-1]
					log.Printf("[Bot] 超级管理员指定目标群: %d", gid)
				}
			}
			if len(args) > 0 {
				b.handleCommand(event, args)
			}
		}
	}
}

// generateBotHelp 自动生成帮助信息
func (b *Bot) generateBotHelp() string {
	return generateAutoHelp()
}

// handleCommand 表驱动命令分发
func (b *Bot) handleCommand(event *napcat.Event, args []string) {
	cmd := args[0]
	userID := event.UserID
	if userID == 0 && event.Sender.UserID != 0 {
		userID = event.Sender.UserID
	}

	def, ok := CmdRegistry[cmd]
	if !ok {
		b.reply(event, "❓ 未知命令，发送 bot help 查看可用命令")
		return
	}

	// 超级管理员专用
	if def.SuperOnly && userID != b.cfg.SuperAdmin {
		b.reply(event, "权限不足，仅超级管理员可操作")
		return
	}

	// 管理员专用
	if def.AdminOnly && !b.cfg.IsAdmin(userID) {
		b.reply(event, "权限不足，仅管理员可操作")
		return
	}

	// 群内专用
	if def.GroupOnly && event.MessageType == "private" {
		b.reply(event, "[错误] 该命令只能在群内使用")
		return
	}

	def.Handler(b, event, args)
}

func (b *Bot) handleMemberJoin(event *napcat.Event) {
	gid := event.GroupID
	if gid == 0 {
		return
	}

	wc := b.cfg.WelcomeConfigs
	if wc == nil {
		return
	}

	cfg, ok := wc[gid]
	if !ok || !cfg.Enabled || len(cfg.Messages) == 0 {
		return
	}

	msg := cfg.Messages[rand.Intn(len(cfg.Messages))]
	atSeg := napcat.AtSegment(strconv.FormatInt(event.UserID, 10))
	b.napcat.SendGroupMessage(gid, []napcat.MessageSegment{atSeg, napcat.TextSegment(" " + msg)})
	log.Printf("[Bot] 欢迎新成员: 群=%d, 用户=%d", gid, event.UserID)
}

func (b *Bot) handleWelcomeCommand(event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "📖 欢迎新成员命令:\n• welcome on <群号> - 开启欢迎\n• welcome off <群号> - 关闭欢迎\n• welcome add <群号> <欢迎语> - 添加欢迎语\n• welcome del <群号> <序号> - 删除第N条\n• welcome list <群号> - 查看所有欢迎语")
		return
	}

	action := args[1]

	var gid int64
	if len(args) >= 3 {
		gid, _ = strconv.ParseInt(args[2], 10, 64)
	}
	if gid == 0 && event.GroupID != 0 {
		gid = event.GroupID
	}
	if gid == 0 {
		b.reply(event, "请指定群号: welcome <命令> <群号>")
		return
	}

	if b.cfg.WelcomeConfigs == nil {
		b.cfg.WelcomeConfigs = make(map[int64]*config.WelcomeConfig)
	}
	if b.cfg.WelcomeConfigs[gid] == nil {
		b.cfg.WelcomeConfigs[gid] = &config.WelcomeConfig{}
	}
	wc := b.cfg.WelcomeConfigs[gid]

	switch action {
	case "on":
		wc.Enabled = true
		if len(wc.Messages) == 0 {
			wc.Messages = []string{"欢迎入群～"}
		}
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("✅ 群 %d 欢迎功能已开启", gid))
	case "off":
		wc.Enabled = false
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("✅ 群 %d 欢迎功能已关闭", gid))
	case "add":
		if len(args) < 4 {
			b.reply(event, "用法: welcome add <群号> <欢迎语>")
			return
		}
		msg := strings.Join(args[3:], " ")
		wc.Messages = append(wc.Messages, msg)
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("✅ 已添加欢迎语（共%d条）\n%s", len(wc.Messages), msg))
	case "del":
		if len(args) < 4 {
			b.reply(event, "用法: welcome del <群号> <序号>")
			return
		}
		idx, err := strconv.Atoi(args[3])
		if err != nil || idx < 1 || idx > len(wc.Messages) {
			b.reply(event, fmt.Sprintf("无效序号，当前共%d条欢迎语", len(wc.Messages)))
			return
		}
		wc.Messages = append(wc.Messages[:idx-1], wc.Messages[idx:]...)
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("✅ 已删除第%d条欢迎语", idx))
	case "list":
		if len(wc.Messages) == 0 {
			b.reply(event, fmt.Sprintf("群 %d 暂无欢迎语", gid))
			return
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("群 %d 欢迎语（开关: %v）:\n\n", gid, wc.Enabled))
		for i, msg := range wc.Messages {
			sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, msg))
		}
		b.reply(event, sb.String())
	default:
		b.reply(event, "未知命令，发送 welcome 查看帮助")
	}
}
