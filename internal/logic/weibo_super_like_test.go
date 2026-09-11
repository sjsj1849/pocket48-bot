package logic

import (
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
	}{
		{"increase", 12, true, "12 (+5)"},
		{"decrease", 3, true, "3 (-4)"},
		{"zero", 0, true, "0 (-7)"},
		{"unchanged", 7, true, "7 (0)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []monitor.WeiboSuperCountResult{{OID: oid, Name: "测试", SignCount: 10, SuperLikeCount: tc.count, SuperLikeKnown: tc.known}}
			snap := buildWeiboSuperCountDailySnapshotV2(rows)
			if !snap[oid].SuperLikeKnown || buildLikeBaselineFromSnapshotV2(snap)[oid] != tc.count {
				t.Fatal("snapshot lost known count")
			}
			h := buildWeiboSuperCountHTMLTable(rows, nil, map[string]int{oid: 7}, nil)
			plain := formatWeiboSuperCountDualRanking(rows, nil, "日报", time.Now(), nil, map[string]int{oid: 7}, nil)
			if !strings.Contains(h, tc.want) || !strings.Contains(plain, tc.want) {
				t.Fatalf("missing %q in daily output", tc.want)
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
	if strings.Contains(buildWeiboSuperCountHTMLTable(rows, nil, map[string]int{oid: 7}, nil), "(-7)") {
		t.Fatal("failure reported as decrease")
	}
}
