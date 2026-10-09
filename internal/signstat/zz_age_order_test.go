package signstat

import "testing"

// ★★★ 所有「列出 8 个超话」的地方都必须按这个年龄顺序（2026-10-08 用户指定）。
//
// 用户原话：「不管是表格还是图表，如果要把 8 个超话都列出来，顺序是需要固定的。
// 就直接按她们的年龄顺序来排，好吗？这样比较清晰一点。」
// 顺序：carmen → choijiwoo → 柳河岚 → stella → juun → ana → 郑伊安 → yeon
func TestAgeOrderFixed(t *testing.T) {
	want := []string{
		"Carmen", "ChoiJiwoo", "柳河岚YUHA", "stella",
		"JUUN", "ANA卢惟那", "郑伊安IAN", "YEON金奈延",
	}
	got := append([]string(nil), want...)
	// 打乱后交给排序函数
	rand8 := []string{
		"YEON金奈延", "郑伊安IAN", "stella", "Carmen",
		"柳河岚YUHA", "JUUN", "ANA卢惟那", "ChoiJiwoo",
	}
	SortByAge(rand8)
	got = rand8

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 位应为 %s，实际 %s\n完整顺序: %v", i, want[i], got[i], got)
		}
	}
}

// 明细行也必须按年龄顺序（面板 / 推送 PNG / 邮件正文共用同一份 Rows）。
func TestDetailRowsAgeOrder(t *testing.T) {
	rows := []DetailRow{
		{Name: "YEON金奈延"}, {Name: "郑伊安IAN"}, {Name: "stella"},
		{Name: "Carmen"}, {Name: "柳河岚YUHA"}, {Name: "JUUN"},
		{Name: "ANA卢惟那"}, {Name: "ChoiJiwoo"},
	}
	SortByAgeDetailRows(rows)
	want := []string{"Carmen", "ChoiJiwoo", "柳河岚YUHA", "stella", "JUUN", "ANA卢惟那", "郑伊安IAN", "YEON金奈延"}
	for i := range want {
		if rows[i].Name != want[i] {
			t.Fatalf("第 %d 位应为 %s，实际 %s\n完整顺序: %v", i, want[i], rows[i].Name, namesOf(rows))
		}
	}
}

func namesOf(rows []DetailRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// 未收录的名字排末尾且不丢数据。
func TestAgeOrderUnknownGoesLast(t *testing.T) {
	list := []string{"陌生人X", "Carmen", "另一个人", "ChoiJiwoo"}
	SortByAge(list)
	if list[0] != "Carmen" || list[1] != "ChoiJiwoo" {
		t.Fatalf("已知超话应排前面，实际 %v", list)
	}
	if len(list) != 4 {
		t.Fatalf("不能丢数据，实际 %v", list)
	}
}
