package logic

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
)

// ---- 命令定义 ----

type CommandDef struct {
	Handler   func(b *Bot, event *napcat.Event, args []string)
	Help      string
	Category  string
	AdminOnly bool
	SuperOnly bool
	GroupOnly bool
	Usage     string
}

// CmdRegistry 注册表
var CmdRegistry map[string]*CommandDef

func init() {
	CmdRegistry = map[string]*CommandDef{
		"on": {
			Handler:   cmdOn,
			Help:      "开启口袋房间监控",
			Category:  "监控控制",
			AdminOnly: true,
		},
		"off": {
			Handler:   cmdOff,
			Help:      "关闭口袋房间监控",
			Category:  "监控控制",
			AdminOnly: true,
		},
		"live": {
			Handler:   cmdLive,
			Help:      "直播通知开关",
			Category:  "功能开关",
			AdminOnly: true,
			Usage:     "live <on/off> [房间号/名字]",
		},
		"gift": {
			Handler:   cmdGift,
			Help:      "礼物回复开关",
			Category:  "功能开关",
			AdminOnly: true,
			Usage:     "gift <on/off> <房间号/名字>",
		},
		"score": {
			Handler:   cmdScore,
			Help:      "年度青春盛典记分监控开关",
			Category:  "功能开关",
			AdminOnly: true,
			Usage:     "score <on/off> <房间号/名字>",
		},
		"admin": {
			Handler:   cmdAdmin,
			Help:      "管理管理员(超级管理员)",
			Category:  "账号管理",
			SuperOnly: true,
			Usage:     "admin <add/remove> <QQ号>",
		},
		"list": {
			Handler:   cmdList,
			Help:      "查看监控列表",
			Category:  "房间管理",
			AdminOnly: true,
			Usage:     "list [channels]",
		},
		"remove": {
			Handler:   cmdRemove,
			Help:      "移除监控房间",
			Category:  "房间管理",
			AdminOnly: true,
			Usage:     "remove <房间号>",
		},
		"login": {
			Handler:   cmdLogin,
			Help:      "口袋48登录",
			Category:  "账号管理",
			AdminOnly: true,
			Usage:     "login <token> | login sms <手机号> | login pwd <密码>",
		},
		"code": {
			Handler:   cmdCode,
			Help:      "输入短信验证码",
			Category:  "账号管理",
			AdminOnly: true,
			Usage:     "code <验证码>",
		},
		"whoami": {
			Handler:   cmdWhoami,
			Help:      "查看当前口袋账号",
			Category:  "账号管理",
			AdminOnly: true,
		},
		"search": {
			Handler:   cmdSearch,
			Help:      "搜索成员房间",
			Category:  "房间管理",
			AdminOnly: true,
			Usage:     "search <名字>",
		},
		"monitor": {
			Handler:   cmdMonitor,
			Help:      "添加房间监控(群内使用)",
			Category:  "房间管理",
			AdminOnly: true,
			GroupOnly: true,
			Usage:     "monitor <房间号>",
		},
		"bind": {
			Handler:   cmdBind,
			Help:      "绑定机器人到本群(超级管理员)",
			Category:  "账号管理",
			SuperOnly: true,
			GroupOnly: true,
		},
		"weibo": {
			Handler:   cmdWeibo,
			Help:      "微博相关功能（cookie/监控/超话）",
			Category:  "微博功能",
			AdminOnly: true,
			Usage:     "weibo <add/del/list/cookie|super|superpost>",
		},
		"archive": {
			Handler:   cmdArchive,
			Help:      "归档状态/重试",
			Category:  "其他",
			AdminOnly: true,
			Usage:     "archive <status/retry>",
		},
		"help": {
			Handler:   cmdHelp,
			Help:      "显示帮助",
			Category:  "其他",
		},
		"test": {
			Handler:   cmdTest,
			Help:      "发送测试消息",
			Category:  "其他",
			AdminOnly: true,
			Usage:     "test <live/weibo>",
		},
		"welcome": {
			Handler:   cmdWelcome,
			Help:      "欢迎新成员设置",
			Category:  "其他",
			AdminOnly: true,
			Usage:     "welcome <on/off/add/del/list> <群号> [参数]",
		},
	}
}

// ---- 通用辅助 ----

// resolveRoomID 解析房间号：支持数字ID或名字搜索
func resolveRoomID(b *Bot, event *napcat.Event, target string) (int64, bool) {
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		return id, true
	}
	servers, err := b.pocket.Search(target)
	if err != nil || len(servers) == 0 {
		b.reply(event, fmt.Sprintf("未找到结果: %s", target))
		return 0, false
	}
	ids, _ := b.pocket.GetChannelIDByServerID(servers[0].ServerID)
	if len(ids) == 0 {
		b.reply(event, fmt.Sprintf("无法获取房间ID: %s", servers[0].ServerName))
		return 0, false
	}
	b.reply(event, fmt.Sprintf("找到 %s (房间ID: %d)", servers[0].ServerName, ids[0]))
	return ids[0], true
}

// ---- 命令处理函数 ----

func cmdOn(b *Bot, event *napcat.Event, args []string) {
	b.isMonitoring = true
	b.reply(event, "✅ 监控已开启")
}

func cmdOff(b *Bot, event *napcat.Event, args []string) {
	b.isMonitoring = false
	b.reply(event, "✅ 监控已关闭")
}

func cmdLive(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		state := "开启"
		if !b.cfg.LiveMonitoring {
			state = "关闭"
		}
		b.reply(event, fmt.Sprintf("全局直播监控状态: %s (用法: live <on/off> [房间号/名字])", state))
		return
	}

	action := strings.ToLower(args[1])
	if action != "on" && action != "off" {
		b.reply(event, "格式错误: live <on/off> [房间号/名字]")
		return
	}

	targetVal := action == "on"

	// 无房间号 -> 全局开关
	if len(args) == 2 {
		b.cfg.LiveMonitoring = targetVal
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("[OK] 全局直播监控已设为 %s", strings.ToUpper(action)))
		return
	}

	roomID, ok := resolveRoomID(b, event, args[2])
	if !ok {
		return
	}
	if b.cfg.LiveSpecific == nil {
		b.cfg.LiveSpecific = make(map[string]bool)
	}
	b.cfg.LiveSpecific[strconv.FormatInt(roomID, 10)] = targetVal
	b.cfg.Save()
	b.reply(event, fmt.Sprintf("[OK] 房间 %d 的直播监控已设为 %s", roomID, strings.ToUpper(action)))
}

func cmdGift(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 3 {
		b.reply(event, "用法: gift <on/off> <房间号/名字>")
		return
	}
	action := strings.ToLower(args[1])
	if action != "on" && action != "off" {
		b.reply(event, "格式错误: gift <on/off> <房间号>")
		return
	}
	roomID, ok := resolveRoomID(b, event, args[2])
	if !ok {
		return
	}
	if b.cfg.GiftSpecific == nil {
		b.cfg.GiftSpecific = make(map[string]bool)
	}
	b.cfg.GiftSpecific[strconv.FormatInt(roomID, 10)] = (action == "on")
	b.cfg.Save()
	b.reply(event, fmt.Sprintf("[OK] 房间 %d 的礼物回复已设为 %s", roomID, strings.ToUpper(action)))
}

func cmdScore(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 3 {
		b.reply(event, "用法: score <on/off> <房间号/名字>")
		return
	}
	action := strings.ToLower(args[1])
	if action != "on" && action != "off" {
		b.reply(event, "格式错误: score <on/off> <房间号>")
		return
	}
	roomID, ok := resolveRoomID(b, event, args[2])
	if !ok {
		return
	}
	if b.cfg.AnnualScoreSpecific == nil {
		b.cfg.AnnualScoreSpecific = make(map[string]bool)
	}
	b.cfg.AnnualScoreSpecific[strconv.FormatInt(roomID, 10)] = (action == "on")
	b.cfg.Save()
	b.reply(event, fmt.Sprintf("[OK] 房间 %d 的年度青春盛典记分监控已设为 %s", roomID, strings.ToUpper(action)))
}

func cmdAdmin(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 3 {
		b.reply(event, "格式错误: admin <add/remove> <QQ号>")
		return
	}
	targetQQ, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		b.reply(event, "无效的QQ号")
		return
	}
	switch args[1] {
	case "add":
		b.cfg.AddAdmin(targetQQ)
		b.reply(event, fmt.Sprintf("[OK] 已添加管理员: %d", targetQQ))
	case "remove":
		b.cfg.RemoveAdmin(targetQQ)
		b.reply(event, fmt.Sprintf("[OK] 已移除管理员: %d", targetQQ))
	default:
		b.reply(event, "格式错误: admin <add/remove> <QQ号>")
	}
}

func cmdList(b *Bot, event *napcat.Event, args []string) {
	groupIDStr := strconv.FormatInt(event.GroupID, 10)
	rooms := b.cfg.GroupSubscriptions[groupIDStr]

	if len(rooms) == 0 {
		b.reply(event, "当前没有正在监控的房间。")
		return
	}

	showChannels := len(args) >= 2 && args[1] == "channels"
	chKey := "房间"
	if showChannels {
		chKey = "频道"
	}
	b.reply(event, fmt.Sprintf("正在获取%s详情，请稍候...", chKey))

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("当前监控的%s:\n", chKey))
	for _, roomID := range rooms {
		info, err := b.getCachedRoomInfo(roomID)
		if err != nil {
			sb.WriteString(fmt.Sprintf("- ID: %d (获取详情失败)\n", roomID))
		} else {
			sb.WriteString(fmt.Sprintf("- ID: %d | %s: %s | 主播: %s\n", roomID, chKey, info.ChannelName, info.OwnerName))
		}
	}
	b.reply(event, sb.String())
}

func cmdRemove(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "格式错误: remove <房间号>")
		return
	}
	targetRoomID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		b.reply(event, "无效的房间ID")
		return
	}

	groupIDStr := strconv.FormatInt(event.GroupID, 10)
	currentRooms := b.cfg.GroupSubscriptions[groupIDStr]
	newRooms := make([]int64, 0, len(currentRooms))
	found := false
	for _, id := range currentRooms {
		if id == targetRoomID {
			found = true
			continue
		}
		newRooms = append(newRooms, id)
	}
	if found {
		b.cfg.GroupSubscriptions[groupIDStr] = newRooms
		b.cfg.Save()
		b.reply(event, fmt.Sprintf("[OK] 已移除房间 %d 的监控", targetRoomID))
	} else {
		b.reply(event, fmt.Sprintf("[Info] 房间 %d 不在监控列表中", targetRoomID))
	}
}

func cmdLogin(b *Bot, event *napcat.Event, args []string) {
	if len(args) == 2 {
		// token login
		b.cfg.UpdateToken(args[1])
		tokenErr := b.pocket.CheckToken()
		if tokenErr == nil {
			b.clearPocketAuthExpired()
		} else {
			b.handlePocketAuthError(tokenErr)
		}
		status := "ok"
		if tokenErr != nil {
			status = "failed: " + tokenErr.Error()
		}
		b.reply(event, fmt.Sprintf("Token 已更新\ntoken_check: %s", status))
		return
	}

	if len(args) >= 3 && args[1] == "sms" {
		targetPhone := args[2]
		if !isValidPocketMobile(targetPhone) {
			b.reply(event, "手机号格式错误，请使用11位中国大陆手机号")
			return
		}
		b.reply(event, fmt.Sprintf("正在向 %s 发送验证码...", targetPhone))
		if err := b.pocket.SendSMS(targetPhone); err != nil {
			b.reply(event, fmt.Sprintf("发送验证码失败: %v", err))
			return
		}
		b.mu.Lock()
		b.pendingPocketSMSMobile = targetPhone
		b.mu.Unlock()
		b.reply(event, fmt.Sprintf("验证码已发送到 %s，请回复: bot code <验证码>", maskMobile(targetPhone)))
		return
	}

	if len(args) == 3 && args[1] == "pwd" {
		password := args[2]
		mobile := b.cfg.PocketUsername
		if mobile == "" {
			b.reply(event, "请先设置手机号")
			return
		}
		if err := b.pocket.LoginWithPassword(mobile, encryptPlainPassword(password)); err != nil {
			b.reply(event, "密码登录失败: "+err.Error())
		} else {
			b.reply(event, "密码登录成功！Token已更新。")
		}
		return
	}

	b.reply(event, "用法:\n  login <token> - 直接设置Token\n  login sms <手机号> - 发送验证码登录\n  login pwd <加密密码> - 密码登录\n  code <验证码> - 输入验证码完成登录")
}

func cmdCode(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "用法: code <验证码>")
		return
	}
	b.mu.RLock()
	mobile := b.pendingPocketSMSMobile
	b.mu.RUnlock()
	if mobile == "" {
		b.reply(event, "没有待完成的短信登录，请先发送: bot login sms <手机号>")
		return
	}
	if err := b.pocket.LoginWithSMS(mobile, args[1]); err != nil {
		b.reply(event, fmt.Sprintf("SMS Login Failed: %v", err))
		return
	}
	b.cfg.PocketUsername = mobile
	b.cfg.Save()
	b.mu.Lock()
	b.pendingPocketSMSMobile = ""
	b.mu.Unlock()
	b.clearPocketAuthExpired()
	b.reply(event, "SMS Login Successful! Token updated.")
}

func cmdWhoami(b *Bot, event *napcat.Event, args []string) {
	mobile := b.cfg.PocketUsername
	if mobile == "" {
		mobile = "(未设置)"
	}
	tokenStatus := "未设置"
	if b.cfg.PocketToken != "" {
		tokenStatus = "已设置"
	}
	b.reply(event, fmt.Sprintf("当前口袋账号:\n手机号: %s\nToken: %s", mobile, tokenStatus))
}

func cmdSearch(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "用法: search <名字>")
		return
	}
	servers, err := b.pocket.Search(args[1])
	if err != nil {
		b.reply(event, "搜索失败: "+err.Error())
		return
	}
	if len(servers) == 0 {
		b.reply(event, "未找到结果")
		return
	}
	var sb strings.Builder
	sb.WriteString("搜索结果:\n")
	for _, s := range servers {
		ids, _ := b.pocket.GetChannelIDByServerID(s.ServerID)
		for _, id := range ids {
			if id >= 0 {
				info, _ := b.getCachedRoomInfo(id)
				name := "未知房间"
				if info != nil {
					name = info.ChannelName
				}
				sb.WriteString(fmt.Sprintf("- %s (房间ID: %d)\n", name, id))
			}
		}
	}
	b.reply(event, sb.String())
}

func cmdMonitor(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "用法: monitor <房间号>")
		return
	}
	roomID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		b.reply(event, "无效的房间ID")
		return
	}
	groupIDStr := strconv.FormatInt(event.GroupID, 10)
	if b.cfg.GroupSubscriptions == nil {
		b.cfg.GroupSubscriptions = make(map[string][]int64)
	}
	for _, id := range b.cfg.GroupSubscriptions[groupIDStr] {
		if id == roomID {
			b.reply(event, "[Info] 该房间已在监控列表中")
			return
		}
	}
	b.cfg.GroupSubscriptions[groupIDStr] = append(b.cfg.GroupSubscriptions[groupIDStr], roomID)
	b.cfg.Save()
	b.reply(event, fmt.Sprintf("[OK] 已添加房间 %d 到监控列表", roomID))
}

func cmdBind(b *Bot, event *napcat.Event, args []string) {
	oldGroup := b.cfg.BoundGroupID
	b.cfg.BoundGroupID = event.GroupID
	b.cfg.Save()
	b.reply(event, fmt.Sprintf("[OK] 机器人已绑定到群 %d (之前: %d)", event.GroupID, oldGroup))
}

// generateAutoHelp 从注册表自动生成帮助文本
func generateAutoHelp() string {
	// 定义分类顺序
	categories := []string{
		"监控控制",
		"房间管理",
		"功能开关",
		"微博功能",
		"账号管理",
		"其他",
	}

	// 按分类分组
	byCategory := make(map[string][]string)
	for cmd, def := range CmdRegistry {
		if def.Help == "" {
			continue
		}
		cat := def.Category
		if cat == "" {
			cat = "其他"
		}
		usage := cmd
		if def.Usage != "" {
			usage = def.Usage
		}
		line := fmt.Sprintf("- bot %s - %s", usage, def.Help)
		byCategory[cat] = append(byCategory[cat], line)
	}

	// 拼接输出
	var sb strings.Builder
	sb.WriteString("📖 可用命令：\n\n")
	for _, cat := range categories {
		lines, ok := byCategory[cat]
		if !ok || len(lines) == 0 {
			continue
		}
		sb.WriteString(fmt.Sprintf("【%s】\n", cat))
		for _, line := range lines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("💡 也可以发 \"帮助\" 或 \"help\" 来获取此帮助")
	return sb.String()
}

func cmdHelp(b *Bot, event *napcat.Event, args []string) {
	if len(args) >= 2 {
		cmdName := strings.ToLower(strings.TrimSpace(args[1]))
		def, ok := CmdRegistry[cmdName]
		if !ok {
			b.reply(event, fmt.Sprintf("❓ 未知命令: %s\n发送 bot help 查看所有可用命令", cmdName))
			return
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📘 命令: %s\n", cmdName))
		sb.WriteString(fmt.Sprintf("📝 说明: %s\n", def.Help))
		if def.Usage != "" {
			sb.WriteString(fmt.Sprintf("📎 用法: bot %s\n", def.Usage))
		} else {
			sb.WriteString(fmt.Sprintf("📎 用法: bot %s\n", cmdName))
		}
		// 权限标记
		var perms []string
		if def.SuperOnly {
			perms = append(perms, "🔒 仅超级管理员")
		} else if def.AdminOnly {
			perms = append(perms, "🔑 仅管理员")
		}
		if def.GroupOnly {
			perms = append(perms, "🏠 仅群内使用")
		}
		if len(perms) > 0 {
			sb.WriteString("⚙️  权限: ")
			sb.WriteString(strings.Join(perms, " | "))
			sb.WriteString("\n")
		}
		b.reply(event, strings.TrimRight(sb.String(), "\n"))
		return
	}
	b.reply(event, generateAutoHelp())
}

func cmdWeibo(b *Bot, event *napcat.Event, args []string) {
	if len(args) >= 2 && args[1] == "super" {
		b.reply(event, b.handleWeiboSuperCommand(event, args))
		return
	}
	if len(args) >= 2 && args[1] == "superpost" {
		b.reply(event, b.handleWeiboSuperPostCommand(event, args))
		return
	}
	if len(args) >= 2 && args[1] == "cookie" {
		if len(args) < 3 {
			b.reply(event, "用法: weibo cookie <import|check|set> [参数]")
			return
		}
		action := strings.ToLower(args[2])
		switch action {
		case "check":
			webOK, webDetail, _ := b.weiboMonitor.CheckWebCookie("")
			mwebOK, mwebDetail, _ := b.weiboMonitor.CheckMWeiboCookie("")
			appAuthOK := b.weiboMonitor.AppConfig != nil && strings.TrimSpace(b.weiboMonitor.AppConfig.Authorization) != ""

			var parts []string
			if !appAuthOK {
				parts = append(parts, "AppAuth=未配置")
			} else {
				parts = append(parts, "AppAuth=已配置")
			}
			parts = append(parts, fmt.Sprintf("weibo.com=%s", weiboCheckStatus(webOK, webDetail)))
			parts = append(parts, fmt.Sprintf("mweibo=%s", weiboCheckStatus(mwebOK, mwebDetail)))

			allOK := appAuthOK || webOK || mwebOK
			if allOK {
				b.reply(event, fmt.Sprintf("[OK] 微博认证状态: %s", strings.Join(parts, " | ")))
			} else {
				b.reply(event, fmt.Sprintf("[警告] 微博认证异常: %s\n💡 粘贴 App 抓包自动更新: bot weibo cookie import <抓包文本>", strings.Join(parts, " | ")))
			}
		case "import":
			if len(args) < 4 {
				b.reply(event, "格式错误: weibo cookie import <抓包文本>")
				return
			}
			rawText := strings.TrimSpace(strings.Join(args[3:], " "))
			// 优先尝试解析为 AppAuth
			if appCfg, ok := extractWeiboAppAuthFromCaptureText(rawText); ok {
				b.updateWeiboAppAuth(appCfg)
				extra := ""
				if strings.TrimSpace(appCfg.GSID) != "" {
					extra = "\n[自动] 已从 gsid 推导 weibo.com Cookie"
				}
				b.reply(event, fmt.Sprintf("[OK] 已导入微博 App 认证: %s%s", maskWeiboAppAuth(appCfg), extra))
				return
			}
			// 尝试解析为 Cookie
			parsedCookie, ok := extractCookieFromCaptureText(rawText)
			if !ok {
				// 最后尝试直接作为 cookie 值
				parsedCookie = strings.TrimSpace(rawText)
				if parsedCookie == "" {
					b.reply(event, "[错误] 未提取到有效认证信息")
					return
				}
			}
			masked, err := b.updateWeiboCookie(event.UserID, parsedCookie)
			if err != nil {
				b.reply(event, fmt.Sprintf("[错误] 导入失败: %v", err))
				return
			}
			b.reply(event, fmt.Sprintf("[OK] 已更新 weibo.com Cookie: %s\n💡 建议使用 App 抓包导入以获得完整功能", masked))
		case "set":
			if len(args) < 4 {
				b.reply(event, "格式错误: weibo cookie set <Cookie>")
				return
			}
			cookie := strings.TrimSpace(strings.Join(args[3:], " "))
			masked, err := b.updateWeiboCookie(event.UserID, cookie)
			if err != nil {
				b.reply(event, fmt.Sprintf("[错误] 更新失败: %v", err))
				return
			}
			b.reply(event, fmt.Sprintf("[OK] weibo.com Cookie 已更新: %s", masked))
		default:
			b.reply(event, "用法: weibo cookie <import|check|set> [参数]")
		}
		return
	}
	if len(args) < 2 {
		b.reply(event, "格式错误: weibo <add/del/list|cookie|super|superpost> [参数]")
		return
	}
	action := args[1]
	switch action {
	case "list":
		if b.cfg.WeiboSubscriptions == nil || len(b.cfg.WeiboSubscriptions[event.GroupID]) == 0 {
			b.reply(event, "该群暂无微博监控")
			return
		}
		var uids []string
		for uid := range b.cfg.WeiboSubscriptions[event.GroupID] {
			uids = append(uids, uid)
		}
		b.reply(event, fmt.Sprintf("微博监控: UID=%s", strings.Join(uids, ", ")))
	case "add":
		if len(args) < 3 {
			b.reply(event, "格式错误: weibo add <UID> [at_all]")
			return
		}
		uid := args[2]
		atAll := len(args) >= 4 && args[3] == "at_all"
		if b.cfg.WeiboSubscriptions == nil {
			b.cfg.WeiboSubscriptions = make(map[int64]map[string]*config.WeiboConfig)
		}
		if _, ok := b.cfg.WeiboSubscriptions[event.GroupID]; !ok {
			b.cfg.WeiboSubscriptions[event.GroupID] = make(map[string]*config.WeiboConfig)
		}
		lastID := ""
		if b.cfg.WeiboSubscriptions[event.GroupID][uid] != nil {
			lastID = b.cfg.WeiboSubscriptions[event.GroupID][uid].LastID
		}
		b.cfg.WeiboSubscriptions[event.GroupID][uid] = &config.WeiboConfig{UID: uid, AtAll: atAll, LastID: lastID}
		b.cfg.Save()
		gid := event.GroupID
		onNew := func(u, newLastID string) {
			if b.cfg.WeiboSubscriptions[gid] != nil && b.cfg.WeiboSubscriptions[gid][u] != nil {
				b.cfg.WeiboSubscriptions[gid][u].LastID = newLastID
				b.cfg.Save()
			}
		}
		if err := b.weiboMonitor.AddConfig(event.GroupID, uid, atAll, lastID, onNew); err != nil {
			b.reply(event, fmt.Sprintf("[错误] 添加微博监控失败: %v", err))
		} else {
			b.reply(event, fmt.Sprintf("[OK] 添加微博监控: UID=%s, @全体=%v", uid, atAll))
		}
		b.weiboMonitor.Start()
	case "del":
		if len(args) < 3 {
			delete(b.cfg.WeiboSubscriptions, event.GroupID)
			b.cfg.Save()
			b.weiboMonitor.RemoveConfig(event.GroupID, "")
			b.reply(event, "[OK] 已删除该群的所有微博监控")
			return
		}
		uidToDel := args[2]
		if groupSubs, ok := b.cfg.WeiboSubscriptions[event.GroupID]; ok {
			if _, ok := groupSubs[uidToDel]; ok {
				delete(groupSubs, uidToDel)
				if len(groupSubs) == 0 {
					delete(b.cfg.WeiboSubscriptions, event.GroupID)
				}
				b.cfg.Save()
				b.weiboMonitor.RemoveConfig(event.GroupID, uidToDel)
				b.reply(event, fmt.Sprintf("[OK] 已删除微博监控 UID: %s", uidToDel))
			} else {
				b.reply(event, fmt.Sprintf("[错误] 未找到要删除的 UID: %s", uidToDel))
			}
		} else {
			b.reply(event, "该群暂无微博监控")
		}
	default:
		b.reply(event, "格式错误: weibo <add/del/list|cookie|super|superpost> [参数]")
	}
}

func cmdArchive(b *Bot, event *napcat.Event, args []string) {
	b.reply(event, b.handleArchiveCommand(args))
}

func cmdTest(b *Bot, event *napcat.Event, args []string) {
	if len(args) < 2 {
		b.reply(event, "用法: test <live/weibo>")
		return
	}
	switch args[1] {
	case "live":
		roomInfo := pocket48.RoomInfo{
			ChannelID:   123456,
			ChannelName: "测试直播间",
			OwnerName:   "测试小偶像",
		}
		testMsg := &pocket48.Message{
			Room: &roomInfo,
			Body: `{"title":"测试直播标题（含封面）","cover":"https://picsum.photos/seed/bot48-live-cover/960/540","liveId":"123456789"}`,
		}
		b.sendLivePush([]int64{event.GroupID}, testMsg, time.Now().Format("2006-01-02 15:04"))
		b.reply(event, "[OK] 已发送测试直播通知")
	case "weibo":
		if b.weiboMonitor == nil {
			b.reply(event, "[错误] 微博监控未启动")
			return
		}
		testCard := monitor.WeiboCard{
			Scheme: "https://weibo.com/123456/abc123",
			Text:   "这是一条<b>测试微博</b>内容",
			Pics: []struct {
				Type     string `json:"type"`
				VideoSrc string `json:"videoSrc"`
				Large    struct {
					URL string `json:"url"`
				} `json:"large"`
			}{
				{Large: struct {
					URL string `json:"url"`
				}{URL: "https://source.48.cn/test-image.jpg"}},
			},
			ID: "1234567890",
			User: struct {
				ScreenName string      `json:"screen_name"`
				ID         interface{} `json:"id"`
				IDStr      string      `json:"idstr"`
				UID        string      `json:"uid"`
			}{ScreenName: "测试博主", IDStr: "123456", UID: "123456"},
		}
		testConfig := &monitor.WeiboConfig{
			GroupID: event.GroupID,
			UID:     "123456",
			AtAll:   false,
		}
		b.weiboMonitor.DispatchPerfectWeibo(testConfig, testCard, "1234567890")
		b.reply(event, "[OK] 已发送测试微博通知")
	default:
		b.reply(event, "用法: test <live/weibo>")
	}
}

func cmdWelcome(b *Bot, event *napcat.Event, args []string) {
	b.handleWelcomeCommand(event, args)
}
