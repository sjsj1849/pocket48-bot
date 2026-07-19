package monitor

import "testing"

func TestInferWeiboLevelText(t *testing.T) {
	cases := map[string]string{
		"https://n.sinaimg.cn/default/x/active_level_page_silver_common.png": "普通",
		"https://n.sinaimg.cn/default/x/active_level_page_gold_common.png":   "普通",
		"https://n.sinaimg.cn/default/x/active_level_page_silver_1.png":      "银1",
		"https://n.sinaimg.cn/default/x/active_level_page_silver_2.png":      "银2",
		"https://n.sinaimg.cn/default/x/active_level_page_gold_1.png":        "金1",
		"https://n.sinaimg.cn/default/x/active_level_page_diamond_3.png":     "钻3",
		"": "",
	}
	for url, want := range cases {
		got := inferWeiboLevelText(url)
		if got != want {
			t.Fatalf("url=%q got=%q want=%q", url, got, want)
		}
	}
}
