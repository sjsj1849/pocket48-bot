package logic

import (
	"strings"
	"testing"
)

// ★ 2026-10-09（用户要求）：正文不再写「为什么没有视频」。
//
//	原断言要求被去重时必须写明原因，现在改为断言**这些说明都不该出现**
//	—— 来源与内容类型改到飞书卡片底栏，视频占位说明一律去掉。
func TestInstagramCaptionOmitsVideoPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name       string
		skipVideo  bool
		wantAbsent []string
	}{
		{"正常发视频", false, []string{"视频单独发送", "请打开原帖观看"}},
		{"去重后不发视频", true, []string{"视频单独发送", "不在其它平台", "请打开原帖观看"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := igReelEvent("ig-fmt-"+tc.name, "翻跳")
			body := instagramDocumentText(e)
			for _, s := range tc.wantAbsent {
				if strings.Contains(body, s) {
					t.Fatalf("正文不该再出现 %q，实际=%q", s, body)
				}
			}
			if strings.Contains(body, "[视频]") {
				t.Fatalf("正文不该再有视频占位标记，实际=%q", body)
			}
		})
	}
}

// 正文也不该再出现 Story / Reels / 帖子 这类来源标注。
func TestInstagramCaptionOmitsKindLabels(t *testing.T) {
	for _, kind := range []string{"story", "reel", "post"} {
		e := igReelEvent("ig-fmt-"+kind, "内容正文")
		e.Kind = kind
		body := instagramDocumentText(e)
		for _, s := range []string{"Story", "Reels", "帖子"} {
			if strings.Contains(body, "\n"+s+"\n") || strings.HasPrefix(body, s) {
				t.Fatalf("kind=%s 时正文不该含来源标注 %q，实际=%q", kind, s, body)
			}
		}
		if !strings.Contains(body, "内容正文") {
			t.Fatalf("kind=%s 时正文必须保留 caption，实际=%q", kind, body)
		}
	}
}

// 来源与类型改到底栏 label，形如「Instagram Reels」。
func TestInstagramKindLabelForFooter(t *testing.T) {
	for kind, want := range map[string]string{
		"story": "Instagram Story",
		"reel":  "Instagram Reels",
		"post":  "Instagram 帖子",
		"":      "Instagram 帖子",
	} {
		if got := "Instagram " + instagramKindLabel(kind); got != want {
			t.Fatalf("kind=%q 底栏应为 %q，实际 %q", kind, want, got)
		}
	}
}

// ★ 名称规范（用户 2026-10-09 要求）：全平台统一大写 Hearts2Hearts，
// 所以 caption 里的 #H2H 要去掉，避免看起来像另一个团。
// ★ 期望值按真实行为写：清掉 H2H 后会TrimRight 掉行尾残留空格。
func TestInstagramCleanCaptionDropsH2H(t *testing.T) {
	cases := []struct{ in, want string }{
		{"gotta catch 'em all!\n\n#Hearts2Hearts #하츠투하츠 #H2H", "gotta catch 'em all!\n\n#Hearts2Hearts #하츠투하츠"},
		{"감정이란 꽃은 짧은 순간 피어나는 걸\n\n#Hearts2Hearts #하츠투하츠 #H2H\n#JIWOO #A_NA", "감정이란 꽃은 짧은 순간 피어나는 걸\n\n#Hearts2Hearts #하츠투하츠\n#JIWOO #A_NA"},
		{"attempt: do not let her know \n\n#Hearts2Hearts #하츠투하츠 #H2H \n#ICONICHEART #Hearts2Hearts_ICONICHEART", "attempt: do not let her know\n\n#Hearts2Hearts #하츠투하츠\n#ICONICHEART #Hearts2Hearts_ICONICHEART"},
		{"#h2h #Hearts2Hearts", "#Hearts2Hearts"},
		{"#H2H", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := instagramCleanCaption(tc.in)
		if got != tc.want {
			t.Fatalf("输入 %q\n期望 %q\n实际 %q", tc.in, tc.want, got)
		}
		if strings.Contains(strings.ToLower(got), "#h2h") {
			t.Fatalf("结果不该残留 H2H: %q", got)
		}
	}
}

// 不含 H2H 的 caption 必须原样保留（不能误删韩文标签或成员标签）。
func TestInstagramCleanCaptionKeepsEverythingElse(t *testing.T) {
	in := "so you coming back or what\n#Hearts2Hearts #하츠투하츠 #JUUN #IAN"
	if got := instagramCleanCaption(in); got != in {
		t.Fatalf("无 H2H 时不该改动：\n期望 %q\n实际 %q", in, got)
	}
}

// ★ caption 靠空行分隔正文与标签，删H2H 时**不能顺手删掉空行**。
//
//	我第一版实现把空行一起吞了，正文与标签会挤成一行。
func TestInstagramCleanCaptionPreservesBlankLines(t *testing.T) {
	in := "第一段\n\n#Hearts2Hearts #H2H"
	got := instagramCleanCaption(in)
	if !strings.Contains(got, "第一段\n\n#Hearts2Hearts") {
		t.Fatalf("正文与标签之间的空行必须保留，实际=%q", got)
	}
}
