package weverse

import (
	"strings"
	"testing"
)

// TestEventVideosMomentPicksUpDirectAndCover 覆盖 moment 分支：它与
// orderedAttachments 的结构不同，此前只填CoverURL，导致这类帖子的封面取不到、
// 且没有任何直链线索。实测 moment 的 videoId 完全可被 ResolveEventVideos 解析
// 出直链（IAN 4-3020364 / STELLA 3-3016446 均验证通过），所以这里只要把字段
// 取全，视频就能正常发出。
func TestEventVideosMomentPicksUpDirectAndCover(t *testing.T) {
	p := Object{
		"extension": Object{
			"moment": Object{
				"video": Object{
					"videoId": "4-3020364",
					"thumb":  "https://img/moment-cover.jpg",
					"url":    "https://cdn/direct.mp4",
				},
			},
		},
	}
	items := eventVideos(p)
	if len(items) != 1 {
		t.Fatalf("moment 视频应被解析出来，实际 %d 个：%+v", len(items), items)
	}
	v := items[0]
	if v.ID != "4-3020364" {
		t.Errorf("videoId 不对：%q", v.ID)
	}
	if v.CoverURL != "https://img/moment-cover.jpg" {
		t.Errorf("封面不应为空：%q", v.CoverURL)
	}
	// 内嵌直链应当被采纳，省掉一次 playInfo 往返
	if v.URL != "https://cdn/direct.mp4" {
		t.Errorf("应采用内嵌直链，实际 %q", v.URL)
	}
}

// TestEventVideosMomentCoverKeyVariants 不同版本的 moment 载荷封面 key 不一致，
// 任一命中即可；非 https 的值必须丢弃，避免把无效地址发出去。
func TestEventVideosMomentCoverKeyVariants(t *testing.T) {
	cases := []struct {
		name string
		vid  Object
		want string
	}{
		{"thumb", Object{"videoId": "1-1", "thumb": "https://a/1.jpg"}, "https://a/1.jpg"},
		{"thumbnailUrl", Object{"videoId": "1-2", "thumbnailUrl": "https://a/2.jpg"}, "https://a/2.jpg"},
		{"imageUrl", Object{"videoId": "1-3", "imageUrl": "https://a/3.jpg"}, "https://a/3.jpg"},
		{"coverUrl", Object{"videoId": "1-4", "coverUrl": "https://a/4.jpg"}, "https://a/4.jpg"},
		{
			"嵌套 uploadInfo",
			Object{"videoId": "1-5", "uploadInfo": Object{"imageUrl": "https://a/5.jpg"}},
			"https://a/5.jpg",
		},
		{"非 https 应丢弃", Object{"videoId": "1-6", "thumb": "http://a/6.jpg"}, ""},
		{"完全没有封面", Object{"videoId": "1-7"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Object{"extension": Object{"moment": Object{"video": c.vid}}}
			items := eventVideos(p)
			if len(items) != 1 {
				t.Fatalf("应解析出 1 个视频，实际 %d", len(items))
			}
			if items[0].CoverURL != c.want {
				t.Errorf("封面 = %q，期望 %q", items[0].CoverURL, c.want)
			}
			// 没有封面也不该丢视频：直链稍后由 ResolveEventVideos 解析
			if items[0].ID == "" {
				t.Error("videoId 不应为空")
			}
		})
	}
}

// TestEventVideosMomentNoVideoId moment 分支没有 videoId 时不能产生空条目，
// 否则 ResolveEventVideos 会拿着空 ID 去请求接口。
func TestEventVideosMomentNoVideoID(t *testing.T) {
	p := Object{"extension": Object{"moment": Object{"video": Object{"thumb": "https://a/x.jpg"}}}}
	if items := eventVideos(p); len(items) != 0 {
		t.Errorf("缺 videoId 不应产出条目，实际 %+v", items)
	}
}

// TestEventVideosDedupesAcrossShapes orderedAttachments 与 moment 指向同一个
// 视频时只能出现一次，否则同一条视频会被发两遍。
func TestEventVideosDedupesAcrossShapes(t *testing.T) {
	p := Object{
		"orderedAttachments": []any{
			Object{"type": "video", "data": Object{
				"videoId": "9-1", "url": "https://cdn/a.mp4",
			}},
		},
		"extension": Object{
			"moment": Object{"video": Object{"videoId": "9-1", "thumb": "https://img/c.jpg"}},
		},
	}
	items := eventVideos(p)
	if len(items) != 1 {
		t.Fatalf("同一视频应只出现一次，实际 %d 个：%+v", len(items), items)
	}
	if items[0].URL != "https://cdn/a.mp4" {
		t.Errorf("应保留 orderedAttachments 里的直链，实际 %q", items[0].URL)
	}
}

// TestVideoSourceRejectsUnplayable 视频源必须可播放：超大文件与未完成转码的
// 清晰度都不能选，否则 QQ/飞书侧会发出打不开的视频。
//
// 注意：数值必须用 float64 ——生产代码的 str() 只认 string 和 float64
// （即标准 encoding/json 反序列化出来的形态），塞 int64/json.Number 都会被当成
// 未知类型静默返回空串，num() 随之得 0，筛选条件就会失效。
func TestVideoSourceRejectsUnplayable(t *testing.T) {
	complete := "true"
	_, err := videoSource(Object{"videos": Object{"list": []any{Object{
		"source":         "https://cdn/big.mp4",
		"size":           float64(80 * 1024 * 1024),
		"encodingOption": Object{"isEncodingComplete": complete, "height": float64(720)},
	}}}})
	if err == nil || !strings.Contains(err.Error(), "QQ") {
		t.Errorf("超大文件应被拒绝并提示，err=%v", err)
	}

	incomplete := "false"
	if _, err := videoSource(Object{"videos": Object{"list": []any{Object{
		"source":         "https://cdn/pending.mp4",
		"size":           float64(1024),
		"encodingOption": Object{"isEncodingComplete": incomplete, "height": float64(1080)},
	}}}}); err == nil {
		t.Error("未完成转码的清晰度应被拒绝")
	}

	// 完整可播放时应选最高清晰度
	ok := "true"
	got, err := videoSource(Object{"videos": Object{"list": []any{
		Object{"source": "https://cdn/low.mp4", "size": float64(2048),
			"encodingOption": Object{"isEncodingComplete": ok, "height": float64(480)}},
		Object{"source": "https://cdn/high.mp4", "size": float64(4096),
			"encodingOption": Object{"isEncodingComplete": ok, "height": float64(1080)}},
	}}})
	if err != nil {
		t.Fatalf("应解析出直链：%v", err)
	}
	if got != "https://cdn/high.mp4" {
		t.Errorf("应选最高清晰度，实际 %q", got)
	}
}