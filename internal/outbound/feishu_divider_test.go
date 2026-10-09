package outbound

import (
	"regexp"
	"strings"
	"testing"
)

func TestIsDividerLine(t *testing.T) {
	cases := []struct {
		line string
		want bool
		desc string
	}{
		{"----", true, "ASCII 短横线"},
		{"--------", true, "更长的短横线"},
		{"——", true, "中文破折号 2 个即成分隔线（长横线阈值=2）"},
		{"———", true, "中文破折号 3 个"},
		{"━━━━", true, "Unicode 制表横线"},
		{"———", true, "混合横线"},
		{"___", true, "下划线"},
		{"===", true, "等号"},
		{"  ----  ", true, "带前后空白"},
		{"-", false, "单个减号"},
		{"--", false, "两个 ASCII 减号未达 3 个"},
		{"", false, "空行"},
		{"   ", false, "纯空白"},
		{"标题 ---- 后面的正文", false, "正文含横杠但不是纯横杠行"},
		{"hello", false, "普通文本"},
		{"-a-b-c-", false, "横杠被字母打断"},
		{"时长 1:56", false, "时长行"},
		{"―", false, "单个长横线不足 2 个"},
		{"—", false, "单个中文破折号不足 2 个"},
		{"——-——", true, "破折号与减号混合也算长横线行"},
	}
	for _, tc := range cases {
		if got := isDividerLine(tc.line); got != tc.want {
			t.Errorf("isDividerLine(%q) = %v, want %v (%s)",
				tc.line, got, tc.want, tc.desc)
		}
	}
}

// 复现用户 2026-10-04 报的线上场景：去掉顶部方框后，
// 标题与「时长」两行中间夹着一行横杠。
func TestRenderBodyWithDividersRealWorld(t *testing.T) {
	text := "标题行\n----\n时长 1:56"
	els := renderBodyWithDividers(text)
	if len(els) != 3 {
		t.Fatalf("期望 3 个元素（文本/hr/时长），实际 %d: %+v", len(els), els)
	}
	if els[0]["tag"] != "div" {
		t.Errorf("第 1 个应是 div，实际 %v", els[0]["tag"])
	}
	if els[1]["tag"] != "hr" {
		t.Errorf("第 2 个应是 hr，实际 %v", els[1]["tag"])
	}
	if els[2]["tag"] != "div" {
		t.Errorf("第 3 个应是 div，实际 %v", els[2]["tag"])
	}
	if got := els[0]["text"].(map[string]string)["content"]; got != "标题行" {
		t.Errorf("第 1 块内容应只含标题行，实际 %q", got)
	}
	if got := els[2]["text"].(map[string]string)["content"]; got != "时长 1:56" {
		t.Errorf("第 3 块内容应只含时长行，实际 %q", got)
	}
}

func TestRenderBodyWithDividersNoDivider(t *testing.T) {
	text := "第一行\n第二行"
	els := renderBodyWithDividers(text)
	if len(els) != 1 {
		t.Fatalf("无横杠时应合并为 1 个 div，实际 %d: %+v", len(els), els)
	}
	if got := els[0]["text"].(map[string]string)["content"]; got != "第一行\n\n第二行" {
		t.Errorf("内容应与原 toFeishuLines 规则一致（段间空行），实际 %q", got)
	}
}

// 连续多条横杠只渲染一条，避免双重分隔线。
func TestRenderBodyWithDividersCollapsesConsecutive(t *testing.T) {
	text := "A\n----\n----\n----\nB"
	els := renderBodyWithDividers(text)
	hrCount := 0
	for _, e := range els {
		if e["tag"] == "hr" {
			hrCount++
		}
	}
	if hrCount != 1 {
		t.Fatalf("连续 3 条横杠应只渲染 1 条 hr，实际 %d 条: %+v", hrCount, els)
	}
}

func TestRenderBodyWithDividersEmpty(t *testing.T) {
	if els := renderBodyWithDividers(""); els != nil {
		t.Errorf("空文本应返回 nil，实际 %+v", els)
	}
	if els := renderBodyWithDividers("   \n  \n"); els != nil {
		t.Errorf("纯空白应返回 nil，实际 %+v", els)
	}
}

// 横杠在首尾时也不应产生悬空元素。
func TestRenderBodyWithDividersAtEdges(t *testing.T) {
	els := renderBodyWithDividers("----\n正文\n----")
	if len(els) != 3 {
		t.Fatalf("首尾横杠应各渲染一条 hr + 中间 div = 3，实际 %d: %+v", len(els), els)
	}
	if els[0]["tag"] != "hr" || els[1]["tag"] != "div" || els[2]["tag"] != "hr" {
		t.Errorf("元素顺序应为 hr/div/hr，实际 %v/%v/%v",
			els[0]["tag"], els[1]["tag"], els[2]["tag"])
	}
}

var _ = regexp.MustCompile
var _ = strings.TrimSpace
