package logic

import (
	"strings"
	"testing"
)

// ★★ 回归（2026-10-05 12:33 用户报）：抖音私信回复在**飞书**里
// 下面那条回复没有「昵称：」前缀，只有裸文本。
//
// 现象很怪：QQ 侧正常、飞书侧缺前缀。根因是飞书卡片有两条独立渲染路径：
//
//	引用块 -> turnParts(quote.Author, ...)  -> 自动加「作者：」
//	正文   -> content.text（就是 doc.Body） -> 裸文本，不加前缀
//
// 而 doc.Body 传的是未格式化的 replyBody。

// ensureDouyinSenderPrefix 本身的行为。
func TestZZEnsureDouyinSenderPrefix(t *testing.T) {
	cases := []struct {
		name, body, sender, want string
	}{
		{"裸文本要补前缀", "那得最最最最开始的时候了吧", "葡萄吞十七(唐欣怡)",
			"葡萄吞十七(唐欣怡)：那得最最最最开始的时候了吧"},
		{"已有全名前缀不重复", "葡萄吞十七(唐欣怡)：xxx", "葡萄吞十七(唐欣怡)", "葡萄吞十七(唐欣怡)：xxx"},
		{"已有半角冒号不重复", "葡萄吞十七:xxx", "葡萄吞十七(唐欣怡)", "葡萄吞十七:xxx"},
		// 昵称与备注不一致时（formatDouyinNamePair 会产出「昵称(备注)」），
		// 正文里可能已带「备注名：」，也不该再加一层。
		{"已有其他前缀不重复", "唐欣怡：xxx", "葡萄吞十七(唐欣怡)", "唐欣怡：xxx"},
		{"空正文原样", "", "葡萄吞十七", ""},
		{"空发送名不加", "xxx", "", "xxx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureDouyinSenderPrefix(tc.body, tc.sender)
			if got != tc.want {
				t.Errorf("ensureDouyinSenderPrefix(%q, %q) = %q，期望 %q",
					tc.body, tc.sender, got, tc.want)
			}
		})
	}
}

// ★ 端到端：飞书 Document 的正文必须带「昵称：」，引用块由飞书自己渲染。
func TestZZFeishuDocBodyHasSenderPrefix(t *testing.T) {
	in := douyinPrivateDocInput{
		kind:       "private_incoming",
		boxName:    "葡萄吞十七",
		lineName:   "葡萄吞十七(唐欣怡)",
		body:       "那得最最最最开始的时候了吧",
		quotedName: "我",
		quotedText: "真的存在这样的时期吗",
	}
	doc := buildDouyinPrivateDocument(in)

	t.Logf("Author = %q", doc.Author)
	t.Logf("Body   = %q", doc.Body)
	t.Logf("Quote  = %+v", doc.Quote)

	if doc.Author != "葡萄吞十七(唐欣怡)" {
		t.Errorf("顶栏应是发送者全名，实际 %q", doc.Author)
	}
	if doc.Quote == nil || doc.Quote.Text == "" {
		t.Fatalf("引用块不该为空（飞书自己给它加『我：』）")
	}
	// ★ 正文必须有前缀
	if !strings.HasPrefix(doc.Body, "葡萄吞十七(唐欣怡)：") {
		t.Errorf("★ 正文缺「昵称：」前缀，实际 %q", doc.Body)
	}
}

// 没有引用时，正文不该被加前缀（顶栏已经写了发送者）。
func TestZZFeishuDocBodyNoQuoteNoPrefix(t *testing.T) {
	in := douyinPrivateDocInput{
		kind:       "private_incoming",
		boxName:    "葡萄吞十七",
		lineName:   "葡萄吞十七(唐欣怡)",
		body:       "今天的照片",
		quotedName: "",
		quotedText: "",
	}
	doc := buildDouyinPrivateDocument(in)
	t.Logf("无引用时 Body = %q", doc.Body)
	if doc.Body != "今天的照片" {
		t.Errorf("无引用时正文应保持原样，实际 %q", doc.Body)
	}
}

// 占位引用（[回复] / sec_uid）不算真引用，不该触发前缀。
func TestZZFeishuDocPlaceholderQuoteNoPrefix(t *testing.T) {
	for _, q := range []string{"[回复]", "MS4wLjABAAAAxxxxx", ""} {
		in := douyinPrivateDocInput{
			kind:       "private_incoming",
			lineName:   "葡萄吞十七(唐欣怡)",
			body:       "回复内容",
			quotedName: "我",
			quotedText: q,
		}
		doc := buildDouyinPrivateDocument(in)
		if strings.Contains(doc.Body, "：") {
			t.Errorf("占位引用 %q 不该加前缀，实际 %q", q, doc.Body)
		}
	}
}
