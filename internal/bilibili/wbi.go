package bilibili

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WBI 签名：B 站对 x/space/wbi/* 系列接口（用户资料、投稿列表）强制校验
// w_rid 签名，缺签名会被风控判为「-352 风控校验失败」。签名密钥来自
// GenWebTicket，需每天轮换（B 站午夜换 key）。
//
// 实现对齐参考项目 Akokk0/bilibili-notify（packages/api/src/wbi.ts）。

// mixinKeyEncTab 是官方前端固定的混淆表，用于从 img_key+sub_key 派生出 mixin_key。
var mixinKeyEncTab = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35, 27, 43, 5, 49, 33, 9, 42, 19, 29, 28,
	14, 39, 12, 38, 41, 13, 37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4, 22, 25, 54,
	21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

const ticketEndpoint = "https://api.bilibili.com/bapis/bilibili.api.ticket.v1.Ticket/GenWebTicket"

// navEndpoint 返回 wbi_img 密钥对，是 GenWebTicket 失败时的回退路径。
const navEndpoint = "https://api.bilibili.com/x/web-interface/nav"

// wbiKeys 是 GenWebTicket 返回的签名密钥对。
type wbiKeys struct {
	ImgKey string `json:"img_key"`
	SubKey string `json:"sub_key"`
}

func mixinKey(img, sub string) string {
	orig := img + sub
	runes := []rune(orig)
	out := make([]rune, 0, 32)
	for _, idx := range mixinKeyEncTab {
		if idx < len(runes) {
			out = append(out, runes[idx])
		}
		if len(out) == 32 {
			break
		}
	}
	return string(out)
}

// encWbi 按 B 站规则对 query 参数签名，返回可直接拼到 URL 的完整查询串。
func encWbi(params map[string]string, keys wbiKeys) string {
	return encWbiAt(params, keys, time.Now().Unix())
}

// encWbiAt 是 encWbi 的可测试内核：wts 由调用方给定，便于对照测试向量。
func encWbiAt(params map[string]string, keys wbiKeys, wts int64) string {
	mix := mixinKey(keys.ImgKey, keys.SubKey)
	merged := make(map[string]string, len(params)+1)
	for k, v := range params {
		merged[k] = v
	}
	merged["wts"] = strconv.FormatInt(wts, 10)

	names := make([]string, 0, len(merged))
	for k := range merged {
		names = append(names, k)
	}
	sort.Strings(names)

	parts := make([]string, 0, len(names))
	for _, k := range names {
		// 官方前端会剔除值里的 !'()* 再签名。
		v := strings.NewReplacer("!", "", "'", "", "(", "", ")", "", "*", "").Replace(merged[k])
		parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}
	query := strings.Join(parts, "&")
	sum := md5.Sum([]byte(query + mix))
	return query + "&w_rid=" + hex.EncodeToString(sum[:])
}

func wbiKeyFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	base := path.Base(raw)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	return base
}

// ticketResponse 只需 nav.img / nav.sub 两个字段。
type ticketResponse struct {
	Code int `json:"code"`
	Data struct {
		Nav struct {
			Img string `json:"img"`
			Sub string `json:"sub"`
		} `json:"nav"`
	} `json:"data"`
}

// fetchWbiKeys 通过 GenWebTicket 拿到当天的 wbi 密钥。
func (c *Client) fetchWbiKeys(ctx context.Context) (wbiKeys, error) {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte("XgwSnGZ1p"))
	_, _ = mac.Write([]byte("ts" + strconv.FormatInt(ts, 10)))
	hexsign := hex.EncodeToString(mac.Sum(nil))

	values := url.Values{}
	values.Set("key_id", "ec02")
	values.Set("hexsign", hexsign)
	values.Set("context[ts]", strconv.FormatInt(ts, 10))
	values.Set("csrf", "")
	endpoint := ticketEndpoint + "?" + values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(""))
	if err != nil {
		return wbiKeys{}, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cookie := c.cookieHeader(ctx); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return wbiKeys{}, fmt.Errorf("B站签名密钥请求失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return wbiKeys{}, fmt.Errorf("B站签名密钥读取失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return wbiKeys{}, fmt.Errorf("B站签名密钥返回 HTTP %d", resp.StatusCode)
	}
	var ticket ticketResponse
	if err := json.Unmarshal(body, &ticket); err != nil {
		return wbiKeys{}, fmt.Errorf("B站签名密钥无法解析")
	}
	if ticket.Code != 0 {
		return wbiKeys{}, fmt.Errorf("B站签名密钥错误码 %d", ticket.Code)
	}
	keys := wbiKeys{
		ImgKey: wbiKeyFromURL(ticket.Data.Nav.Img),
		SubKey: wbiKeyFromURL(ticket.Data.Nav.Sub),
	}
	if keys.ImgKey == "" || keys.SubKey == "" {
		return wbiKeys{}, fmt.Errorf("B站签名密钥为空")
	}
	return keys, nil
}

// ensureWbiKeys 返回缓存的密钥；force=true 时强制刷新（应对 -352 的 key 轮换）。
func (c *Client) ensureWbiKeys(ctx context.Context, force bool) wbiKeys {
	c.mu.Lock()
	if !force && c.wbi.ImgKey != "" {
		keys := c.wbi
		c.mu.Unlock()
		return keys
	}
	c.mu.Unlock()

	keys, err := c.fetchAnyWbiKeys(ctx)
	if err != nil {
		c.mu.Lock()
		cached := c.wbi
		c.mu.Unlock()
		return cached
	}
	c.mu.Lock()
	c.wbi = keys
	c.mu.Unlock()
	return keys
}

// wbiGet 发起一次带 WBI 签名的 GET。若返回 -352（密钥轮换）则刷新密钥重试一次。
func (c *Client) wbiGet(ctx context.Context, endpoint, referer string, params map[string]string, out any) error {
	attempt := func() error {
		keys := c.ensureWbiKeys(ctx, false)
		if keys.ImgKey == "" {
			// 缓存为空：再走一次双路取密钥（ticket + nav 回退）。
			if refreshed, err := c.fetchAnyWbiKeys(ctx); err == nil {
				c.mu.Lock()
				c.wbi = refreshed
				c.mu.Unlock()
				keys = refreshed
			}
		}
		if keys.ImgKey == "" {
			return fmt.Errorf("B站签名密钥不可用")
		}
		return c.get(ctx, endpoint+"?"+encWbi(params, keys), referer, out)
	}
	err := attempt()
	if err != nil && isRiskControl(err) {
		keys := c.ensureWbiKeys(ctx, true)
		if keys.ImgKey == "" {
			return err
		}
		return c.get(ctx, endpoint+"?"+encWbi(params, keys), referer, out)
	}
	return err
}

// isRiskControl 判定是否为「签名失效」类错误（-352 风控校验失败）。
func isRiskControl(err error) bool {
	var target *RiskControlError
	return errors.As(err, &target)
}

// navWbiKeys 通过 x/web-interface/nav 拿 wbi_img。
//
// 为什么需要回退：GenWebTicket 那条路在拿到 SESSDATA 之后反而会失败
// （它要求 csrf 与登录态匹配，匿名时反而能通）。实测登录态下 nav 能稳定
// 返回 wbi_img.img_url / sub_url，所以登录用户一律走这条回退路径。
func (c *Client) fetchWbiKeysFromNav(ctx context.Context) (wbiKeys, error) {
	var nav struct {
		Code int `json:"code"`
		Data struct {
			WbiImg struct {
				ImgURL string `json:"img_url"`
				SubURL string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}
	// nav 的 wbi_img 只有在请求带足身份信息（buvid3 + 登录 Cookie）时才会返回，
	// 所以这里不能走普通的 c.get（它不带 Cookie），必须自己拼请求头。
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, navEndpoint, nil)
	if err != nil {
		return wbiKeys{}, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cookie := c.cookieHeader(ctx); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return wbiKeys{}, fmt.Errorf("nav 请求失败：%v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return wbiKeys{}, fmt.Errorf("nav 读取失败：%v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return wbiKeys{}, fmt.Errorf("nav 返回 HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, &nav); err != nil {
		return wbiKeys{}, fmt.Errorf("nav 无法解析")
	}
	if nav.Code != 0 {
		return wbiKeys{}, fmt.Errorf("nav 接口错误码 %d", nav.Code)
	}
	keys := wbiKeys{
		ImgKey: wbiKeyFromURL(nav.Data.WbiImg.ImgURL),
		SubKey: wbiKeyFromURL(nav.Data.WbiImg.SubURL),
	}
	if keys.ImgKey == "" || keys.SubKey == "" {
		return wbiKeys{}, fmt.Errorf("nav 接口未返回 wbi 密钥")
	}
	return keys, nil
}

// fetchAnyWbiKeys 依次尝试两条取密钥的路，任一成功即可。
func (c *Client) fetchAnyWbiKeys(ctx context.Context) (wbiKeys, error) {
	keys, ticketErr := c.fetchWbiKeys(ctx)
	if ticketErr == nil {
		return keys, nil
	}
	keys, navErr := c.fetchWbiKeysFromNav(ctx)
	if navErr == nil {
		return keys, nil
	}
	return wbiKeys{}, fmt.Errorf("%v；nav 回退也失败：%v", ticketErr, navErr)
}
