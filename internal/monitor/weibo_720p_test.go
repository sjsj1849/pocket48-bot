package monitor

import (
	"encoding/json"
	"strings"
	"testing"
)

// weibo720pSample 是 2026-10-04 实测的真实响应（mid=5350064312812042）里与视频相关的部分。
//
// 关键事实：media_info 里那几个字段（**包括名字叫 stream_url_hd 的**）
// 实际只有 540x1172，只有 urls.mp4_720p_mp4 是 720x1564。
const weibo720pSample = `{
  "type": "video",
  "video_url": "",
  "page_title": "测试视频",
  "media_info": {
    "stream_url":    "https://f.video.weibocdn.com/o0/STREAM_540.mp4?label=mp4_sd",
    "stream_url_hd": "https://f.video.weibocdn.com/o0/STREAM_HD_540.mp4?label=mp4_hd",
    "mp4_hd_url":    "",
    "mp4_sd_url":    ""
  },
  "urls": {
    "mp4_720p_mp4": "https://f.video.weibocdn.com/o0/REAL_720.mp4?label=mp4_720p",
    "mp4_hd_mp4":   "https://f.video.weibocdn.com/o0/HD_540.mp4?label=mp4_hd",
    "mp4_ld_mp4":   "https://f.video.weibocdn.com/o0/LD_540.mp4?label=mp4_ld"
  },
  "page_pic": {"url": "https://wx1.sinaimg.cn/orj360/cover.jpg"}
}`

func parseWeiboCard(t *testing.T, payload string) WeiboCard {
	t.Helper()
	// ★ 用具名类型而不是再抄一份 —— 这份副本曾经是「漏改就静默丢字段」
	//   的第 6 处（2026-10-04 给 media_info 加 Duration 时暴露）。
	var pi weiboPageInfoFields
	if err := json.Unmarshal([]byte(payload), &pi); err != nil {
		t.Fatalf("page_info 样本解析失败: %v", err)
	}
	return WeiboCard{PageInfo: &pi}
}

// TestWeiboCollectFallsBackWhenNo720P 保证没有 720p 档时不会推空。
func TestWeiboCollectFallsBackWhenNo720P(t *testing.T) {
	payload := strings.Replace(weibo720pSample,
		`"urls": {
    "mp4_720p_mp4": "https://f.video.weibocdn.com/o0/REAL_720.mp4?label=mp4_720p",
    "mp4_hd_mp4":   "https://f.video.weibocdn.com/o0/HD_540.mp4?label=mp4_hd",
    "mp4_ld_mp4":   "https://f.video.weibocdn.com/o0/LD_540.mp4?label=mp4_ld"
  }`, `"urls": {}`, 1)
	if payload == weibo720pSample {
		t.Fatal("样本替换失败，urls 片段没匹配上")
	}

	card := parseWeiboCard(t, payload)
	videos := collectWeiboVideos(card)
	if len(videos) != 1 {
		t.Fatalf("无 720p 时仍应产出 1 个视频，实际 %d 个", len(videos))
	}
	if videos[0].URL == "" {
		t.Fatal("无 720p 时选档结果不应为空")
	}
	// 回退时应落到 media_info 里的某一档
	if !strings.Contains(videos[0].URL, "weibocdn.com") {
		t.Fatalf("回退结果应来自 media_info，实际 %q", videos[0].URL)
	}
}

// TestWeiboCollectPrefers720PForMixMedia 覆盖九宫格混排里的视频项。
//
// 这里刻意走「整张卡片 JSON 解析」而不是在测试里复刻匿名结构体 ——
// MixMediaInfo 是内联匿名 struct，复刻一份会在每次改字段时编译失败，
// 反而掩盖真正的回归。
func TestWeiboCollectPrefers720PForMixMedia(t *testing.T) {
	payload := `{
      "mix_media_info": {
        "items": [
          {
            "type": "video",
            "data": {
              "type": "video",
              "video_url": "",
              "urls": {
                "mp4_720p_mp4": "https://f.video.weibocdn.com/o0/MIX_720.mp4?label=mp4_720p",
                "mp4_hd_mp4":   "https://f.video.weibocdn.com/o0/MIX_HD_540.mp4?label=mp4_hd"
              },
              "media_info": {
                "stream_url":    "https://f.video.weibocdn.com/o0/MIX_STREAM_540.mp4",
                "stream_url_hd": "https://f.video.weibocdn.com/o0/MIX_STREAM_HD_540.mp4"
              },
              "page_pic": {"url": "https://wx1.sinaimg.cn/orj360/mix.jpg"}
            }
          }
        ]
      }
    }`

	var card WeiboCard
	if err := json.Unmarshal([]byte(payload), &card); err != nil {
		t.Fatalf("九宫格样本解析失败: %v", err)
	}
	if card.MixMediaInfo == nil || len(card.MixMediaInfo.Items) != 1 {
		t.Fatalf("九宫格样本未解析出 items: %+v", card.MixMediaInfo)
	}
	if got := card.MixMediaInfo.Items[0].Data.URLs.MP4720p; !strings.Contains(got, "MIX_720") {
		t.Fatalf("九宫格项的 urls.mp4_720p_mp4 未解析出，实际 %q", got)
	}

	videos := collectWeiboVideos(card)
	if len(videos) != 1 {
		t.Fatalf("应产出 1 个视频，实际 %d 个: %+v", len(videos), videos)
	}
	if !strings.Contains(videos[0].URL, "MIX_720") {
		t.Fatalf("九宫格项也必须优先 720p，实际 %q", videos[0].URL)
	}
	if strings.Contains(videos[0].URL, "MIX_HD_540") || strings.Contains(videos[0].URL, "MIX_STREAM") {
		t.Fatalf("九宫格项选到了低清档，实际 %q", videos[0].URL)
	}
}

// TestWeiboCoverStillPresent 保证改选档顺序没有把封面弄丢。
func TestWeiboCoverStillPresent(t *testing.T) {
	card := parseWeiboCard(t, weibo720pSample)
	videos := collectWeiboVideos(card)
	if len(videos) != 1 {
		t.Fatalf("应产出 1 个视频，实际 %d 个", len(videos))
	}
	if !strings.Contains(videos[0].Cover, "cover.jpg") {
		t.Fatalf("封面丢失，实际 %q", videos[0].Cover)
	}
}

// TestWeiboVideoURLsJSONTags 锁住 JSON 标签，防止改字段名导致静默失效。
// 这类 bug（标签写错 → Unmarshal 静默得到空串）纯靠功能测试很难定位。
func TestWeiboVideoURLsJSONTags(t *testing.T) {
	payload := `{"mp4_720p_mp4":"a","mp4_hd_mp4":"b","mp4_ld_mp4":"c"}`
	var u weiboVideoURLs
	if err := json.Unmarshal([]byte(payload), &u); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if u.MP4720p != "a" || u.MP4HD != "b" || u.MP4LD != "c" {
		t.Fatalf("JSON 标签错位: %+v", u)
	}
}
