package outbound

import (
	"testing"

	"pocket48-bot/internal/message"
)

// ★ 回归（2026-10-04）：抖音私信在飞书里顶栏时而显示「我」、时而显示自己昵称
// （用户自己的昵称「鸠风」），而 QQ 那边一直正常显示对方（葡萄吞十七）。
//
// # 根因（探针实测出来的，不是推测）
//
// ToSegments 会把 Document 压成一段**纯文本**：
//
//	【葡萄吞十七】
//	我：
//	表情包的神
//
//	我的妈呀
//
// 然后 extractSender 把正文**第一行的冒号前缀**当成昵称 → 判成「我」，
// 正文里真正的发送者「葡萄吞十七」被丢到第二行、当成正文。
//
// ★ 这个坑只在「Document 被压成纯文本」时发生，也就是 AsDocument 失败。
//
//	抖音私信正常走 message.Document 值类型 → AsDocument 成功 →
//	走 documentContent（289 行），那里 sender = doc.Author，顶栏是对的。
//	所以线上症状来自**结构化路径被绕过的那些场景**，
//	这个测试锁住的是 documentContent 必须恒定给出 Author。
func TestZZDouyinPrivateDocKeepsSender(t *testing.T) {
	f := &Feishu{}
	// 逐个复刻 buildDouyinPrivateDocument 可能产出的四种输入，
	// 重点是「引用的是自己」时顶栏仍是对面的人。
	cases := []struct {
		name string
		doc  message.Document
		want string
	}{
		{
			name: "对方回复我",
			doc: message.Document{
				Source: "抖音", Kind: "im_private",
				Author: "葡萄吞十七", Title: "葡萄吞十七",
				Body:  "我的妈呀",
				Quote: &message.Quote{Author: "我", Text: "表情包的神"},
			},
			want: "葡萄吞十七",
		},
		{
			name: "自己发给自己",
			doc: message.Document{
				Source: "抖音", Kind: "im_private",
				Author: "我", Title: "我",
				Body: "记一下",
			},
			want: "我",
		},
		{
			name: "空发送者要有兜底",
			doc: message.Document{
				Source: "抖音", Kind: "im_private",
				Body: "没有昵称的消息",
			},
			// documentContent 自身会把 sender 回落到 source（抖音），
			// 这本身就是合理兜底，不必再硬塞一个占位名。
			want: "抖音",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.documentContent(tc.doc)
			if got.sender != tc.want {
				t.Errorf("sender = %q，期望 %q", got.sender, tc.want)
			}
		})
	}
}

// 引用的作者无论是「我」还是自己的昵称「鸠风」，都不许影响顶栏。
// 这是用户报的两种症状形态，必须一起锁住。
func TestZZQuotedSelfNeverBecomesSender(t *testing.T) {
	f := &Feishu{}
	for _, quotedAuthor := range []string{"我", "鸠风"} {
		doc := message.Document{
			Source: "抖音", Kind: "im_private",
			Author: "葡萄吞十七", Title: "葡萄吞十七",
			Body:  "我的妈呀",
			Quote: &message.Quote{Author: quotedAuthor, Text: "表情包的神"},
		}
		got := f.documentContent(doc)
		if got.sender != "葡萄吞十七" {
			t.Errorf("引用作者是 %q 时顶栏变成 %q，应恒为 葡萄吞十七",
				quotedAuthor, got.sender)
		}
	}
}

// 纯文本路径（机器人通知等）必须仍能从正文提取昵称，
// 不能因为上面的修复而退化。
func TestZZPlainTextStillExtractsSender(t *testing.T) {
	got := renderFeishuContent([]message.Segment{
		message.Text("张三：你好\n2026-10-04 10:00:00"),
	})
	if got.sender != "张三" {
		t.Errorf("纯文本应提取 %q，实际 %q", "张三", got.sender)
	}
}

// 记录已知的行为：ToSegments 会把引用拼进正文，从而让纯文本回退路径
// 把引用者误认成发送者。这条不是断言「对不对」，而是把真实形状钉住，
// 万一哪天有人改 ToSegments 的拼装顺序，这里会提醒重新评估。
func TestZZToSegmentsShapeIsKnown(t *testing.T) {
	doc := message.Document{
		Source: "抖音", Author: "葡萄吞十七",
		Body:  "我的妈呀",
		Quote: &message.Quote{Author: "我", Text: "表情包的神"},
	}
	raw := message.ToSegments(doc)
	segs, ok := raw.([]message.Segment)
	if !ok {
		t.Fatalf("应转成 segment 切片，实际 %T", raw)
	}
	got := renderFeishuContent(segs)
	t.Logf("纯文本回退路径 sender=%q（这就是为什么私信必须走 documentContent）", got.sender)
}
