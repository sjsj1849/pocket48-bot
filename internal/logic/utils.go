package logic

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
)

func parseNumberishToInt64(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int:
		if n <= 0 {
			return 0, false
		}
		return int64(n), true
	case int64:
		if n <= 0 {
			return 0, false
		}
		return n, true
	case int32:
		if n <= 0 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if n <= 0 {
			return 0, false
		}
		return int64(n + 0.5), true
	case float32:
		if n <= 0 {
			return 0, false
		}
		return int64(n + 0.5), true
	case json.Number:
		if i, err := n.Int64(); err == nil && i > 0 {
			return i, true
		}
		if f, err := n.Float64(); err == nil && f > 0 {
			return int64(f + 0.5), true
		}
	case string:
		t := strings.TrimSpace(n)
		if t == "" {
			return 0, false
		}
		if i, err := strconv.ParseInt(t, 10, 64); err == nil && i > 0 {
			return i, true
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil && f > 0 {
			return int64(f + 0.5), true
		}
	}
	return 0, false
}

func parseNumberishToFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		if n <= 0 {
			return 0, false
		}
		return float64(n), true
	case int64:
		if n <= 0 {
			return 0, false
		}
		return float64(n), true
	case int32:
		if n <= 0 {
			return 0, false
		}
		return float64(n), true
	case float64:
		if n <= 0 {
			return 0, false
		}
		return n, true
	case float32:
		if n <= 0 {
			return 0, false
		}
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil && f > 0 {
			return f, true
		}
	case string:
		t := strings.TrimSpace(n)
		if t == "" {
			return 0, false
		}
		if f, err := strconv.ParseFloat(t, 64); err == nil && f > 0 {
			return f, true
		}
	}
	return 0, false
}

func findDirectValueByKeys(payload map[string]interface{}, keys []string) (interface{}, bool) {
	if payload == nil || len(keys) == 0 {
		return nil, false
	}
	want := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key != "" {
			want[key] = struct{}{}
		}
	}
	for key, value := range payload {
		if _, ok := want[strings.ToLower(strings.TrimSpace(key))]; ok {
			return value, true
		}
	}
	return nil, false
}

func directStringByKeys(payload map[string]interface{}, keys []string) string {
	value, ok := findDirectValueByKeys(payload, keys)
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

func directInt64ByKeys(payload map[string]interface{}, keys []string) int64 {
	value, ok := findDirectValueByKeys(payload, keys)
	if !ok {
		return 0
	}
	n, _ := parseNumberishToInt64(value)
	return n
}

func directFloat64ByKeys(payload map[string]interface{}, keys []string) float64 {
	value, ok := findDirectValueByKeys(payload, keys)
	if !ok {
		return 0
	}
	n, _ := parseNumberishToFloat64(value)
	return n
}

func directBoolByKeys(payload map[string]interface{}, keys []string) bool {
	value, ok := findDirectValueByKeys(payload, keys)
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		t := strings.ToLower(strings.TrimSpace(v))
		return t == "1" || t == "true" || t == "yes" || t == "on"
	default:
		n, ok := parseNumberishToFloat64(value)
		return ok && n > 0
	}
}

func parseAnnualScoreGiftMessage(body string) (*AnnualScoreGift, bool) {
	body = strings.TrimSpace(body)
	if body == "" || !(strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[")) {
		return nil, false
	}

	var payload interface{}
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, false
	}

	giftInfo := findObjectByKey(payload, "giftInfo")
	if giftInfo == nil {
		if root, ok := payload.(map[string]interface{}); ok {
			giftInfo = root
		}
	}
	if giftInfo == nil || !directBoolByKeys(giftInfo, []string{"isScore", "is_score"}) {
		return nil, false
	}

	giftName := directStringByKeys(giftInfo, []string{"giftName", "gift_name", "presentName", "itemName", "giftTitle", "presentTitle"})
	if giftName == "" {
		giftName = "礼物"
	}
	giftNum := directInt64ByKeys(giftInfo, []string{"giftNum", "giftCount", "count", "num", "quantity"})
	giftNum = normalizeGiftNum(giftNum)

	unitScore := directFloat64ByKeys(giftInfo, []string{"tpNum", "tp_num", "score", "scoreNum", "points", "point"})
	if unitScore <= 0 {
		return nil, false
	}

	return &AnnualScoreGift{
		GiftID:       directInt64ByKeys(giftInfo, []string{"giftId", "gift_id", "presentId", "itemId"}),
		GiftName:     giftName,
		GiftNum:      giftNum,
		ReceiverName: directStringByKeys(giftInfo, []string{"userName", "receiverName", "toName", "memberName"}),
		UnitScore:    unitScore,
		TotalScore:   unitScore * float64(giftNum),
	}, true
}

func formatScoreValue(score float64) string {
	if score <= 0 {
		return "0"
	}
	return strconv.FormatFloat(score, 'f', -1, 64)
}

func findObjectByKey(payload interface{}, key string) map[string]interface{} {
	want := strings.ToLower(strings.TrimSpace(key))
	var walk func(interface{}) map[string]interface{}
	walk = func(v interface{}) map[string]interface{} {
		switch node := v.(type) {
		case map[string]interface{}:
			for k, child := range node {
				if strings.ToLower(strings.TrimSpace(k)) == want {
					if m, ok := child.(map[string]interface{}); ok {
						return m
					}
					if s, ok := child.(string); ok {
						s = strings.TrimSpace(s)
						if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
							var nested map[string]interface{}
							if err := json.Unmarshal([]byte(s), &nested); err == nil {
								return nested
							}
						}
					}
				}
			}
			for _, child := range node {
				if m := walk(child); m != nil {
					return m
				}
			}
		case []interface{}:
			for _, child := range node {
				if m := walk(child); m != nil {
					return m
				}
			}
		}
		return nil
	}
	return walk(payload)
}

func findNumberByKeys(payload interface{}, keys []string) (int64, bool) {
	if payload == nil || len(keys) == 0 {
		return 0, false
	}
	order := make(map[string]int, len(keys))
	for idx, key := range keys {
		order[strings.ToLower(strings.TrimSpace(key))] = idx
	}

	bestIdx := len(keys) + 1
	bestVal := int64(0)
	found := false

	var walk func(interface{})
	walk = func(v interface{}) {
		switch node := v.(type) {
		case map[string]interface{}:
			for k, child := range node {
				lk := strings.ToLower(strings.TrimSpace(k))
				if idx, ok := order[lk]; ok {
					if n, ok := parseNumberishToInt64(child); ok {
						if !found || idx < bestIdx {
							bestIdx = idx
							bestVal = n
							found = true
						}
					}
				}
				walk(child)
			}
		case []interface{}:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(payload)
	return bestVal, found
}

func extractChickenLegFromRaw(raw json.RawMessage, giftName string, giftNum int64) (int64, int64, string) {
	num := normalizeGiftNum(giftNum)
	if len(raw) == 0 {
		return 0, 0, "unknown"
	}

	var payload interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, 0, "unknown"
	}

	giftInfo := findObjectByKey(payload, "giftInfo")
	scopes := []interface{}{giftInfo, payload}
	for idx, scope := range scopes {
		if scope == nil {
			continue
		}

		total, totalOK := findNumberByKeys(scope, []string{
			"totalChickenLeg",
			"totalChickenLegs",
			"totalGiftValue",
			"totalValue",
			"totalPrice",
			"totalDiamond",
			"totalCoin",
			"totalAmount",
		})
		if totalOK && total > 0 {
			source := "raw.payload.total"
			if idx == 0 {
				source = "raw.giftInfo.total"
			}
			if num > 0 && total%num == 0 {
				return total / num, total, source
			}
			return 0, total, source
		}

		unit, unitOK := findNumberByKeys(scope, []string{
			"chickenLeg",
			"chickenLegs",
			"giftValue",
			"giftPrice",
			"diamond",
			"coin",
			"price",
			"value",
			"money",
			"amount",
		})
		if unitOK && unit > 0 {
			total := unit * num
			source := "raw.payload.unit"
			if idx == 0 {
				source = "raw.giftInfo.unit"
			}
			return unit, total, source
		}

	}

	_ = giftName
	return 0, 0, "unknown"
}

func normalizeWatchTimestampSec(ts int64) int64 {
	if ts <= 0 {
		return time.Now().Unix()
	}
	if ts >= 1_000_000_000_000 {
		return ts / 1000
	}
	return ts
}

func formatWatchDuration(sec int64) string {
	if sec <= 0 {
		return "未知"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	if h > 0 {
		return fmt.Sprintf("%d小时%d分%d秒", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%d分%d秒", m, s)
	}
	return fmt.Sprintf("%d秒", s)
}

func normalizeMessageTimestampMs(ts int64) int64 {
	if ts <= 0 {
		return time.Now().UnixMilli()
	}
	if ts < 1_000_000_000_000 {
		return ts * 1000
	}
	return ts
}

func parseCommandArgs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	lower := strings.ToLower(raw)
	for _, prefix := range []string{"weibo cookie import ", "weibo cookie set ", "weibo cookie reset "} {
		if strings.HasPrefix(lower, prefix) {
			parts := strings.Fields(strings.TrimSpace(raw[:len(prefix)]))
			tail := strings.TrimSpace(raw[len(prefix):])
			if tail != "" {
				parts = append(parts, tail)
			}
			return parts
		}
	}
	return strings.Fields(raw)
}

func maskMobile(mobile string) string {
	if len(mobile) < 7 {
		return mobile
	}
	return mobile[:3] + "****" + mobile[len(mobile)-4:]
}

func isValidPocketMobile(mobile string) bool {
	return pocketMobilePattern.MatchString(mobile)
}

func normalizePocketText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	clean := make([]rune, 0, len(text))
	for _, r := range text {
		if r < 32 && r != '\n' && r != '\t' {
			continue
		}
		clean = append(clean, r)
	}

	result := strings.TrimSpace(string(clean))
	if result == "" {
		return ""
	}

	return result
}

func isEmojiOnlyText(text string) bool {
	hasEmoji := false
	for _, r := range text {
		if unicode.IsSpace(r) {
			continue
		}
		if isLikelyEmojiRune(r) {
			hasEmoji = true
			continue
		}
		return false
	}
	return hasEmoji
}

func isLikelyEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F1E6 && r <= 0x1F1FF: // flags
		return true
	case r >= 0x1F300 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin tones
		return true
	case r == 0x200D || r == 0x20E3:
		return true
	default:
		return false
	}
}

func appendTextWithQQFaces(segments []interface{}, text string) []interface{} {
	if text == "" {
		return segments
	}

	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}

		close := strings.IndexByte(text[i+1:], ']')
		if close < 0 {
			continue
		}
		close += i + 1

		name := text[i+1 : close]
		_, ok := qqFaceNameToID[name]
		if !ok {
			continue
		}

		if start < i {
			segments = append(segments, napcat.TextSegment(text[start:i]))
		}
		segments = append(segments, napcat.FaceSegment(qqFaceNameToID[name]))
		start = close + 1
		i = close
	}

	if start < len(text) {
		segments = append(segments, napcat.TextSegment(text[start:]))
	}

	return segments
}

func parseLivePushBody(body string) (string, string, string, int64) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", "", 0
	}

	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", "", "", 0
	}

	title := findStringField(payload, []string{"liveTitle", "title", "pushTitle"})
	cover := findStringField(payload, []string{"liveCover", "cover", "coverUrl", "image", "pic"})
	liveID := findStringField(payload, []string{"liveId", "liveID", "id", "liveRoomId"})
	roomID := findInt64Field(payload, []string{"roomId", "liveRoomId", "chatroomId", "nimRoomId"})

	return title, cover, liveID, roomID
}

func findStringField(payload interface{}, keys []string) string {
	switch v := payload.(type) {
	case map[string]interface{}:
		for _, key := range keys {
			rawVal, ok := v[key]
			if !ok {
				continue
			}
			if str, ok := rawVal.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					return str
				}
			}
		}
		for _, nested := range v {
			if str, ok := nested.(string); ok {
				str = strings.TrimSpace(str)
				if (strings.HasPrefix(str, "{") && strings.HasSuffix(str, "}")) || (strings.HasPrefix(str, "[") && strings.HasSuffix(str, "]")) {
					var parsed interface{}
					if err := json.Unmarshal([]byte(str), &parsed); err == nil {
						if found := findStringField(parsed, keys); found != "" {
							return found
						}
					}
				}
			}
			if found := findStringField(nested, keys); found != "" {
				return found
			}
		}
	case []interface{}:
		for _, item := range v {
			if str, ok := item.(string); ok {
				str = strings.TrimSpace(str)
				if (strings.HasPrefix(str, "{") && strings.HasSuffix(str, "}")) || (strings.HasPrefix(str, "[") && strings.HasSuffix(str, "]")) {
					var parsed interface{}
					if err := json.Unmarshal([]byte(str), &parsed); err == nil {
						if found := findStringField(parsed, keys); found != "" {
							return found
						}
					}
				}
			}
			if found := findStringField(item, keys); found != "" {
				return found
			}
		}
	}

	return ""
}

func findInt64Field(payload interface{}, keys []string) int64 {
	switch v := payload.(type) {
	case map[string]interface{}:
		for _, key := range keys {
			rawVal, ok := v[key]
			if !ok {
				continue
			}
			switch n := rawVal.(type) {
			case float64:
				return int64(n)
			case int64:
				return n
			case int:
				return int64(n)
			case string:
				if cleaned := strings.TrimSpace(n); cleaned != "" {
					if parsed, err := strconv.ParseInt(cleaned, 10, 64); err == nil {
						return parsed
					}
				}
			}
		}
	}
	return 0
}

func normalizeMediaURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if strings.HasPrefix(raw, "/") {
		return "https://source.48.cn" + raw
	}

	return "https://source.48.cn/" + strings.TrimPrefix(raw, "/")
}

func parseReplyMessage(body string, ref *pocket48.Reply) (string, string) {
	var replyObj struct {
		ReplyName string `json:"replyName"`
		ReplyText string `json:"replyText"`
		Text      string `json:"text"`
		Content   string `json:"content"`
		Msg       string `json:"msg"`
		Message   string `json:"message"`
		ReplyInfo struct {
			ReplyName string `json:"replyName"`
			ReplyText string `json:"replyText"`
			Text      string `json:"text"`
		} `json:"replyInfo"`
	}

	bodyText := strings.TrimSpace(body)
	if strings.HasPrefix(bodyText, "{") {
		_ = json.Unmarshal([]byte(body), &replyObj)
	}

	replyName := strings.TrimSpace(replyObj.ReplyName)
	replyText := strings.TrimSpace(replyObj.ReplyText)
	answerText := strings.TrimSpace(replyObj.Text)
	if replyName == "" {
		replyName = strings.TrimSpace(replyObj.ReplyInfo.ReplyName)
	}
	if replyText == "" {
		replyText = strings.TrimSpace(replyObj.ReplyInfo.ReplyText)
	}
	if answerText == "" {
		answerText = strings.TrimSpace(replyObj.ReplyInfo.Text)
	}
	if answerText == "" {
		answerText = strings.TrimSpace(replyObj.Content)
	}
	if answerText == "" {
		answerText = strings.TrimSpace(replyObj.Msg)
	}
	if answerText == "" {
		answerText = strings.TrimSpace(replyObj.Message)
	}

	if ref != nil {
		if replyName == "" {
			replyName = strings.TrimSpace(ref.ReplyName)
		}
		if replyText == "" {
			replyText = strings.TrimSpace(ref.ReplyText)
		}
		if answerText == "" {
			answerText = strings.TrimSpace(ref.Text)
		}
	}

	if answerText == "" {
		if !strings.HasPrefix(bodyText, "{") {
			answerText = bodyText
		}
	}
	if answerText == "" && strings.HasPrefix(bodyText, "{") {
		if fallback := extractLikelyReplyAnswer(bodyText); fallback != "" {
			answerText = fallback
		}
	}

	answerText = normalizePocketText(answerText)

	quotedText := ""
	if replyName != "" && replyText != "" {
		quotedText = normalizePocketText(fmt.Sprintf("%s:%s", replyName, replyText))
	}

	return quotedText, answerText
}

func extractGiftSummaryText(body string, ref *pocket48.Reply, idolName string, idolNick string) string {
	body = strings.TrimSpace(body)
	if body == "" || !strings.HasPrefix(body, "{") {
		return ""
	}

	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}

	var values []string
	collectStringValues(payload, &values)
	for _, v := range values {
		t := normalizePocketText(v)
		if strings.Contains(t, "送给") {
			if strings.HasPrefix(t, "送给") {
				sender := extractLikelyGiftSender(payload, ref, idolName, idolNick)
				if sender != "" {
					t = normalizePocketText(sender + " " + t)
				}
			}
			return t
		}
	}

	return ""
}

func extractLikelyGiftSender(payload interface{}, ref *pocket48.Reply, idolName string, idolNick string) string {
	if ref != nil {
		name := normalizePocketText(ref.ReplyName)
		if isPossibleGiftSenderName(name, idolName, idolNick) {
			return name
		}
	}

	keys := []string{"senderName", "fromName", "fromUserName", "fromNickName", "fromNickname", "userName", "userNickName", "userNickname", "nickname", "nickName", "fansName", "giftSenderName", "name"}
	for _, key := range keys {
		if name := normalizePocketText(findStringField(payload, []string{key})); isPossibleGiftSenderName(name, idolName, idolNick) {
			return name
		}
	}

	var values []string
	collectStringValues(payload, &values)
	for _, v := range values {
		name := normalizePocketText(v)
		if !isPossibleGiftSenderName(name, idolName, idolNick) {
			continue
		}
		return name
	}

	return ""
}

func isPossibleGiftSenderName(name string, idolName string, idolNick string) bool {
	if name == "" {
		return false
	}
	if strings.Contains(name, "送给") || strings.Contains(name, "个") {
		return false
	}
	if strings.Contains(name, "{") || strings.Contains(name, "}") || strings.Contains(name, ":") {
		return false
	}
	if name == "直播" || name == "[文本]" {
		return false
	}
	if idolName != "" && name == normalizePocketText(idolName) {
		return false
	}
	if idolNick != "" && name == normalizePocketText(idolNick) {
		return false
	}
	runes := []rune(name)
	if len(runes) > 30 {
		return false
	}
	return true
}

func extractLikelyReplyAnswer(body string) string {
	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}

	keys := []string{"text", "content", "msg", "message", "answer", "replyContent", "replyMsg", "replyMessage"}
	if text := findStringField(payload, keys); text != "" {
		normalized := normalizePocketText(text)
		if normalized != "" && !strings.Contains(normalized, "送给") {
			return normalized
		}
	}

	var values []string
	collectStringValues(payload, &values)
	for _, v := range values {
		t := normalizePocketText(v)
		if t == "" {
			continue
		}
		if strings.Contains(t, "送给") {
			continue
		}
		if strings.Contains(t, "{") || strings.Contains(t, "}") {
			continue
		}
		return t
	}

	return ""
}

func collectStringValues(payload interface{}, values *[]string) {
	switch v := payload.(type) {
	case map[string]interface{}:
		for _, nested := range v {
			collectStringValues(nested, values)
		}
	case []interface{}:
		for _, item := range v {
			collectStringValues(item, values)
		}
	case string:
		text := strings.TrimSpace(v)
		if text == "" {
			return
		}
		if (strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")) || (strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]")) {
			var nested interface{}
			if err := json.Unmarshal([]byte(text), &nested); err == nil {
				collectStringValues(nested, values)
				return
			}
		}
		*values = append(*values, text)
	}
}

func parseEmbeddedReplyMessage(body string) (string, string, bool) {
	var wrapper struct {
		MessageType string `json:"messageType"`
		ReplyInfo   struct {
			ReplyName string `json:"replyName"`
			ReplyText string `json:"replyText"`
			Text      string `json:"text"`
		} `json:"replyInfo"`
	}

	bodyText := strings.TrimSpace(body)
	if !strings.HasPrefix(bodyText, "{") {
		return "", "", false
	}
	if err := json.Unmarshal([]byte(bodyText), &wrapper); err != nil {
		return "", "", false
	}
	if strings.TrimSpace(wrapper.MessageType) != string(pocket48.MsgReply) {
		return "", "", false
	}

	quotedText := ""
	replyName := strings.TrimSpace(wrapper.ReplyInfo.ReplyName)
	replyText := strings.TrimSpace(wrapper.ReplyInfo.ReplyText)
	answerText := normalizePocketText(wrapper.ReplyInfo.Text)
	if replyName != "" && replyText != "" {
		quotedText = normalizePocketText(fmt.Sprintf("%s:%s", replyName, replyText))
	}

	return quotedText, answerText, true
}

func parseGiftReplyMessage(body string) (giftText string, replyText string, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" || !strings.HasPrefix(body, "{") {
		return "", "", false
	}

	// Try parsing as giftReplyInfo structure (Pocket48 actual format)
	var giftReply struct {
		GiftReplyInfo struct {
			ReplyText string `json:"replyText"` // e.g. "送给 胡晓慧 1个高级马卡龙"
			ReplyName string `json:"replyName"` // e.g. "小7大王盖毛毯" (sender)
			Text      string `json:"text"`      // e.g. "啵啵啵" (reply)
		} `json:"giftReplyInfo"`
	}

	if err := json.Unmarshal([]byte(body), &giftReply); err == nil {
		gi := giftReply.GiftReplyInfo
		if gi.ReplyText != "" && gi.ReplyName != "" {
			giftText = gi.ReplyName + " " + strings.TrimSpace(gi.ReplyText)
			replyText = normalizePocketText(gi.Text)
			return giftText, replyText, true
		}
	}

	// Try parsing as giftInfo structure (alternative format)
	var wrapper struct {
		GiftInfo struct {
			GiftName string `json:"giftName"`
			Sender   string `json:"sender"`
			Receiver string `json:"receiver"`
		} `json:"giftInfo"`
		ReplyInfo struct {
			ReplyName string `json:"replyName"`
			ReplyText string `json:"replyText"`
			Text      string `json:"text"`
		} `json:"replyInfo"`
	}

	if err := json.Unmarshal([]byte(body), &wrapper); err == nil {
		giftName := strings.TrimSpace(wrapper.GiftInfo.GiftName)
		sender := strings.TrimSpace(wrapper.GiftInfo.Sender)
		receiver := strings.TrimSpace(wrapper.GiftInfo.Receiver)
		replyName := strings.TrimSpace(wrapper.ReplyInfo.ReplyName)
		replyText = normalizePocketText(wrapper.ReplyInfo.Text)

		if giftName != "" && sender != "" && receiver != "" {
			giftText = fmt.Sprintf("%s 送给 %s %d个%s", sender, receiver, 1, giftName)
			if replyName != "" && replyText != "" {
				replyText = fmt.Sprintf("%s(%s):%s", replyName, receiver, replyText)
			}
			return giftText, replyText, true
		}
	}

	// Fallback: flexible search for "送给" in any string field
	var payload interface{}
	if err := json.Unmarshal([]byte(body), &payload); err == nil {
		var values []string
		collectStringValues(payload, &values)
		// Find sender name (a name-like field that's not the gift text)
		senderName := ""
		for _, v := range values {
			t := strings.TrimSpace(v)
			if t != "" && !strings.Contains(t, "送给") && !strings.Contains(t, "\n") && len(t) < 20 && len(t) > 1 {
				senderName = t
				break
			}
		}
		for _, v := range values {
			t := normalizePocketText(v)
			if strings.Contains(t, "送给") {
				if senderName != "" && !strings.HasPrefix(t, senderName) {
					giftText = senderName + " " + t
				} else {
					giftText = t
				}
				// Find reply text (short text that's not the gift text)
				for _, rv := range values {
					rt := normalizePocketText(rv)
					if rt != "" && rt != t && rt != senderName && !strings.Contains(rt, "送给") && len(rt) > 1 && len(rt) < 100 {
						replyText = rt
						break
					}
				}
				return giftText, replyText, true
			}
		}
	}

	return "", "", false
}

func parseAudioGiftReplyMessage(body string) (giftText string, voiceURL string, duration int, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" || !strings.HasPrefix(body, "{") {
		return "", "", 0, false
	}

	var wrapper struct {
		GiftReplyInfo struct {
			VoiceURL  string `json:"voiceUrl"`
			ReplyText string `json:"replyText"`
			ReplyName string `json:"replyName"`
			Duration  int    `json:"duration"`
		} `json:"giftReplyInfo"`
		MessageType string `json:"messageType"`
	}
	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		return "", "", 0, false
	}

	gi := wrapper.GiftReplyInfo
	voiceURL = normalizeMediaURL(gi.VoiceURL)
	replyName := strings.TrimSpace(gi.ReplyName)
	replyText := strings.TrimSpace(gi.ReplyText)
	if replyName != "" && replyText != "" {
		giftText = replyName + " " + replyText
	} else if replyText != "" {
		giftText = replyText
	} else if replyName != "" {
		giftText = replyName
	}

	return giftText, voiceURL, gi.Duration, voiceURL != ""
}

func parseFlipCardBody(body string) (question string, answer string, answerType string, ok bool) {
	body = strings.TrimSpace(body)
	if body == "" || !strings.HasPrefix(body, "{") {
		return "", "", "", false
	}

	var wrapper struct {
		FilpCardInfo struct {
			Question   string `json:"question"`
			Answer     string `json:"answer"`
			AnswerType string `json:"answerType"`
		} `json:"filpCardInfo"`
		FlipCardInfo struct {
			Question   string `json:"question"`
			Answer     string `json:"answer"`
			AnswerType string `json:"answerType"`
		} `json:"flipCardInfo"`
	}

	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		return "", "", "", false
	}

	question = normalizePocketText(wrapper.FilpCardInfo.Question)
	answer = normalizePocketText(wrapper.FilpCardInfo.Answer)
	answerType = strings.TrimSpace(wrapper.FilpCardInfo.AnswerType)

	if question == "" && answer == "" {
		question = normalizePocketText(wrapper.FlipCardInfo.Question)
		answer = normalizePocketText(wrapper.FlipCardInfo.Answer)
		answerType = strings.TrimSpace(wrapper.FlipCardInfo.AnswerType)
	}

	if question == "" && answer == "" {
		return "", "", "", false
	}

	return question, answer, answerType, true
}

func buildFlipCardForwardText(idolName, question, answer, answerType string, msgType pocket48.MessageType) string {
	if idolName == "" {
		idolName = "成员"
	}

	var sb strings.Builder
	sb.WriteString("【公开翻牌】\n")

	if question != "" {
		sb.WriteString("粉丝提问: ")
		sb.WriteString(question)
		sb.WriteString("\n")
	}

	if answer != "" {
		sb.WriteString(idolName)
		sb.WriteString(": ")
		sb.WriteString(answer)
	} else {
		sb.WriteString(idolName)
		sb.WriteString(": [已翻牌]")
	}

	if answer == "" || answerType != "" && answerType != "1" {
		sb.WriteString(" (类型: ")
		if answerType != "" {
			sb.WriteString(answerType)
		} else {
			sb.WriteString(string(msgType))
		}
		sb.WriteString(")")
	}

	return sb.String()
}

func buildFlipCardAudioIntroText(idolName, question string) string {
	if idolName == "" {
		idolName = "成员"
	}
	if question != "" {
		return fmt.Sprintf("【公开翻牌】\n粉丝提问: %s\n%s: 【回复见下方】", question, idolName)
	}
	return fmt.Sprintf("【公开翻牌】\n%s: [语音翻牌]", idolName)
}

func extractNumber(s string) (int64, error) {
	var nums string
	for _, c := range s {
		if c >= '0' && c <= '9' {
			nums += string(c)
		}
	}
	if nums == "" {
		return 0, fmt.Errorf("no number found")
	}
	return strconv.ParseInt(nums, 10, 64)
}



// normalizeGiftNum ensures gift number is at least 1
func normalizeGiftNum(num int64) int64 {
	if num <= 0 {
		return 1
	}
	return num
}
