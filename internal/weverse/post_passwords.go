package weverse

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

var ErrPostPassword = errors.New("帖子密码不正确")
var postPasswordMu sync.Mutex

type PostPassword struct {
	PostID     string `json:"postId"`
	URL        string `json:"url"`
	Password   string `json:"password,omitempty"`
	VerifiedAt string `json:"verifiedAt"`
}

func ParsePostURL(raw string) (string, string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Scheme != "https" || u.Host != "weverse.io" || u.User != nil {
		return "", "", fmt.Errorf("请填写 Weverse 帖子链接")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 || !slugRE.MatchString(parts[0]) || (parts[1] != "artist" && parts[1] != "fanpost") || !idRE.MatchString(parts[2]) {
		return "", "", fmt.Errorf("请填写 Weverse 帖子链接，支持带评论的链接")
	}
	if len(parts) != 3 && !(len(parts) == 5 && parts[3] == "comment" && idRE.MatchString(parts[4])) {
		return "", "", fmt.Errorf("帖子链接格式不正确")
	}
	return parts[2], "https://weverse.io/" + strings.Join(parts[:3], "/"), nil
}
func LoadPostPasswords(dir string) ([]PostPassword, error) {
	rows := []PostPassword{}
	e := Read(dir, "post-passwords.json", &rows)
	return rows, e
}
func (c *Client) withPostPassword(ep string) (string, error) {
	u, e := url.Parse(ep)
	if e != nil {
		return ep, e
	}
	const prefix = "/post/v1.0/post-"
	if !strings.HasPrefix(u.Path, prefix) {
		return ep, nil
	}
	id := strings.TrimPrefix(u.Path, prefix)
	if !idRE.MatchString(id) {
		return ep, nil
	}
	q := u.Query()
	if q.Has("lockPassword") {
		return ep, nil
	}
	rows, e := LoadPostPasswords(c.Dir)
	if e != nil {
		return "", fmt.Errorf("无法读取帖子密码配置")
	}
	for _, row := range rows {
		if row.PostID == id {
			q.Set("lockPassword", row.Password)
			u.RawQuery = q.Encode()
			return u.String(), nil
		}
	}
	return ep, nil
}

// Verify before persisting. Explicit candidates override any previous stored password.
func (c *Client) SavePostPassword(ctx context.Context, raw, password string) (PostPassword, error) {
	id, link, e := ParsePostURL(raw)
	if e != nil {
		return PostPassword{}, e
	}
	if len(password) == 0 || len(password) > 256 {
		return PostPassword{}, fmt.Errorf("请输入帖子密码（最多 256 字节）")
	}
	q := url.Values{"fieldSet": {"postV1"}, "lockPassword": {password}}
	var post Object
	if e = c.call(ctx, "/post/v1.0/post-"+id+"?"+q.Encode(), true, &post); e != nil {
		return PostPassword{}, e
	}
	if text(post, "postId", "id") != id {
		return PostPassword{}, fmt.Errorf("接口未返回目标帖子，未保存密码")
	}
	row := PostPassword{PostID: id, URL: link, Password: password, VerifiedAt: time.Now().Format(time.RFC3339)}
	postPasswordMu.Lock()
	defer postPasswordMu.Unlock()
	rows, e := LoadPostPasswords(c.Dir)
	if e != nil {
		return PostPassword{}, e
	}
	found := false
	for i := range rows {
		if rows[i].PostID == id {
			rows[i] = row
			found = true
		}
	}
	if !found {
		if len(rows) >= 200 {
			return PostPassword{}, fmt.Errorf("最多保存 200 条帖子密码")
		}
		rows = append(rows, row)
	}
	if e = Write(c.Dir, "post-passwords.json", rows); e != nil {
		return PostPassword{}, e
	}
	row.Password = ""
	return row, nil
}
func DeletePostPassword(dir, id string) error {
	if !idRE.MatchString(id) {
		return fmt.Errorf("帖子编号不正确")
	}
	postPasswordMu.Lock()
	defer postPasswordMu.Unlock()
	rows, e := LoadPostPasswords(dir)
	if e != nil {
		return e
	}
	next := []PostPassword{}
	for _, row := range rows {
		if row.PostID != id {
			next = append(next, row)
		}
	}
	return Write(dir, "post-passwords.json", next)
}
