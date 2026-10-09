package extract

import (
	"strings"
	"testing"
)

// TestZZTiktokDescClean 用**线上真实抓到**的 og:description 验证清洗。
//
// 这段文本是 2026-10-04 实测产物，原样粘在这里 —— 不要凭想象改：
// 清洗规则必须对着真实数据写，否则线上会静默退化成「什么都没清掉」。
const zzRealOgDesc = "171.4K 获赞，1291 评论。来自 Hearts2Hearts (@hearts2hearts) 的 TikTok 视频：" +
	"“ダンスしよう！ @あの  #Hearts2Hearts #하츠투하츠 #ICONICHEART #Hearts2Hearts_ICONICHEART”。" +
	"ICONIC HEART - Hearts2Hearts。"

func TestZZTiktokDescCleanReal(t *testing.T) {
	got := cleanTiktokDesc(zzRealOgDesc)

	// 必须去掉的东西
	for _, bad := range []string{"171.4K 获赞", "1291 评论", "来自 Hearts2Hearts", "的 TikTok 视频"} {
		if strings.Contains(got, bad) {
			t.Errorf("清洗后仍含统计/宣传前缀 %q：\n得到 %q", bad, got)
		}
	}

	// 必须保留的东西 —— 这是用户真正写的文案
	for _, want := range []string{"ダンスしよう", "#Hearts2Hearts", "#ICONICHEART"} {
		if !strings.Contains(got, want) {
			t.Errorf("清洗后丢了正文 %q：\n得到 %q", want, got)
		}
	}
}

func TestZZTiktokDescCleanKeepsPlainText(t *testing.T) {
	// 短文案、没有统计前缀时不能被误伤
	const plain = "내 봥 ㅋㅋ ♡ #Hearts2Hearts #하츠투하츠 #H2H #ICONICHEART"
	if got := cleanTiktokDesc(plain); got != plain {
		t.Errorf("普通文案被改动了：\n得到 %q\n期望 %q", got, plain)
	}
}

func TestZZTiktokDescCleanEmpty(t *testing.T) {
	if got := cleanTiktokDesc(""); got != "" {
		t.Errorf("空输入应返回空串，得到 %q", got)
	}
	if got := cleanTiktokDesc("   \n  "); got != "" {
		t.Errorf("纯空白应返回空串，得到 %q", got)
	}
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
