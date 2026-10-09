package logic

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/dedupe"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/outbound"
	"pocket48-bot/internal/tiktokmonitor"
)

// tiktokSender 把 TikTok 作品推送到飞书/QQ。
//
// 复用抖音那套发送通道（outbound.SendToTargetIDs + napcat.VideoSegment），
// 这样「视频必须独立成条」等飞书约束自然继承，不用重写一遍。
type tiktokSender struct {
	cfg      *config.Config
	outbound outbound.Sender
}

// SendTiktokVideo 推送一条作品：先卡片文字（含标题/时长/链接按钮），再视频文件。
//
// 为什么拆成两条消息：飞书卡片内不能内嵌视频/音频，视频必须以
// msg_type=media 独立成条。抖音侧也是同样处理。
func (s *tiktokSender) SendTiktokVideo(ctx context.Context, v tiktokmonitor.Video, localPath string, seconds int) error {
	if s == nil || s.cfg == nil {
		return fmt.Errorf("TikTok 发送器未初始化")
	}
	if localPath == "" {
		return fmt.Errorf("TikTok 作品 %s 没有本地视频", v.ID)
	}

	targets := s.targetIDs(v.AuthorName)
	if len(targets) == 0 {
		return fmt.Errorf("TikTok 未配置任何投递目标")
	}

	// 卡片走 Document：来源固定为 TikTok，发送者为作者昵称。
	// seconds 不再进正文，但仍参与跨平台去重比对（见 crossdedupe.Record）。
	doc := buildTiktokDocument(v)

	// ★ 封面图（2026-10-04 补，用户报「TikTok 只有标题没有封面图」）。
	//
	// 根因不是采集侧：sidecar 一直在抓封面（collector.py 里
	// originCover / cover / animatedCover 三级兜底 + og:image），
	// Video.Cover 字段也早就声明好了 —— 但**推送侧从来没读过它**。
	// 抖音 / X / 小红书 / B站 都有
	//     if len(images) == 0 && post.Cover != "" { images = []string{post.Cover} }
	// 这个兜底，唯独 TikTok 漏了，于是卡片永远只有文字 + 视频。
	//
	// 封面失败不能影响主流程：视频已经拿到手了，为一张图放弃推送是本末倒置。
	if cover := strings.TrimSpace(v.Cover); cover != "" {
		if local, err := downloadMediaFile(cover); err == nil {
			doc.Media = append(doc.Media, message.Media{Kind: "image", Source: local})
		} else {
			log.Printf("[TikTok] 封面下载失败（不影响投递）%s: %v", cover, err)
		}
	}

	s.sendToTargets(targets, doc)
	s.sendToTargets(targets, []napcat.MessageSegment{
		napcat.VideoSegment(localPath, tiktokmonitor.Link(v)),
	})

	log.Printf("[TikTok] 已投递到 %d 个目标：%s", len(targets), truncate(v.Desc, 50))
	return nil
}

// AlertText 生成连续失败告警文案（返回空串表示不需要告警）。
func (s *tiktokSender) AlertText(username string, failures int, cause error) string {
	if failures <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"TikTok 监控 @%s 已连续 %d 次采集失败：%v\n"+
			"TikTok 接口限流较严格，若提示限流建议把轮询间隔调大（当前 %v）。",
		username, failures, cause, tiktokScanInterval)
}

// buildTiktokBody 拼正文。
//
// 刻意**不加**「XXX发布了新视频」这类前缀 —— 用户明确要求 TikTok 侧
// 不要这种包装，原标题直接给出。抖音那边原有的前缀是历史遗留，不在此处照抄。
func buildTiktokBody(v tiktokmonitor.Video) string {
	// 刻意只给原标题。用户明确要求 TikTok 侧不要时长/播放量/点赞 ——
	// 这些字段每次采集都在变，却对"发了新视频"这个通知没有信息量，
	// 反而把标题挤到卡片第二屏。跨平台去重改用时长比对（见 crossdedupe），
	// 不再依赖正文里写出来的时长。
	title := strings.TrimSpace(v.Desc)
	if title == "" {
		title = "(无标题)"
	}
	return title
}

// buildTiktokDocument 构造飞书卡片用的结构化消息。
//
// 为什么必须走 Document：此前 TikTok 只发裸文本，没有【昵称|来源】头，
// 飞书 flattenFeishuSegments 取不到 title，来源一路回落到兜底 "Pocket48"
// —— 卡片顶栏和底栏圆点左侧都显示成了 pocket 48。
// Document 的 Source/Author 是结构化字段，不依赖正文标点猜测。
func buildTiktokDocument(v tiktokmonitor.Video) message.Document {
	// ★ 顶栏团名统一大写（2026-10-05 用户要求）。
	//
	// 根因：sidecar 的 authorName 优先取 uniqueId —— 那是 @handle，
	// 小写（hearts2hearts）；而抖音/B站/微博那边取的是显示昵称，
	// 是 Hearts2Hearts。同一个人于是两种写法，看起来像两个账号。
	//
	// 去重侧早就有 NormalizeGroup（ToLower）所以**比对一直没问题**，
	// 差的是给人看的字。这里用 CanonicalGroupName 做显示层归一。
	sender := dedupe.CanonicalGroupName(v.AuthorName)
	if sender == "" {
		sender = "TikTok 用户"
	}
	doc := message.Document{
		Source: "TikTok",
		Kind:   "video",
		Title:  sender,
		Author: sender,
		Body:   buildTiktokBody(v),
		Link:   tiktokmonitor.Link(v),
	}
	if v.CreateTime > 0 {
		doc.CreatedAt = time.Unix(v.CreateTime, 0)
	}
	return doc
}

// formatDuration 秒 -> 「M:SS」。
func formatDuration(total int) string {
	if total <= 0 {
		return ""
	}
	m := total / 60
	s := total % 60
	if m >= 60 {
		return fmt.Sprintf("%d:%02d:%02d", m/60, m%60, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

// formatCount 大数字加千分位。
func formatCount(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return strings.Join(append([]string{s}, parts...), ",")
}

// targetIDs 取该账号的投递目标。
//
// 优先按 TikTok 自己的订阅配置（TIKTOK_SUBSCRIPTIONS），
// 没有则回落到抖音同名账号的目标 —— 用户是「同一个人两边都发」，
// 这样默认就能推到已经在收抖音内容的地方，不需要重新配一遍。
func (s *tiktokSender) targetIDs(account string) []string {
	if s.cfg == nil {
		return nil
	}
	if s.cfg.TiktokSubscriptions != nil {
		if item := s.cfg.TiktokSubscriptions[account]; item != nil && len(item.TargetIDs) > 0 {
			return item.TargetIDs
		}
		// ★ 必须做大小写不敏感的兜底（2026-10-05 修）。
		//
		// map 查找对大小写敏感，而 account 传的是**作品作者昵称**
		//（collector.py 改成 nickname 优先 + CanonicalGroupName 显示归一之后
		//是"Hearts2Hearts"），配置里的 key 是用户名 "hearts2hearts"
		// => 直接查表必然miss => 「TikTok 未配置任何投递目标」，
		// 所有新作品都发不出去（不推进游标，下轮继续失败）。
		//
		// 症状：20:41 起连续两条发送失败，日志明确写了「不推进游标」。
		for key, item := range s.cfg.TiktokSubscriptions {
			if item == nil || len(item.TargetIDs) == 0 {
				continue
			}
			if strings.EqualFold(key, account) {
				return item.TargetIDs
			}
		}
	}
	// 回落到抖音同名账号
	for _, byAccount := range s.cfg.DouyinSubscriptions {
		for name, item := range byAccount {
			if item == nil {
				continue
			}
			if strings.EqualFold(name, account) && len(item.TargetIDs) > 0 {
				return item.TargetIDs
			}
		}
	}
	return nil
}

// sendToTargets 与抖音侧同一套投递逻辑（含 QQ 数字 ID 特判）。
func (s *tiktokSender) sendToTargets(targetIDs []string, content interface{}) {
	if s == nil || s.outbound == nil || s.cfg == nil {
		return
	}
	resolve := func(id string) outbound.Target {
		t := s.cfg.ResolveTarget(id)
		if t.Address == "" {
			return outbound.Target{}
		}
		kind := outbound.GroupChat
		if t.Kind == "private" {
			kind = outbound.PrivateChat
		}
		if t.Platform == "qq" {
			if nid, err := strconv.ParseInt(t.Address, 10, 64); err == nil {
				return outbound.Target{Platform: "qq", Kind: kind, ID: nid, Address: t.Address}
			}
		}
		return outbound.Target{Platform: t.Platform, Kind: kind, Address: t.Address}
	}
	outbound.SendToTargetIDs(s.outbound, resolve, targetIDs, content)
}

// storageRootOf 已被移到 crossdedupe.go，签名改为直接收 configPath。
//
// 原来这里有一份同名实现（收 *config.Config，注释还写着错的前提
// 「config.json 位于 <storage>/config.json」），与 crossTitleIndex 那份各写各的。
// 两份都少跳了一层目录，导致去重索引落到项目根、state.json 也落到项目根。
// 现在只保留 crossdedupe.go 里那一份，任何地方都不许再自己推导。
