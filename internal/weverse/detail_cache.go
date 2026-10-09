package weverse

import (
	"context"
	"sync"
	"time"
)

// 帖子与父评论的正文一经发布就不再变化（只有评论会新增），而通知流兜底
// 每轮都会重复回帖拉同一批帖子的详情。实测一轮活动流就带来 79 次
// /post/v1.0/post-<id> 请求、约 10 秒。这里按 postID/commentID 做进程内缓存，
// TTL 只用于兜住"帖子被删除"之类的边界情况，正常情况下命中一次就不再请求。

const (
	postDetailTTL    = 30 * time.Minute
	commentDetailTTL = 30 * time.Minute

	// detailCacheMax 限制缓存条目数，避免长期运行后无界增长。超出后按
	// 插入顺序淘汰最旧的（见 detailCacheStore）。
	detailCacheMax = 4096
)

type detailEntry struct {
	at   time.Time
	body Object
}

// detailCache 同时缓存 post 与 comment 详情，key 加前缀区分。
type detailCache struct {
	mu      sync.Mutex
	entries map[string]*detailEntry
	order   []string
}

func newDetailCache() *detailCache {
	return &detailCache{entries: make(map[string]*detailEntry)}
}

func (d *detailCache) get(key string, ttl time.Duration) (Object, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > ttl {
		delete(d.entries, key)
		return nil, false
	}
	return e.body, true
}

func (d *detailCache) put(key string, body Object) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.entries[key]; !exists {
		d.order = append(d.order, key)
	}
	d.entries[key] = &detailEntry{at: time.Now(), body: body}
	for len(d.order) > detailCacheMax {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.entries, oldest)
	}
}

// postDetail 返回帖子详情，优先走缓存。
func (c *Client) postDetail(ctx context.Context, postID string) (Object, error) {
	key := "post:" + postID
	if body, ok := c.details.get(key, postDetailTTL); ok {
		return body, nil
	}
	var body Object
	if err := c.call(ctx, "/post/v1.0/post-"+postID+"?fieldSet=postV1", true, &body); err != nil {
		return nil, err
	}
	c.details.put(key, body)
	return body, nil
}

// commentDetail 返回单条评论详情，优先走缓存。
func (c *Client) commentDetail(ctx context.Context, commentID string) (Object, error) {
	key := "comment:" + commentID
	if body, ok := c.details.get(key, commentDetailTTL); ok {
		return body, nil
	}
	var body Object
	if err := c.call(ctx, "/comment/v1.0/comment-"+commentID+"?fieldSet=commentV1", true, &body); err != nil {
		return nil, err
	}
	c.details.put(key, body)
	return body, nil
}
