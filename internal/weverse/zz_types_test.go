package weverse

import (
	"context"
	"testing"
	"time"
)

// TestZZTypes 统计「活动流里需要回帖」的那些活动到底是什么类型，
// 以及 member 维度对 comment 类型的覆盖是否完整。验证完即删。
func TestZZTypes(t *testing.T) {
	dir := Dir("/root/pocket48-bot/config.json")
	cfg, _ := LoadSettings(dir)
	c := NewClient(dir, cfg.ProxyURL)
	defer c.HTTP.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 用 24 小时窗口，保证有样本
	since := time.Now().Add(-24 * time.Hour)

	members, _ := c.Members(ctx, 235)
	ids := map[string]bool{}
	for _, m := range members {
		ids[m.ID] = true
	}

	// member 维度覆盖的"评论所在帖"
	memberCommentPosts := map[string]bool{}
	for _, m := range members {
		raw, err := c.pages(ctx, "/comment/v1.0/member-"+m.ID+"/comments?fieldSet=memberCommentsV1&sortType=LATEST&limit=100", "after", "createdAt", since)
		if err != nil {
			continue
		}
		for _, item := range raw {
			x := obj(item)
			if !ids[text(obj(x["author"]), "memberId", "id")] {
				continue
			}
			memberCommentPosts[memberCommentPostID(x)] = true
		}
	}
	t.Logf("member 维度覆盖的评论帖 = %d 个", len(memberCommentPosts))

	acts, err := c.pages(ctx, "/noti/feed/v2.0/activities?communityId=235&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", "next", "time", since)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("活动流总数 = %d", len(acts))

	all := map[string]int{}
	needFetch := map[string]int{}
	covered := 0
	for _, raw := range acts {
		n := obj(raw)
		typ := text(n, "activityType", "type")
		_, postID, _ := notificationTarget(n)
		all[typ]++
		if postID == "" {
			continue
		}
		isMoment := containsUpper(typ, "MOMENT") && !containsUpper(typ, "COMMENT")
		isComment := containsUpper(typ, "COMMENT") || containsUpper(typ, "REPLY")
		if isMoment {
			needFetch[typ]++
			continue
		}
		if isComment {
			if memberCommentPosts[postID] {
				covered++
			} else {
				needFetch[typ+" (评论未覆盖)"]++
			}
		}
	}

	t.Logf("--- 活动流类型分布 ---")
	for k, v := range all {
		t.Logf("  %-28s %d", k, v)
	}
	t.Logf("--- comment 类被 member 覆盖 = %d ---", covered)
	t.Logf("--- 仍需回帖的分布 ---")
	for k, v := range needFetch {
		t.Logf("  %-40s %d", k, v)
	}
}

func containsUpper(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	up := func(x byte) byte {
		if x >= 'a' && x <= 'z' {
			return x - 32
		}
		return x
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			if up(s[i+j]) != up(sub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func memberCommentPostID(cmt Object) string {
	root := obj(obj(cmt["root"])["data"])
	if id := text(root, "postId", "id"); id != "" {
		return id
	}
	return text(obj(obj(cmt["parent"])["data"]), "postId")
}
