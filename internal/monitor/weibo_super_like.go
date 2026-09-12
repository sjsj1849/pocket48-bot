package monitor

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Only an explicit count or the API's empty-ranking marker establishes a value.
// Missing cards, expired sessions and malformed responses must not become zero.
func parseMWeiboSuperLike(body []byte) (int, error) {
	type card struct {
		ItemID    string `json:"itemid"`
		Desc      string `json:"desc"`
		CardGroup []struct {
			ItemID string `json:"itemid"`
			Desc   string `json:"desc"`
		} `json:"card_group"`
	}
	var payload struct {
		OK   int `json:"ok"`
		Data struct {
			Cards []card `json:"cards"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("invalid chaolike JSON")
	}
	if payload.OK != 1 {
		return 0, fmt.Errorf("chaolike response not successful")
	}
	countRE := regexp.MustCompile(`(?i)超LIKE(?:榜)?\s*[(（]\s*([0-9][0-9,]*)\s*人\s*[)）]`)
	empty := false
	parse := func(id, desc string) (int, bool) {
		if id == "badge_chaolike_record_empty" {
			empty = true
		}
		if matches := countRE.FindStringSubmatch(desc); len(matches) == 2 {
			n, err := strconv.Atoi(strings.ReplaceAll(matches[1], ",", ""))
			return n, err == nil
		}
		return 0, false
	}
	for _, c := range payload.Data.Cards {
		if n, ok := parse(c.ItemID, c.Desc); ok {
			return n, nil
		}
		for _, child := range c.CardGroup {
			if n, ok := parse(child.ItemID, child.Desc); ok {
				return n, nil
			}
		}
	}
	if empty {
		return 0, nil
	}
	return 0, fmt.Errorf("chaolike count missing")
}

func (m *WeiboMonitor) enrichSuperLikeFromMWeibo(res *WeiboSuperCountResult, baseID string) {
	if res == nil || res.SuperLikeKnown || res.SuperLikeCount > 0 {
		return
	}
	baseID = strings.TrimPrefix(strings.TrimSpace(baseID), "1022:")
	if !strings.HasPrefix(baseID, "100808") || len(baseID) <= 6 {
		return
	}
	m.mu.RLock()
	cookie := buildWeiboCookieHeader(m.MWeiboCookie)
	if cookie == "" {
		cookie = buildWeiboCookieHeader(m.Cookie)
	}
	m.mu.RUnlock()
	if cookie == "" {
		return
	}
	containerID := "231140" + strings.TrimPrefix(baseID, "100808") + "_-_chaolikenew"
	req, err := http.NewRequest(http.MethodGet, "https://m.weibo.cn/api/container/getIndex?containerid="+url.QueryEscape(containerID), nil)
	if err != nil {
		return
	}
	applyWeiboRequestHeaders(req, cookie)
	req.Header.Set("Referer", "https://m.weibo.cn/p/index?containerid="+containerID)
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[Weibo][SuperLike] fetch failed oid=%s", baseID)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("[Weibo][SuperLike] http=%d oid=%s", resp.StatusCode, baseID)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return
	}
	n, err := parseMWeiboSuperLike(body)
	if err != nil {
		log.Printf("[Weibo][SuperLike] oid=%s: %v", baseID, err)
		return
	}
	res.SuperLikeCount, res.SuperLikeKnown = n, true
	res.SuperLikeText = fmt.Sprintf("超LIKE %d人", n)
}
