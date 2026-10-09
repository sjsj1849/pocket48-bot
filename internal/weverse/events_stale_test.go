package weverse

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestEventsSkipsStaleActivities 回归测试：活动流第一页里早于 since 的条目
// 必须被跳过，不为其回帖拉详情。
//
// pagesLimit 只用时间决定「是否继续翻页」，并不会丢弃整页里早于 since 的条目
// （实测一页 100 条可横跨数天）。若 Events 不过滤，就会为早已处理过的历史活动
// 反复请求 /post/v1.0/post-<id>：实测每轮多耗约 10 秒、79 次无效请求。
func TestEventsSkipsStaleActivities(t *testing.T) {
	now := time.Now().UnixMilli()
	stale := now - int64(72*time.Hour/time.Millisecond) // 3 天前
	fresh := now - int64(4*time.Minute/time.Millisecond)
	artist := Object{"memberId": "carmen", "profileName": "CARMEN"}

	var staleFetch, freshFetch atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/member/v1.1/community-235/artistMembers":
			respond(w, Object{"data": []any{artist}})
		case "/noti/feed/v2.0/activities":
			respond(w, Object{"data": []any{
				Object{"activityId": "old", "activityType": "ARTIST_COMMENT",
					"webUrl": "https://weverse.io/hearts2hearts/fanpost/1-stale/comment/fan", "time": stale},
				Object{"activityId": "new", "activityType": "ARTIST_COMMENT",
					"webUrl": "https://weverse.io/hearts2hearts/fanpost/1-fresh/comment/fan", "time": fresh},
			}})
		case "/comment/v1.0/member-carmen/comments":
			respond(w, Object{"data": []any{}})
		case "/post/v1.0/post-1-stale":
			staleFetch.Add(1)
			respond(w, Object{"body": "stale post"})
		case "/post/v1.0/post-1-fresh":
			freshFetch.Add(1)
			respond(w, Object{"body": "fresh post"})
		default:
			respond(w, Object{"data": []any{}})
		}
	})

	mustWrite(t, c.Dir, "settings.json", Settings{
		Enabled: true,
		Subscriptions: []Subscription{{
			ID: "s1", CommunityID: 235, CommunityName: "Hearts2Hearts",
			Slug: "hearts2hearts", Enabled: true, Posts: true, Comments: true,
		}},
	})
	mustWrite(t, c.Dir, "status.json", Status{
		LastSuccess: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
	})

	if _, err := c.Events(context.Background()); err != nil {
		t.Fatal(err)
	}
	if staleFetch.Load() != 0 {
		t.Errorf("3 天前的历史活动不应触发回帖拉详情，实际请求 %d 次", staleFetch.Load())
	}
	if freshFetch.Load() == 0 {
		t.Error("窗口内的新活动必须正常回帖拉详情")
	}
}

// TestPostDetailCached 验证帖子详情缓存生效：同一 postID 连续两次只发一次请求。
func TestPostDetailCached(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/post/v1.0/post-9-9":
			calls.Add(1)
			respond(w, Object{"body": "cached body"})
		default:
			respond(w, Object{"data": []any{}})
		}
	})

	for i := 0; i < 3; i++ {
		body, err := c.postDetail(context.Background(), "9-9")
		if err != nil {
			t.Fatal(err)
		}
		if text(body, "body") != "cached body" {
			t.Fatalf("第 %d 次返回内容错误: %q", i+1, text(body, "body"))
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("帖子正文不可变，3 次调用应只发 1 个请求，实际 %d 个", n)
	}
}
