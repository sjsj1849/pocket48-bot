package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/outbound"
)

// ★★ 回归（2026-10-04 线上实测）：飞书私聊回信发不出去。
//
// 症状：接收与解析全正常 —— 日志有 `[Feishu-Extract] 已处理 ... 媒体=1`，
// 但出站每一条都400：
//     {"code":99992361,"msg":"open_id cross app"}
//     target=oc_317b3b325e1dec10b4bc4f5451a0295f
//
// 根因：outbound/feishu.go 按 target.Kind 决定 receive_id_type
// （PrivateChat -> open_id），而我们把 chat_id（oc_）当成了私聊地址。
// 飞书拿 chat_id 去匹配 open_id，必然 cross app。
//
// 飞书 ID 语义：
//   oc_ = chat_id（会话/群）  → 群聊回信用它
//   ou_ = open_id（用户）    → 私聊回信用它

// 私聊必须用 senderId（ou_），绝不能用 chatId（oc_）。
func TestZZFeishuP2PUsesSenderOpenID(t *testing.T) {
	ev := outbound.FeishuIncomingEvent{
		ChatType: "p2p",
		ChatID:   "oc_317b3b325e1dec10b4bc4f5451a0295f", // 会话 ID
		SenderID: "ou_62e0b39938b2e1309ad569bcf5b092ca", // 用户 open_id
		Text:     "https://example.invalid/x",
	}
	got, ok := feishuReplyTarget(ev)
	if !ok {
		t.Fatal("私聊应能算出回信目标")
	}
	if strings.HasPrefix(got.Address, "oc_") {
		t.Fatalf("★ 私聊回信不能用 chat_id(%q)，会被飞书判为 open_id cross app",
			got.Address)
	}
	if got.Address != ev.SenderID {
		t.Errorf("私聊地址 = %q，期望发送者 open_id %q", got.Address, ev.SenderID)
	}
	if got.Kind != outbound.PrivateChat {
		t.Errorf("Kind = %v，期望 PrivateChat（决定 receive_id_type=open_id）", got.Kind)
	}
}

// 群聊必须用 chatId（oc_）—— 群里发信要发到会话，不是某个用户。
func TestZZFeishuGroupUsesChatID(t *testing.T) {
	ev := outbound.FeishuIncomingEvent{
		ChatType: "group",
		ChatID:   "oc_group123",
		SenderID: "ou_somebody", // 群聊里这个无效
		Text:     "@_user_1 https://example.invalid/x",
	}
	got, ok := feishuReplyTarget(ev)
	if !ok {
		t.Fatal("群聊应能算出回信目标")
	}
	if got.Address != ev.ChatID {
		t.Errorf("群聊地址 = %q，期望 chat_id %q", got.Address, ev.ChatID)
	}
	if got.Kind != outbound.GroupChat {
		t.Errorf("Kind = %v，期望 GroupChat", got.Kind)
	}
}

// 私聊缺 senderId 时必须放弃，不能退回用 chat_id —— 那必然失败，
// 静默发一条注定 400 的请求只会污染日志。
func TestZZFeishuP2PWithoutSenderIDAborts(t *testing.T) {
	ev := outbound.FeishuIncomingEvent{
		ChatType: "p2p",
		ChatID:   "oc_only_chat_id",
		SenderID: "",
	}
	if _, ok := feishuReplyTarget(ev); ok {
		t.Error("★ 私聊缺 senderId 时必须放弃，不能退回 chat_id（必然 cross app）")
	}
}

// 地址绝不能落进 Target.ID（int64），那是 QQ 数字 ID 专用字段。
func TestZZFeishuTargetNeverUsesInt64ID(t *testing.T) {
	for _, ev := range []outbound.FeishuIncomingEvent{
		{ChatType: "p2p", ChatID: "oc_c", SenderID: "ou_s"},
		{ChatType: "group", ChatID: "oc_g", SenderID: "ou_s"},
	} {
		got, ok := feishuReplyTarget(ev)
		if !ok {
			t.Fatalf("chatType=%s 应可算出目标", ev.ChatType)
		}
		if got.ID != 0 {
			t.Errorf("chatType=%s 用了 Target.ID(%d)，飞书必须走 Address",
				ev.ChatType, got.ID)
		}
		if got.Address == "" {
			t.Errorf("chatType=%s Address 为空", ev.ChatType)
		}
	}
}
