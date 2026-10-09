package logic

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pocket48-bot/internal/extract"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/outbound"
)

// feishuReplyTarget 算出某条飞书消息的回信目标。
//
// ★ 私聊回open_id、群回 chat_id，两者 target 形状不同，混用会 404。
//
//	ChatType: "p2p" = 私聊，"group" = 群。
//
// feishuReplyTarget 算出某条飞书消息的回信目标。
//
// ★★ 私聊必须用 **senderId（ou_）**，不能用 chatId（oc_）—— 2026-10-04 实测。
//
//	线上错误：HTTP 400 {"code":99992361,"msg":"open_id cross app"}
//	原因：outbound/feishu.go:308 按 target.Kind 决定 receive_id_type
//	（PrivateChat -> open_id）。原先传 Kind=PrivateChat 却把 chat_id
//	（oc_ 开头）当 Address，飞书拿 chat_id 去匹配 open_id —— 必然 cross app，
//	接收与解析全都正常（[Feishu-Extract] 已处理），但一条都发不出去。
//
//	飞书 ID 语义：
//	  oc_ = chat_id（会话/群 ID）—— 群聊回信用它
//	  ou_ = open_id（用户 ID，跟应用+组织走）—— 私聊回信用它
//
// ★ Target.ID 是 int64，只服务 QQ 数字 ID；飞书一律走 Address。
func feishuReplyTarget(ev outbound.FeishuIncomingEvent) (outbound.Target, bool) {
	if ev.ChatType == "p2p" {
		// 私聊：必须是发送者的 open_id。
		openID := strings.TrimSpace(ev.SenderID)
		if openID == "" {
			// 拿不到 open_id 就无法回信 —— chat_id 在这里一定失败。
			log.Printf("[Feishu-Extract] 私聊缺少 senderId，无法回信 chat=%s", ev.ChatID)
			return outbound.Target{}, false
		}
		return outbound.Target{
			Platform: "feishu",
			Kind:     outbound.PrivateChat,
			Address:  openID,
		}, true
	}

	// 群聊：用 chat_id。senderId 在群里无效（要发的人不是单个用户）。
	chatID := strings.TrimSpace(ev.ChatID)
	if chatID == "" {
		return outbound.Target{}, false
	}
	return outbound.Target{
		Platform: "feishu",
		Kind:     outbound.GroupChat,
		Address:  chatID,
	}, true
}

// handleFeishuIncoming 处理飞书长连接收到的一条消息。
//
// ★ 存在的意义：此前飞书**只有出站没有入站**（outbound/feishu.go 全是发送），
//
//	链接提取的两个调用点都在 NapCat 路径上，所以「在飞书私聊发链接毫无反应」
//	—— 既不是权限问题，也不是解析器不支持，而是机器人压根收不到消息。
//
// 与 QQ 的差别只有两点：
//  1. 群里必须 @ 机器人才响应（QQ 用 NapCat 事件的 at 标志，
//     飞书用 mentions 解析出的 MentionBot）；
//  2. 回信走 outbound.Send 而非 NapCat。
//
// ★★ 2026-10-08 新增**命令分支**：此前这里只抽链接，抽不到就 return，
//
//	所以「bot login sms <手机号>」在飞书上毫无反应 —— 不是配置问题，
//	是 QQ 那套命令体系从来没接到飞书。现在先试命令、再试链接。
//
// 其余（抽链接、并发去重、解析、下载、发送顺序）全部复用 QQ 那套语义，
// 不复制第二份 runExtract —— 复制必然随时间漂移。
func (b *Bot) handleFeishuIncoming(ev outbound.FeishuIncomingEvent) {
	msg := strings.TrimSpace(ev.Text)
	if msg == "" {
		return
	}
	isGroup := ev.ChatType != "p2p"
	// 群里不 @ 就当没看见；私聊直接响应。
	if isGroup && !ev.MentionBot {
		return
	}

	// 命令优先：「bot login sms ...」里没有链接，先给命令让路。
	if b.handleFeishuCommand(ev, msg) {
		return
	}

	link := extractFirstLink(msg)
	if link == "" {
		return
	}
	// 同一链接并发时只处理一次，避免连发刷屏。
	if _, loaded := extractInFlight.LoadOrStore(link, time.Now()); loaded {
		return
	}

	wantGIF := extractWantsGIF(msg)
	// 解析 + 下载可能耗时数十秒，绝不能阻塞长连接的读循环。
	go func() {
		defer extractInFlight.Delete(link)
		b.runExtractToFeishu(ev, link, wantGIF)
	}()
}

// runExtractToFeishu 执行提取并回信到飞书。
func (b *Bot) runExtractToFeishu(ev outbound.FeishuIncomingEvent, link string, wantGIF bool) {
	target, ok := feishuReplyTarget(ev)
	if !ok || b.outbound == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	post, err := b.extractClient().Extract(ctx, link)
	if err != nil {
		b.outbound.Send(target, message.Text("🔗 "+friendlyExtractError(err)))
		log.Printf("[Feishu-Extract] 解析失败 %s: %v", link, err)
		return
	}

	b.sendExtractedToFeishu(target, post, wantGIF)
	log.Printf("[Feishu-Extract] 已处理 %s 平台=%s 媒体=%d", link, post.Platform, len(post.Items))
}

// sendExtractedToFeishu 按与 QQ 一致的顺序发送：正文+媒体合成一条 -> 视频独立成条。
//
// ★ 用户口径（AskUserQuestion 明确回答）：
//
//	「正文和视频是合并不了的」「正文和封面是可以合并的」
//	「格式和我们在 QQ 上直接转发监控动态的格式统一一下」
//	监控推送本身就是「一条卡片（正文+封面）+ 一条视频」，所以照那个形状发。
//
// ★★ 2026-10-09 修正：图片**不再逐张独立成条**，全部并进卡片。
// 此前每张图一条消息，6 图微博刷出 7 条消息。飞书卡片原生支持多个 img 元素
// （outbound/feishu.go：content.images → uploadImages → sendCard），
// 一条消息里「正文 + 全部图片」才是飞书该有的样子。视频仍独立成条。
func (b *Bot) sendExtractedToFeishu(target outbound.Target, post *extract.Post, wantGIF bool) {
	if b.outbound == nil {
		return
	}
	doc := message.Document{
		Source: string(post.Platform),
		Kind:   "link_extract",
		Title:  strings.TrimSpace(post.Author),
		Author: strings.TrimSpace(post.Author),
		Body:   formatExtractedText(post),
		Link:   post.URL,
	}

	// 图片全部并进卡片。按 post.Items 的原顺序，封面不重复。
	//
	// ★ 记忆里的坑：outbound 是**异步队列**，这里传本地临时路径后
	// 绝不能 defer 删除 —— 后台真正发送时文件已经没了，图片会静默丢失。
	// downloadMediaFile 的返回值交给适配器去管，本函数不碰。
	for _, item := range post.Items {
		if item.Kind != "image" {
			continue
		}
		local, err := resolveExtractMedia(item)
		if err != nil {
			log.Printf("[Feishu-Extract] 图片准备失败 %s: %v", item.URL, err)
			continue
		}
		doc.Media = append(doc.Media, message.Media{Kind: "image", Source: local})
	}

	// 图文帖有图时封面已在卡内，不必再单独塞一张（否则同一张图出现两次）。
	// 只有「一张图都没有」时才把 Cover 当唯一封面用。
	//
	// ★ 走 resolveExtractMedia 而不是 downloadMediaFile：后者只认 http 直链，
	//   封面已经是本地路径时会直接失败。视频帖的 page_info.page_pic 就属于这一类。
	if post.Cover != "" && len(doc.Media) == 0 {
		coverItem := extract.Item{Kind: "image", URL: post.Cover, Cover: post.Cover}
		if local, err := resolveExtractMedia(coverItem); err == nil {
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: local})
		} else {
			log.Printf("[Feishu-Extract] 封面准备失败 %s: %v", post.Cover, err)
		}
	}

	// 先发卡片（正文 + 全部图片），即使后面视频失败，用户也拿到了文字和图。
	b.outbound.Send(target, doc)

	// 视频逐个独立成条 —— 飞书卡片内不能内嵌视频/音频。
	for _, item := range post.Items {
		if item.Kind != "video" {
			continue
		}
		b.sendExtractedVideoFeishu(target, item, wantGIF)
	}
}

// sendExtractedVideoFeishu 发一条视频。
//
// 与 QQ 的差别：失败时**只记日志**，不再回一条文字 ——
// 那种补救文案在 QQ 上是必要的（用户看得到日志），
// 但在这里会变成第二条噪音消息。
func (b *Bot) sendExtractedVideoFeishu(target outbound.Target, item extract.Item, wantGIF bool) {
	local, err := resolveExtractMedia(item)
	if err != nil {
		log.Printf("[Feishu-Extract] 视频准备失败 %s: %v", item.URL, err)
		return
	}
	_ = wantGIF // GIF 方案已被用户否决，统一按视频发。
	b.outbound.Send(target, []message.Segment{message.Video(local, "")})
}

// startFeishuLongConn 拉起飞书入站长连接。
//
// 不阻塞 Start：连接建立、鉴权、重连都在后台 goroutine 里，
// 失败只记日志 —— 长连接挂掉不应该让整个 Bot 起不来。
//
// 凭据为空时直接跳过：不是每套部署都配了飞书应用。
func (b *Bot) startFeishuLongConn() {
	if b == nil || b.cfg == nil {
		return
	}
	appID := strings.TrimSpace(b.cfg.FeishuAppID)
	appSecret := strings.TrimSpace(b.cfg.FeishuAppSecret)
	if appID == "" || appSecret == "" {
		b.LogInfo("飞书长连接未启用（缺少 FEISHU_APP_ID / FEISHU_APP_SECRET）")
		return
	}

	client := outbound.NewFeishuWSClient(outbound.FeishuWSOptions{
		AppID: appID,
		// sidecar 用官方 lark-oapi；手写协议猜错过端点路径与 code 类型。
		AppSecret: appSecret,
		// sidecar 脚本在项目目录下，与 config.json 同级。
		// Config 只有 ConfigPath()，所以从它上跳一级拿项目根 ——
		// 这与 crossdedupe.storageRootOf 是同一个道理。
		Script:  filepath.Join(filepath.Dir(b.cfg.ConfigPath()), "sidecar", "feishu-ws", "feishu_ws.py"),
		OnEvent: b.handleFeishuIncoming,
		Logger:  log.New(os.Stderr, "", log.LstdFlags),
	})
	b.feishuWS = client

	// ctx 用 Background：Bot 没有统一的生命周期 ctx，
	// 停止交给 StopFeishuLongConn（由关闭流程调用）。
	go client.Start(context.Background())
	log.Printf("[Feishu-WS] 长连接已启动 appID=%s", appID)
}

// StopFeishuLongConn 关闭长连接（由 Bot 关闭流程调用）。
func (b *Bot) StopFeishuLongConn() {
	if b != nil && b.feishuWS != nil {
		b.feishuWS.Stop()
	}
}
