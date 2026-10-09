package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/pocket48"
)

// TestParseEmbeddedReplyDetailReturnsQuotedName 覆盖线上问题：包间里成员互相
// 回复时，Document 的 Author 之前被写死成 room.OwnerName（胡晓慧），把
// 「金兔牙子」的话标成了房间主人发的，QQ 与飞书表现还不一致。
// replyName（被回复者昵称）必须单独取出，才能正确署名与对齐引用块。
func TestParseEmbeddedReplyDetailReturnsQuotedName(t *testing.T) {
	// 真实报文（2026-10-02 21:51:37）：金兔牙子回复哼唧小虎
	body := `{"messageType":"REPLY","replyInfo":{"replyName":"哼唧小虎","replyText":"没有没有 穿上去一定美若天仙","text":"那不穿呢"}}`

	quoted, answer, replyName, ok := parseEmbeddedReplyDetail(body)
	if !ok {
		t.Fatal("应解析成功")
	}
	if replyName != "哼唧小虎" {
		t.Errorf("被回复者昵称 = %q，应为哼唧小虎", replyName)
	}
	if answer != "那不穿呢" {
		t.Errorf("回复正文 = %q，应为那不穿呢", answer)
	}
	if !strings.Contains(quoted, "哼唧小虎") || !strings.Contains(quoted, "没有没有") {
		t.Errorf("被回复内容应含昵称与原文，实际 %q", quoted)
	}
}

// TestParseEmbeddedReplyDetailNonReply 非 REPLY 帧不应被误解析。
func TestParseEmbeddedReplyDetailNonReply(t *testing.T) {
	for _, body := range []string{
		`{"messageType":"TEXT","content":"普通消息"}`,
		`not json`,
		``,
	} {
		if _, _, _, ok := parseEmbeddedReplyDetail(body); ok {
			t.Errorf("不应解析成功：%q", body)
		}
	}
}

// TestPocket48SourceIDIsStableAndContentAddressed 挂载依赖两侧算出同一个键：
// 原始消息与引用它的回复都必须落在同一SourceID 上。
func TestPocket48SourceIDIsStableAndContentAddressed(t *testing.T) {
	room := &pocket48.RoomInfo{ServerID: 1181227, ChannelID: 1279287}
	other := &pocket48.RoomInfo{ServerID: 999, ChannelID: 888}

	base := pocket48SourceID(room, "哼唧小虎", "没有没有 穿上去一定美若天仙")
	if base == "" || !strings.HasPrefix(base, "p48:") {
		t.Fatalf("SourceID 格式不对：%q", base)
	}
	// 同内容同房间 → 同键
	if again := pocket48SourceID(room, "哼唧小虎", "没有没有 穿上去一定美若天仙"); again != base {
		t.Errorf("同输入应得同键：%q vs %q", base, again)
	}
	// 空白差异不应改变键（回复帧与原文可能有换行差异）
	if spaced := pocket48SourceID(room, "哼唧小虎", "  没有没有   穿上去一定美若天仙  "); spaced != base {
		t.Errorf("空白差异不应改变键：%q vs %q", base, spaced)
	}
	// 不同说话人 / 不同内容 / 不同房间 → 不同键
	if other1 := pocket48SourceID(room, "金兔牙子", "那不穿呢"); other1 == base {
		t.Error("不同消息不应得到相同键")
	}
	if other2 := pocket48SourceID(other, "哼唧小虎", "没有没有 穿上去一定美若天仙"); other2 == base {
		t.Error("不同房间不应得到相同键")
	}
}

// TestPocket48SourceIDNilRoom nil room 不能 panic。
func TestPocket48SourceIDNilRoom(t *testing.T) {
	if got := pocket48SourceID(nil, "a", "b"); got == "" {
		t.Error("nil room 也应返回可用键")
	}
}
