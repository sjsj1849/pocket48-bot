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
	ID    string `json:"id"`
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
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
func (c *Client) Members(ctx context.Context, id int64) ([]Member, error) {
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
			result = append(result, Member{ID: id, Name: str(x["profileName"]), Image: str(x["profileImageUrl"])})
		}
	}
	return result, nil
}
