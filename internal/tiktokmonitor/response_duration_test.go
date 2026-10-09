package tiktokmonitor

import (
	"encoding/json"
	"testing"
)

// TestResponseKeepsDurationAndDownload 锁住一个**静默**踩过的坑。
//
// sidecar detail 操作返回的是扁平一层 JSON：
//
//	{"id":"1","desc":"x","duration":12.5,"width":720,"height":1280,
//	 "path":"/tmp/1.mp4","bytes":123,"downloaded":true}
//
// response 同时内嵌 *Detail 与 *Downloaded，两者都声明了
// json:"duration"/"width"/"height"。Go 的 encoding/json 在同一深度
// 遇到多个同名候选字段时，会把它们**全部丢弃**（不是后者覆盖前者）。
//
// 后果：Go 侧 Detail.Duration 静默变成 0，探针只打印「媒体 时长 = 0 秒」，
// 没有任何报错。而时长是跨平台去重的判定维度（团名 + 时长差 <= 3s），
// 为 0 等于二级判定永久关闭 —— 同一视频在多个平台被重复推送。
//
// 这个测试就是防止有人把 UnmarshalJSON 删掉「简化」代码。
func TestResponseKeepsDurationAndDownload(t *testing.T) {
	raw := []byte(`{
		"id": "7688182139010977025",
		"desc": "ダンスしよう！",
		"cover": "https://p16-common-sign.tiktokcdn.com/cover.jpeg",
		"author": "hearts2hearts",
		"authorId": "999",
		"duration": 12.233333,
		"width": 720,
		"height": 1280,
		"downloaded": true,
		"path": "/root/pocket48-bot/storage/tiktok/videos/7688182139010977025.mp4",
		"bytes": 2155862,
		"cached": false
	}`)

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("Unmarshal 失败：%v", err)
	}
	if r.Detail == nil {
		t.Fatal("*Detail 为 nil")
	}
	if r.Detail.Duration != 12.233333 {
		t.Errorf("Detail.Duration = %v，期望 12.233333 —— 同名 JSON tag 冲突导致字段被丢弃？",
			r.Detail.Duration)
	}
	if r.Detail.Width != 720 || r.Detail.Height != 1280 {
		t.Errorf("分辨率 = %dx%d，期望 720x1280", r.Detail.Width, r.Detail.Height)
	}
	if r.Downloaded == nil {
		t.Fatal("*Downloaded 为 nil（path 非空时应解出来）")
	}
	if r.Downloaded.Path == "" {
		t.Error("Downloaded.Path 为空")
	}
	if r.Downloaded.Bytes != 2155862 {
		t.Errorf("Downloaded.Bytes = %d，期望 2155862", r.Downloaded.Bytes)
	}
}

// TestResponseWithoutDownload 确认没下载时 Downloaded 保持 nil，
// 调用方靠 r.Downloaded == nil 判断「视频没取到但元数据可用」。
func TestResponseWithoutDownload(t *testing.T) {
	raw := []byte(`{"id":"1","desc":"只有元数据","duration":7.5,"width":720,"height":1280}`)

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("Unmarshal 失败：%v", err)
	}
	if r.Downloaded != nil {
		t.Errorf("Downloaded 应为 nil，实际 = %+v", r.Downloaded)
	}
	if r.Detail == nil || r.Detail.Duration != 7.5 {
		t.Errorf("纯元数据响应里 duration 也应保住，实际 = %+v", r.Detail)
	}
}

// TestResponseTimelineUnaffected 确认 timeline 响应（videos 数组）
// 没被自定义 UnmarshalJSON 影响。
func TestResponseTimelineUnaffected(t *testing.T) {
	raw := []byte(`{
		"user": {"username":"hearts2hearts","userId":"1","nickname":"心连心"},
		"videos": [
			{"id":"a","desc":"第一条","duration":9.5},
			{"id":"b","desc":"第二条","duration":3.0}
		]
	}`)

	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("Unmarshal 失败：%v", err)
	}
	if len(r.Videos) != 2 {
		t.Fatalf("len(Videos) = %d，期望 2", len(r.Videos))
	}
	if r.Videos[0].ID != "a" || r.Videos[1].Duration != 3.0 {
		t.Errorf("videos 内容不对：%+v", r.Videos)
	}
	if r.User == nil || r.User.Nickname != "心连心" {
		t.Errorf("user 未解出来：%+v", r.User)
	}
}
