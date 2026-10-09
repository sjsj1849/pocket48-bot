package weverse

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLiveDuplicatePostProbe 直接打 Weverse API 验两个 postId 是否各自独立存在。
//
// 背景：2026-10-09 00:36，YUHA 同一内容（memberId / 正文 / 图片文件名全同，
// 时间差 610ms）被推了两次，postId 分别是 3-242338419 与 4-242329429。
// 采集侧只按 event.ID(=post:<postId>) 去重，两个 id 不同所以都推了。
//
// 这个探针要回答的是「这是不是我们把同一条读了两遍」：
// 若两个 postId 各自能被 Weverse 查到内容，就是平台上真的两条帖子。
func TestLiveDuplicatePostProbe(t *testing.T) {
	if os.Getenv("LIVE_VERSE") == "" {
		t.Skip("设置 LIVE_VERSE=1 才跑")
	}
	dir := Dir("/root/pocket48-bot/config.json")
	client := NewClient(dir, "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for _, id := range []string{"3-242338419", "4-242329429"} {
		obj, err := client.postDetail(ctx, id)
		if err != nil {
			t.Errorf("postDetail(%s) 失败: %v", id, err)
			continue
		}
		p, _ := obj["post"].(map[string]any)
		author, _ := obj["author"].(map[string]any)
		t.Logf("---- post-%s ----", id)
		t.Logf("  postId     = %v", obj["postId"])
		t.Logf("  memberId   = %v", author["memberId"])
		t.Logf("  authorName = %v", author["profileName"])
		t.Logf("  publishedAt= %v", obj["publishedAt"])
		t.Logf("  plainBody  = %v", obj["plainBody"])
		ext, _ := p["extensions"].(map[string]any)
		b, _ := json.Marshal(ext)
		t.Logf("  extensions = %.900s", string(b))
	}
}
