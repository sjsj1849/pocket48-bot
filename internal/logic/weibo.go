package logic

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/outbound"
)

func (b *Bot) notifyWeiboCookieInvalid(uid string) {
	// First try browser cookie refresh; only email after recovery fails.
	// Parallel alert+refresh was spamming when refresh would have succeeded.
	if b.cfg.WeiboBrowserAuthEnabled && b.weiboAuth != nil {
		if got, err := b.weiboAuth.RequestRefreshAndWait("cookie_invalid", 50*time.Second); err != nil {
			log.Printf("[Weibo-auth] request refresh after cookie invalid: %v", err)
		} else {
			log.Printf("[Weibo-auth] cookie refresh wait done uid=%s gotCookies=%v", uid, got)
			// Give monitor a moment to pick up hot-updated cookies.
			time.Sleep(2 * time.Second)
		}
	}

	webOK, webDetail, _ := b.weiboMonitor.CheckWebCookie(uid)
	mwebOK, mwebDetail, _ := b.weiboMonitor.CheckMWeiboCookie(uid)
	if webOK || mwebOK {
		log.Printf("[Weibo] UID %s cookie recovered after refresh (no email): web=%v(%s) mweb=%v(%s)",
			uid, webOK, webDetail, mwebOK, mwebDetail)
		return
	}

	msg := fmt.Sprintf("🚨 微博 Cookie 恢复失败（UID=%s 连续失败后自动 refresh 仍不可用）\nwww.weibo.com: %s\nmweibo.com: %s\n说明：已先尝试浏览器登录态恢复，两侧链路仍失败，需人工处理。\n建议：扫码登录 / bot weibo cookie set <Cookie> / bot weibo cookie check",
		uid, webDetail, mwebDetail)
	// Alerts are email-only (no QQ private spam).
	b.notifyAdmins(msg)
}

func extractWeiboCookiePayload(msg string) (string, bool) {
	payload := strings.TrimSpace(msg)
	for _, marker := range []string{"Cookie", "cookie"} {
		idx := strings.Index(payload, marker)
		if idx >= 0 {
			payload = strings.TrimSpace(payload[idx+len(marker):])
			break
		}
	}
	payload = strings.TrimLeft(payload, " ：:")
	payload = strings.TrimSpace(strings.TrimPrefix(payload, "设置"))
	payload = strings.TrimSpace(strings.TrimPrefix(payload, "更新"))
	payload = strings.TrimSpace(strings.TrimPrefix(payload, "重设"))
	payload = strings.TrimSpace(strings.TrimPrefix(payload, "微博"))
	payload = strings.TrimLeft(payload, " ：:")
	payload = strings.TrimSpace(payload)

	if payload == "" {
		return "", false
	}
	return payload, true
}

func extractCookieFromCaptureText(text string) (string, bool) {
	lines := strings.Split(text, "\n")
	cookieMap := map[string]string{}

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		if strings.HasPrefix(strings.ToLower(line), "set-cookie:") {
			cookiePart := strings.TrimSpace(line[len("Set-Cookie:"):])
			eqIdx := strings.Index(cookiePart, "=")
			if eqIdx <= 0 {
				continue
			}
			key := strings.TrimSpace(cookiePart[:eqIdx])
			valuePart := strings.TrimSpace(cookiePart[eqIdx+1:])
			semiIdx := strings.Index(valuePart, ";")
			if semiIdx >= 0 {
				valuePart = strings.TrimSpace(valuePart[:semiIdx])
			}
			if key != "" && valuePart != "" {
				cookieMap[key] = valuePart
			}
			continue
		}

		cookieLine := line
		if strings.HasPrefix(strings.ToLower(cookieLine), "cookie:") {
			cookieLine = strings.TrimSpace(cookieLine[len("Cookie:"):])
		}
		if !strings.Contains(cookieLine, "=") {
			continue
		}
		for _, kvRaw := range strings.Split(cookieLine, ";") {
			kv := strings.TrimSpace(kvRaw)
			eqIdx := strings.Index(kv, "=")
			if eqIdx <= 0 {
				continue
			}
			k := strings.TrimSpace(kv[:eqIdx])
			v := strings.TrimSpace(kv[eqIdx+1:])
			if k != "" && v != "" {
				cookieMap[k] = sanitizeCookieValue(v)
			}
		}
	}

	// 兼容被平台折叠成单行的抓包文本（Set-Cookie 不在行首）
	readValue := func(src, key string) string {
		lowerSrc := strings.ToLower(src)
		needle := strings.ToLower(key) + "="
		idx := strings.Index(lowerSrc, needle)
		if idx < 0 {
			return ""
		}
		start := idx + len(needle)
		end := start
		for end < len(src) {
			ch := src[end]
			if ch == ';' || ch == '\n' || ch == '\r' || ch == '	' || ch == '\'' {
				break
			}
			end++
		}
		return sanitizeCookieValue(strings.TrimSpace(src[start:end]))
	}

	for _, key := range []string{"SCF", "SUB", "SUBP", "WBPSESS", "ALF", "SSOLoginState"} {
		if cookieMap[key] == "" {
			if value := readValue(text, key); value != "" {
				cookieMap[key] = value
			}
		}
	}

	if cookieMap["SUB"] == "" {
		return "", false
	}

	orderedKeys := []string{"SCF", "SUB", "SUBP", "WBPSESS", "ALF", "SSOLoginState", "_T_WM", "MLOGIN", "XSRF-TOKEN", "mweibo_short_token", "M_WEIBOCN_PARAMS", "WEIBOCN_FROM"}
	parts := make([]string, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if value := strings.TrimSpace(cookieMap[key]); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "; "), true
}

func extractLooseKVValue(src, key string) string {
	src = strings.TrimSpace(src)
	if src == "" || strings.TrimSpace(key) == "" {
		return ""
	}
	lowerSrc := strings.ToLower(src)
	needle := strings.ToLower(strings.TrimSpace(key)) + "="
	idx := strings.Index(lowerSrc, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := start
	for end < len(src) {
		ch := src[end]
		if ch == '&' || ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t' || ch == '\'' || ch == '"' || ch == ';' {
			break
		}
		end++
	}
	return strings.TrimSpace(src[start:end])
}

func extractWeiboAppAuthFromCaptureText(text string) (*config.WeiboAppConfig, bool) {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return nil, false
	}
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "api.weibo.cn") && !strings.Contains(lower, "authorization:") && !strings.Contains(lower, "wb-sut") {
		return nil, false
	}
	cfg := &config.WeiboAppConfig{}

	readHeader := func(name string) string {
		needle := strings.ToLower(name) + ":"
		for _, line := range strings.Split(raw, "\n") {
			trimmed := strings.TrimSpace(strings.Trim(line, "'\""))
			if strings.HasPrefix(strings.ToLower(trimmed), needle) {
				return strings.TrimSpace(trimmed[len(name)+1:])
			}
		}
		re := regexp.MustCompile(`(?i)(?:-H\s+)?['\"]?` + regexp.QuoteMeta(name) + `:\s*([^'"\n\r]+)['\"]?`)
		if m := re.FindStringSubmatch(raw); len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}

	readCurlStringArg := func(flag string) string {
		re := regexp.MustCompile(`(?i)(?:^|\s)` + regexp.QuoteMeta(flag) + `\s+['\"]([^'\"]+)['\"]`)
		if m := re.FindStringSubmatch(raw); len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}

	readKV := func(src, key string) string {
		re := regexp.MustCompile(`(?i)(?:^|[?&\s'";/])` + regexp.QuoteMeta(key) + `=([^&\s'";]+)`)
		if m := re.FindStringSubmatch(src); len(m) >= 2 {
			return strings.TrimSpace(m[1])
		}
		return ""
	}

	urlText := readCurlStringArg("curl")
	if urlText == "" {
		re := regexp.MustCompile(`https://api\.weibo\.cn[^\s'\"]+`)
		urlText = strings.TrimSpace(re.FindString(raw))
	}
	if urlText != "" {
		if parsedURL, err := url.Parse(urlText); err == nil {
			cfg.Host = parsedURL.Host
			cfg.RequestPath = parsedURL.Path
			q := parsedURL.Query()
			cfg.GSID = firstNonEmpty(cfg.GSID, q.Get("gsid"))
			cfg.Aid = firstNonEmpty(cfg.Aid, q.Get("aid"))
			cfg.S = firstNonEmpty(cfg.S, q.Get("s"))
			cfg.CapturedOID = firstNonEmpty(cfg.CapturedOID, q.Get("containerid"), q.Get("topic_id"), q.Get("flowId"), q.Get("fid"))
		}
	}

	if host := readHeader("Host"); host != "" {
		cfg.Host = host
	}
	if cfg.Host != "" && !strings.Contains(strings.ToLower(cfg.Host), "api.weibo.cn") {
		return nil, false
	}
	if cfg.Host == "" && strings.Contains(lower, "api.weibo.cn") {
		cfg.Host = "api.weibo.cn"
	}

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "POST ") || strings.HasPrefix(trimmed, "GET ") {
			parts := strings.Fields(trimmed)
			if len(parts) >= 2 {
				cfg.RequestPath = strings.TrimSpace(parts[1])
				break
			}
		}
	}
	cfg.Authorization = readHeader("Authorization")
	cfg.XSessionID = firstNonEmpty(readHeader("X-Sessionid"), readHeader("X-SessionID"))
	cfg.XValidator = readHeader("X-Validator")
	cfg.XShanhaiPass = readHeader("x-shanhai-pass")
	cfg.XLogUID = readHeader("X-Log-Uid")
	cfg.XEngineType = readHeader("x-engine-type")
	cfg.CronetRID = readHeader("cronet_rid")
	cfg.SNRT = readHeader("SNRT")
	cfg.AcceptLanguage = readHeader("Accept-Language")
	cfg.AcceptEncoding = readHeader("Accept-Encoding")
	cfg.UserAgent = readHeader("User-Agent")

	dataText := firstNonEmpty(readCurlStringArg("--data"), readCurlStringArg("--data-raw"), readCurlStringArg("--data-binary"))
	cfg.RawCapture = raw
	cfg.RequestBody = dataText
	cfg.GSID = firstNonEmpty(cfg.GSID, readKV(urlText, "gsid"), readKV(raw, "gsid"), readKV(dataText, "gsid"), extractLooseKVValue(urlText, "gsid"), extractLooseKVValue(raw, "gsid"), extractLooseKVValue(dataText, "gsid"))
	cfg.Aid = firstNonEmpty(cfg.Aid, readKV(urlText, "aid"), readKV(raw, "aid"), readKV(dataText, "aid"), extractLooseKVValue(urlText, "aid"), extractLooseKVValue(raw, "aid"), extractLooseKVValue(dataText, "aid"))
	cfg.S = firstNonEmpty(cfg.S, readKV(urlText, "s"), readKV(raw, "s"), readKV(dataText, "s"), extractLooseKVValue(urlText, "s"), extractLooseKVValue(raw, "s"), extractLooseKVValue(dataText, "s"))
	cfg.CapturedOID = firstNonEmpty(cfg.CapturedOID, readKV(urlText, "containerid"), readKV(urlText, "topic_id"), readKV(urlText, "flowId"), readKV(urlText, "fid"), readKV(raw, "containerid"), readKV(raw, "topic_id"), readKV(raw, "flowId"), readKV(raw, "fid"), readKV(dataText, "containerid"), readKV(dataText, "topic_id"), readKV(dataText, "flowId"), readKV(dataText, "fid"), extractLooseKVValue(urlText, "containerid"), extractLooseKVValue(urlText, "topic_id"), extractLooseKVValue(urlText, "flowId"), extractLooseKVValue(urlText, "fid"), extractLooseKVValue(raw, "containerid"), extractLooseKVValue(raw, "topic_id"), extractLooseKVValue(raw, "flowId"), extractLooseKVValue(raw, "fid"), extractLooseKVValue(dataText, "containerid"), extractLooseKVValue(dataText, "topic_id"), extractLooseKVValue(dataText, "flowId"), extractLooseKVValue(dataText, "fid"))
	if cfg.CapturedOID != "" && !strings.HasPrefix(cfg.CapturedOID, "1022:") {
		cfg.CapturedOID = "1022:" + cfg.CapturedOID
	}
	// flowId/fid 参数会包含 "_-_recommend" "_-_feed" 等后缀，提取纯 OID
	if idx := strings.Index(cfg.CapturedOID, "_-_"); idx >= 0 {
		cfg.CapturedOID = cfg.CapturedOID[:idx]
	}

	if cfg.RequestPath != "" {
		if idx := strings.Index(cfg.RequestPath, "?"); idx >= 0 {
			queryPart := cfg.RequestPath[idx+1:]
			cfg.RequestPath = cfg.RequestPath[:idx]
			if vals, err := url.ParseQuery(queryPart); err == nil {
				cfg.GSID = firstNonEmpty(cfg.GSID, vals.Get("gsid"))
				cfg.Aid = firstNonEmpty(cfg.Aid, vals.Get("aid"))
				cfg.S = firstNonEmpty(cfg.S, vals.Get("s"))
				cfg.CapturedOID = firstNonEmpty(cfg.CapturedOID, vals.Get("containerid"), vals.Get("topic_id"), vals.Get("flowId"), vals.Get("fid"))
			}
		}
	}
	if cfg.RequestPath == "" {
		cfg.RequestPath = "/2/statuses/container_timeline_topicpage"
	}
	if cfg.Host == "" {
		cfg.Host = "api.weibo.cn"
	}
	if cfg.Authorization == "" {
		return nil, false
	}
	return cfg, true
}

func maskWeiboAppAuth(cfg *config.WeiboAppConfig) string {
	if cfg == nil {
		return "未配置"
	}
	mask := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		if len(s) <= 8 {
			return strings.Repeat("*", len(s))
		}
		return s[:4] + "..." + s[len(s)-4:]
	}
	parts := []string{}
	if cfg.Host != "" {
		parts = append(parts, "host="+cfg.Host)
	}
	if cfg.RequestPath != "" {
		parts = append(parts, "path="+cfg.RequestPath)
	}
	if cfg.CapturedOID != "" {
		parts = append(parts, "oid="+cfg.CapturedOID)
	}
	if cfg.GSID != "" {
		parts = append(parts, "gsid="+mask(cfg.GSID))
	}
	if cfg.Authorization != "" {
		parts = append(parts, "auth="+mask(cfg.Authorization))
	}
	return strings.Join(parts, ", ")
}

// detectWeiboSuperCookieText 判断文本是否看起来像 weibo.com 完整 Cookie
// （而不是 AppAuth 抓包）。判断依据：包含 SCF/SUBP/ALF 等浏览器特有字段，
// 且不包含 api.weibo.cn / Authorization 等 AppAuth 特征。
func detectWeiboSuperCookieText(text string) bool {
	lower := strings.ToLower(text)
	// 明确是 AppAuth 抓包
	if strings.Contains(lower, "api.weibo.cn") || strings.Contains(lower, "wb-sut") {
		return false
	}
	// 没有 = 号或分号，不可能是完整 cookie
	if !strings.Contains(text, "=") || !strings.Contains(text, ";") {
		return false
	}
	// 有 SCF、SUBP、ALF 等浏览器特有字段之一，基本可确认是完整 cookie
	markers := []string{"SCF=", "SUBP=", "ALF=", "WBPSESS=", "SSOLoginState=", "XSRF-TOKEN=", "MLOGIN=", "WEIBOCN_FROM="}
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// sanitizeCookieValue 清除 curl 抓包残留的 ' -H 'xxx' 等杂质
func sanitizeCookieValue(v string) string {
	v = strings.TrimSpace(v)
	// 去掉末尾的 '  -H '...(单引号+空格+-H+空格+单引号)
	if idx := strings.Index(v, "'  -H '"); idx >= 0 {
		v = strings.TrimSpace(v[:idx])
	}
	// 去掉末尾单引号（curl 值包裹）
	v = strings.Trim(v, "'")
	// 去掉 "Connection: keep-alive" 之类的 HTTP 头残留
	v = strings.TrimSpace(v)
	for _, suffix := range []string{"Connection:", "keep-alive", "Connection: keep-alive"} {
		if strings.HasSuffix(strings.ToLower(v), strings.ToLower(suffix)) {
			v = strings.TrimSpace(v[:len(v)-len(suffix)])
		}
	}
	return strings.TrimSpace(v)
}

func (b *Bot) updateWeiboAppAuth(appCfg *config.WeiboAppConfig) error {
	if appCfg == nil {
		return fmt.Errorf("app 配置为空")
	}
	copied := *appCfg
	b.cfg.WeiboApp = &copied

	// 注意：不再自动从 gsid 推导 weibo.com Cookie。
	// gsid（App 用）与 weibo.com Cookie（浏览器用）分属不同认证体系，
	// 混合字段会导致签名不匹配，签到必失败。Cookie 需要单独导入。

	if err := b.cfg.Save(); err != nil {
		return err
	}
	b.weiboMonitor.SetAppAuth(&monitor.WeiboAppAuth{
		RawCapture:     copied.RawCapture,
		Host:           copied.Host,
		RequestPath:    copied.RequestPath,
		RequestBody:    copied.RequestBody,
		CapturedOID:    copied.CapturedOID,
		Authorization:  copied.Authorization,
		GSID:           copied.GSID,
		Aid:            copied.Aid,
		S:              copied.S,
		XSessionID:     copied.XSessionID,
		XValidator:     copied.XValidator,
		XShanhaiPass:   copied.XShanhaiPass,
		XLogUID:        copied.XLogUID,
		XEngineType:    copied.XEngineType,
		CronetRID:      copied.CronetRID,
		SNRT:           copied.SNRT,
		AcceptLanguage: copied.AcceptLanguage,
		AcceptEncoding: copied.AcceptEncoding,
		UserAgent:      copied.UserAgent,
	})
	return nil
}

func normalizeWeiboCookie(raw string) (string, error) {
	cookie := strings.TrimSpace(strings.Trim(raw, "\"'"))
	if cookie == "" {
		return "", fmt.Errorf("cookie 不能为空")
	}

	if strings.Contains(strings.ToLower(cookie), "set-cookie:") || strings.Contains(strings.ToLower(cookie), "cookie:") || strings.Contains(cookie, "\n") || strings.Contains(cookie, "\r") {
		if parsed, ok := extractCookieFromCaptureText(cookie); ok {
			return parsed, nil
		}
	}

	if !strings.Contains(cookie, "=") {
		if strings.HasPrefix(cookie, "_2A") {
			return "SUB=" + cookie, nil
		}
		return "", fmt.Errorf("cookie 看起来不包含有效 SUB")
	}

	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		cookie = strings.TrimSpace(cookie[len("Cookie:"):])
	}

	parts := strings.Split(cookie, ";")
	cookieMap := map[string]string{}
	for _, part := range parts {
		kv := strings.TrimSpace(part)
		if kv == "" || !strings.Contains(kv, "=") {
			continue
		}
		pair := strings.SplitN(kv, "=", 2)
		k := strings.TrimSpace(pair[0])
		v := strings.TrimSpace(pair[1])
		if k == "" || v == "" {
			continue
		}
		cookieMap[k] = v
	}

	if sub := strings.TrimSpace(cookieMap["SUB"]); sub == "" {
		if strings.HasPrefix(cookie, "SUB=") {
			cookieMap["SUB"] = strings.TrimSpace(strings.TrimPrefix(cookie, "SUB="))
		}
	}

	if strings.TrimSpace(cookieMap["SUB"]) == "" {
		return "", fmt.Errorf("cookie 看起来不包含有效 SUB")
	}

	orderedKeys := []string{"SCF", "SUB", "SUBP", "WBPSESS", "ALF", "SSOLoginState", "_T_WM", "MLOGIN", "XSRF-TOKEN", "mweibo_short_token", "M_WEIBOCN_PARAMS", "WEIBOCN_FROM"}
	out := make([]string, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if value := strings.TrimSpace(cookieMap[key]); value != "" {
			out = append(out, key+"="+value)
		}
	}

	if len(out) == 0 {
		return "", fmt.Errorf("cookie 解析失败")
	}

	return strings.Join(out, "; "), nil
}

func maskCookie(cookie string) string {
	trimmed := strings.TrimSpace(cookie)
	if trimmed == "" {
		return "(empty)"
	}

	subValue := ""
	parts := strings.Split(trimmed, ";")
	for _, p := range parts {
		kv := strings.TrimSpace(p)
		if strings.HasPrefix(kv, "SUB=") {
			subValue = strings.TrimPrefix(kv, "SUB=")
			break
		}
	}
	if subValue == "" && !strings.Contains(trimmed, "=") {
		subValue = trimmed
	}

	if subValue == "" {
		if len(trimmed) <= 12 {
			return trimmed
		}
		return trimmed[:6] + "..." + trimmed[len(trimmed)-4:]
	}

	if len(subValue) <= 12 {
		return "SUB=" + subValue
	}
	return "SUB=" + subValue[:6] + "..." + subValue[len(subValue)-4:]
}

// weiboCheckStatus 格式化 cookie 检查结果
func weiboCheckStatus(ok bool, detail string) string {
	if ok {
		return "OK"
	}
	if detail != "" {
		return "ERR:" + detail
	}
	return "ERR"
}

func (b *Bot) updateWeiboCookie(operatorID int64, rawCookie string) (string, error) {
	cookie, err := normalizeWeiboCookie(rawCookie)
	if err != nil {
		return "", err
	}

	b.cfg.WeiboCookie = cookie
	b.weiboMonitor.SetCookie(cookie)

	// 自动同步到 MWeiboCookie（同一个 SUB token 可同时用于两个域名）
	if b.cfg.WeiboMWeiboCookie == "" || b.cfg.WeiboMWeiboCookie != cookie {
		b.cfg.WeiboMWeiboCookie = cookie
		b.weiboMonitor.SetMWeiboCookie(cookie)
		b.LogInfo("Weibo mweibo cookie auto-synced from weibo.com cookie")
	}

	if err := b.cfg.Save(); err != nil {
		return "", err
	}

	b.LogInfo("Weibo cookie hot-updated by user %d", operatorID)
	return maskCookie(cookie), nil
}

func (b *Bot) updateWeiboMWeiboCookie(operatorID int64, rawCookie string) (string, error) {
	cookie, err := normalizeWeiboCookie(rawCookie)
	if err != nil {
		return "", err
	}

	b.cfg.WeiboMWeiboCookie = cookie
	b.weiboMonitor.SetMWeiboCookie(cookie)
	if err := b.cfg.Save(); err != nil {
		return "", err
	}

	b.LogInfo("Weibo mweibo cookie hot-updated by user %d", operatorID)
	return maskCookie(cookie), nil
}

func (b *Bot) firstWeiboUID() string {
	if b.cfg.WeiboSubscriptions == nil {
		return ""
	}
	if groupSubs, ok := b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]; ok {
		for uid := range groupSubs {
			return uid
		}
	}
	for _, groupSubs := range b.cfg.WeiboSubscriptions {
		for uid := range groupSubs {
			return uid
		}
	}
	return ""
}

func (b *Bot) checkWeiboCookieStatus() (bool, string, error) {
	uid := b.firstWeiboUID()
	if uid == "" {
		return false, "", fmt.Errorf("当前没有微博监控UID，无法检查Cookie")
	}

	type probe struct {
		name string
		fn   func(string) (bool, string, error)
	}
	probes := []probe{
		{name: "www.weibo.com", fn: b.weiboMonitor.CheckWebCookie},
		{name: "mweibo.com", fn: b.weiboMonitor.CheckMWeiboCookie},
	}

	allOK := true
	parts := make([]string, 0, len(probes))
	var lastErr error
	for _, p := range probes {
		ok, detail, err := p.fn(uid)
		if err != nil {
			allOK = false
			lastErr = err
			parts = append(parts, fmt.Sprintf("%s=ERR:%v", p.name, err))
			continue
		}
		if !ok {
			allOK = false
		}
		parts = append(parts, fmt.Sprintf("%s=%s", p.name, detail))
	}
	if !allOK && lastErr != nil {
		return false, strings.Join(parts, " | "), lastErr
	}
	return allOK, strings.Join(parts, " | "), nil
}

func (b *Bot) runWeiboSuperAutoSignLoop() {
	// 启动后立即尝试一次自动签到，不等第一个 tick
	b.tryWeiboSuperAutoSign()

	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		b.tryWeiboSuperAutoSign()
	}
}

// tryWeiboSuperAutoSign 单次自动签到尝试，提取为独立方法供启动立即调用
func (b *Bot) tryWeiboSuperAutoSign() {
	b.weiboAutoSignMu.Lock()
	defer b.weiboAutoSignMu.Unlock()

	if !b.cfg.WeiboSuperAutoEnabled {
		return
	}
	today := time.Now().Format("2006-01-02")
	if strings.TrimSpace(b.cfg.WeiboSuperLastRunDate) == today {
		return
	}
	result, anySuccess, authExpired := b.signAllWeiboSuperTopics()
	if result == "" {
		return
	}
	if !anySuccess && authExpired && b.cfg.WeiboBrowserAuthEnabled && b.weiboAuth != nil {
		log.Printf("[WeiboSuper] 检测到游客页，立即请求浏览器恢复 Cookie")
		gotCookies, refreshErr := b.weiboAuth.RequestRefreshAndWait("super_sign_auth_expired", 50*time.Second)
		if refreshErr != nil {
			log.Printf("[WeiboSuper] request browser recovery failed: %v", refreshErr)
		} else if gotCookies {
			webOK, detail, checkErr := b.weiboMonitor.CheckWebCookie("")
			if checkErr != nil {
				log.Printf("[WeiboSuper] verify refreshed web cookie failed: %v", checkErr)
			} else if webOK {
				log.Printf("[WeiboSuper] browser Cookie verified (%s), retrying sign once", detail)
				result, anySuccess, _ = b.signAllWeiboSuperTopics()
			}
		}
	}
	// 只有至少一个超话签到成功时才标记为今日已跑
	// 全部失败时不标记，让下一个 tick 可以重试（如新 cookie 导入后）
	if anySuccess {
		b.cfg.WeiboSuperLastRunDate = today
	} else {
		log.Printf("[WeiboSuper] 全部签到失败（可能 Cookie 过期），保留重试机会")
	}
	if err := b.cfg.Save(); err != nil {
		log.Printf("[WeiboSuper] save auto run date failed: %v", err)
	}
	if anySuccess || b.shouldNotifyWeiboAutoSignFailure() {
		b.notifyAdminsQQ("[Weibo超话自动签到]\n" + result)
	} else {
		log.Printf("[WeiboSuper] repeated failure notification suppressed (1h cooldown)")
	}
}

func (b *Bot) shouldNotifyWeiboAutoSignFailure() bool {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.lastWeiboAutoSignFailureNotifyAt.IsZero() && now.Sub(b.lastWeiboAutoSignFailureNotifyAt) < time.Hour {
		return false
	}
	b.lastWeiboAutoSignFailureNotifyAt = now
	return true
}

func (b *Bot) getGlobalWeiboSuperTopics() map[string]*config.WeiboSuperTopic {
	if b.cfg.WeiboSuperTopics == nil {
		b.cfg.WeiboSuperTopics = make(map[int64]map[string]*config.WeiboSuperTopic)
	}
	if _, ok := b.cfg.WeiboSuperTopics[0]; !ok || b.cfg.WeiboSuperTopics[0] == nil {
		b.cfg.WeiboSuperTopics[0] = make(map[string]*config.WeiboSuperTopic)
	}
	if len(b.cfg.WeiboSuperTopics[0]) == 0 {
		for groupID, oldTopics := range b.cfg.WeiboSuperTopics {
			if groupID == 0 || len(oldTopics) == 0 {
				continue
			}
			for oid, topic := range oldTopics {
				if _, exists := b.cfg.WeiboSuperTopics[0][oid]; !exists {
					b.cfg.WeiboSuperTopics[0][oid] = topic
				}
			}
		}
	}
	return b.cfg.WeiboSuperTopics[0]
}

func normalizeWeiboSuperOID(oid string) string {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return ""
	}
	if idx := strings.IndexByte(oid, ' '); idx >= 0 {
		oid = oid[:idx]
	}
	if strings.HasPrefix(oid, "1022:") {
		return oid
	}
	if strings.Contains(oid, ":") {
		return oid
	}
	return "1022:" + oid
}

func (b *Bot) getWeiboSuperCountGroups() map[string]*config.WeiboSuperCountGroupInfo {
	if b.cfg.WeiboSuperCountGroups == nil {
		b.cfg.WeiboSuperCountGroups = make(map[string]*config.WeiboSuperCountGroupInfo)
	}
	return b.cfg.WeiboSuperCountGroups
}

func (b *Bot) filterResultsByGroup(results []monitor.WeiboSuperCountResult, groupName string) []monitor.WeiboSuperCountResult {
	topics := b.getWeiboSuperCountTopics()
	filtered := make([]monitor.WeiboSuperCountResult, 0, len(results))
	for _, r := range results {
		oid := strings.TrimPrefix(normalizeWeiboSuperOID(r.OID), "1022:")
		if t, ok := topics[oid]; ok && t.GroupName == groupName {
			filtered = append(filtered, r)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].SignCount != filtered[j].SignCount {
			return filtered[i].SignCount > filtered[j].SignCount
		}
		return strings.ToLower(filtered[i].Name) < strings.ToLower(filtered[j].Name)
	})
	return filtered
}

func (b *Bot) getWeiboSuperCountTopics() map[string]*config.WeiboSuperCountTopic {
	if b.cfg.WeiboSuperCountTopics == nil {
		b.cfg.WeiboSuperCountTopics = make(map[string]*config.WeiboSuperCountTopic)
	}
	// 统一 key 格式：把带 "1022:" 前缀的旧 key 迁移到无前缀，避免 lookup 不一致
	var prefixedKeys []string
	for k, v := range b.cfg.WeiboSuperCountTopics {
		if strings.HasPrefix(k, "1022:") {
			normKey := strings.TrimPrefix(k, "1022:")
			if _, exists := b.cfg.WeiboSuperCountTopics[normKey]; !exists {
				b.cfg.WeiboSuperCountTopics[normKey] = v
			}
			prefixedKeys = append(prefixedKeys, k)
		}
	}
	for _, k := range prefixedKeys {
		delete(b.cfg.WeiboSuperCountTopics, k)
	}
	return b.cfg.WeiboSuperCountTopics
}

func normalizeWeiboSuperNameKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	s = strings.TrimSpace(strings.TrimSuffix(s, "超话"))
	return s
}

func (b *Bot) findWeiboSuperCountTopic(key string) (string, *config.WeiboSuperCountTopic) {
	topics := b.getWeiboSuperCountTopics()
	if len(topics) == 0 {
		return "", nil
	}
	needle := normalizeWeiboSuperNameKey(key)
	if needle == "" {
		return "", nil
	}
	if topic, ok := topics[strings.TrimPrefix(normalizeWeiboSuperOID(key), "1022:")]; ok {
		return normalizeWeiboSuperOID(key), topic
	}
	for oid, topic := range topics {
		if normalizeWeiboSuperNameKey(oid) == needle {
			return oid, topic
		}
		if normalizeWeiboSuperNameKey(topic.Name) == needle {
			return oid, topic
		}
	}
	return "", nil
}

func (b *Bot) findWeiboSuperTopic(key string) (string, *config.WeiboSuperTopic) {
	topics := b.getGlobalWeiboSuperTopics()
	if len(topics) == 0 {
		return "", nil
	}
	needle := normalizeWeiboSuperNameKey(key)
	if needle == "" {
		return "", nil
	}
	if topic, ok := topics[strings.TrimPrefix(normalizeWeiboSuperOID(key), "1022:")]; ok {
		return normalizeWeiboSuperOID(key), topic
	}
	for oid, topic := range topics {
		if normalizeWeiboSuperNameKey(oid) == needle {
			return oid, topic
		}
		if normalizeWeiboSuperNameKey(topic.Name) == needle {
			return oid, topic
		}
	}
	return "", nil
}

// shouldSignWeiboCountForExact 判断日报第二轮要不要为该超话补一次签到取精确值。
//
// ★ 2026-10-04 用户规则：
//  1. 不在自动签到列表的超话 —— 不签到，日报原样显示模糊数字
//  2. 在自动签到列表的超话   —— 才通过签到来拿详细数据
//
// 因此只有「含万（模糊）」且「在自动签到列表内」才签到。
func shouldSignWeiboCountForExact(isRounded bool, oidNorm string, autoTopics map[string]*config.WeiboSuperTopic) bool {
	if !isRounded {
		return false
	}
	if _, inAuto := autoTopics[oidNorm]; !inAuto {
		return false
	}
	return true
}

func (b *Bot) signAllWeiboSuperTopics() (string, bool, bool) {
	topics := b.getGlobalWeiboSuperTopics()
	if len(topics) == 0 {
		return "", false, false
	}
	var lines []string
	anySuccess := false
	authExpired := false
	for oid, topic := range topics {
		res, err := b.weiboMonitor.SignWeiboSuperTopic(oid)
		name := strings.TrimSpace(topic.Name)
		if name == "" {
			name = oid
		}
		if err != nil {
			if monitor.IsWeiboAuthExpired(err) {
				authExpired = true
			}
			topic.LastSignStatus = "失败: " + err.Error()
			lines = append(lines, fmt.Sprintf("[%s] 签到失败: %v", name, err))
			continue
		}
		res.Name = name
		topic.LastSignDate = time.Now().Format("2006-01-02")
		topic.LastSignStatus = fmt.Sprintf("code=%d %s", res.Code, strings.TrimSpace(res.Message))
		if res.Rank > 0 {
			topic.LastSignRank = res.Rank
		}
		if res.Success {
			if res.AlreadyDone {
				lines = append(lines, fmt.Sprintf("[%s] 今日已签到", name))
				anySuccess = true
			} else {
				lines = append(lines, fmt.Sprintf("[%s] 签到成功", name))
				anySuccess = true
			}
		} else {
			lines = append(lines, fmt.Sprintf("[%s] 签到失败(code=%d): %s", name, res.Code, res.Message))
		}
	}
	if len(lines) == 0 {
		return "", false, authExpired
	}
	if err := b.cfg.Save(); err != nil {
		log.Printf("[WeiboSuper] save sign result failed: %v", err)
	}
	return strings.Join(lines, "\n"), anySuccess, authExpired
}

func (b *Bot) fetchWeiboSuperCountAll() ([]monitor.WeiboSuperCountResult, []string) {
	return b.fetchWeiboSuperCountAllBefore(time.Time{})
}

func (b *Bot) fetchWeiboSuperCountAllBefore(deadline time.Time) ([]monitor.WeiboSuperCountResult, []string) {
	topics := b.getWeiboSuperCountTopics()
	results := make([]monitor.WeiboSuperCountResult, 0, len(topics))
	failed := make([]string, 0)

	// 第一轮：日报在 23:59 才开始取数，以有限并发确保全部超话尽量在
	// 午夜前完成。手动查询仍保持串行，避免无截止时间的操作突然放大流量。
	type countJob struct {
		oid      string
		nameHint string
	}
	type countOutcome struct {
		job countJob
		res *monitor.WeiboSuperCountResult
		err error
	}
	jobs := make([]countJob, 0, len(topics))
	for oid, topic := range topics {
		jobs = append(jobs, countJob{oid: oid, nameHint: strings.TrimSpace(topic.Name)})
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].oid < jobs[j].oid })
	outcomes := make([]countOutcome, len(jobs))
	parallelism := 1
	if !deadline.IsZero() {
		parallelism = 4
	}
	limit := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, job := range jobs {
		i, job := i, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			res, err := fetchWeiboDailyCount(deadline, func() (*monitor.WeiboSuperCountResult, error) {
				return b.weiboMonitor.FetchSuperCountByOID(job.oid, job.nameHint)
			})
			outcomes[i] = countOutcome{job: job, res: res, err: err}
		}()
	}
	wg.Wait()

	for _, outcome := range outcomes {
		oid, nameHint, res, err := outcome.job.oid, outcome.job.nameHint, outcome.res, outcome.err
		if err != nil {
			log.Printf("[Weibo][Count] unavailable oid=%s name=%q: %v", oid, nameHint, err)
			name := nameHint
			if name == "" {
				name = oid
			}
			failed = append(failed, fmt.Sprintf("[%s] %v", name, err))
			b.maybeNotifyWeiboAppAuthInvalid(err, oid, name)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(res.Source), "web") {
			b.maybeNotifyWeiboAppAuthInvalid(fmt.Errorf("weibo super count fell back to web"), oid, strings.TrimSpace(res.Name))
		}
		if strings.TrimSpace(res.Name) == "" {
			if nameHint != "" {
				res.Name = nameHint
			} else {
				res.Name = oid
			}
		}
		results = append(results, *res)
	}
	// 第二轮：破万的超话走签到回退换精确值。
	//
	// ★ 2026-10-04 逻辑调整（用户明确要求）：
	//   1) 不在自动签到列表里的超话 —— 不签到，日报原样显示模糊数字。
	//   2) 在自动签到列表里的超话   —— 才通过签到来拿详细数据。
	//
	// 原 ReportSign 标记机制（标记为 1 则自动签到跳过、专走日报签到流）
	// 与此逻辑直接冲突：两者范围完全相同却互相排斥，
	// 任何一次 ReportSign 被置 1 都会让该超话彻底停签，因此一并移除。
	// report_sign 字段保留在 config 结构体里，仅为兼容旧配置文件。
	autoTopics := b.getGlobalWeiboSuperTopics()
	for i, r := range results {
		isRounded := strings.Contains(r.SignText, "万")
		if !isRounded || (!deadline.IsZero() && !time.Now().Before(deadline)) {
			continue
		}

		oidNorm := strings.TrimPrefix(normalizeWeiboSuperOID(r.OID), "1022:")
		if !shouldSignWeiboCountForExact(isRounded, oidNorm, autoTopics) {
			log.Printf("[Weibo][Count] skip sign (not in auto-sign list), keep rounded oid=%s name=%s sign=%s",
				r.OID, r.Name, r.SignText)
			continue
		}

		signRes, err := b.weiboMonitor.SignWeiboSuperTopic(r.OID)
		if err != nil || (!deadline.IsZero() && !time.Now().Before(deadline)) {
			continue
		}
		if signRes.Rank > 0 && signRes.Rank != r.SignCount {
			log.Printf("[Weibo][Count] override rounded count oid=%s label=%d exact=%d", r.OID, r.SignCount, signRes.Rank)
			results[i].SignCount = signRes.Rank
			results[i].SignText = fmt.Sprintf("签到%d人", signRes.Rank)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SignCount != results[j].SignCount {
			return results[i].SignCount > results[j].SignCount
		}
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})
	return results, failed
}

func (b *Bot) maybeNotifyWeiboAppAuthInvalid(err error, oid, name string) {
	// 2026-07-18：APP 超话链路暂时关闭，只走网页 Cookie；不再因 App 回退/失效刷告警。
	// 保留函数体供以后重新打开 App 路径时使用。
	_ = err
	_ = oid
	_ = name
	return

	if b == nil || b.cfg == nil {
		return
	}
	reason := strings.TrimSpace(strings.ToLower(fmt.Sprint(err)))
	if reason == "" {
		return
	}
	shouldNotify := false
	switch {
	case strings.Contains(reason, "sso_api_error"):
		shouldNotify = true
	case strings.Contains(reason, "客户端身份校验失败"):
		shouldNotify = true
	case strings.Contains(reason, "weibo app authorization 缺失"):
		shouldNotify = true
	case strings.Contains(reason, "fell back to web"):
		shouldNotify = true
	case strings.Contains(reason, "topicpage http=401") || strings.Contains(reason, "topicpage http=403"):
		shouldNotify = true
	case strings.Contains(reason, "重新登录"):
		shouldNotify = true
	}
	if !shouldNotify {
		return
	}
	loc, locErr := time.LoadLocation("Asia/Shanghai")
	if locErr != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	today := time.Now().In(loc).Format("2006-01-02")
	if strings.TrimSpace(b.cfg.WeiboAppAuthInvalidLastNotifyDate) == today {
		return
	}
	topicName := strings.TrimSpace(name)
	if topicName == "" {
		topicName = strings.TrimSpace(oid)
	}
	msg := fmt.Sprintf("🚨 微博超话 app 鉴权疑似失效，当前超话统计已回退到 web 端。\n对象: %s\nOID: %s\n原因: %v\n请尽快更新 WEIBO_APP 抓包参数（Authorization / gsid / aid / s 等）。", topicName, strings.TrimSpace(oid), err)
	b.notifyAdmins(msg)
	b.cfg.WeiboAppAuthInvalidLastNotifyDate = today
	if saveErr := b.cfg.Save(); saveErr != nil {
		log.Printf("[WeiboSuperCount] save app auth invalid notify state failed: %v", saveErr)
	}
}

func buildWeiboSuperCountDailySnapshot(results []monitor.WeiboSuperCountResult) map[string]int {
	snapshot := make(map[string]int, len(results))
	for _, item := range results {
		oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
		if oid == "" {
			continue
		}
		snapshot[oid] = item.SignCount
	}
	return snapshot
}

func buildWeiboSuperCountDailySnapshotV2(results []monitor.WeiboSuperCountResult) map[string]*config.WeiboSuperCountSnapshotItem {
	snapshot := make(map[string]*config.WeiboSuperCountSnapshotItem, len(results))
	for _, item := range results {
		oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
		if oid == "" {
			continue
		}
		snapshot[oid] = &config.WeiboSuperCountSnapshotItem{
			Name:               strings.TrimSpace(item.Name),
			SignCount:          item.SignCount,
			SuperLikeCount:     item.SuperLikeCount,
			SuperLikeKnown:     item.SuperLikeKnown || item.SuperLikeCount > 0,
			Heat24h:            strings.TrimSpace(item.Heat24h),
			ReadCount:          strings.TrimSpace(item.ReadCount),
			PostCount:          strings.TrimSpace(item.PostCount),
			FansCount:          strings.TrimSpace(item.FansCount),
			LevelText:          strings.TrimSpace(item.LevelText),
			CreatorOfficerText: strings.TrimSpace(item.CreatorOfficerText),
			FanDiamondText:     strings.TrimSpace(item.FanDiamondText),
			DailyRankText:      strings.TrimSpace(item.DailyRankText),
			CheckinExpText:     strings.TrimSpace(item.CheckinExpText),
			CheckinStreakText:  strings.TrimSpace(item.CheckinStreakText),
		}
	}
	return snapshot
}

func buildSignBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
	if len(snapshot) == 0 {
		return nil
	}
	baseline := make(map[string]int, len(snapshot))
	for rawOID, item := range snapshot {
		if item == nil {
			continue
		}
		oid := normalizeWeiboSuperOID(strings.TrimSpace(rawOID))
		if oid == "" {
			continue
		}
		baseline[oid] = item.SignCount
	}
	return baseline
}

func buildLikeBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
	if len(snapshot) == 0 {
		return nil
	}
	baseline := make(map[string]int, len(snapshot))
	for rawOID, item := range snapshot {
		if item == nil {
			continue
		}
		oid := normalizeWeiboSuperOID(strings.TrimSpace(rawOID))
		if oid == "" {
			continue
		}
		if item.SuperLikeKnown || item.SuperLikeCount > 0 {
			baseline[oid] = item.SuperLikeCount
		}
	}
	return baseline
}

func buildPostBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
	return buildTextMetricBaselineFromSnapshotV2(snapshot, func(item *config.WeiboSuperCountSnapshotItem) string {
		return item.PostCount
	})
}

func buildReadBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
	return buildTextMetricBaselineFromSnapshotV2(snapshot, func(item *config.WeiboSuperCountSnapshotItem) string {
		return item.ReadCount
	})
}

func buildFansBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
	return buildTextMetricBaselineFromSnapshotV2(snapshot, func(item *config.WeiboSuperCountSnapshotItem) string {
		return item.FansCount
	})
}

func buildTextMetricBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem, value func(*config.WeiboSuperCountSnapshotItem) string) map[string]int {
	if len(snapshot) == 0 {
		return nil
	}
	baseline := make(map[string]int, len(snapshot))
	for rawOID, item := range snapshot {
		if item == nil {
			continue
		}
		oid := normalizeWeiboSuperOID(strings.TrimSpace(rawOID))
		if oid == "" {
			continue
		}
		n, ok := monitor.ParseChineseNumber(strings.TrimSpace(value(item)))
		if ok && n > 0 {
			baseline[oid] = n
		}
	}
	return baseline
}

func (b *Bot) buildWeiboSuperCountResultsFromSnapshot(snapshot map[string]int) []monitor.WeiboSuperCountResult {
	if len(snapshot) == 0 {
		return nil
	}
	topics := b.getWeiboSuperCountTopics()
	results := make([]monitor.WeiboSuperCountResult, 0, len(snapshot))
	for rawOID, signCount := range snapshot {
		oid := normalizeWeiboSuperOID(strings.TrimSpace(rawOID))
		if oid == "" {
			continue
		}
		name := oid
		if topic, ok := topics[oid]; ok && topic != nil {
			if tname := strings.TrimSpace(topic.Name); tname != "" {
				name = tname
			}
		}
		results = append(results, monitor.WeiboSuperCountResult{
			OID:       oid,
			Name:      name,
			SignCount: signCount,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SignCount != results[j].SignCount {
			return results[i].SignCount > results[j].SignCount
		}
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})
	return results
}

func (b *Bot) buildWeiboSuperCountResultsFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) []monitor.WeiboSuperCountResult {
	if len(snapshot) == 0 {
		return nil
	}
	topics := b.getWeiboSuperCountTopics()
	results := make([]monitor.WeiboSuperCountResult, 0, len(snapshot))
	for rawOID, item := range snapshot {
		if item == nil {
			continue
		}
		oid := normalizeWeiboSuperOID(strings.TrimSpace(rawOID))
		if oid == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			if topic, ok := topics[oid]; ok && topic != nil {
				name = strings.TrimSpace(topic.Name)
			}
		}
		if name == "" {
			name = oid
		}
		results = append(results, monitor.WeiboSuperCountResult{
			OID:                oid,
			Name:               name,
			SignCount:          item.SignCount,
			SuperLikeCount:     item.SuperLikeCount,
			SuperLikeKnown:     item.SuperLikeKnown || item.SuperLikeCount > 0,
			Heat24h:            strings.TrimSpace(item.Heat24h),
			ReadCount:          strings.TrimSpace(item.ReadCount),
			PostCount:          strings.TrimSpace(item.PostCount),
			FansCount:          strings.TrimSpace(item.FansCount),
			LevelText:          strings.TrimSpace(item.LevelText),
			CreatorOfficerText: strings.TrimSpace(item.CreatorOfficerText),
			FanDiamondText:     strings.TrimSpace(item.FanDiamondText),
			DailyRankText:      strings.TrimSpace(item.DailyRankText),
			CheckinExpText:     strings.TrimSpace(item.CheckinExpText),
			CheckinStreakText:  strings.TrimSpace(item.CheckinStreakText),
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].SignCount != results[j].SignCount {
			return results[i].SignCount > results[j].SignCount
		}
		return strings.ToLower(results[i].Name) < strings.ToLower(results[j].Name)
	})
	return results
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func blankFallback(v string, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	return v
}

func formatSignedDelta(delta int) string {
	if delta == 0 {
		return ""
	}
	sign := "-"
	if delta > 0 {
		sign = "+"
	} else {
		delta = -delta
	}
	if delta >= 1_000_000 {
		return sign + formatCompactDelta(float64(delta)/1_000_000) + "M"
	}
	if delta >= 1_000 {
		return sign + formatCompactDelta(float64(delta)/1_000) + "K"
	}
	return fmt.Sprintf("%s%d", sign, delta)
}

func formatCompactDelta(value float64) string {
	formatted := fmt.Sprintf("%.1f", value)
	return strings.TrimSuffix(formatted, ".0")
}

func appendDelta(value string, delta int) string {
	if text := formatSignedDelta(delta); text != "" {
		return value + " (" + text + ")"
	}
	return value
}

func formatWeiboSuperCountRanking(results []monitor.WeiboSuperCountResult, failed []string, title string, now time.Time, baseline map[string]int) string {
	lines := []string{title, now.Format("2006-01-02 15:04:05")}
	if len(results) == 0 {
		lines = append(lines, "- 暂无可用签到数据")
	} else {
		for i, item := range results {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = item.OID
			}
			line := fmt.Sprintf("%d) %s - 签到%d人", i+1, name, item.SignCount)
			if item.SuperLikeKnown || item.SuperLikeCount > 0 {
				line += fmt.Sprintf(" | 超LIKE%d人", item.SuperLikeCount)
			}
			if item.LevelText != "" {
				line += fmt.Sprintf(" | 等级%s", item.LevelText)
			}
			if baseline != nil {
				oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
				if prev, ok := baseline[oid]; ok {
					if delta := formatSignedDelta(item.SignCount - prev); delta != "" {
						line += fmt.Sprintf(" (%s)", delta)
					}
				} else {
					line += " (new)"
				}
			}
			lines = append(lines, line)
		}
	}
	if len(failed) > 0 {
		lines = append(lines, "")
		lines = append(lines, "获取失败:")
		for _, f := range failed {
			lines = append(lines, "- "+f)
		}
	}
	return strings.Join(lines, "\n")
}

func formatWeiboSuperCountDualRanking(results []monitor.WeiboSuperCountResult, failed []string, title string, now time.Time, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int) string {
	lines := []string{title, now.Format("2006-01-02 15:04:05")}

	if len(results) == 0 {
		lines = append(lines, "- 暂无可用签到数据")
	} else {
		signRank := make([]monitor.WeiboSuperCountResult, len(results))
		copy(signRank, results)
		sort.Slice(signRank, func(i, j int) bool {
			if signRank[i].SignCount != signRank[j].SignCount {
				return signRank[i].SignCount > signRank[j].SignCount
			}
			return strings.ToLower(signRank[i].Name) < strings.ToLower(signRank[j].Name)
		})

		lines = append(lines, "", "[签到榜]")
		for i, item := range signRank {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = item.OID
			}
			oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
			signText := fmt.Sprintf("%d人", item.SignCount)
			if signBaseline != nil {
				if prev, ok := signBaseline[oid]; ok {
					signText = appendDelta(signText, item.SignCount-prev)
				} else {
					signText += " (new)"
				}
			}
			line := fmt.Sprintf("%d) %s - 签到%s", i+1, name, signText)
			if rc := strings.TrimSpace(item.ReadCount); rc != "" {
				readText := rc
				if curr, ok := monitor.ParseChineseNumber(rc); ok {
					if prev, exists := readBaseline[oid]; exists {
						readText = appendDelta(readText, curr-prev)
					}
				}
				line += fmt.Sprintf(" | 阅读%s", readText)
			}
			likePart := ""
			if item.SuperLikeKnown || item.SuperLikeCount > 0 {
				likePart = fmt.Sprintf("%d", item.SuperLikeCount)
				if likeBaseline != nil {
					if prev, ok := likeBaseline[oid]; ok {
						likePart = appendDelta(likePart, item.SuperLikeCount-prev)
					}
				}
				line += fmt.Sprintf(" | 超LIKE%s人", likePart)
			}
			fans := strings.TrimSpace(item.FansCount)
			if fans != "" {
				fansText := fans
				if curr, ok := monitor.ParseChineseNumber(fans); ok {
					if prev, exists := fansBaseline[oid]; exists {
						fansText = appendDelta(fansText, curr-prev)
					}
				}
				line += fmt.Sprintf(" | 粉丝%s", fansText)
			}
			posts := strings.TrimSpace(item.PostCount)
			if posts != "" {
				postPart := fmt.Sprintf(" | 帖子%s", posts)
				if postBaseline != nil {
					if curr, ok := monitor.ParseChineseNumber(posts); ok && curr > 0 {
						if prev, ok := postBaseline[oid]; ok && prev > 0 {
							postPart = fmt.Sprintf(" | 帖子%s", appendDelta(posts, curr-prev))
						}
					}
				}
				line += postPart
			}
			if strings.TrimSpace(item.LevelText) != "" {
				line += fmt.Sprintf(" | 等级%s", strings.TrimSpace(item.LevelText))
			}
			if strings.TrimSpace(item.Heat24h) != "" {
				line += fmt.Sprintf(" | %s", strings.TrimSpace(item.Heat24h))
			}
			lines = append(lines, line)
		}
	}

	if len(failed) > 0 {
		lines = append(lines, "", "获取失败:")
		for _, f := range failed {
			lines = append(lines, "- "+f)
		}
	}

	return strings.Join(lines, "\n")
}

// weiboSuperCountHTMLSection is one table block inside the daily email.
type weiboSuperCountHTMLSection struct {
	GroupKey string
	Title    string
	Results  []monitor.WeiboSuperCountResult
}

type weiboSuperCountImage struct {
	Title    string
	Sections []weiboSuperCountHTMLSection
	// Key is the plan key (e.g. "image-1") this PNG was rendered from.
	Key string
	// TargetIDs are this image's own delivery targets; empty falls back to the
	// global WEIBO_SUPER_COUNT/REPORT_IMAGE_TARGETS list.
	TargetIDs []string
}

// buildWeiboSuperCountHTMLTable renders a single ranking table (dynamic columns: only columns with data).
func buildWeiboSuperCountHTMLTable(results []monitor.WeiboSuperCountResult, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int) string {
	esc := html.EscapeString
	type row struct {
		rank                                                   int
		name, sign, like, read, fans, posts, level, heat       string
		signDelta, likeDelta, readDelta, fansDelta, postsDelta string
		hasLike, hasRead, hasFans, hasPosts, hasLevel, hasHeat bool
	}
	rows := make([]row, 0, len(results))
	showLike, showRead, showFans, showPosts, showLevel, showHeat := true, false, false, false, false, false
	if len(results) > 0 {
		signRank := make([]monitor.WeiboSuperCountResult, len(results))
		copy(signRank, results)
		sort.Slice(signRank, func(i, j int) bool {
			if signRank[i].SignCount != signRank[j].SignCount {
				return signRank[i].SignCount > signRank[j].SignCount
			}
			return strings.ToLower(signRank[i].Name) < strings.ToLower(signRank[j].Name)
		})
		for i, item := range signRank {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = item.OID
			}
			oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
			signDelta := ""
			if signBaseline != nil {
				if prev, ok := signBaseline[oid]; ok {
					signDelta = formatSignedDelta(item.SignCount - prev)
				} else {
					signDelta = "new"
				}
			}
			likeText := "未获取"
			likeDelta := ""
			hasLike := item.SuperLikeKnown || item.SuperLikeCount > 0
			if hasLike {
				likeText = fmt.Sprintf("%d", item.SuperLikeCount)
				if likeBaseline != nil {
					if prev, ok := likeBaseline[oid]; ok {
						likeDelta = formatSignedDelta(item.SuperLikeCount - prev)
					}
				}
				showLike = true
			}
			read := strings.TrimSpace(item.ReadCount)
			hasRead := read != "" && read != "-"
			readDelta := ""
			if hasRead {
				if curr, ok := monitor.ParseChineseNumber(read); ok {
					if prev, exists := readBaseline[oid]; exists {
						readDelta = formatSignedDelta(curr - prev)
					}
				}
				showRead = true
			}
			fans := strings.TrimSpace(item.FansCount)
			hasFans := fans != "" && fans != "-"
			fansDelta := ""
			if hasFans {
				if curr, ok := monitor.ParseChineseNumber(fans); ok {
					if prev, exists := fansBaseline[oid]; exists {
						fansDelta = formatSignedDelta(curr - prev)
					}
				}
				showFans = true
			}
			posts := strings.TrimSpace(item.PostCount)
			hasPosts := posts != "" && posts != "-"
			postsDelta := ""
			if hasPosts {
				if postBaseline != nil {
					if curr, ok := monitor.ParseChineseNumber(posts); ok && curr > 0 {
						if prev, ok := postBaseline[oid]; ok && prev > 0 {
							postsDelta = formatSignedDelta(curr - prev)
						}
					}
				}
				showPosts = true
			}
			level := strings.TrimSpace(item.LevelText)
			hasLevel := level != "" && level != "-"
			if hasLevel {
				showLevel = true
			}
			heat := strings.TrimSpace(item.Heat24h)
			hasHeat := heat != "" && heat != "-"
			if hasHeat {
				showHeat = true
			}
			rows = append(rows, row{
				rank: i + 1, name: name,
				sign: fmt.Sprintf("%d", item.SignCount), signDelta: signDelta, likeDelta: likeDelta,
				readDelta: readDelta, fansDelta: fansDelta, postsDelta: postsDelta,
				like: likeText, read: read, fans: fans, posts: posts, level: level, heat: heat,
				hasLike: hasLike, hasRead: hasRead, hasFans: hasFans, hasPosts: hasPosts, hasLevel: hasLevel, hasHeat: hasHeat,
			})
		}
	}

	type col struct {
		key, label, align string
	}
	cols := []col{
		{key: "rank", label: "#", align: "center"},
		{key: "name", label: "超话", align: "left"},
		{key: "sign", label: "签到", align: "right"},
	}
	if showLike {
		cols = append(cols, col{key: "like", label: "超LIKE", align: "right"})
	}
	if showRead {
		cols = append(cols, col{key: "read", label: "阅读", align: "right"})
	}
	if showFans {
		cols = append(cols, col{key: "fans", label: "粉丝", align: "right"})
	}
	if showPosts {
		cols = append(cols, col{key: "posts", label: "帖子", align: "right"})
	}
	if showLevel {
		cols = append(cols, col{key: "level", label: "等级", align: "center"})
	}
	if showHeat {
		cols = append(cols, col{key: "heat", label: "24h热度", align: "left"})
	}

	thStyle := func(align string) string {
		return fmt.Sprintf(`padding:11px 12px;border-bottom:1px solid #cfd9d6;color:#7a8495;font-size:12px;font-weight:650;text-align:%s;`, align)
	}
	// extra is appended last so callers can override font-size/color cleanly.
	tdStyle := func(align string, extra string) string {
		base := fmt.Sprintf(`padding:10px 12px;border-bottom:1px solid #cfd9d6;font-size:13px;text-align:%s;`, align)
		extra = strings.TrimSpace(extra)
		if extra == "" {
			return base
		}
		if !strings.HasSuffix(extra, ";") {
			extra += ";"
		}
		return base + extra
	}

	var thead strings.Builder
	thead.WriteString(`<tr style="background:#d9e7e2;">`)
	for _, c := range cols {
		fmt.Fprintf(&thead, `<th style="%s">%s</th>`, thStyle(c.align), esc(c.label))
	}
	thead.WriteString(`</tr>`)

	var tableRows strings.Builder
	metricHTML := func(value, delta string) string {
		valueHTML := fmt.Sprintf(`<div style="white-space:nowrap;">%s</div>`, esc(value))
		if delta == "" {
			return valueHTML
		}
		color := "#667085"
		if strings.HasPrefix(delta, "+") {
			color = "#0f9d58"
		} else if strings.HasPrefix(delta, "-") {
			color = "#d93025"
		} else if delta == "new" {
			color = "#2466b3"
		}
		return valueHTML + fmt.Sprintf(`<div style="margin-top:2px;white-space:nowrap;color:%s;font-size:11px;font-weight:600;">(%s)</div>`, color, esc(delta))
	}
	if len(rows) == 0 {
		fmt.Fprintf(&tableRows, `<tr><td colspan="%d" style="padding:18px;text-align:center;color:#7a8495;font-size:14px;">暂无可用签到数据</td></tr>`, len(cols))
	} else {
		for _, r := range rows {
			bg := "#ffffff"
			if r.rank%2 == 0 {
				bg = "#f2f7f5"
			}
			fmt.Fprintf(&tableRows, `<tr style="background:%s;">`, bg)
			for _, c := range cols {
				switch c.key {
				case "rank":
					// Keep rank cell minimal — mobile mail clients are picky about first-column styles.
					fmt.Fprintf(&tableRows, `<td align="center" style="padding:10px 8px;border-bottom:1px solid #cfd9d6;color:#667085;font-size:13px;">%d</td>`, r.rank)
				case "name":
					fmt.Fprintf(&tableRows, `<td align="left" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;font-size:14px;font-weight:600"), esc(r.name))
				case "sign":
					fmt.Fprintf(&tableRows, `<td align="right" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;font-size:14px;vertical-align:top"), metricHTML(r.sign, r.signDelta))
				case "like":
					val := "未获取"
					delta := ""
					if r.hasLike {
						val = r.like
						delta = r.likeDelta
					}
					fmt.Fprintf(&tableRows, `<td align="right" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;vertical-align:top"), metricHTML(val, delta))
				case "read":
					val := "-"
					if r.hasRead {
						val = r.read
					}
					fmt.Fprintf(&tableRows, `<td align="right" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;vertical-align:top"), metricHTML(val, r.readDelta))
				case "fans":
					val := "-"
					if r.hasFans {
						val = r.fans
					}
					fmt.Fprintf(&tableRows, `<td align="right" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;vertical-align:top"), metricHTML(val, r.fansDelta))
				case "posts":
					val := "-"
					if r.hasPosts {
						val = r.posts
					}
					fmt.Fprintf(&tableRows, `<td align="right" style="%s">%s</td>`, tdStyle(c.align, "color:#172033;vertical-align:top"), metricHTML(val, r.postsDelta))
				case "level":
					val := "-"
					if r.hasLevel {
						val = r.level
					}
					fmt.Fprintf(&tableRows, `<td align="center" style="%s">%s</td>`, tdStyle(c.align, "color:#172033"), esc(val))
				case "heat":
					val := "-"
					if r.hasHeat {
						val = r.heat
					}
					fmt.Fprintf(&tableRows, `<td align="left" style="%s">%s</td>`, tdStyle(c.align, "color:#667085;font-size:12px"), esc(val))
				}
			}
			tableRows.WriteString(`</tr>`)
		}
	}

	// Avoid overflow/border-radius on tables — many mobile mail clients mangle them and
	// can surface raw <td> fragments as plain text (seen on 2026-07-19 rank #6).
	return fmt.Sprintf(`<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" border="0" style="border-collapse:collapse;border:1px solid %s;">
<thead>
%s
</thead>
<tbody>
%s
</tbody>
</table>`, reportCardBorder, thead.String(), tableRows.String())
}

// formatWeiboSuperCountDualRankingHTML builds multi-section HTML (one table per group) for daily email.
// Only columns that have real data within each section are rendered.
// forScreenshot=true trims the email-only "download" footer so PNG looks clean.
// 微博超话日报的配色直接沿用 Weverse 周报/月报/年报的色板，让两类报表
// 在群里看起来是同一套设计。墨绿主色 #2f6657，浅绿辅助 #d9e7e2 / #c8ddd5，
// 页面底色 #edf1f2。此前分组徽章用蓝（#2466b3/#edf4ff）而顶条用绿，
// 混在一起显得很杂乱。
const (
	reportInkGreen    = "#2f6657" // 主色：顶条、kicker、分组徽章文字
	reportPaleGreen   = "#e6f2ee" // 浅绿底：kicker 徽章
	reportHeaderGreen = "#d9e7e2" // 表头底
	reportRowGreen    = "#c8ddd5" // 隔行/首列底
	reportPageBg      = "#edf1f2" // 页面底色
	reportCardBorder  = "#b8c4c1" // 卡片描边
	reportTextDark    = "#20282d" // 正文
	reportTextMuted   = "#5f6c72" // 次要文字
)

func formatWeiboSuperCountDualRankingHTML(sections []weiboSuperCountHTMLSection, failed []string, title string, now time.Time, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int, forScreenshot bool) string {
	esc := html.EscapeString
	title = strings.TrimSpace(title)
	if title == "" {
		title = "超话签到人数日报"
	}
	displayTitle := strings.Trim(title, "[]")
	timeText := now.Format("2006-01-02 15:04:05")

	totalTopics := 0
	var sectionsHTML strings.Builder
	if len(sections) == 0 {
		sectionsHTML.WriteString(`<div style="padding:18px;text-align:center;color:#7a8495;font-size:14px;">暂无可用签到数据</div>`)
	} else {
		for i, sec := range sections {
			totalTopics += len(sec.Results)
			secTitle := strings.TrimSpace(sec.Title)
			if secTitle == "" {
				secTitle = fmt.Sprintf("分组 %d", i+1)
			}
			marginTop := "0"
			if i > 0 {
				marginTop = "22px"
			}
			fmt.Fprintf(&sectionsHTML, `<div style="margin-top:%s;">
<table role="presentation" cellspacing="0" cellpadding="0" border="0" style="margin:0 0 10px;border-collapse:collapse;">
<tr>
<td style="padding:4px 10px;border-radius:6px;background:%s;color:%s;font-size:12px;font-weight:650;">%s</td>
<td style="padding-left:8px;color:#667085;font-size:12px;">%d 个超话</td>
</tr>
</table>
%s
</div>`, marginTop, reportHeaderGreen, reportInkGreen, esc(secTitle), len(sec.Results), buildWeiboSuperCountHTMLTable(sec.Results, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline))
		}
	}

	failedHTML := ""
	if len(failed) > 0 {
		var fb strings.Builder
		fb.WriteString(`<div style="margin-top:18px;padding:14px 16px;background:#fff8f6;border:1px solid #f0d5cf;border-radius:8px;">`)
		fb.WriteString(`<div style="font-size:13px;font-weight:650;color:#b42318;margin-bottom:8px;">获取失败</div>`)
		fb.WriteString(`<ul style="margin:0;padding-left:18px;color:#7a2e22;font-size:13px;line-height:1.7;">`)
		for _, f := range failed {
			fmt.Fprintf(&fb, "<li>%s</li>", esc(f))
		}
		fb.WriteString(`</ul></div>`)
		failedHTML = fb.String()
	}

	footer := ""
	bodyPad := "28px 12px"
	cardPadTop := "26px 22px 10px"
	cardPadTables := "0 16px 16px"
	cardMax := "720px"
	if forScreenshot {
		// tight card for mobile 3:4 export — less outer blank
		bodyPad = "0"
		cardPadTop = "18px 16px 8px"
		cardPadTables = "0 12px 12px"
		cardMax = "680px"
		footer = ""
	} else {
		footer = `<tr><td style="padding:8px 28px 28px;" align="center">
<div style="display:inline-block;padding:12px 20px;background:#3478d4;color:#ffffff;text-decoration:none;border-radius:8px;font-size:14px;font-weight:650;">下载日报图片附件（PNG）</div>
<p style="margin:12px 0 0;color:#98a1af;font-size:12px;line-height:1.6;">邮件附件为手机比例卡片图，可直接保存；如配置了多个出图方案，会附带多张图片。管理面板：https://pocket48.jiufeng.cloud</p>
</td></tr>`
	}

	brandFooter := ""
	if !forScreenshot {
		brandFooter = `<tr><td style="padding:16px 28px;border-top:1px solid #edf0f4;color:#98a1af;font-size:12px;line-height:1.6;">Pocket48 Console 自动发送 · 微博超话签到人数日报</td></tr>`
	} else {
		brandFooter = ""
	}

	// Header title: for screenshots the plan name IS the report name (e.g.
	// "heart to heart 的超话日报"), with no Pocket48 brand mark.
	headerTitle := displayTitle
	headerKicker := "微博超话日报"
	if forScreenshot {
		if !strings.Contains(displayTitle, "日报") {
			headerTitle = displayTitle + " 的超话日报"
		}
		headerKicker = "每日报告"
	}

	return fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:0;background:#edf1f2;color:#20282d;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#edf1f2;padding:%s;"><tr><td align="center">
<table id="report-card" role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:%s;background:#ffffff;border:1px solid #b8c4c1;border-radius:12px;overflow:hidden;box-shadow:0 10px 30px rgba(31,52,89,.06);">
<tr><td style="height:4px;background:linear-gradient(90deg,#2f6657,#3d7a69);font-size:0;line-height:0;">&nbsp;</td></tr>
<tr><td style="padding:%s;">
<span style="display:inline-block;padding:5px 9px;border-radius:6px;background:#e6f2ee;color:#2f6657;font-size:11px;font-weight:800;letter-spacing:1px;">%s</span>
<h1 style="margin:10px 0 4px;font-size:20px;line-height:1.3;color:#172033;">%s</h1>
<p style="margin:0;color:#5f6c72;font-size:12px;line-height:1.5;">统计时间：%s · 共 %d 个超话 · %d 个分组</p>
</td></tr>
<tr><td style="padding:%s;">
%s
%s
</td></tr>
%s
%s
</table>
</td></tr></table>
</body></html>`, bodyPad, cardMax, cardPadTop, esc(headerKicker), esc(headerTitle), esc(timeText), totalTopics, len(sections), cardPadTables, sectionsHTML.String(), failedHTML, footer, brandFooter)
}

// buildWeiboSuperCountEmailSections groups results into email sections (one table per group).
// If no groups configured, returns a single section with all results.
// buildWeiboSuperCountSections groups results by configured groups and returns
// HTML sections. Standalone version (no Bot instance needed) for resend tool.
func buildWeiboSuperCountSections(groups map[string]*config.WeiboSuperCountGroupInfo, topics map[string]*config.WeiboSuperCountTopic, results []monitor.WeiboSuperCountResult) []weiboSuperCountHTMLSection {
	if len(groups) == 0 {
		return []weiboSuperCountHTMLSection{{GroupKey: "__all__", Title: "全部超话", Results: results}}
	}
	sortedGroupIDs := make([]string, 0, len(groups))
	for gid := range groups {
		sortedGroupIDs = append(sortedGroupIDs, gid)
	}
	sort.Strings(sortedGroupIDs)

	sections := make([]weiboSuperCountHTMLSection, 0, len(sortedGroupIDs))
	used := make(map[string]struct{})
	for _, gid := range sortedGroupIDs {
		ginfo := groups[gid]
		name := gid
		if ginfo != nil && strings.TrimSpace(ginfo.Name) != "" {
			name = ginfo.Name
		}
		var groupResults []monitor.WeiboSuperCountResult
		for _, r := range results {
			oid := strings.TrimPrefix(normalizeWeiboSuperOID(r.OID), "1022:")
			if t, ok := topics[oid]; ok && t.GroupName == gid {
				groupResults = append(groupResults, r)
			}
		}
		if len(groupResults) == 0 && name != gid {
			for _, r := range results {
				oid := strings.TrimPrefix(normalizeWeiboSuperOID(r.OID), "1022:")
				if t, ok := topics[oid]; ok && t.GroupName == name {
					groupResults = append(groupResults, r)
				}
			}
		}
		if len(groupResults) == 0 {
			continue
		}
		sort.Slice(groupResults, func(i, j int) bool {
			if groupResults[i].SignCount != groupResults[j].SignCount {
				return groupResults[i].SignCount > groupResults[j].SignCount
			}
			return strings.ToLower(groupResults[i].Name) < strings.ToLower(groupResults[j].Name)
		})
		for _, r := range groupResults {
			used[normalizeWeiboSuperOID(r.OID)] = struct{}{}
		}
		sections = append(sections, weiboSuperCountHTMLSection{GroupKey: gid, Title: name, Results: groupResults})
	}
	var leftover []monitor.WeiboSuperCountResult
	for _, r := range results {
		if _, ok := used[normalizeWeiboSuperOID(r.OID)]; !ok {
			leftover = append(leftover, r)
		}
	}
	if len(leftover) > 0 {
		sections = append(sections, weiboSuperCountHTMLSection{GroupKey: "__ungrouped__", Title: "未分组", Results: leftover})
	}
	if len(sections) == 0 {
		return []weiboSuperCountHTMLSection{{GroupKey: "__all__", Title: "全部超话", Results: results}}
	}
	return sections
}

// buildWeiboSuperCountImages applies the configured attachment layout. Only
// groups referenced by a plan produce an image; unreferenced groups are omitted
// entirely (the operator chooses exactly which groups become daily images).
func buildWeiboSuperCountImages(plans map[string]*config.WeiboSuperCountImageGroupInfo, sections []weiboSuperCountHTMLSection) []weiboSuperCountImage {
	if len(plans) == 0 {
		return []weiboSuperCountImage{{Sections: sections}}
	}
	planKeys := make([]string, 0, len(plans))
	for key := range plans {
		planKeys = append(planKeys, key)
	}
	sort.Strings(planKeys)
	byGroup := make(map[string]weiboSuperCountHTMLSection, len(sections))
	for _, section := range sections {
		byGroup[section.GroupKey] = section
	}
	images := make([]weiboSuperCountImage, 0, len(plans))
	for _, key := range planKeys {
		plan := plans[key]
		if plan == nil {
			continue
		}
		selected := make([]weiboSuperCountHTMLSection, 0, len(plan.GroupKeys))
		for _, groupKey := range plan.GroupKeys {
			if section, ok := byGroup[strings.TrimSpace(groupKey)]; ok {
				selected = append(selected, section)
			}
		}
		if len(selected) > 0 {
			images = append(images, weiboSuperCountImage{
				Title:     strings.TrimSpace(plan.Name),
				Sections:  selected,
				Key:       key,
				TargetIDs: append([]string{}, plan.TargetIDs...),
			})
		}
	}
	if len(images) == 0 {
		return []weiboSuperCountImage{{Sections: sections}}
	}
	return images
}

func buildWeiboSuperCountImageAttachments(cfg *config.Config, sections []weiboSuperCountHTMLSection, failed []string, title string, now time.Time, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int) []emailAttachment {
	images := buildWeiboSuperCountImages(cfg.WeiboSuperCountImageGroups, sections)
	attachments := make([]emailAttachment, 0, len(images))
	for index, image := range images {
		imageTitle := title
		if image.Title != "" {
			imageTitle = image.Title
		}
		shotHTML := formatWeiboSuperCountDualRankingHTML(image.Sections, failed, imageTitle, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline, true)
		png, err := renderHTMLToPNG(shotHTML)
		if err != nil {
			log.Printf("[WeiboSuperCount] html→png failed image=%d name=%q: %v", index+1, image.Title, err)
			continue
		}
		filename := fmt.Sprintf("weibo-super-count-%s-%02d.png", now.Format("2006-01-02"), index+1)
		attachments = append(attachments, emailAttachment{
			Name:        filename,
			ContentType: "image/png",
			Data:        png,
			TargetIDs:   append([]string{}, image.TargetIDs...),
		})
	}
	return attachments
}

func (b *Bot) buildWeiboSuperCountEmailSections(results []monitor.WeiboSuperCountResult) []weiboSuperCountHTMLSection {
	return buildWeiboSuperCountSections(b.getWeiboSuperCountGroups(), b.getWeiboSuperCountTopics(), results)
}

// ResendWeiboSuperCountDailyEmail rebuilds the daily HTML (+ optional PNG) from
// already-fetched results and sends via ALERT_EMAIL_* (used for manual backfill).
func ResendWeiboSuperCountDailyEmail(
	cfg *config.Config,
	results []monitor.WeiboSuperCountResult,
	failed []string,
	title string,
	now time.Time,
	signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int,
) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	// Build grouped sections from config.
	sections := buildWeiboSuperCountSections(cfg.WeiboSuperCountGroups, cfg.WeiboSuperCountTopics, results)
	htmlBody := formatWeiboSuperCountDualRankingHTML(sections, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline, false)
	subject := "微博超话日报｜" + strings.Trim(strings.TrimSpace(title), "[]")
	atts := buildWeiboSuperCountImageAttachments(cfg, sections, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	plain := formatWeiboSuperCountDualRanking(results, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	return sendAdminHTMLEmail(cfg, subject, htmlBody, plain, atts...)
}

// sendWeiboSuperCountDailyEmail sends one email with multi-group tables and one
// or more PNG attachments according to the configured image layout.
func (b *Bot) sendWeiboSuperCountDailyEmail(reportText, title string, results []monitor.WeiboSuperCountResult, failed []string, now time.Time, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline map[string]int) {
	sections := b.buildWeiboSuperCountEmailSections(results)
	htmlBody := formatWeiboSuperCountDualRankingHTML(sections, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline, false)
	subject := "微博超话日报｜" + strings.Trim(strings.TrimSpace(title), "[]")
	atts := buildWeiboSuperCountImageAttachments(b.cfg, sections, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	b.notifyAdminsEmailReport(subject, htmlBody, reportText, atts...)
	// Fan each PNG out to ITS OWN targets, so image 1 can go to群A while image 2
	// goes to群B. An image without its own list falls back to the global
	// WEIBO_REPORT_IMAGE_TARGETS (backward compatible with older setups).
	for _, att := range atts {
		targetIDs := att.TargetIDs
		if len(targetIDs) == 0 {
			targetIDs = b.cfg.WeiboReportImageTargets
		}
		if len(targetIDs) == 0 {
			continue
		}
		for _, targetID := range targetIDs {
			target := b.cfg.ResolveTarget(targetID)
			if target.ID == "" {
				continue
			}
			b.sendReportImage(target, att.Data, reportCardTitle(title), now)
		}
	}
}

// reportCardTitle 把配置里的标题转成飞书卡片顶栏文案：去掉邮件专用的方括号
// 包裹，并保证有一个可读的名字。
func reportCardTitle(title string) string {
	trimmed := strings.Trim(strings.TrimSpace(title), "[]")
	if trimmed == "" {
		return "微博超话日报"
	}
	if !strings.Contains(trimmed, "日报") {
		return trimmed + " 的超话日报"
	}
	return trimmed
}

// reportImageFileTTL 是日报临时图片的存活时间。投递是异步的，必须等适配器
// 真正读完文件（QQ 上传、飞书 upload + 发消息）才能删。60 秒足够覆盖
// 三个目标顺序发送与偶发的网络重试。
const reportImageFileTTL = 60 * time.Second

// sendReportImage delivers report PNG bytes.
//
// Feishu receives a structured Document so the card carries the usual chrome —
// title on top, source on the bottom-left, timestamp bottom-right — with the
// report image inside. QQ keeps the plain image segment so its rendering stays
// exactly as before.
func (b *Bot) sendReportImage(target config.DeliveryTarget, png []byte, title string, createdAt time.Time) {
	if b == nil || len(png) == 0 {
		return
	}
	if strings.EqualFold(target.Platform, "feishu") {
		cardTitle := strings.TrimSpace(title)
		if cardTitle == "" {
			cardTitle = "微博超话日报"
		}
		when := createdAt
		if when.IsZero() {
			when = time.Now()
		}
		doc := &message.Document{
			Source:    "微博",
			Title:     cardTitle,
			Author:    "微博",
			CreatedAt: when,
			// readMedia 支持 base64:// 前缀的内联字节，日报图片无需落盘。
			Media: []message.Media{{
				Kind:   "image",
				Source: "base64://" + base64.StdEncoding.EncodeToString(png),
			}},
		}
		if b.feishu != nil {
			b.feishu.SendNow(context.Background(), outbound.Target{
				Platform: "feishu",
				Kind:     outbound.GroupChat,
				Address:  target.Address,
			}, doc)
			return
		}
	}
	f, err := os.CreateTemp("", "p48-report-*.png")
	if err != nil {
		log.Printf("[Report] temp png create failed: %v", err)
		return
	}
	path := f.Name()
	// 不能在这里 defer os.Remove：投递是**异步**的（Feishu.Send 只入队就返回，
	// worker 稍后才读文件），文件在适配器真正读取前就被删掉，结果是
	// "open /tmp/p48-report-*.png: no such file or directory"，
	// 群里只剩一个空占位。改为延迟删除，并留出足够的上传窗口。
	defer func() {
		time.AfterFunc(reportImageFileTTL, func() { _ = os.Remove(path) })
	}()
	if _, err := f.Write(png); err != nil {
		f.Close()
		log.Printf("[Report] temp png write failed: %v", err)
		return
	}
	f.Close()
	b.sendTarget(target, []interface{}{napcat.ImageSegment(path)})
}

func (b *Bot) runWeiboSuperCountDailyPushLoop() {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if !b.cfg.WeiboSuperCountEnabled {
			continue
		}
		numTopics := len(b.getWeiboSuperCountTopics())
		if numTopics == 0 {
			continue
		}
		now := time.Now().In(loc)
		if !withinWeiboDailyCollectionWindow(now) {
			continue
		}
		today := now.Format("2006-01-02")
		if strings.TrimSpace(b.cfg.WeiboSuperCountLastPushDate) == today {
			continue
		}

		deadline := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, loc)
		started := time.Now()
		log.Printf("[WeiboSuperCount] daily collection started topics=%d deadline=%s", numTopics, deadline.Format(time.RFC3339))
		results, failed := b.fetchWeiboSuperCountAllBefore(deadline)
		log.Printf("[WeiboSuperCount] daily collection finished results=%d failed=%d elapsed=%s", len(results), len(failed), time.Since(started).Round(time.Millisecond))
		yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
		var signBaseline map[string]int
		var likeBaseline map[string]int
		var yesterdayV2 map[string]*config.WeiboSuperCountSnapshotItem
		if b.cfg.WeiboSuperCountDailySnapshotsV2 != nil {
			yesterdayV2 = b.cfg.WeiboSuperCountDailySnapshotsV2[yesterday]
			signBaseline = buildSignBaselineFromSnapshotV2(yesterdayV2)
			likeBaseline = buildLikeBaselineFromSnapshotV2(yesterdayV2)
		}
		if signBaseline == nil && b.cfg.WeiboSuperCountDailySnapshots != nil {
			signBaseline = b.cfg.WeiboSuperCountDailySnapshots[yesterday]
		}
		readBaseline := buildReadBaselineFromSnapshotV2(yesterdayV2)
		fansBaseline := buildFansBaselineFromSnapshotV2(yesterdayV2)
		postBaseline := buildPostBaselineFromSnapshotV2(yesterdayV2)

		// Email: ONE combined daily report with PNG attachment.
		// QQ: keep per-group text for readability in chat.
		// Delivery: WEIBO_SUPER_COUNT_DELIVERY = email | qq | both (default email).
		delivery := strings.ToLower(strings.TrimSpace(b.cfg.WeiboSuperCountDelivery))
		if delivery == "" {
			delivery = "email"
		}
		wantEmail := delivery == "email" || delivery == "both"
		wantQQ := delivery == "qq" || delivery == "both"

		titleEmail := "[超话签到人数日报]"
		reportEmail := formatWeiboSuperCountDualRanking(results, failed, titleEmail, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
		if wantEmail {
			b.sendWeiboSuperCountDailyEmail(reportEmail, titleEmail, results, failed, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
		}
		if wantQQ {
			qqRecipients := b.collectWeiboSuperCountQQRecipients()
			groups := b.getWeiboSuperCountGroups()
			if len(groups) == 0 {
				b.notifyQQUsers(reportEmail, qqRecipients...)
			} else {
				sortedGroupIDs := make([]string, 0, len(groups))
				for gid := range groups {
					sortedGroupIDs = append(sortedGroupIDs, gid)
				}
				sort.Strings(sortedGroupIDs)
				for _, gid := range sortedGroupIDs {
					ginfo := groups[gid]
					groupResults := b.filterResultsByGroup(results, gid)
					if len(groupResults) == 0 {
						continue
					}
					title := fmt.Sprintf("[超话签到人数日报 - %s]", ginfo.Name)
					groupFailed := failed // show failures once per group text (cheap)
					report := formatWeiboSuperCountDualRanking(groupResults, groupFailed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
					b.notifyQQUsers(report, qqRecipients...)
				}
			}
		}

		if b.cfg.WeiboSuperCountDailySnapshots == nil {
			b.cfg.WeiboSuperCountDailySnapshots = make(map[string]map[string]int)
		}
		if b.cfg.WeiboSuperCountDailySnapshotsV2 == nil {
			b.cfg.WeiboSuperCountDailySnapshotsV2 = make(map[string]map[string]*config.WeiboSuperCountSnapshotItem)
		}
		b.cfg.WeiboSuperCountDailySnapshots[today] = buildWeiboSuperCountDailySnapshot(results)
		b.cfg.WeiboSuperCountDailySnapshotsV2[today] = buildWeiboSuperCountDailySnapshotV2(results)
		b.cfg.WeiboSuperCountLastPushDate = today
		if err := b.cfg.Save(); err != nil {
			log.Printf("[WeiboSuperCount] save last push date failed: %v", err)
		}
	}
}

func withinWeiboDailyCollectionWindow(now time.Time) bool {
	return now.Hour() == 23 && now.Minute() == 59 && now.Second() >= 15
}

// runWeiboAppAuthHealthCheckLoop 主动健康检查微博 App 认证，每 2 小时检查一次。
// 认证失效时通知管理员，恢复时也通知。通知冷却 4 小时避免刷屏。
// 2026-07-18：APP 链路暂时关闭，此循环直接 return，不删实现。
func (b *Bot) runWeiboAppAuthHealthCheckLoop() {
	// Temporarily disabled — web Cookie path only. Re-enable by removing this early return.
	log.Printf("[WeiboAppAuth] health check loop disabled (APP path off; web only)")
	return

	if b.cfg.WeiboApp == nil {
		return
	}

	// 启动 5 分钟后首次检查（给其他初始化留时间）
	time.Sleep(5 * time.Minute)
	b.weiboAppAuthHealthCheckTick()

	ticker := time.NewTicker(2 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		b.weiboAppAuthHealthCheckTick()
	}
}

func (b *Bot) weiboAppAuthHealthCheckTick() {
	// APP path temporarily off — no health notify.
	return

	if b.cfg.WeiboApp == nil {
		return
	}

	ok, detail, err := b.weiboMonitor.CheckWeiboAppAuth()
	now := time.Now().Unix()

	// 健康 — 之前有通知过认证失效则发恢复通知
	if ok {
		lastNotify := strings.TrimSpace(b.cfg.WeiboAppAuthHealthCheckNotifyAt)
		if lastNotify != "" {
			b.cfg.WeiboAppAuthHealthCheckNotifyAt = ""
			if saveErr := b.cfg.Save(); saveErr != nil {
				log.Printf("[WeiboAppAuth] save health check notify state failed: %v", saveErr)
			}
			b.notifyAdminsQQ(fmt.Sprintf("✅ 微博 App 认证已恢复。\n详情: %s", detail))
			log.Printf("[WeiboAppAuth] health check OK, sent recovery notice: %s", detail)
		}
		return
	}

	// 异常 — 检查冷却期
	cooldown := int64(4 * 3600) // 4 小时
	lastNotifyStr := strings.TrimSpace(b.cfg.WeiboAppAuthHealthCheckNotifyAt)
	if lastNotifyStr != "" {
		lastNotify, parseErr := strconv.ParseInt(lastNotifyStr, 10, 64)
		if parseErr == nil && now-lastNotify < cooldown {
			return // 冷却中
		}
	}

	// 发通知
	reason := detail
	if err != nil {
		reason = fmt.Sprintf("%v", err)
	}
	msg := fmt.Sprintf("🚨 微博 App 认证失效（主动健康检查发现）\n详情: %s\n请尽快更新 WEIBO_APP 抓包参数（Authorization / gsid / aid / s 等）。", reason)
	b.notifyAdmins(msg)

	b.cfg.WeiboAppAuthHealthCheckNotifyAt = fmt.Sprintf("%d", now)
	if saveErr := b.cfg.Save(); saveErr != nil {
		log.Printf("[WeiboAppAuth] save health check notify state failed: %v", saveErr)
	}
	log.Printf("[WeiboAppAuth] health check FAILED, admin notified: %s", reason)
}

func (b *Bot) handleWeiboSuperCountGroupCommand(args []string) string {
	if len(args) < 5 {
		return "用法: weibo super count group <create|list|rename|del> [参数]"
	}
	action := strings.ToLower(strings.TrimSpace(args[4]))
	groups := b.getWeiboSuperCountGroups()
	topics := b.getWeiboSuperCountTopics()

	switch action {
	case "list":
		if len(groups) == 0 {
			return "暂无分组，请先执行：weibo super count group create <名称>"
		}
		var lines []string
		sortedIDs := make([]string, 0, len(groups))
		for gid := range groups {
			sortedIDs = append(sortedIDs, gid)
		}
		sort.Strings(sortedIDs)
		for _, gid := range sortedIDs {
			ginfo := groups[gid]
			count := 0
			for _, t := range topics {
				if t.GroupName == gid {
					count++
				}
			}
			lines = append(lines, fmt.Sprintf("- %s (%d个超话)  ID=%s", ginfo.Name, count, gid))
		}
		return "分组列表:\n" + strings.Join(lines, "\n")

	case "create":
		if len(args) < 6 {
			return "格式错误: weibo super count group create <名称>"
		}
		displayName := strings.TrimSpace(strings.Join(args[5:], " "))
		if displayName == "" {
			return "格式错误: 名称不能为空"
		}
		// Generate a stable group ID from name (lowercase, no spaces)
		gid := strings.ToLower(strings.ReplaceAll(displayName, " ", "_"))
		if _, exists := groups[gid]; exists {
			return fmt.Sprintf("分组已存在: %s (名称=%s)", gid, displayName)
		}
		groups[gid] = &config.WeiboSuperCountGroupInfo{Name: displayName}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 已创建分组「%s」(ID=%s)", displayName, gid)

	case "rename":
		if len(args) < 7 {
			return "格式错误: weibo super count group rename <旧名称> <新名称>"
		}
		oldName := strings.TrimSpace(args[5])
		newName := strings.TrimSpace(strings.Join(args[6:], " "))
		if newName == "" {
			return "格式错误: 新名称不能为空"
		}
		// Try to find by display name first, then by ID
		var foundGID string
		for gid, ginfo := range groups {
			if ginfo.Name == oldName || gid == oldName {
				foundGID = gid
				break
			}
		}
		if foundGID == "" {
			return fmt.Sprintf("未找到分组: %s", oldName)
		}
		groups[foundGID].Name = newName
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 分组已重命名为「%s」", newName)

	case "del", "delete":
		if len(args) < 6 {
			return "格式错误: weibo super count group del <名称>"
		}
		targetName := strings.TrimSpace(strings.Join(args[5:], " "))
		var foundGID string
		for gid, ginfo := range groups {
			if ginfo.Name == targetName || gid == targetName {
				foundGID = gid
				break
			}
		}
		if foundGID == "" {
			return fmt.Sprintf("未找到分组: %s", targetName)
		}
		displayName := groups[foundGID].Name
		// Move topics in this group to ungrouped
		for _, t := range topics {
			if t.GroupName == foundGID {
				t.GroupName = ""
			}
		}
		delete(groups, foundGID)
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 已删除分组「%s」，其下超话已取消分组", displayName)

	default:
		return "用法: weibo super count group <create|list|rename|del> [参数]"
	}
}

func (b *Bot) handleWeiboSuperCountCommand(args []string) string {
	topics := b.getWeiboSuperCountTopics()

	// Usage: bot weibo super count [组名|list|yesterday|...]
	// - no arg: show all groups in one message
	// - 组名: filter by that group
	// - list/yesterday/bind/etc: existing subcommands

	if len(args) >= 4 {
		sub := strings.ToLower(strings.TrimSpace(args[3]))
		// Known subcommands — dispatch as before
		switch sub {
		case "group", "enable", "on", "off", "list", "yesterday", "bind", "unbind", "del", "delete":
			return b.handleWeiboSuperCountSubcommand(args, topics, sub)
		default:
			// Not a known subcommand → treat as group name
		}
	}

	// Query: either just "bot weibo super count" or "bot weibo super count <组名>"
	if !b.cfg.WeiboSuperCountEnabled {
		return "该功能已关闭，请先开启：weibo super count enable on"
	}
	if len(topics) == 0 {
		return "暂无超话签到人数绑定，请先执行：weibo super count bind <oid> [名称]"
	}

	results, failed := b.fetchWeiboSuperCountAll()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	now := time.Now().In(loc)
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	var signBaseline map[string]int
	var likeBaseline map[string]int
	var yesterdayV2 map[string]*config.WeiboSuperCountSnapshotItem
	if b.cfg.WeiboSuperCountDailySnapshotsV2 != nil {
		yesterdayV2 = b.cfg.WeiboSuperCountDailySnapshotsV2[yesterday]
		signBaseline = buildSignBaselineFromSnapshotV2(yesterdayV2)
		likeBaseline = buildLikeBaselineFromSnapshotV2(yesterdayV2)
	}
	if signBaseline == nil && b.cfg.WeiboSuperCountDailySnapshots != nil {
		signBaseline = b.cfg.WeiboSuperCountDailySnapshots[yesterday]
	}
	readBaseline := buildReadBaselineFromSnapshotV2(yesterdayV2)
	fansBaseline := buildFansBaselineFromSnapshotV2(yesterdayV2)
	postBaseline := buildPostBaselineFromSnapshotV2(yesterdayV2)

	// Check if a group name was provided as argument
	queryGroup := ""
	if len(args) >= 4 {
		queryGroup = strings.TrimSpace(strings.Join(args[3:], " "))
	}

	if queryGroup != "" {
		// Filter by group
		groups := b.getWeiboSuperCountGroups()
		resolvedGroup := queryGroup
		for gid, ginfo := range groups {
			if ginfo.Name == queryGroup || gid == queryGroup {
				resolvedGroup = gid
				break
			}
		}
		filtered := b.filterResultsByGroup(results, resolvedGroup)
		if len(filtered) == 0 {
			return fmt.Sprintf("分组「%s」下没有数据或分组不存在", queryGroup)
		}
		ginfo := groups[resolvedGroup]
		title := "[超话签到人数查询]"
		if ginfo != nil {
			title = fmt.Sprintf("[超话签到人数查询 - %s]", ginfo.Name)
		}
		return formatWeiboSuperCountDualRanking(filtered, failed, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	}

	// No group specified → show all groups in one message
	groups := b.getWeiboSuperCountGroups()
	if len(groups) == 0 {
		// No groups → flat output (backward compat)
		return formatWeiboSuperCountDualRanking(results, failed, "[超话签到人数查询]", now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	}

	// Build per-group sections
	sortedGroupIDs := make([]string, 0, len(groups))
	for gid := range groups {
		sortedGroupIDs = append(sortedGroupIDs, gid)
	}
	sort.Strings(sortedGroupIDs)
	var parts []string
	for _, gid := range sortedGroupIDs {
		ginfo := groups[gid]
		groupResults := b.filterResultsByGroup(results, gid)
		if len(groupResults) == 0 {
			continue
		}
		title := fmt.Sprintf("[超话签到人数查询 - %s]", ginfo.Name)
		section := formatWeiboSuperCountDualRanking(groupResults, nil, title, now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
		parts = append(parts, section)
	}
	if len(parts) == 0 {
		return "暂无可用签到数据"
	}
	return strings.Join(parts, "\n\n")
}

// handleWeiboSuperCountSubcommand dispatches known subcommands (list, bind, yesterday, etc.)
func (b *Bot) handleWeiboSuperCountSubcommand(args []string, topics map[string]*config.WeiboSuperCountTopic, sub string) string {
	switch sub {
	case "group":
		return b.handleWeiboSuperCountGroupCommand(args)
	case "enable":
		if len(args) < 5 {
			state := "off"
			if b.cfg.WeiboSuperCountEnabled {
				state = "on"
			}
			return fmt.Sprintf("当前 super count 功能: %s", state)
		}
		toggle := strings.ToLower(strings.TrimSpace(args[4]))
		if toggle != "on" && toggle != "off" {
			return "格式错误: weibo super count enable <on/off>"
		}
		b.cfg.WeiboSuperCountEnabled = toggle == "on"
		if b.cfg.WeiboSuperCountEnabled {
			b.cfg.WeiboSuperCountLastPushDate = ""
		}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] super count 功能已%s", map[bool]string{true: "开启", false: "关闭"}[b.cfg.WeiboSuperCountEnabled])
	case "on", "off":
		b.cfg.WeiboSuperCountEnabled = sub == "on"
		if b.cfg.WeiboSuperCountEnabled {
			b.cfg.WeiboSuperCountLastPushDate = ""
		}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] super count 功能已%s", map[bool]string{true: "开启", false: "关闭"}[b.cfg.WeiboSuperCountEnabled])
	case "list":
		if len(topics) == 0 {
			return "暂无超话签到人数绑定"
		}
		// Check for -g flag
		listGroup := ""
		cleanArgs := make([]string, 0)
		for i := 4; i < len(args); i++ {
			if args[i] == "-g" && i+1 < len(args) {
				listGroup = strings.TrimSpace(args[i+1])
				i++ // skip group name
			} else {
				cleanArgs = append(cleanArgs, args[i])
			}
		}
		if listGroup != "" {
			lines := []string{fmt.Sprintf("分组「%s」的超话签到人数绑定:", listGroup)}
			count := 0
			for oid, t := range topics {
				if t.GroupName == listGroup {
					name := strings.TrimSpace(t.Name)
					if name == "" {
						name = oid
					}
					lines = append(lines, fmt.Sprintf("- %s (oid=%s)", name, oid))
					count++
				}
			}
			if count == 0 {
				return fmt.Sprintf("分组「%s」下没有超话绑定", listGroup)
			}
			return strings.Join(lines, "\n")
		}
		// Group by group name
		groups := b.getWeiboSuperCountGroups()
		if len(groups) == 0 {
			// Fallback: flat list
			lines := []string{"超话签到人数绑定列表:"}
			for oid, t := range topics {
				name := strings.TrimSpace(t.Name)
				if name == "" {
					name = oid
				}
				lines = append(lines, fmt.Sprintf("- %s (oid=%s)", name, oid))
			}
			return strings.Join(lines, "\n")
		}
		sortedGroupIDs := make([]string, 0, len(groups))
		for gid := range groups {
			sortedGroupIDs = append(sortedGroupIDs, gid)
		}
		sort.Strings(sortedGroupIDs)
		lines := []string{"超话签到人数绑定列表（按分组）:"}
		for _, gid := range sortedGroupIDs {
			ginfo := groups[gid]
			lines = append(lines, "", fmt.Sprintf("▎ %s:", ginfo.Name))
			groupCount := 0
			for oid, t := range topics {
				if t.GroupName == gid {
					name := strings.TrimSpace(t.Name)
					if name == "" {
						name = oid
					}
					lines = append(lines, fmt.Sprintf("  - %s (oid=%s)", name, oid))
					groupCount++
				}
			}
			if groupCount == 0 {
				lines = append(lines, "  (空)")
			}
		}
		return strings.Join(lines, "\n")
	case "yesterday":
		if !b.cfg.WeiboSuperCountEnabled {
			return "该功能已关闭，请先开启：weibo super count enable on"
		}
		if len(topics) == 0 {
			return "暂无超话签到人数绑定，请先执行：weibo super count bind <oid> [名称]"
		}
		loc, _ := time.LoadLocation("Asia/Shanghai")
		if loc == nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		now := time.Now().In(loc)
		yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
		beforeYesterday := now.AddDate(0, 0, -2).Format("2006-01-02")

		var results []monitor.WeiboSuperCountResult
		var signBaseline map[string]int
		var likeBaseline map[string]int
		var readBaseline map[string]int
		var fansBaseline map[string]int
		var postBaseline map[string]int

		if b.cfg.WeiboSuperCountDailySnapshotsV2 != nil {
			yesterdayV2 := b.cfg.WeiboSuperCountDailySnapshotsV2[yesterday]
			if len(yesterdayV2) > 0 {
				results = b.buildWeiboSuperCountResultsFromSnapshotV2(yesterdayV2)
			}
			beforeV2 := b.cfg.WeiboSuperCountDailySnapshotsV2[beforeYesterday]
			signBaseline = buildSignBaselineFromSnapshotV2(beforeV2)
			likeBaseline = buildLikeBaselineFromSnapshotV2(beforeV2)
			readBaseline = buildReadBaselineFromSnapshotV2(beforeV2)
			fansBaseline = buildFansBaselineFromSnapshotV2(beforeV2)
			postBaseline = buildPostBaselineFromSnapshotV2(beforeV2)
		}

		if len(results) == 0 {
			var snapshot map[string]int
			if b.cfg.WeiboSuperCountDailySnapshots != nil {
				snapshot = b.cfg.WeiboSuperCountDailySnapshots[yesterday]
				if signBaseline == nil {
					signBaseline = b.cfg.WeiboSuperCountDailySnapshots[beforeYesterday]
				}
			}
			if len(snapshot) == 0 {
				return fmt.Sprintf("昨日（%s）暂无快照数据，可能日报时服务离线或尚未生成日报", yesterday)
			}
			results = b.buildWeiboSuperCountResultsFromSnapshot(snapshot)
		}

		if len(results) == 0 {
			return fmt.Sprintf("昨日（%s）快照为空", yesterday)
		}
		return formatWeiboSuperCountDualRanking(results, nil, "[超话签到人数昨日补查]", now, signBaseline, likeBaseline, readBaseline, fansBaseline, postBaseline)
	case "bind":
		if len(args) < 5 {
			return "格式错误: weibo super count bind <oid> [名称] [-g 分组名]"
		}
		oid := normalizeWeiboSuperOID(strings.TrimSpace(args[4]))
		if oid == "" {
			return "格式错误: oid 不能为空"
		}
		name := ""
		bindGroup := ""
		nameParts := make([]string, 0)
		for i := 5; i < len(args); i++ {
			if args[i] == "-g" && i+1 < len(args) {
				bindGroup = strings.TrimSpace(args[i+1])
				i++ // skip group name
			} else {
				nameParts = append(nameParts, args[i])
			}
		}
		if len(nameParts) > 0 {
			name = strings.TrimSpace(strings.Join(nameParts, " "))
		}
		// If group not specified, use the first existing group as default
		if bindGroup == "" {
			groups := b.getWeiboSuperCountGroups()
			for gid := range groups {
				bindGroup = gid
				break
			}
		}
		topic := &config.WeiboSuperCountTopic{OID: oid, Name: name}
		if bindGroup != "" {
			topic.GroupName = bindGroup
		}
		topics[oid] = topic
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		msg := ""
		if bindGroup == "" {
			msg = "未指定分组"
		} else {
			if ginfo, ok := b.getWeiboSuperCountGroups()[bindGroup]; ok {
				msg = fmt.Sprintf("分组「%s」", ginfo.Name)
			} else {
				msg = fmt.Sprintf("分组「%s」", bindGroup)
			}
		}
		if name == "" {
			return fmt.Sprintf("[OK] 已绑定超话签到人数 oid=%s (%s)", oid, msg)
		}
		return fmt.Sprintf("[OK] 已绑定超话签到人数 %s (oid=%s, %s)", name, oid, msg)
	case "unbind", "del", "delete":
		if len(args) < 5 {
			return "格式错误: weibo super count unbind <oid|名称>"
		}
		key := strings.TrimSpace(strings.Join(args[4:], " "))
		oid, topic := b.findWeiboSuperCountTopic(key)
		if topic == nil {
			return fmt.Sprintf("未找到超话签到人数绑定: %s", key)
		}
		name := strings.TrimSpace(topic.Name)
		if name == "" {
			name = oid
		}
		delete(topics, oid)
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 已解绑超话签到人数: %s", name)
	default:
		if !b.cfg.WeiboSuperCountEnabled {
			return "该功能已关闭，请先开启：weibo super count enable on"
		}
		key := strings.TrimSpace(strings.Join(args[3:], " "))
		oid, topic := b.findWeiboSuperCountTopic(key)
		if topic == nil {
			return fmt.Sprintf("未找到超话签到人数绑定: %s", key)
		}
		res, err := b.weiboMonitor.FetchSuperCountByOID(oid, topic.Name)
		if err != nil {
			return fmt.Sprintf("查询失败: %v", err)
		}
		name := strings.TrimSpace(res.Name)
		if name == "" {
			if strings.TrimSpace(topic.Name) != "" {
				name = strings.TrimSpace(topic.Name)
			} else {
				name = oid
			}
		}
		lines := []string{fmt.Sprintf("[超话签到人数] %s", name), fmt.Sprintf("签到%d人", res.SignCount)}
		if strings.TrimSpace(res.TodayInteraction) != "" {
			lines = append(lines, res.TodayInteraction)
		}
		if strings.TrimSpace(res.Heat24h) != "" {
			lines = append(lines, res.Heat24h)
		}
		if strings.TrimSpace(res.SuperLikeText) != "" {
			lines = append(lines, res.SuperLikeText)
		}
		if strings.TrimSpace(res.LevelText) != "" {
			lines = append(lines, "超话等级 "+res.LevelText)
		}
		if strings.TrimSpace(res.PostCount) != "" || strings.TrimSpace(res.FansCount) != "" {
			postLabel := blankFallback(res.PostLabel, "帖子")
			fansLabel := blankFallback(res.FansLabel, "粉丝")
			lines = append(lines, fmt.Sprintf("%s%s / %s%s", postLabel, blankFallback(res.PostCount, "?"), fansLabel, blankFallback(res.FansCount, "?")))
		}
		if strings.TrimSpace(res.Source) != "" {
			lines = append(lines, "数据来源 "+res.Source)
		}
		return strings.Join(lines, "\n")
	}
}

func (b *Bot) migrateWeiboSuperPostSubscriptionsToBoundGroup() bool {
	if b.cfg == nil || b.cfg.BoundGroupID == 0 {
		return false
	}
	if b.cfg.WeiboSuperPostSubscriptions == nil {
		return false
	}
	zeroGroup := b.cfg.WeiboSuperPostSubscriptions[0]
	if len(zeroGroup) == 0 {
		return false
	}
	if b.cfg.WeiboSuperPostSubscriptions[b.cfg.BoundGroupID] == nil {
		b.cfg.WeiboSuperPostSubscriptions[b.cfg.BoundGroupID] = make(map[string]*config.WeiboSuperPostConfig)
	}
	for key, item := range zeroGroup {
		if item == nil {
			continue
		}
		if _, exists := b.cfg.WeiboSuperPostSubscriptions[b.cfg.BoundGroupID][key]; !exists {
			b.cfg.WeiboSuperPostSubscriptions[b.cfg.BoundGroupID][key] = item
		}
	}
	delete(b.cfg.WeiboSuperPostSubscriptions, 0)
	return true
}

func extractWeiboCardAuthorUIDForBot(card *monitor.WeiboCard) string {
	if card == nil {
		return ""
	}
	for _, v := range []string{strings.TrimSpace(card.User.IDStr), strings.TrimSpace(card.User.UID)} {
		if v != "" {
			return v
		}
	}
	switch v := card.User.ID.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%.0f", v)
	default:
		return ""
	}
}

func (b *Bot) handleWeiboSuperPostCommand(event *napcat.Event, args []string) string {
	if len(args) < 3 {
		return "用法: weibo superpost <bind|unbind|list|test> ..."
	}
	action := strings.ToLower(strings.TrimSpace(args[2]))
	gid := b.resolveTargetGroupID(event)
	if gid == 0 {
		return "未找到目标群，请先绑定群或在群里执行该命令"
	}
	if b.cfg.WeiboSuperPostSubscriptions == nil {
		b.cfg.WeiboSuperPostSubscriptions = make(map[int64]map[string]*config.WeiboSuperPostConfig)
	}
	if _, ok := b.cfg.WeiboSuperPostSubscriptions[gid]; !ok {
		b.cfg.WeiboSuperPostSubscriptions[gid] = make(map[string]*config.WeiboSuperPostConfig)
	}
	groupSubs := b.cfg.WeiboSuperPostSubscriptions[gid]
	switch action {
	case "list":
		if len(groupSubs) == 0 {
			return "该群暂无超话发帖监控"
		}
		lines := []string{"超话发帖监控:"}
		for key, item := range groupSubs {
			name := strings.TrimSpace(item.Name)
			if name == "" {
				name = key
			}
			lines = append(lines, fmt.Sprintf("- %s | uid=%s | oid=%s | last=%s", name, item.UID, item.OID, firstNonEmpty(item.LastPostID, "-")))
		}
		return strings.Join(lines, "\n")
	case "bind":
		if len(args) < 5 {
			return "格式错误: weibo superpost bind <uid> <oid> [名称]"
		}
		uid := strings.TrimSpace(args[3])
		oid := normalizeWeiboSuperOID(strings.TrimSpace(args[4]))
		name := ""
		if len(args) >= 6 {
			name = strings.TrimSpace(strings.Join(args[5:], " "))
		}
		key := uid + "|" + oid
		lastPostID := ""
		if groupSubs[key] != nil {
			lastPostID = groupSubs[key].LastPostID
		}
		groupSubs[key] = &config.WeiboSuperPostConfig{UID: uid, OID: oid, Name: name, LastPostID: lastPostID}
		onNew := func(u, o, last string) {
			if b.cfg.WeiboSuperPostSubscriptions[gid] != nil && b.cfg.WeiboSuperPostSubscriptions[gid][key] != nil {
				b.cfg.WeiboSuperPostSubscriptions[gid][key].LastPostID = last
				b.cfg.Save()
			}
		}
		if err := b.weiboMonitor.AddSuperPostConfig(gid, uid, oid, name, false, lastPostID, onNew); err != nil {
			return fmt.Sprintf("添加超话发帖监控失败: %v", err)
		}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		b.weiboMonitor.Start()
		return fmt.Sprintf("[OK] 已添加超话发帖监控: uid=%s oid=%s%s", uid, oid, func() string {
			if name != "" {
				return " 名称=" + name
			}
			return ""
		}())
	case "unbind":
		if len(args) < 5 {
			return "格式错误: weibo superpost unbind <uid> <oid>"
		}
		uid := strings.TrimSpace(args[3])
		oid := normalizeWeiboSuperOID(strings.TrimSpace(args[4]))
		key := uid + "|" + oid
		if _, ok := groupSubs[key]; !ok {
			return fmt.Sprintf("未找到超话发帖监控: %s", key)
		}
		delete(groupSubs, key)
		if len(groupSubs) == 0 {
			delete(b.cfg.WeiboSuperPostSubscriptions, gid)
		}
		b.weiboMonitor.RemoveSuperPostConfig(gid, key)
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 已删除超话发帖监控: %s", key)
	case "test":
		if len(args) < 5 {
			return "格式错误: weibo superpost test <uid> <oid>"
		}
		uid := strings.TrimSpace(args[3])
		oid := normalizeWeiboSuperOID(strings.TrimSpace(args[4]))
		postID, card, err := b.weiboMonitor.FetchLatestSuperPostForTest(oid, uid)
		if err != nil {
			return fmt.Sprintf("测试失败: %v", err)
		}
		if strings.TrimSpace(postID) == "" {
			return fmt.Sprintf("未发现 uid=%s 在 oid=%s 超话里的帖子", uid, oid)
		}
		authorUID := ""
		authorName := ""
		if card != nil {
			authorUID = extractWeiboCardAuthorUIDForBot(card)
			authorName = strings.TrimSpace(card.User.ScreenName)
		}
		if authorUID == "" {
			authorUID = uid
		}
		if authorName != "" {
			return fmt.Sprintf("[OK] 测试命中超话帖子: uid=%s oid=%s post=%s 作者=%s(%s)", uid, oid, postID, authorName, authorUID)
		}
		return fmt.Sprintf("[OK] 测试命中超话帖子: uid=%s oid=%s post=%s 作者uid=%s", uid, oid, postID, authorUID)
	}
	return "用法: weibo superpost <bind|unbind|list|test> ..."
}

func (b *Bot) handleWeiboSuperCommand(event *napcat.Event, args []string) string {
	if len(args) < 3 {
		return "用法: weibo super <list|add|del|sign|auto|count> ..."
	}
	action := strings.ToLower(strings.TrimSpace(args[2]))
	if action == "count" {
		return b.handleWeiboSuperCountCommand(args)
	}
	topics := b.getGlobalWeiboSuperTopics()

	switch action {
	case "list":
		if len(topics) == 0 {
			return "暂无超话配置"
		}
		lines := make([]string, 0, len(topics)+1)
		lines = append(lines, "超话配置:")
		for oid, t := range topics {
			name := strings.TrimSpace(t.Name)
			if name == "" {
				name = oid
			}
			status := strings.TrimSpace(t.LastSignStatus)
			if status == "" {
				status = "-"
			}
			date := strings.TrimSpace(t.LastSignDate)
			if date == "" {
				date = "-"
			}
			lines = append(lines, fmt.Sprintf("- %s (oid=%s) last=%s %s", name, oid, date, status))
		}
		return strings.Join(lines, "\n")

	case "add":
		if len(args) < 4 {
			return "格式错误: weibo super add <oid> [名称]"
		}
		oid := strings.TrimSpace(args[3])
		if oid == "" {
			return "格式错误: oid 不能为空"
		}
		name := ""
		if len(args) >= 5 {
			name = strings.TrimSpace(strings.Join(args[4:], " "))
		}
		topics[oid] = &config.WeiboSuperTopic{OID: oid, Name: name}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		if name == "" {
			return fmt.Sprintf("[OK] 已添加超话 oid=%s", oid)
		}
		return fmt.Sprintf("[OK] 已添加超话 %s (oid=%s)", name, oid)

	case "del":
		if len(args) < 4 {
			return "格式错误: weibo super del <oid|名称>"
		}
		key := strings.TrimSpace(strings.Join(args[3:], " "))
		oid, topic := b.findWeiboSuperTopic(key)
		if topic == nil {
			return fmt.Sprintf("未找到超话: %s", key)
		}
		name := strings.TrimSpace(topic.Name)
		if name == "" {
			name = oid
		}
		delete(topics, oid)
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 已删除超话: %s", name)

	case "sign":
		if len(args) == 3 || strings.EqualFold(strings.TrimSpace(args[3]), "all") {
			if len(topics) == 0 {
				return "暂无超话配置"
			}
			lines := make([]string, 0, len(topics))
			for oid, topic := range topics {
				res, err := b.weiboMonitor.SignWeiboSuperTopic(oid)
				name := strings.TrimSpace(topic.Name)
				if name == "" {
					name = oid
				}
				if err != nil {
					topic.LastSignStatus = "失败: " + err.Error()
					lines = append(lines, fmt.Sprintf("[%s] 失败: %v", name, err))
					continue
				}
				res.Name = name
				topic.LastSignDate = time.Now().Format("2006-01-02")
				topic.LastSignStatus = fmt.Sprintf("code=%d %s", res.Code, strings.TrimSpace(res.Message))
				if res.Success {
					if res.AlreadyDone {
						lines = append(lines, fmt.Sprintf("[%s] 今日已签到", name))
					} else {
						lines = append(lines, fmt.Sprintf("[%s] 签到成功", name))
					}
				} else {
					lines = append(lines, fmt.Sprintf("[%s] 签到失败(code=%d): %s", name, res.Code, res.Message))
				}
			}
			_ = b.cfg.Save()
			return strings.Join(lines, "\n")
		}

		key := strings.TrimSpace(strings.Join(args[3:], " "))
		oid, topic := b.findWeiboSuperTopic(key)
		if topic == nil {
			return fmt.Sprintf("未找到超话: %s", key)
		}
		res, err := b.weiboMonitor.SignWeiboSuperTopic(oid)
		if err != nil {
			topic.LastSignStatus = "失败: " + err.Error()
			_ = b.cfg.Save()
			return fmt.Sprintf("签到失败: %v", err)
		}
		name := strings.TrimSpace(topic.Name)
		if name == "" {
			name = oid
		}
		topic.LastSignDate = time.Now().Format("2006-01-02")
		topic.LastSignStatus = fmt.Sprintf("code=%d %s", res.Code, strings.TrimSpace(res.Message))
		_ = b.cfg.Save()
		if res.Success {
			if res.AlreadyDone {
				return fmt.Sprintf("[%s] 今日已签到", name)
			}
			return fmt.Sprintf("[%s] 签到成功", name)
		}
		return fmt.Sprintf("[%s] 签到失败(code=%d): %s", name, res.Code, res.Message)

	case "auto":
		if len(args) < 4 {
			state := "off"
			if b.cfg.WeiboSuperAutoEnabled {
				state = "on"
			}
			return fmt.Sprintf("当前超话自动签到: %s", state)
		}
		toggle := strings.ToLower(strings.TrimSpace(args[3]))
		if toggle != "on" && toggle != "off" {
			return "格式错误: weibo super auto <on/off>"
		}
		b.cfg.WeiboSuperAutoEnabled = toggle == "on"
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 超话自动签到已%s", map[bool]string{true: "开启", false: "关闭"}[b.cfg.WeiboSuperAutoEnabled])
	}

	return "用法: weibo super <list|add|del|sign|auto|count> ..."
}
