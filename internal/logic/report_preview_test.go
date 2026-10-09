package logic

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"pocket48-bot/internal/monitor"
)

// TestGenerateDailyReportPreviewForReview 不是断言型测试，而是把当前配色的
// 日报渲染成 PNG 供人工比对视觉效果。用环境变量 REPORT_PREVIEW_OUT 指定输出
// 路径，不设置就跳过。
func TestGenerateDailyReportPreviewForReview(t *testing.T) {
	out := os.Getenv("REPORT_PREVIEW_OUT")
	if out == "" {
		t.Skip("设置 REPORT_PREVIEW_OUT 后才会生成预览图")
	}
	now := time.Date(2026, 10, 3, 0, 5, 0, 0, time.FixedZone("CST", 8*3600))
	sections := []weiboSuperCountHTMLSection{
		{
			Title: "Hearts2Hearts",
			Results: []monitor.WeiboSuperCountResult{
				{OID: "1022:1", Name: "Hearts2Hearts超话", SignCount: 1280, SignText: "签到1280人", FansCount: "3.2万", ReadCount: "8.6万", PostCount: "42", LevelText: "Lv6", DailyRankText: "第3名"},
				{OID: "1022:2", Name: "叶罗丽", SignCount: 860, SignText: "签到860人", FansCount: "2.1万", ReadCount: "5.4万", PostCount: "31", LevelText: "Lv5"},
				{OID: "1022:3", Name: "SNH48", SignCount: 540, SignText: "签到540人", FansCount: "1.4万", ReadCount: "3.1万", PostCount: "18", LevelText: "Lv4"},
			},
		},
		{
			Title: "SNH48",
			Results: []monitor.WeiboSuperCountResult{
				{OID: "1022:4", Name: "SNH48女团", SignCount: 2100, SignText: "签到2100人", FansCount: "6.8万", ReadCount: "15.2万", PostCount: "77", LevelText: "Lv7"},
			},
		},
	}
	html := formatWeiboSuperCountDualRankingHTML(sections, nil, "微博超话日报", now,
		map[string]int{"1022:1": 1200, "1022:2": 900, "1022:3": 500, "1022:4": 2000},
		nil, nil, nil, nil, true)

	dir := filepath.Dir(out)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	htmlPath := out + ".html"
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	png, err := renderWeverseReportPNG(html)
	if err != nil {
		t.Fatalf("渲染 PNG 失败: %v", err)
	}
	if err := os.WriteFile(out, png, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("预览图已生成: %s (%d bytes), HTML: %s", out, len(png), htmlPath)
}
