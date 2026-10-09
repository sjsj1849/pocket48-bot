package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 真实样本：mid=5351887681618773，2026-10-08 用线上 Cookie 实测。
//
//	ok=1, page_info.page_pic = null, data.pics 有 6 项,
//	data.text 里没有任何 .jpg（text_has_jpg=False）。
//
// ★ 这正是「飞书只提取到文本、没有任何图片」的那条微博。
// 旧实现只从 page_info.page_pic 取封面、再从正文正则抓 jpg，两条路都落空。
const realSixPicShow = `{
  "ok": 1,
  "data": {
    "id": "5351887681618773",
    "text": "生日快乐我们的ian[爱你][爱你]<span class=\"url-icon\"><img src=\"/att/att2.gif\"></span>",
    "user": {"screen_name": "Hearts2Hearts"},
    "page_pic": null,
    "page_info": {"object_type": null},
    "pics": [
      {"pid":"007j5mi6gy1ihvcx1rt65j30u015i14l","size":"l",
       "url":"https://wx3.sinaimg.cn/orj360/007j5mi6gy1ihvcx1rt65j30u015i14l.jpg",
       "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/007j5mi6gy1ihvcx1rt65j30u015i14l.jpg","geo":{"width":"1080","height":"1494"}}},
      {"pid":"007j5mi6gy1ihvcx2su68j30u012twov","size":"l",
       "url":"https://wx2.sinaimg.cn/orj360/007j5mi6gy1ihvcx2su68j30u012twov.jpg",
       "large":{"size":"large","url":"https://wx2.sinaimg.cn/mw2000/007j5mi6gy1ihvcx2su68j30u012twov.jpg","geo":{"width":"1080","height":"1397"}}},
      {"pid":"p3","url":"https://wx3.sinaimg.cn/orj360/p3.jpg",
       "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/p3.jpg"}},
      {"pid":"p4","url":"https://wx3.sinaimg.cn/orj360/p4.jpg",
       "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/p4.jpg"}},
      {"pid":"p5","url":"https://wx3.sinaimg.cn/orj360/p5.jpg",
       "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/p5.jpg"}},
      {"pid":"p6","url":"https://wx3.sinaimg.cn/orj360/p6.jpg",
       "large":{"size":"large","url":"https://wx3.sinaimg.cn/mw2000/p6.jpg"}}
    ]
  }
}`

func newWeiboFixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 回归：6 图微博必须出 6 条 image，且用 large（mw2000）而不是缩略图（orj360）。
func TestWeiboMultiPicPostYieldsEveryImage(t *testing.T) {
	orig := weiboShowURL
	srv := newWeiboFixtureServer(t, realSixPicShow)
	weiboShowURL = srv.URL + "?id="
	defer func() { weiboShowURL = orig }()

	r := &WeiboResolver{Cookie: "SUB=test"}
	post, err := r.Resolve(context.Background(), "https://weibo.com/6694957538/5351887681618773")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(post.Items) != 6 {
		t.Fatalf("图片条数 = %d，期望 6（旧实现会给 0）", len(post.Items))
	}
	for i, item := range post.Items {
		if item.Kind != "image" {
			t.Errorf("item[%d].Kind = %q，期望 image", i, item.Kind)
		}
		if want := "/mw2000/"; !strings.Contains(item.URL, want) {
			t.Errorf("item[%d].URL = %q，期望含 %q（拿到了缩略图）", i, item.URL, want)
		}
	}
	// 封面回填成第一张大图，卡片才有缩略图可显示。
	if post.Cover != post.Items[0].URL {
		t.Errorf("Cover = %q，期望等于第一张图 %q", post.Cover, post.Items[0].URL)
	}
}

// 实况图形状：pics[].videoSrc 有视频时，视频单独成条。
// 该字段取自监控侧 internal/monitor/weibo.go 已验证的形状；
// statuses/show 2026-10-08 实测未下发，这里锁住行为避免以后改坏。
func TestWeiboLivePhotoEmitsVideoItem(t *testing.T) {
	body := `{"ok":1,"data":{"id":"1","text":"live","user":{"screen_name":"a"},
      "page_info":{"object_type":null},
      "pics":[{"url":"https://wx1.sinaimg.cn/orj360/x.jpg",
               "large":{"size":"large","url":"https://wx1.sinaimg.cn/mw2000/x.jpg"},
               "type":"video",
               "videoSrc":"https://video.weibocdn.com/live.mp4"}]}}`
	orig := weiboShowURL
	srv := newWeiboFixtureServer(t, body)
	weiboShowURL = srv.URL + "?id="
	defer func() { weiboShowURL = orig }()

	r := &WeiboResolver{Cookie: "SUB=test"}
	post, err := r.Resolve(context.Background(), "https://weibo.com/1/1234567890123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(post.Items) != 1 || post.Items[0].Kind != "video" {
		t.Fatalf("Items = %+v，期望 1 条 video", post.Items)
	}
	if post.Items[0].URL != "https://video.weibocdn.com/live.mp4" {
		t.Errorf("video URL = %q", post.Items[0].URL)
	}
	if post.Items[0].Cover != "https://wx1.sinaimg.cn/mw2000/x.jpg" {
		t.Errorf("video Cover = %q，期望静态大图", post.Items[0].Cover)
	}
}

// video 帖不受影响：仍只出一条 video，图片不重复发。
func TestWeiboVideoPostUnchanged(t *testing.T) {
	body := `{"ok":1,"data":{"id":"1","text":"v","user":{"screen_name":"a"},
      "page_info":{"page_pic":{"url":"https://wx1.sinaimg.cn/orj480/cover.jpg"},
                   "object_type":"video",
                   "urls":{"mp4_720p_mp4":"https://f.video.weibocdn.com/720.mp4"}},
      "pics":[{"url":"https://wx1.sinaimg.cn/orj360/a.jpg",
               "large":{"size":"large","url":"https://wx1.sinaimg.cn/mw2000/a.jpg"}}]}}`
	orig := weiboShowURL
	srv := newWeiboFixtureServer(t, body)
	weiboShowURL = srv.URL + "?id="
	defer func() { weiboShowURL = orig }()

	r := &WeiboResolver{Cookie: "SUB=test"}
	post, err := r.Resolve(context.Background(), "https://weibo.com/1/1234567890123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(post.Items) != 1 || post.Items[0].Kind != "video" {
		t.Fatalf("Items = %+v，期望 1 条 video", post.Items)
	}
	if post.Items[0].URL != "https://f.video.weibocdn.com/720.mp4" {
		t.Errorf("video URL = %q，期望 720p", post.Items[0].URL)
	}
}

// 任何图片都没有时必须返回 0 条（不能凭空造一条）。
func TestWeiboNoMediaYieldsNoItems(t *testing.T) {
	body := `{"ok":1,"data":{"id":"1","text":"纯文字","user":{"screen_name":"a"},
      "page_info":{"object_type":null},"pics":null}}`
	orig := weiboShowURL
	srv := newWeiboFixtureServer(t, body)
	weiboShowURL = srv.URL + "?id="
	defer func() { weiboShowURL = orig }()

	r := &WeiboResolver{Cookie: "SUB=test"}
	post, err := r.Resolve(context.Background(), "https://weibo.com/1/1234567890123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(post.Items) != 0 {
		t.Fatalf("Items = %+v，期望空", post.Items)
	}
}

// 样本本身必须是合法 JSON 且确实没有 page_pic ——
// 否则测试会因为「样本写错了」而假绿。
func TestRealSixPicSampleShape(t *testing.T) {
	var out weiboShowResponse
	if err := json.Unmarshal([]byte(realSixPicShow), &out); err != nil {
		t.Fatalf("样本无法反序列化: %v", err)
	}
	if out.Data.PageInfo.PagePic.URL != "" {
		t.Errorf("样本 page_pic 应为空，实际 %q", out.Data.PageInfo.PagePic.URL)
	}
	if strings.Contains(out.Data.Text, ".jpg") {
		t.Errorf("样本正文不应含 .jpg，实际 %q", out.Data.Text)
	}
	if len(out.Data.Pics) != 6 {
		t.Errorf("样本 pics = %d，期望 6", len(out.Data.Pics))
	}
	if out.Data.Pics[0].Large.URL == "" {
		t.Error("样本第一张图缺 large.url")
	}
}
