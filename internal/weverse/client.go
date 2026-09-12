// Package weverse integrates the Weverse web API. Protocol references:
// https://github.com/yt-dlp/yt-dlp/blob/master/yt_dlp/extractor/weverse.py
// and the public Weverse web client. No third-party runtime is required.
package weverse

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("内容已删除或不可见")

var ErrLogin = errors.New("请先在面板浏览器登录 Weverse、加入目标社区，再同步登录态")
var slugRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var idRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

type Client struct {
	Dir         string
	HTTP        *http.Client
	APIBase     string
	AccountBase string
	mu          sync.Mutex
}

func validateProxy(raw string) error {
	if raw == "" {
		return nil
	}
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("代理需为 http(s) 地址")
	}
	return nil
}
func NewClient(dir, proxy string) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		if u, e := url.Parse(proxy); e == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{Dir: dir, HTTP: &http.Client{Timeout: 20 * time.Second, Transport: tr}, APIBase: "https://global.apis.naver.com/weverse/wevweb", AccountBase: "https://accountapi.weverse.io"}
}
func signedPath(ep string, now time.Time) string {
	u, _ := url.Parse(ep)
	q := u.Query()
	q.Set("appId", "be4d79eb8fc7bd008ee82c8ec4ff6fd4")
	q.Set("language", "en")
	q.Set("os", "WEB")
	q.Set("platform", "WEB")
	q.Set("wpf", "pc")
	u.RawQuery = q.Encode()
	p := u.String()
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	prefix := p
	if len(prefix) > 255 {
		prefix = prefix[:255]
	}
	mac := hmac.New(sha1.New, []byte("1b9cb6378d959b45714bec49971ade22e6e24e42"))
	mac.Write([]byte(prefix + ts))
	return p + "&wmsgpad=" + ts + "&wmd=" + url.QueryEscape(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}
func (c *Client) request(ctx context.Context, method, u string, payload any, headers map[string]string) ([]byte, int, error) {
	var data io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, 0, e
		}
		data = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, u, data)
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Origin", "https://weverse.io")
	req.Header.Set("Referer", "https://weverse.io/")
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return nil, 0, fmt.Errorf("Weverse 连接失败，请检查网络或代理")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return b, resp.StatusCode, e
}
func tokenExpires(token string) time.Time {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return time.Time{}
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return time.Time{}
	}
	var v struct {
		Exp int64 `json:"exp"`
	}
	json.Unmarshal(b, &v)
	return time.Unix(v.Exp, 0)
}
func (c *Client) refresh(ctx context.Context, s *Session) error {
	if s.RefreshToken == "" {
		return ErrLogin
	}
	b, code, e := c.request(ctx, "POST", c.AccountBase+"/api/v1/token/refresh", map[string]string{"refreshToken": s.RefreshToken}, map[string]string{"X-ACC-APP-SECRET": "5419526f1c624b38b10787e5c10b2a7a", "X-ACC-SERVICE-ID": "weverse", "X-ACC-TRACE-ID": strconv.FormatInt(time.Now().UnixNano(), 10)})
	if e != nil {
		return e
	}
	if code != 200 {
		return ErrLogin
	}
	var next Session
	if json.Unmarshal(b, &next) != nil || next.AccessToken == "" {
		return ErrLogin
	}
	s.AccessToken = next.AccessToken
	if next.RefreshToken != "" {
		s.RefreshToken = next.RefreshToken
	}
	return Write(c.Dir, "session.json", *s)
}
func (c *Client) call(ctx context.Context, ep string, auth bool, out any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var s Session
	if auth {
		unlock, err := sessionLock(ctx, c.Dir)
		if err != nil {
			return err
		}
		defer unlock()
		if e := Read(c.Dir, "session.json", &s); e != nil {
			return e
		}
		if s.AccessToken == "" || (!tokenExpires(s.AccessToken).IsZero() && time.Until(tokenExpires(s.AccessToken)) < time.Minute) {
			if e := c.refresh(ctx, &s); e != nil {
				return e
			}
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		headers := map[string]string{"WEV-timezone-id": "Asia/Shanghai", "WEV-device-Id": s.DeviceID}
		if auth {
			headers["Authorization"] = "Bearer " + s.AccessToken
		}
		b, code, e := c.request(ctx, "GET", c.APIBase+signedPath(ep, time.Now()), nil, headers)
		if e != nil {
			return e
		}
		if code == 401 && auth && attempt == 0 {
			if e := c.refresh(ctx, &s); e != nil {
				return e
			}
			continue
		}
		if code == 401 || code == 403 {
			return ErrLogin
		}
		if code == 404 || code == 410 {
			return ErrNotFound
		}
		if code != 200 {
			return fmt.Errorf("Weverse 接口返回 HTTP %d", code)
		}
		if e = json.Unmarshal(b, out); e != nil {
			return fmt.Errorf("Weverse 返回了无法识别的数据")
		}
		return nil
	}
	return ErrLogin
}

type Object = map[string]any

func obj(v any) Object { m, _ := v.(map[string]any); return m }
func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	}
	return ""
}
func num(v any) int64  { n, _ := strconv.ParseInt(str(v), 10, 64); return n }
func list(v any) []any { a, _ := v.([]any); return a }
func text(m Object, keys ...string) string {
	for _, k := range keys {
		if s := str(m[k]); s != "" {
			return s
		}
	}
	return ""
}
