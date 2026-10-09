package outbound

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
)

// TestFeishuRendersNapcatAtSegment 验证飞书能正确渲染微博等平台直接投递的
// napcat 原始段（@全体成员 + 【昵称|来源】 + 正文）。
//
// 背景：微博/抖音/X 直接传napcat 段（不经 Document 转换）。历史上两个 bug：
//  1. renderFeishuContent 的 switch 没有 "at" case，段被静默丢弃
//  2. 即使渲染出 <at id=all></at>，其中的冒号会被 extractSender 误当作
//     "昵称：正文" 分隔符，把标记塞进卡片头部，并连带导致首行标题
//     【昵称|来源】解析失败、来源回落到 "Pocket48"
func TestFeishuRendersNapcatAtSegment(t *testing.T) {
	segments := []napcat.MessageSegment{
		napcat.AtSegment("all"),
		napcat.TextSegment("\n"),
		napcat.TextSegment("【未 Injuries|微博】\n"),
		napcat.TextSegment("正文内容\n\n微博链接：https://weibo.com/1/ABC\n"),
		napcat.TextSegment("\n2026-10-02 20:15:00"),
	}

	content := renderFeishuContent(segments)

	if content.source != "微博" {
		t.Errorf("来源应为 微博，实际 %q", content.source)
	}
	if content.sender != "未 Injuries" {
		t.Errorf("发送者应为 未 Injuries，实际 %q", content.sender)
	}
	if content.sender == "Pocket48" || content.source == "Pocket48" {
		t.Errorf("解析回落到兜底名：sender=%q source=%q", content.sender, content.source)
	}
	if strings.Contains(content.text, "<at") {
		t.Errorf("正文残留 at 标记：%q", content.text)
	}
	if strings.Contains(content.text, "【") {
		t.Errorf("标题未从正文剥离：%q", content.text)
	}
	if !strings.Contains(content.text, "正文内容") {
		t.Errorf("正文丢失：%q", content.text)
	}
	if content.timestamp != "2026-10-02 20:15:00" {
		t.Errorf("时间戳应被提取到 footer，实际 %q", content.timestamp)
	}
	t.Logf("sender=%q source=%q timestamp=%q", content.sender, content.source, content.timestamp)
	t.Logf("text=%q", content.text)
}

// TestFeishuAtSegmentSpecificQQ 指定 @某人 时不应把 at 标记当成昵称。
func TestFeishuAtSegmentSpecificQQ(t *testing.T) {
	content := renderFeishuContent([]napcat.MessageSegment{
		napcat.AtSegment("123456"),
		napcat.TextSegment("正文"),
	})
	if strings.Contains(content.sender, "<at") {
		t.Errorf("发送者不应含 at 标记：%q", content.sender)
	}
	if !strings.Contains(content.text, "正文") {
		t.Errorf("正文应保留，实际 %q", content.text)
	}
}

// TestFeishuNicknameColonUnaffected 普通「昵称：正文」不受 at 修复影响。
func TestFeishuNicknameColonUnaffected(t *testing.T) {
	content := renderFeishuContent([]message.Segment{
		message.Text("小包：你好呀"),
	})
	if content.sender != "小包" {
		t.Errorf("发送者应为 小包，实际 %q", content.sender)
	}
	if !strings.Contains(content.text, "你好呀") {
		t.Errorf("正文应保留，实际 %q", content.text)
	}
}