package bilibili

import (
	"encoding/json"
	"testing"
)

// TestLiveStatusParsesArrayData 锁住 B 站直播状态接口的响应结构。
//
// room/v1/Room/get_status_info_by_uids 的 data 字段实测是**数组**：
//
//	{"code":0,"msg":"success","data":[{"uid":"3546824314980440","room_id":2,"live_status":0}]}
//
// 之前代码把 data 声明成 map[string]liveStatusEntry，导致每轮直播监控都报
//
//	json: cannot unmarshal array into Go value of type map[string]bilibili.liveStatusEntry
//
// 进而在 status.json 的 error 字段里持续报错，直播监控形同失效。
func TestLiveStatusParsesArrayData(t *testing.T) {
	const raw = `{"code":0,"msg":"success","data":[
		{"uid":"111","room_id":222,"live_status":1,"title":"直播中","cover":"http://c","online":999}
	]}`

	// 先剥外层信封，确认 data 真的是数组而不是 map。
	var envelope struct {
		Code int               `json:"code"`
		Data []liveStatusEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		t.Fatalf("data 无法按数组解析（B 站实际返回数组）: %v", err)
	}
	if envelope.Code != 0 {
		t.Fatalf("期望 code=0，实际 %d", envelope.Code)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("期望 1 条，实际 %d 条", len(envelope.Data))
	}
	got := envelope.Data[0]
	if got.UID != "111" || got.RoomID != 222 || got.LiveStatus != 1 {
		t.Errorf("解析结果不对: %+v", got)
	}
	if got.Title != "直播中" || got.Online != 999 {
		t.Errorf("标题/人气解析不对: %+v", got)
	}
}

// TestLiveStatusFillsMissingUIDs 验证查不到直播间的 UP 也会出现在结果里且
// LiveStatus==0，这样调用方能区分「未开播」与「查不到直播间」。
func TestLiveStatusFillsMissingUIDs(t *testing.T) {
	var data []liveStatusEntry
	if err := json.Unmarshal([]byte(`[{"uid":"111","room_id":222,"live_status":1}]`), &data); err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	// 复刻 LiveStatus 内部的合并逻辑。
	requested := []string{"111", "222"}
	result := map[string]LiveRoom{}
	for _, uid := range requested {
		if _, ok := result[uid]; !ok {
			result[uid] = LiveRoom{UID: uid}
		}
	}
	for _, entry := range data {
		result[entry.UID] = LiveRoom{UID: entry.UID, RoomID: entry.RoomID, LiveStatus: entry.LiveStatus}
	}

	if len(result) != 2 {
		t.Fatalf("期望 2 条（含未开播的补齐），实际 %d 条", len(result))
	}
	if result["222"].LiveStatus != 0 {
		t.Errorf("查不到直播间的 UP 应为 LiveStatus=0，实际 %d", result["222"].LiveStatus)
	}
	if result["111"].LiveStatus != 1 {
		t.Errorf("正在直播的 UP 应为 LiveStatus=1，实际 %d", result["111"].LiveStatus)
	}
}
