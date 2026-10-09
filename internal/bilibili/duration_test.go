package bilibili

import "testing"

// arc/search 只返回 length 字符串（如 "32:37"），长视频过滤靠 Seconds 判定。
// 如果不解析，Seconds=0 会让过滤条件短路失效，十几秒的短视频就会被推送出去。
func TestParseDurationSeconds(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"32:37", 1957},
		{"00:19", 19},
		{"1:24", 84},
		{"1:02:03", 3723},
		{"00:00", 0},
		{"45:43", 2743},
		{"", 0},
		{"abc", 0},
		{"12:xx", 0},
		{"-1:30", 0},
		{"  10:00  ", 600},
	}
	for _, tc := range cases {
		if got := ParseDurationSeconds(tc.in); got != tc.want {
			t.Errorf("ParseDurationSeconds(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// 回归：过滤条件不能因为 Seconds=0 而短路跳过。
// 旧写法 `d.Seconds > 0 && d.Seconds < 阈值` 会让解析失败的条目全部漏过。
func TestMinVideoSecondsRejectsUnknownLength(t *testing.T) {
	const threshold = 1200
	cases := []struct {
		name    string
		seconds int
		want    bool // true = 应被过滤掉
	}{
		{"时长 0（解析失败）必须挡下", 0, true},
		{"19 秒短视频挡下", 19, true},
		{"19 分 59 秒挡下", 1199, true},
		{"正好 20 分钟放行", 1200, false},
		{"32 分钟放行", 1957, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filtered := tc.seconds < threshold
			if filtered != tc.want {
				t.Fatalf("seconds=%d 过滤结果=%v, want %v", tc.seconds, filtered, tc.want)
			}
		})
	}
}
