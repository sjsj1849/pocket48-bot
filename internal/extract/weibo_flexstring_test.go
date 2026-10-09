package extract

import (
	"encoding/json"
	"testing"
)

// TestFlexStringAcceptsNumberAndString 锁住「二态字段」的容错行为。
//
// 2026-10-04 端到端实测：page_info.object_type 返回的是数字，
// 而代码声明成 string，导致整个响应 Unmarshal 失败 —— 解析器直接报错，
// 一条内容都取不出来。纯函数测试当时全绿。
func TestFlexStringAcceptsNumberAndString(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"字符串", `"video"`, "video"},
		{"数字", `62`, "62"},
		{"负数", `-1`, "-1"},
		{"浮点", `1.5`, "1.5"},
		{"null", `null`, ""},
		{"布尔", `true`, "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f flexString
			if err := json.Unmarshal([]byte(tc.payload), &f); err != nil {
				t.Fatalf("Unmarshal(%q) 失败: %v", tc.payload, err)
			}
			if f.String() != tc.want {
				t.Fatalf("Unmarshal(%q) = %q, 期望 %q", tc.payload, f.String(), tc.want)
			}
		})
	}
}

// TestWeiboPagePicAcceptsObjectAndString 覆盖 page_pic 的三种形态。
//
// 真实响应里 page_pic 是**对象** {"url": ...}，
// 但也有接口版本返回纯字符串 URL，还有返回 null 的。
// 任何一种没处理都会让整个响应 Unmarshal 失败。
func TestWeiboPagePicAcceptsObjectAndString(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"对象", `{"url":"https://wx1.sinaimg.cn/a.jpg"}`, "https://wx1.sinaimg.cn/a.jpg"},
		{"字符串", `"https://wx1.sinaimg.cn/b.jpg"`, "https://wx1.sinaimg.cn/b.jpg"},
		{"null", `null`, ""},
		{"空对象", `{}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p weiboPagePic
			if err := json.Unmarshal([]byte(tc.payload), &p); err != nil {
				t.Fatalf("Unmarshal(%q) 失败: %v", tc.payload, err)
			}
			if p.URL != tc.want {
				t.Fatalf("Unmarshal(%q).URL = %q, 期望 %q", tc.payload, p.URL, tc.want)
			}
		})
	}
}

// 用真实响应形态（object_type 为数字）验证整条链路不炸。
func TestWeiboResponseParsesNumericObjectType(t *testing.T) {
	payload := `{
      "ok": 1,
      "data": {
        "id": "5350064312812042",
        "page_info": {
          "type": "video",
          "object_type": 62,
          "media_info": {
            "stream_url":    "https://f.video.weibocdn.com/o0/A.mp4",
            "stream_url_hd": "https://f.video.weibocdn.com/o0/B.mp4"
          },
          "urls": {
            "mp4_720p_mp4": "https://f.video.weibocdn.com/o0/C.mp4"
          },
          "page_pic": {"url": "https://wx1.sinaimg.cn/x.jpg"}
        }
      }
    }`
	var out weiboShowResponse
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("object_type 为数字时整个响应解析失败: %v", err)
	}
	if out.Data.ID != "5350064312812042" {
		t.Fatalf("id 丢失，实际 %q", out.Data.ID)
	}
	if got := pickBestWeiboVideo(out.Data.PageInfo.URLs, out.Data.PageInfo.MediaInfo); got == "" {
		t.Fatal("应能选出视频")
	}
}
