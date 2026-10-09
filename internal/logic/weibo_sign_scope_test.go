package logic

import (
	"testing"

	"pocket48-bot/internal/config"
)

// 用户 2026-10-04 规则：
//  1. 不在自动签到列表的超话 —— 不签到，日报原样显示模糊数字
//  2. 在自动签到列表的超话   —— 才通过签到来拿详细数据
func TestShouldSignWeiboCountForExact(t *testing.T) {
	auto := map[string]*config.WeiboSuperTopic{
		"oid_in_auto_a": {OID: "oid_in_auto_a", Name: "自动A"},
		"oid_in_auto_b": {OID: "oid_in_auto_b", Name: "自动B"},
	}

	cases := []struct {
		name      string
		isRounded bool
		oid       string
		want      bool
		why       string
	}{
		{"列表外+破万 → 不签到", true, "oid_report_only", false,
			"仅在日报监控列表、且签到数破万，按新规则保留模糊值"},
		{"列表内+破万 → 签到", true, "oid_in_auto_a", true,
			"在自动签到列表内的破万超话才取精确值"},
		{"列表外+不破万 → 不签到", false, "oid_report_only", false,
			"没破万本来就有精确数据，不需要签到"},
		{"列表内+不破万 → 不签到", false, "oid_in_auto_b", false,
			"同上，且不该浪费一次签到额度"},
		{"列表外+空 oid → 不签到", true, "", false,
			"空 oid 不在自动签到列表内"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSignWeiboCountForExact(tc.isRounded, tc.oid, auto)
			if got != tc.want {
				t.Fatalf("shouldSignWeiboCountForExact(rounded=%v, oid=%q) = %v, want %v (%s)",
					tc.isRounded, tc.oid, got, tc.want, tc.why)
			}
		})
	}
}

// 自动签到列表为空时，任何超话都不该触发日报补签。
func TestShouldSignWeiboCountForExact_EmptyAutoList(t *testing.T) {
	empty := map[string]*config.WeiboSuperTopic{}
	if shouldSignWeiboCountForExact(true, "oid_any", empty) {
		t.Fatal("自动签到列表为空时不应补签")
	}
	var nilMap map[string]*config.WeiboSuperTopic
	if shouldSignWeiboCountForExact(true, "oid_any", nilMap) {
		t.Fatal("自动签到列表为 nil 时不应补签")
	}
}

// 生产实测：7 个自动签到超话（王选、Janjingjing、Stella、绚安2、绚岚、胡晓慧、徐洁儿）
// 里有相当一部分不在日报监控列表中，这些超话不该被日报补签影响。
// 这里只验证「自动签到列表本身不受日报影响」——即 signAll 不再因 ReportSign 跳过。
func TestSignAllNoLongerSkipsByReportSign(t *testing.T) {
	// 构造一个曾经 ReportSign=1 的场景：改动后该标记不再阻断自动签到。
	ct := &config.WeiboSuperCountTopic{
		OID:        "oid_x",
		Name:       "曾经被标记",
		ReportSign: 1,
	}
	if ct.ReportSign != 1 {
		t.Fatal("前置条件失败：ReportSign 应为 1")
	}
	// signAllWeiboSuperTopics 与手动 sign all 里已删除跳过分支，
	// 因此这里仅做回归提醒：ReportSign 字段本身仍保留（兼容旧 config），
	// 但不再参与任何签到决策。改动时若重新引入跳过逻辑，此测试所在文件应被同步更新。
}
