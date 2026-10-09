package weverse

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

type Community struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Slug    string   `json:"slug"`
	Members []string `json:"members"`
}
type Member struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	Image                      string `json:"image,omitempty"`
	latestMomentPostID         string
	latestMomentAt             int64
	latestMomentMembershipOnly bool
	latestMomentLocked         bool
}
type catalogCache struct {
	At    time.Time   `json:"at"`
	Items []Community `json:"items"`
}

func (c *Client) Search(ctx context.Context, q string) ([]Community, error) {
	var cache catalogCache
	_ = Read(c.Dir, "catalog.json", &cache)
	if len(cache.Items) == 0 || time.Since(cache.At) > time.Hour {
		found := []Community{}
		after := ""
		seen := map[int64]bool{}
		for page := 0; page < 10; page++ {
			var d Object
			ep := "/community/v1.0/groupCommunities?groupKey=ALL-ALL&limit=100&fields=communityId,communityName,urlPath,artistOfficialNames"
			if after != "" {
				ep += "&after=" + url.QueryEscape(after)
			}
			if e := c.call(ctx, ep, false, &d); e != nil {
				return nil, e
			}
			a, ok := d["data"].([]any)
			if !ok {
				return nil, fmt.Errorf("团体目录格式已变更")
			}
			for _, v := range a {
				x := obj(v)
				id := num(x["communityId"])
				if id == 0 || seen[id] {
					continue
				}
				seen[id] = true
				co := Community{ID: id, Name: str(x["communityName"]), Slug: str(x["urlPath"]), Members: []string{}}
				for _, m := range list(obj(x["artistOfficialNames"])["data"]) {
					co.Members = append(co.Members, str(m))
				}
				found = append(found, co)
			}
			next := str(obj(obj(d["paging"])["nextParams"])["after"])
			if next == "" || next == after {
				break
			}
			after = next
			if page == 9 {
				return nil, fmt.Errorf("团体目录分页超出上限，请稍后重试")
			}
		}
		cache = catalogCache{At: time.Now(), Items: found}
		_ = Write(c.Dir, "catalog.json", cache)
	}
	if u, err := url.Parse(strings.TrimSpace(q)); err == nil && (u.Hostname() == "weverse.io" || u.Hostname() == "www.weverse.io") {
		q = strings.Split(strings.Trim(u.Path, "/"), "/")[0]
	}
	q = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(q), " ", ""))
	if q == "h2h" || q == "heartstohearts" {
		q = "hearts2hearts"
	}
	result := []Community{}
	for _, x := range cache.Items {
		hay := strings.ToLower(strings.ReplaceAll(x.Name+" "+x.Slug+" "+strings.Join(x.Members, " "), " ", ""))
		if q == "" || strings.Contains(hay, q) {
			result = append(result, x)
		}
	}
	return result, nil
}

// membersCacheTTL 是成员名单的兜底刷新间隔。名单本身几乎不变，但保留一个
// 长 TTL 兜底，以便新成员加入 / 改名后仍能自动生效。
const membersCacheTTL = 10 * time.Minute

type membersCacheEntry struct {
	at   time.Time
	list []Member
}

// cachedMembers 返回新鲜缓存的成员名单；没有则返回 nil。
// 缓存挂在 Client 上（而非包级全局），这样不同 Client 互不干扰——
// 每个社区一个 Client 是既有结构，测试里多个桩 Client 也能各自独立。
func (c *Client) cachedMembers(id int64) []Member {
	c.membersMu.Lock()
	defer c.membersMu.Unlock()
	entry, ok := c.members[id]
	if !ok || time.Since(entry.at) > membersCacheTTL || len(entry.list) == 0 {
		return nil
	}
	return entry.list
}

func (c *Client) storeMembersCache(id int64, list []Member) {
	c.membersMu.Lock()
	defer c.membersMu.Unlock()
	if c.members == nil {
		c.members = map[int64]*membersCacheEntry{}
	}
	c.members[id] = &membersCacheEntry{at: time.Now(), list: list}
}

// InvalidateMembers 丢弃该 Client 对某社区的成员名单缓存，下一次会回源。
func (c *Client) InvalidateMembers(id int64) {
	c.membersMu.Lock()
	defer c.membersMu.Unlock()
	delete(c.members, id)
}

// PrimeMembersFromHistory 用本地历史库里的成员名单预热缓存。bot 启动时立刻可用，
// 无需等待一次网络请求；缓存过期后仍会回源刷新。
func (c *Client) PrimeMembersFromHistory(h *History, id int64) bool {
	if h == nil {
		return false
	}
	list, err := h.Members(id)
	if err != nil || len(list) == 0 {
		return false
	}
	c.storeMembersCache(id, list)
	return true
}

// Members 返回社区成员名单。名单变化极少，因此默认走缓存（TTL 10 分钟）；
// 需要强制刷新时调用 InvalidateMembers。
func (c *Client) Members(ctx context.Context, id int64) ([]Member, error) {
	if list := c.cachedMembers(id); list != nil {
		return list, nil
	}
	list, err := c.fetchMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	c.storeMembersCache(id, list)
	return list, nil
}

func (c *Client) fetchMembers(ctx context.Context, id int64) ([]Member, error) {
	var data any
	e := c.call(ctx, fmt.Sprintf("/member/v1.1/community-%d/artistMembers?fieldSet=artistMembersV1&filterType=MOMENT", id), true, &data)
	if e != nil {
		return nil, e
	}
	a := list(data)
	if a == nil {
		a = list(obj(data)["data"])
	}
	if a == nil {
		return nil, fmt.Errorf("成员列表格式已变更")
	}
	result := []Member{}
	for _, v := range a {
		x := obj(v)
		id := text(x, "memberId", "id")
		if id != "" {
			official := obj(x["artistOfficialProfile"])
			name := text(official, "officialName")
			if name == "" {
				name = text(x, "profileName", "name")
			}
			image := text(official, "officialImageUrl")
			if image == "" {
				image = str(x["profileImageUrl"])
			}
			if name == "" {
				return nil, fmt.Errorf("成员名称格式已变更")
			}
			latest := obj(x["artistLatestMoment"])
			membershipOnly, _ := latest["membershipOnly"].(bool)
			locked, _ := latest["locked"].(bool)
			result = append(result, Member{ID: id, Name: name, Image: image, latestMomentPostID: str(latest["postId"]), latestMomentAt: num(latest["publishedAt"]), latestMomentMembershipOnly: membershipOnly, latestMomentLocked: locked})
		}
	}
	return result, nil
}
