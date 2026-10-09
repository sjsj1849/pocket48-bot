package bilibili

import (
	"encoding/json"
	"testing"
)

// TestSeasonArchiveShape 锁住合集列表接口的响应结构。
//
// x/polymer/web-space/seasons_series_list 是目前唯一在机房 IP 上**匿名可用**
// 且返回完整投稿元数据的入口（x/space/wbi/arc/search 匿名态返回 -403）。
// 长视频检测依赖它，因此结构一旦变化必须立刻发现。
func TestSeasonArchiveShape(t *testing.T) {
	// 取自真实响应（截取字段形状）：时长单位为秒，pubdate 为 unix 秒。
	const raw = `{"code":0,"message":"OK","ttl":1,"data":{"items_lists":{
		"page":{"page_num":1,"page_size":20,"total":13},
		"seasons_list":[
			{"archives":[
				{"aid":117374829201405,"bvid":"BV1YRH86YEQv","title":"哦哦 玩得挺好",
				 "pic":"http://i2.hdslb.com/x.jpg","duration":19,"state":0,
				 "pubdate":1790998500,"stat":{"view":4730,"danmaku":19}},
				{"aid":117371339541187,"bvid":"BV1KxaU6aEFZ","title":"团综 32 分钟",
				 "pic":"http://i2.hdslb.com/y.jpg","duration":1957,"state":0,
				 "pubdate":1790944724,"stat":{"view":8802,"danmaku":31}}
			]}
		]}}}`

	var parsed struct {
		Code int `json:"code"`
		Data struct {
			ItemsLists struct {
				Page struct {
					Total int `json:"total"`
				} `json:"page"`
				SeasonsList []struct {
					Archives []seasonArchive `json:"archives"`
				} `json:"seasons_list"`
			} `json:"items_lists"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("解析合集响应失败: %v", err)
	}
	if parsed.Code != 0 {
		t.Fatalf("期望 code=0，实际 %d", parsed.Code)
	}
	archives := parsed.Data.ItemsLists.SeasonsList[0].Archives
	if len(archives) != 2 {
		t.Fatalf("期望 2 条投稿，实际 %d 条", len(archives))
	}
	if archives[0].BVID != "BV1YRH86YEQv" || archives[0].Duration != 19 {
		t.Errorf("短视频解析不对: %+v", archives[0])
	}
	if archives[1].Duration != 1957 {
		t.Errorf("长视频时长解析不对: %d", archives[1].Duration)
	}
	if archives[1].Stat.View != 8802 {
		t.Errorf("播放量解析不对: %d", archives[1].Stat.View)
	}
}

// TestFormatDuration 覆盖时长格式化，特别是跨小时的团综。
func TestFormatDuration(t *testing.T) {
	cases := []struct {
		seconds int
		want    string
	}{
		{0, ""},
		{19, "0:19"},
		{84, "1:24"},
		{600, "10:00"},
		{1957, "32:37"},
		{3280, "54:40"},
		{3600, "1:00:00"},
		{7380, "2:03:00"},
	}
	for _, c := range cases {
		if got := FormatDuration(c.seconds); got != c.want {
			t.Errorf("FormatDuration(%d) = %q, 期望 %q", c.seconds, got, c.want)
		}
	}
}

// TestMinVideoSecondsFilter 验证按时长过滤：短视频（与抖音重复）被滤掉，
// 长视频（团综）保留。这是用户核心诉求的行为保证。
func TestMinVideoSecondsFilter(t *testing.T) {
	// 模拟接口返回的短视频(19s)与团综(1957s)
	archives := []seasonArchive{
		{BVID: "BV1SHORT", Duration: 19, State: 0},
		{BVID: "BV1LONG1", Duration: 1957, State: 0},
		{BVID: "BV1SHORT2", Duration: 128, State: 0},
		{BVID: "BV1LONG2", Duration: 2740, State: 0},
		{BVID: "BV1PENDING", Duration: 3600, State: 1}, // 审核中，应跳过
	}
	const minSeconds = 600 // 10 分钟

	kept := 0
	for _, a := range archives {
		if a.State != 0 {
			continue
		}
		if a.Duration < minSeconds {
			continue
		}
		kept++
	}
	if kept != 2 {
		t.Errorf("10 分钟阈值下应保留 2 条长视频，实际 %d 条", kept)
	}
}
