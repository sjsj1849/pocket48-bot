package logic

import (
	"strings"
	"sync"
	"testing"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/outbound"
)

// recordingSender 记录发出去的消息，用来断言「回复真的落到飞书而不是 QQ」。
type recordingSender struct {
	mu   sync.Mutex
	sent []string
}

func (r *recordingSender) Send(target outbound.Target, content interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch c := content.(type) {
	case message.Segment:
		r.sent = append(r.sent, c.Data["text"])
	case []message.Segment:
		for _, seg := range c {
			r.sent = append(r.sent, seg.Data["text"])
		}
	case message.Document:
		// 卡片消息：body 才是正文。
		r.sent = append(r.sent, c.Title+"\n"+c.Body)
	case string:
		r.sent = append(r.sent, c)
	}
}

func (r *recordingSender) QueueDepth() int { return 0 }

func (r *recordingSender) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.sent, "\n---\n")
}

const testOpenID = "ou_62e0b39938b2e1309ad569bcf5b092ca"

// 端到端（离线可跑的那一段）：飞书私聊里敲 "bot help"，
// 必须走命令分支并把帮助文本回发到飞书，而不是被"没链接就 return"吞掉。
func TestFeishuIncomingRoutesCommandToFeishu(t *testing.T) {
	sender := &recordingSender{}
	b := &Bot{
		cfg: &config.Config{
			FeishuPrivateRoutes: "2063428750=" + testOpenID,
			SuperAdmin:          2063428750,
		},
		outbound: sender,
	}
	b.handleFeishuIncoming(outbound.FeishuIncomingEvent{
		ChatType: "p2p",
		ChatID:   "oc_chat",
		SenderID: testOpenID,
		Text:     "bot help",
	})
	out := sender.dump()
	if out == "" {
		t.Fatal("飞书私聊发 bot help 一条都没回 —— 命令分支没接上")
	}
	if !strings.Contains(out, "Pocket48") && !strings.Contains(out, "命令") {
		t.Errorf("回复不像帮助文本：%.200s", out)
	}
}

// 命令执行完必须把 reply 改道清掉，否则之后 QQ 的私聊回复会全部跑到飞书。
func TestFeishuCommandClearsReplyOverride(t *testing.T) {
	sender := &recordingSender{}
	b := &Bot{
		cfg: &config.Config{
			FeishuPrivateRoutes: "2063428750=" + testOpenID,
			SuperAdmin:          2063428750,
		},
		outbound: sender,
	}
	b.handleFeishuIncoming(outbound.FeishuIncomingEvent{
		ChatType: "p2p", ChatID: "oc_chat", SenderID: testOpenID, Text: "bot help",
	})
	if ctx := b.currentFeishuReply(); ctx != nil {
		t.Fatal("命令结束后 reply 改道没有清除 —— 会污染后续 QQ 回复")
	}
}

// 非管理员必须明确拒绝，而不是静默无响应。
func TestFeishuCommandRejectsNonAdmin(t *testing.T) {
	sender := &recordingSender{}
	b := &Bot{
		cfg: &config.Config{
			FeishuPrivateRoutes: "2063428750=" + testOpenID,
			SuperAdmin:          2063428750,
		},
		outbound: sender,
	}
	b.handleFeishuIncoming(outbound.FeishuIncomingEvent{
		ChatType: "p2p", ChatID: "oc_chat", SenderID: "ou_stranger", Text: "bot help",
	})
	if !strings.Contains(sender.dump(), "管理员") {
		t.Errorf("陌生 open_id 应收到拒绝提示，实际：%.200s", sender.dump())
	}
}

// 群里没 @ 机器人就当没看见（不得泄露命令能力）。
func TestFeishuGroupRequiresMention(t *testing.T) {
	sender := &recordingSender{}
	b := &Bot{
		cfg: &config.Config{
			FeishuPrivateRoutes: "2063428750=" + testOpenID,
			SuperAdmin:          2063428750,
		},
		outbound: sender,
	}
	b.handleFeishuIncoming(outbound.FeishuIncomingEvent{
		ChatType: "group", ChatID: "oc_group", SenderID: testOpenID,
		Text: "bot help", MentionBot: false,
	})
	if sender.dump() != "" {
		t.Errorf("群里未 @ 机器人却回了信：%.200s", sender.dump())
	}
}

// 带链接的消息仍然走提取链路，不能被命令分支抢走。
func TestFeishuLinkStillGoesToExtract(t *testing.T) {
	sender := &recordingSender{}
	b := &Bot{
		cfg: &config.Config{
			FeishuPrivateRoutes: "2063428750=" + testOpenID,
			SuperAdmin:          2063428750,
		},
		outbound: sender,
	}
	b.handleFeishuIncoming(outbound.FeishuIncomingEvent{
		ChatType: "p2p", ChatID: "oc_chat", SenderID: testOpenID,
		Text: "https://weibo.com/6694957538/5351887681618773",
	})
	// 提取是异步的，这里只断言「没有立刻回命令帮助」——
	// 命中命令分支会同步回一段文本。
	if strings.Contains(sender.dump(), "命令") {
		t.Errorf("纯链接被当成命令处理了：%.200s", sender.dump())
	}
}
