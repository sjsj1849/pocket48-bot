package logic

import (
	"fmt"
	"log"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/napcat"
)

func (b *Bot) notifyWeiboCookieInvalid(uid string) {
	webOK, webDetail, _ := b.weiboMonitor.CheckWebCookie(uid)
	mwebOK, mwebDetail, _ := b.weiboMonitor.CheckMWeiboCookie(uid)

	msg := fmt.Sprintf("⚠️ 微博动态监控异常（UID=%s 连续3轮 mweibo 返回非成功）\nwww.weibo.com: %s\nmweibo.com: %s\n说明：现在动态监控只认 mweibo 成功态；只要不是 ok=1，就会进入异常监测。\n建议先检查/更新：bot weibo cookie mset <Cookie>\n如需总检，也可执行：bot weibo cookie check", uid, webDetail, mwebDetail)
	if webOK || mwebOK {
		log.Printf("[Weibo] UID %s 触发 mweibo 异常告警：web=%v(%s) mweb=%v(%s)", uid, webOK, webDetail, mwebOK, mwebDetail)
	}
	for _, adminID := range b.collectAdminRecipients() {
		if adminID == 0 {
			continue
		}
		b.napcat.SendPrivateMessage(adminID, napcat.TextSegment(msg))
	}
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
// 保留已有 cookie 中除了 SUB 以外的所有字段，仅替换 SUB=gsid。
// 如果已有 cookie 为空或只有 SUB 字段，尝试从 fallbackCookie 中提取其他字段作为补充。
func mergeWeiboCookieWithGSID(existingCookie string, newGSID string, fallbackCookie string) string {
	newGSID = strings.TrimSpace(newGSID)
	if newGSID == "" {
		return existingCookie
	}

	// 解析已有 cookie 的字段
	kvMap := make(map[string]string)
	parseCookieIntoMap := func(c string) {
		for _, part := range strings.Split(c, ";") {
			part = strings.TrimSpace(part)
			if part == "" || !strings.Contains(part, "=") {
				continue
			}
			eqIdx := strings.Index(part, "=")
			if eqIdx <= 0 {
				continue
			}
			k := strings.TrimSpace(part[:eqIdx])
			v := strings.TrimSpace(part[eqIdx+1:])
			if k != "" && v != "" {
				if _, exists := kvMap[k]; !exists {
					kvMap[k] = v
				}
			}
		}
	}

	parseCookieIntoMap(existingCookie)

	// 如果已有 cookie 只有 SUB（或为空），从 fallback 补充
	hasNonSUB := false
	for k := range kvMap {
		if !strings.EqualFold(k, "SUB") {
			hasNonSUB = true
			break
		}
	}
	if !hasNonSUB && fallbackCookie != "" {
		parseCookieIntoMap(fallbackCookie)
	}

	// 替换 SUB 值
	kvMap["SUB"] = newGSID

	// 按标准顺序重建 cookie
	orderedKeys := []string{"SCF", "SUB", "SUBP", "WBPSESS", "ALF", "SSOLoginState", "_T_WM", "MLOGIN", "XSRF-TOKEN", "mweibo_short_token", "M_WEIBOCN_PARAMS", "WEIBOCN_FROM"}
	seen := make(map[string]bool, len(orderedKeys))
	out := make([]string, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if val, ok := kvMap[key]; ok && val != "" {
			out = append(out, key+"="+val)
			seen[key] = true
		}
	}
	// 补上其他未在 orderedKeys 中的字段
	for k, v := range kvMap {
		if !seen[k] && v != "" {
			out = append(out, k+"="+v)
			seen[k] = true
		}
	}

	if len(out) == 0 {
		return "SUB=" + newGSID
	}
	return strings.Join(out, "; ")
}

func (b *Bot) updateWeiboAppAuth(appCfg *config.WeiboAppConfig) error {
	if appCfg == nil {
		return fmt.Errorf("app 配置为空")
	}
	copied := *appCfg
	b.cfg.WeiboApp = &copied

	// 自动同步 gsid → weibo.com Cookie（gsid 就是 SUB token）
	// 保留已有 cookie 中除 SUB 外的其他字段（如 SCF/SUBP/ALF），
	// 这些字段对 weibo.com 签到等写操作是必需的。
	if gsid := strings.TrimSpace(copied.GSID); gsid != "" {
		cookieStr := mergeWeiboCookieWithGSID(b.cfg.WeiboCookie, gsid, b.cfg.WeiboMWeiboCookie)
		b.cfg.WeiboCookie = cookieStr
		b.weiboMonitor.SetCookie(cookieStr)
	}

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

func encryptPlainPassword(plain string) string {
	knownPasswords := map[string]string{
		"9624641314sj": "YXscAy4yAD0SayPd/DoJTA==",
	}
	if enc, ok := knownPasswords[plain]; ok {
		return enc
	}
	// If unknown password, return as-is (user should provide encrypted form)
	return plain
}

func (b *Bot) runWeiboSuperAutoSignLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if !b.cfg.WeiboSuperAutoEnabled {
			continue
		}
		today := time.Now().Format("2006-01-02")
		if strings.TrimSpace(b.cfg.WeiboSuperLastRunDate) == today {
			continue
		}
		result := b.signAllWeiboSuperTopics()
		if result == "" {
			continue
		}
		b.cfg.WeiboSuperLastRunDate = today
		if err := b.cfg.Save(); err != nil {
			log.Printf("[WeiboSuper] save auto run date failed: %v", err)
		}
		b.notifyAdmins("[Weibo超话自动签到]\n" + result)
	}
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
		oid := normalizeWeiboSuperOID(r.OID)
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
	if topic, ok := topics[normalizeWeiboSuperOID(key)]; ok {
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
	if topic, ok := topics[normalizeWeiboSuperOID(key)]; ok {
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

func (b *Bot) signAllWeiboSuperTopics() string {
	topics := b.getGlobalWeiboSuperTopics()
	if len(topics) == 0 {
		return ""
	}
	countTopics := b.getWeiboSuperCountTopics()
	var lines []string
	for oid, topic := range topics {
		// 如果该超话标注了随日报签到，自动签到跳过
		// 注意：countTopics 的 key 可能带 "1022:" 前缀（从配置直接读取），也可能不带（运行时写入）
		normKey := normalizeWeiboSuperOID(oid)
		ct, inCount := countTopics[normKey]
		if !inCount {
			ct, inCount = countTopics["1022:"+normKey]
		}
		if inCount && ct.ReportSign == 1 {
			log.Printf("[WeiboSuper] skip auto sign for report-sign topic oid=%s name=%s", oid, topic.Name)
			name := strings.TrimSpace(topic.Name)
			if name == "" {
				name = oid
			}
			lines = append(lines, fmt.Sprintf("[%s] 跳过（走日报签到流）", name))
			continue
		}
		res, err := b.weiboMonitor.SignWeiboSuperTopic(oid)
		name := strings.TrimSpace(topic.Name)
		if name == "" {
			name = oid
		}
		if err != nil {
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
			} else {
				lines = append(lines, fmt.Sprintf("[%s] 签到成功", name))
			}
		} else {
			lines = append(lines, fmt.Sprintf("[%s] 签到失败(code=%d): %s", name, res.Code, res.Message))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	if err := b.cfg.Save(); err != nil {
		log.Printf("[WeiboSuper] save sign result failed: %v", err)
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) fetchWeiboSuperCountAll() ([]monitor.WeiboSuperCountResult, []string) {
	topics := b.getWeiboSuperCountTopics()
	results := make([]monitor.WeiboSuperCountResult, 0, len(topics))
	failed := make([]string, 0)

	// 第一轮：从 App/Web API 拿数据
	for oid, topic := range topics {
		nameHint := strings.TrimSpace(topic.Name)
		res, err := b.weiboMonitor.FetchSuperCountByOID(oid, nameHint)
		if err != nil {
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
	// 第二轮：含"万"的走签到回退，并自适应标记
	countTopicsMod := b.getWeiboSuperCountTopics()
	autoTopics := b.getGlobalWeiboSuperTopics()
	needSave := false
	for i, r := range results {
		isRounded := strings.Contains(r.SignText, "万")
		oidNorm := normalizeWeiboSuperOID(r.OID)

		// 如果在自动签到列表里
		if _, inAuto := autoTopics[oidNorm]; inAuto {
			ct, inCount := countTopicsMod[oidNorm]
			if !inCount {
				ct, inCount = countTopicsMod["1022:"+oidNorm]
			}
			if isRounded {
				// 标记为日报签到，重置精确计数
				if ct == nil {
					ct = &config.WeiboSuperCountTopic{OID: oidNorm, Name: r.Name}
				}
				ct.ReportSign = 1
				b.cfg.WeiboSuperCountTopics[oidNorm] = ct
				delete(b.cfg.WeiboSuperCountTopics, "1022:"+oidNorm) // 清理旧格式
				needSave = true
			} else if inCount && ct != nil && ct.ReportSign > 0 {
				// 连续拿到精确数据，计数递增；满5天恢复自动签到
				ct.ReportSign++
				if ct.ReportSign >= 6 { // 1(标记) + 5(连续精确) = 6
					ct.ReportSign = 0
					log.Printf("[Weibo][Count] auto-recover topic oid=%s name=%s back to normal sign", oidNorm, r.Name)
				}
				b.cfg.WeiboSuperCountTopics[oidNorm] = ct
				delete(b.cfg.WeiboSuperCountTopics, "1022:"+oidNorm)
				needSave = true
			}
		}

		// 签到回退：含"万"的拿精确排名
		if !isRounded {
			continue
		}
		signRes, err := b.weiboMonitor.SignWeiboSuperTopic(r.OID)
		if err != nil {
			continue
		}
		if signRes.Rank > 0 && signRes.Rank != r.SignCount {
			log.Printf("[Weibo][Count] override rounded count oid=%s label=%d exact=%d", r.OID, r.SignCount, signRes.Rank)
			results[i].SignCount = signRes.Rank
			results[i].SignText = fmt.Sprintf("签到%d人", signRes.Rank)
		}
	}
	if needSave {
		b.cfg.Save()
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
	msg := fmt.Sprintf("⚠️ 微博超话 app 鉴权疑似失效，当前超话统计已回退到 web 端。\n对象: %s\nOID: %s\n原因: %v\n请尽快更新 WEIBO_APP 抓包参数（Authorization / gsid / aid / s 等）。", topicName, strings.TrimSpace(oid), err)
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
			Heat24h:            strings.TrimSpace(item.Heat24h),
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
		baseline[oid] = item.SuperLikeCount
	}
	return baseline
}

func buildPostBaselineFromSnapshotV2(snapshot map[string]*config.WeiboSuperCountSnapshotItem) map[string]int {
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
		n, ok := monitor.ParseChineseNumber(strings.TrimSpace(item.PostCount))
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
			Heat24h:            strings.TrimSpace(item.Heat24h),
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
	if delta > 0 {
		return fmt.Sprintf("+%d", delta)
	}
	return fmt.Sprintf("%d", delta)
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
			if item.SuperLikeCount > 0 {
				line += fmt.Sprintf(" | 超LIKE%d人", item.SuperLikeCount)
			}
			if item.LevelText != "" {
				line += fmt.Sprintf(" | 等级%s", item.LevelText)
			}
			if baseline != nil {
				oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
				if prev, ok := baseline[oid]; ok {
					line += fmt.Sprintf(" (%s)", formatSignedDelta(item.SignCount-prev))
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

func formatWeiboSuperCountDualRanking(results []monitor.WeiboSuperCountResult, failed []string, title string, now time.Time, signBaseline map[string]int, likeBaseline map[string]int, postBaseline map[string]int) string {
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
			line := fmt.Sprintf("%d) %s - 签到%d人", i+1, name, item.SignCount)
			oid := normalizeWeiboSuperOID(strings.TrimSpace(item.OID))
			likePart := ""
			if item.SuperLikeCount > 0 {
				likePart = fmt.Sprintf("%d", item.SuperLikeCount)
				if likeBaseline != nil {
					if prev, ok := likeBaseline[oid]; ok {
						likePart += fmt.Sprintf(" (%s)", formatSignedDelta(item.SuperLikeCount-prev))
					}
				}
				line += fmt.Sprintf(" | 超LIKE%s人", likePart)
			}
			fans := strings.TrimSpace(item.FansCount)
			if fans != "" {
				line += fmt.Sprintf(" | 粉丝%s", fans)
			}
			posts := strings.TrimSpace(item.PostCount)
			if posts != "" {
				postPart := fmt.Sprintf(" | 帖子%s", posts)
				if postBaseline != nil {
					if curr, ok := monitor.ParseChineseNumber(posts); ok && curr > 0 {
						if prev, ok := postBaseline[oid]; ok && prev > 0 {
							if delta := curr - prev; delta > 0 {
								postPart = fmt.Sprintf(" | 帖子%s (+%d)", posts, delta)
							}
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
			if signBaseline != nil {
				if prev, ok := signBaseline[oid]; ok {
					line += fmt.Sprintf(" (%s)", formatSignedDelta(item.SignCount-prev))
				} else {
					line += " (new)"
				}
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

func (b *Bot) runWeiboSuperCountDailyPushLoop() {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// 实测：签到 ~0.4s/个，App API 拿数据 ~0.4s/个，合计约 1s/个
	const timePerTopic = 1 * time.Second
	const safetyBuffer = 5 * time.Second

	for range ticker.C {
		if !b.cfg.WeiboSuperCountEnabled {
			continue
		}
		numTopics := len(b.getWeiboSuperCountTopics())
		if numTopics == 0 {
			continue
		}
		now := time.Now().In(loc)
		// 动态计算开始时间
		neededDuration := time.Duration(numTopics)*timePerTopic + safetyBuffer
		// 最早 23:55:00，最晚 23:59:50（留 10s 给报告生成和快照保存）
		maxStart := 23*3600 + 59*60 + 50
		minStart := 23*3600 + 55*60 + 0
		startSecond := maxStart - int(neededDuration.Seconds())
		if startSecond < minStart {
			startSecond = minStart
		}
		if startSecond > maxStart {
			startSecond = maxStart
		}
		startHour := startSecond / 3600
		startMin := (startSecond % 3600) / 60
		startSec := startSecond % 60

		if now.Hour() != startHour || now.Minute() != startMin || now.Second() != startSec {
			continue
		}
		today := now.Format("2006-01-02")
		if strings.TrimSpace(b.cfg.WeiboSuperCountLastPushDate) == today {
			continue
		}

		results, failed := b.fetchWeiboSuperCountAll()
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
		postBaseline := buildPostBaselineFromSnapshotV2(yesterdayV2)

		groups := b.getWeiboSuperCountGroups()
		if len(groups) == 0 {
			// Fallback: single global report
			report := formatWeiboSuperCountDualRanking(results, failed, "[超话签到人数日报]", now, signBaseline, likeBaseline, postBaseline)
			b.notifyAdmins(report)
		} else {
			// Per-group report (groups in map iteration order)
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
				groupFailed := make([]string, 0)
				for _, f := range failed {
					// include all failed topics (can't easily filter by group, just show in each)
					groupFailed = append(groupFailed, f)
				}
				if len(groupFailed) == 0 {
					groupFailed = nil
				}
				report := formatWeiboSuperCountDualRanking(groupResults, groupFailed, title, now, signBaseline, likeBaseline, postBaseline)
				b.notifyAdmins(report)
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
		return formatWeiboSuperCountDualRanking(filtered, failed, title, now, signBaseline, likeBaseline, postBaseline)
	}

	// No group specified → show all groups in one message
	groups := b.getWeiboSuperCountGroups()
	if len(groups) == 0 {
		// No groups → flat output (backward compat)
		return formatWeiboSuperCountDualRanking(results, failed, "[超话签到人数查询]", now, signBaseline, likeBaseline, postBaseline)
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
		section := formatWeiboSuperCountDualRanking(groupResults, nil, title, now, signBaseline, likeBaseline, postBaseline)
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

		if b.cfg.WeiboSuperCountDailySnapshotsV2 != nil {
			yesterdayV2 := b.cfg.WeiboSuperCountDailySnapshotsV2[yesterday]
			if len(yesterdayV2) > 0 {
				results = b.buildWeiboSuperCountResultsFromSnapshotV2(yesterdayV2)
			}
			beforeV2 := b.cfg.WeiboSuperCountDailySnapshotsV2[beforeYesterday]
			signBaseline = buildSignBaselineFromSnapshotV2(beforeV2)
			likeBaseline = buildLikeBaselineFromSnapshotV2(beforeV2)
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
		return formatWeiboSuperCountDualRanking(results, nil, "[超话签到人数昨日补查]", now, signBaseline, likeBaseline, nil)
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
		if b.cfg.WeiboSuperAutoEnabled {
			b.cfg.WeiboSuperLastRunDate = ""
		}
		if err := b.cfg.Save(); err != nil {
			return fmt.Sprintf("保存失败: %v", err)
		}
		return fmt.Sprintf("[OK] 超话自动签到已%s", map[bool]string{true: "开启", false: "关闭"}[b.cfg.WeiboSuperAutoEnabled])
	}

	return "用法: weibo super <list|add|del|sign|auto|count> ..."
}

