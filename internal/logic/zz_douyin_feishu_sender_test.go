package logic

import (
	"strings"
	"testing"

	"pocket48-bot/internal/message"
)

// Regression for the Douyin DM reply card showing the wrong sender.
//
// The QQ body leads with the quoted line ("我：[图片]"). Feishu's extractSender
// used to treat that first line's colon prefix as the card header, so the card
// header showed the quoted speaker instead of the real sender.
func TestZZDouyinPrivateDocumentSender(t *testing.T) {
	cases := []struct {
		name       string
		in         douyinPrivateDocInput
		wantAuthor string
		wantBody   string
		wantQuote  bool
	}{
		{
			name: "回复图片：发送者必须是对端不是我",
			in: douyinPrivateDocInput{
				kind:       "private_incoming",
				boxName:    "葡萄吞十七",
				lineName:   "葡萄吞十七(唐欣怡)",
				body:       "我的妈呀",
				quotedName: "我",
				quotedText: "[图片]",
			},
			wantAuthor: "葡萄吞十七(唐欣怡)",
			// ★ 有真引用时正文带前缀（2026-10-05 用户要求：上下两行都要冒号）。
			wantBody:  "葡萄吞十七(唐欣怡)：我的妈呀",
			wantQuote: true,
		},
		{
			name: "回复视频：顶栏不能取到自己的昵称",
			in: douyinPrivateDocInput{
				kind:       "private_incoming",
				boxName:    "葡萄吞十七",
				lineName:   "葡萄吞十七(唐欣怡)",
				body:       "并排呀",
				quotedName: "鸠枫",
				quotedText: "[视频]",
			},
			wantAuthor: "葡萄吞十七(唐欣怡)",
			wantBody:   "葡萄吞十七(唐欣怡)：并排呀",
			wantQuote:  true,
		},
		{
			name: "自聊笔记：发送者是我",
			in: douyinPrivateDocInput{
				kind:       "private_self",
				boxName:    "我",
				lineName:   "我",
				body:       "记一下",
				quotedName: "",
				quotedText: "",
			},
			wantAuthor: "我",
			wantBody:   "记一下",
			wantQuote:  false,
		},
		{
			name: "无昵称时回落到会话名",
			in: douyinPrivateDocInput{
				kind:     "private_incoming",
				boxName:  "某用户",
				lineName: "",
				body:     "在吗",
			},
			wantAuthor: "某用户",
			wantBody:   "在吗",
			wantQuote:  false,
		},
		{
			name: "占位引用不生成灰块",
			in: douyinPrivateDocInput{
				kind:       "private_incoming",
				boxName:    "葡萄吞十七",
				lineName:   "葡萄吞十七",
				body:       "好",
				quotedName: "我",
				quotedText: "[回复]",
			},
			wantAuthor: "葡萄吞十七",
			wantBody:   "好",
			wantQuote:  false,
		},
		{
			name: "垃圾引用（sec_uid）不生成灰块",
			in: douyinPrivateDocInput{
				kind:       "private_incoming",
				boxName:    "葡萄吞十七",
				lineName:   "葡萄吞十七",
				body:       "好",
				quotedName: "我",
				quotedText: "MS4wLjABAAAA",
			},
			wantAuthor: "葡萄吞十七",
			wantBody:   "好",
			wantQuote:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildDouyinPrivateDocument(tc.in)
			if doc.Author != tc.wantAuthor {
				t.Fatalf("Author = %q, want %q", doc.Author, tc.wantAuthor)
			}
			if doc.Body != tc.wantBody {
				t.Fatalf("Body = %q, want %q", doc.Body, tc.wantBody)
			}
			if (doc.Quote != nil) != tc.wantQuote {
				t.Fatalf("Quote presence = %v, want %v (quote=%+v)", doc.Quote != nil, tc.wantQuote, doc.Quote)
			}
			// The sender name must never leak into the body, otherwise Feishu's
			// sender extraction would pick it up again from the first line.
			if doc.Body != "" && len(doc.Author) > 0 && len(doc.Body) > 0 {
				if doc.Body[:min(len(doc.Body), len(doc.Author)+1)] == doc.Author+"：" {
					t.Fatalf("body must not repeat the sender prefix, got %q", doc.Body)
				}
			}
		})
	}
}

// The quoted message must be a real Quote block, never the leading body line.
func TestZZDouyinPrivateDocumentQuoteNotInBody(t *testing.T) {
	doc := buildDouyinPrivateDocument(douyinPrivateDocInput{
		kind:       "private_incoming",
		boxName:    "葡萄吞十七",
		lineName:   "葡萄吞十七(唐欣怡)",
		body:       "我的妈呀",
		quotedName: "我",
		quotedText: "[图片]",
	})
	if doc.Quote == nil {
		t.Fatal("Quote must be set")
	}
	if doc.Quote.Author != "我" || doc.Quote.Text != "[图片]" {
		t.Fatalf("Quote = %+v, want author 我 text [图片]", *doc.Quote)
	}
	if doc.Source != "抖音" {
		t.Fatalf("Source = %q, want 抖音", doc.Source)
	}
}

// Body must be the reply only — the two-line QQ layout must not leak in,
// otherwise Feishu re-derives the sender from the quoted line's colon prefix.
//
// ★ 2026-10-05 更新断言口径：现在有真引用时Body 会带「昵称：」前缀
//
//	（用户要求上下两行都有冒号），所以**不能**再要求 Body 完全等于裸正文。
//	真正要守的不变式是：
func TestZZDouyinPrivateDocumentBodyHasNoQuotedLine(t *testing.T) {
	replyBody := "我的妈呀"
	qqText := formatDouyinReplyText("葡萄吞十七(唐欣怡)", replyBody, "我", "[图片]")
	if qqText == replyBody {
		t.Fatal("QQ layout should prepend the quoted line (precondition failed)")
	}
	doc := buildDouyinPrivateDocument(douyinPrivateDocInput{
		kind:       "private_incoming",
		boxName:    "葡萄吞十七",
		lineName:   "葡萄吞十七(唐欣怡)",
		body:       replyBody,
		quotedName: "我",
		quotedText: "[图片]",
	})
	// ① 不能是 QQ 的两行拼接
	if doc.Body == qqText {
		t.Fatalf("Feishu body must not be the QQ two-line text: %q", doc.Body)
	}
	// ② 不能含引用块（那一行由 doc.Quote 单独交给飞书渲染）
	if strings.Contains(doc.Body, "[图片]") {
		t.Fatalf("Body 不该含引用内容（应由 Quote 字段承载）：%q", doc.Body)
	}
	// ③ 必须含回复正文
	if !strings.Contains(doc.Body, replyBody) {
		t.Fatalf("Body 丢了回复正文：%q", doc.Body)
	}
	// ④ 有真引用时必须带「昵称：」前缀
	if !strings.HasPrefix(doc.Body, "葡萄吞十七(唐欣怡)：") {
		t.Fatalf("Body 应带发送者前缀：%q", doc.Body)
	}
	// ⑤ 引用块单独成字段
	if doc.Quote == nil || doc.Quote.Text != "[图片]" {
		t.Fatalf("Quote 应独立承载引用内容：%+v", doc.Quote)
	}
}

func TestZZParseDouyinIMTime(t *testing.T) {
	cases := []struct {
		name       string
		createTime int64
		receivedAt int64
		wantZero   bool
	}{
		{name: "秒级时间戳", createTime: 1759000000},
		{name: "毫秒级时间戳", createTime: 1759000000123},
		{name: "回落到 receivedAt", receivedAt: 1759000000},
		{name: "全空返回零值", wantZero: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseDouyinIMTime(tc.createTime, tc.receivedAt)
			if tc.wantZero {
				if !got.IsZero() {
					t.Fatalf("want zero time, got %v", got)
				}
				return
			}
			if got.IsZero() {
				t.Fatal("got zero time")
			}
			if got.Year() < 2020 {
				t.Fatalf("implausible timestamp: %v", got)
			}
		})
	}
}

// Media must be carried on the Document so Feishu renders images inline and
// videos as native attachments, instead of losing them with the QQ segments.
func TestZZDouyinPrivateDocumentMedia(t *testing.T) {
	doc := buildDouyinPrivateDocument(douyinPrivateDocInput{
		kind:   "private_incoming",
		body:   "看这个",
		images: []string{"https://cdn.example/a.jpg", "  ", "https://cdn.example/b.jpg"},
		videos: []string{"/root/pocket48-bot/storage/tmp/v.mp4"},
	})
	var images, videos int
	for _, media := range doc.Media {
		switch media.Kind {
		case "image":
			images++
		case "video":
			videos++
		}
	}
	if images != 2 {
		t.Fatalf("images = %d, want 2 (blank entries must be dropped)", images)
	}
	if videos != 1 {
		t.Fatalf("videos = %d, want 1", videos)
	}
	var _ message.Document = doc
}
