package logic

import (
	"fmt"
	"os"
	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
	"strings"
	"testing"
	"time"
)

func TestSuperLikeSnapshotAndDailyComparison(t *testing.T) {
	oid := normalizeWeiboSuperOID("100808test")
	for _, tc := range []struct {
		name  string
		count int
		known bool
		want  string
		delta string
	}{
		{"increase", 12, true, "12 (+5)", "+5"},
		{"decrease", 3, true, "3 (-4)", "-4"},
		{"zero", 0, true, "0 (-7)", "-7"},
		{"unchanged", 7, true, "7", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []monitor.WeiboSuperCountResult{{OID: oid, Name: "测试", SignCount: 10, SuperLikeCount: tc.count, SuperLikeKnown: tc.known}}
			snap := buildWeiboSuperCountDailySnapshotV2(rows)
			if !snap[oid].SuperLikeKnown || buildLikeBaselineFromSnapshotV2(snap)[oid] != tc.count {
				t.Fatal("snapshot lost known count")
			}
			h := buildWeiboSuperCountHTMLTable(rows, nil, map[string]int{oid: 7}, nil, nil, nil)
			plain := formatWeiboSuperCountDualRanking(rows, nil, "日报", time.Now(), nil, map[string]int{oid: 7}, nil, nil, nil)
			if !strings.Contains(plain, tc.want) || !strings.Contains(h, fmt.Sprintf(">%d</div>", tc.count)) || (tc.delta != "" && !strings.Contains(h, "("+tc.delta+")")) {
				t.Fatalf("missing %q in daily output", tc.want)
			}
			if tc.name == "unchanged" && (strings.Contains(h, "(0)") || strings.Contains(plain, "(0)")) {
				t.Fatal("unchanged delta should be hidden")
			}
		})
	}
	legacy := map[string]*config.WeiboSuperCountSnapshotItem{oid: {SuperLikeCount: 0}}
	if _, ok := buildLikeBaselineFromSnapshotV2(legacy)[oid]; ok {
		t.Fatal("legacy missing zero treated as valid baseline")
	}
	legacy[oid].SuperLikeCount = 7
	if buildLikeBaselineFromSnapshotV2(legacy)[oid] != 7 {
		t.Fatal("lost historical positive count")
	}
	rows := []monitor.WeiboSuperCountResult{{OID: oid, Name: "失败", SignCount: 10}}
	if strings.Contains(buildWeiboSuperCountHTMLTable(rows, nil, map[string]int{oid: 7}, nil, nil, nil), "(-7)") {
		t.Fatal("failure reported as decrease")
	}
}

func TestSuperLikeColumnRemainsVisibleWhenCollectionFails(t *testing.T) {
	rows := []monitor.WeiboSuperCountResult{{OID: "test", Name: "八小妹", SignCount: 10}}
	h := buildWeiboSuperCountHTMLTable(rows, nil, nil, nil, nil, nil)
	if !strings.Contains(h, "超LIKE") || !strings.Contains(h, "未获取") {
		t.Fatal("missing column/error marker")
	}
	if strings.Contains(h, "(-") {
		t.Fatal("unknown value treated as decrease")
	}
}

func TestDailyMetricDeltasAreCompactAndUseUniformRows(t *testing.T) {
	oid := normalizeWeiboSuperOID("100808metrics")
	rows := []monitor.WeiboSuperCountResult{{
		OID: oid, Name: "测试", SignCount: 2000,
		SuperLikeCount: 7, SuperLikeKnown: true,
		ReadCount: "2.4万", FansCount: "12.4万", PostCount: "4567",
	}}
	sign := map[string]int{oid: 1000}
	like := map[string]int{oid: 7}
	read := map[string]int{oid: 23000}
	fans := map[string]int{oid: 123000}
	posts := map[string]int{oid: 4500}

	plain := formatWeiboSuperCountDualRanking(rows, nil, "日报", time.Now(), sign, like, read, fans, posts)
	for _, want := range []string{"签到2000人 (+1K)", "超LIKE7人", "阅读2.4万 (+1K)", "粉丝12.4万 (+1K)", "帖子4567 (+67)"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("plain report missing %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "(0)") {
		t.Fatal("plain report contains unchanged delta")
	}

	h := buildWeiboSuperCountHTMLTable(rows, sign, like, read, fans, posts)
	if strings.Contains(h, "(0)") {
		t.Fatal("HTML report contains unchanged delta")
	}
	if got := strings.Count(h, `margin-top:2px;white-space:nowrap`); got != 4 {
		t.Fatalf("expected four changed metrics on uniform second rows, got %d", got)
	}
}

func TestFormatSignedDeltaUsesCompactUnits(t *testing.T) {
	for _, tc := range []struct {
		value int
		want  string
	}{
		{0, ""}, {999, "+999"}, {1000, "+1K"}, {1500, "+1.5K"}, {-12500, "-12.5K"}, {1000000, "+1M"},
	} {
		if got := formatSignedDelta(tc.value); got != tc.want {
			t.Fatalf("formatSignedDelta(%d)=%q, want %q", tc.value, got, tc.want)
		}
	}
}

// Optional visual regression fixture. The output path must be outside the repo,
// for example: RENDER_WEIBO_DAILY_PNG=/tmp/weibo-daily.png go test ./internal/logic -run TestRenderWeiboDailyDeltaLayout
func TestRenderWeiboDailyDeltaLayout(t *testing.T) {
	output := strings.TrimSpace(os.Getenv("RENDER_WEIBO_DAILY_PNG"))
	if output == "" {
		t.Skip("set RENDER_WEIBO_DAILY_PNG to render the visual fixture")
	}
	oid := normalizeWeiboSuperOID("100808visual")
	rows := []monitor.WeiboSuperCountResult{{
		OID: oid, Name: "四位数示例超话", SignCount: 12567,
		SuperLikeCount: 1250, SuperLikeKnown: true,
		ReadCount: "2.4万", FansCount: "12.4万", PostCount: "4567", LevelText: "LV.12",
	}}
	htmlBody := formatWeiboSuperCountDualRankingHTML(
		[]weiboSuperCountHTMLSection{{Title: "排版检查", Results: rows}}, nil,
		"[超话签到人数日报 · 视觉检查]", time.Date(2026, 9, 27, 23, 59, 30, 0, time.FixedZone("CST", 8*3600)),
		map[string]int{oid: 11567}, map[string]int{oid: 1250}, map[string]int{oid: 23000}, map[string]int{oid: 123000}, map[string]int{oid: 4500}, true,
	)
	png, err := renderHTMLToPNG(htmlBody)
	if err != nil {
		t.Fatalf("render fixture: %v", err)
	}
	if err := os.WriteFile(output, png, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
