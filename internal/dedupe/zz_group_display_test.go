package dedupe

import "testing"

// ★ 回归（2026-10-04，用户要求「所有 hearts2 hearts 都统一用大写 H」）。
//
// 要点：**比对侧本来就没问题** —— NormalizeGroup 内部 ToLower，
// 所以 TikTok 的 hearts2hearts 与抖音的 Hearts2Hearts 一直能匹配上。
// 有问题的是**给人看的字**：同一个人在消息里一会儿大写一会儿小写，
// 看起来像两个人。这个函数只管展示。
func TestZZCanonicalGroupName(t *testing.T) {
	cases := map[string]string{
		"hearts2hearts":    "Hearts2Hearts",
		"Hearts2Hearts":    "Hearts2Hearts",
		"HEARTS2HEARTS":    "Hearts2Hearts",
		"  hearts2hearts ": "Hearts2Hearts",
		// 未登记的团名原样保留，不能反把所有团名首字母大写 ——
		// 那会把 SEVENTHSENSE / AKB48 这类全大写团名改成错的写法。
		"SEVENTHSENSE": "SEVENTHSENSE",
		"  aespa  ":    "aespa",
		"":             "",
	}
	for in, want := range cases {
		if got := CanonicalGroupName(in); got != want {
			t.Errorf("CanonicalGroupName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// 展示归一化不能影响比对：归一化后的大小写差异仍必须相等。
func TestZZCanonicalDoesNotBreakMatching(t *testing.T) {
	if NormalizeGroup("Hearts2Hearts") != NormalizeGroup("hearts2hearts") {
		t.Fatal("展示归一化不应改变比对用的键")
	}
	if NormalizeGroup(CanonicalGroupName("hearts2hearts")) !=
		NormalizeGroup("HEARTS2HEARTS") {
		t.Fatal("归一化后的团名仍必须与任意大小写写法相等")
	}
}
