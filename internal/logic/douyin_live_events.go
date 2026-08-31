package logic

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const douyinProcessedGiftLimit = 2048

func douyinLivePayload(body map[string]interface{}) map[string]interface{} {
	if payload, ok := mapValueFold(body, "payload").(map[string]interface{}); ok {
		return payload
	}
	return body
}

// douyinLiveObject deliberately checks only the decoded message and its
// immediate payload. Recursive same-name searches can select an unrelated
// nested user/gift/stat object when the upstream schema grows.
func douyinLiveObject(body map[string]interface{}, key string) map[string]interface{} {
	if object, ok := mapValueFold(body, key).(map[string]interface{}); ok {
		return object
	}
	if payload, ok := mapValueFold(body, "payload").(map[string]interface{}); ok {
		if object, ok := mapValueFold(payload, key).(map[string]interface{}); ok {
			return object
		}
	}
	return nil
}

func mapValueFold(m map[string]interface{}, key string) interface{} {
	for candidate, value := range m {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return nil
}

func findDouyinObject(value interface{}, key string) map[string]interface{} {
	switch node := value.(type) {
	case map[string]interface{}:
		if child, ok := mapValueFold(node, key).(map[string]interface{}); ok {
			return child
		}
		for _, child := range node {
			if found := findDouyinObject(child, key); found != nil {
				return found
			}
		}
	case []interface{}:
		for _, child := range node {
			if found := findDouyinObject(child, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func findDouyinValue(value interface{}, key string) interface{} {
	switch node := value.(type) {
	case map[string]interface{}:
		if found := mapValueFold(node, key); found != nil {
			return found
		}
		for _, child := range node {
			if found := findDouyinValue(child, key); found != nil {
				return found
			}
		}
	case []interface{}:
		for _, child := range node {
			if found := findDouyinValue(child, key); found != nil {
				return found
			}
		}
	}
	return nil
}

func firstDouyinNestedNumber(value interface{}, keys ...string) int64 {
	for _, key := range keys {
		if result := numberAsInt64(findDouyinValue(value, key)); result > 0 {
			return result
		}
	}
	return 0
}

func firstDouyinNestedString(value interface{}, keys ...string) string {
	for _, key := range keys {
		if result := douyinString(findDouyinValue(value, key)); result != "" {
			return result
		}
	}
	return ""
}

func firstDouyinNumber(m map[string]interface{}, keys ...string) int64 {
	for _, key := range keys {
		if value := numberAsInt64(mapValueFold(m, key)); value > 0 {
			return value
		}
	}
	return 0
}

func douyinString(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		if number := numberAsInt64(value); number > 0 {
			return strconv.FormatInt(number, 10)
		}
		return ""
	}
}

func firstDouyinString(m map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if result := douyinString(mapValueFold(m, key)); result != "" {
			return result
		}
	}
	return ""
}

func douyinBool(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		result, _ := strconv.ParseBool(v)
		if !result {
			n, _ := strconv.ParseInt(v, 10, 64)
			return n != 0
		}
	}
	return false
}

func containsDouyinGiftMessageID(ids []string, id string) bool {
	for _, current := range ids {
		if current == id {
			return true
		}
	}
	return false
}

func (m *DouyinMonitor) applyDouyinGift(liveID string, body map[string]interface{}) {
	m.applyDouyinGiftEvent(liveID, "WebcastGiftMessage", douyinGiftEventKey("WebcastGiftMessage", body), time.Now(), body)
}

// applyDouyinGiftEvent uses only the protocol's diamondCount as a unit price.
// It never labels this value as sound wave. repeatCount, comboCount and
// groupCount are cumulative counters, so only their positive delta is added.
func (m *DouyinMonitor) applyDouyinGiftEvent(liveID, method, eventKey string, capturedAt time.Time, body map[string]interface{}) {
	gift := douyinLiveObject(body, "gift")
	if gift == nil && (method == "WebcastLightGiftMessage" || method == "LightGiftMessage") {
		gift = douyinLiveObject(body, "giftInfo")
	}
	if gift == nil {
		gift = douyinLiveObject(body, "giftStruct")
	}
	if gift == nil {
		return
	}
	common := douyinLiveObject(body, "common")
	user := douyinLiveObject(body, "user")
	msgID := ""
	if common != nil {
		msgID = firstDouyinString(common, "msgId", "logId")
	}
	if msgID == "" {
		msgID = firstDouyinString(body, "msgId", "logId")
	}
	giftID := firstDouyinString(gift, "id", "giftId")
	userID := ""
	if user != nil {
		userID = firstDouyinString(user, "id", "secUid")
	}
	payload := douyinLivePayload(body)
	comboID := firstDouyinString(payload, "comboId", "groupId")
	count := firstDouyinNumber(payload, "repeatCount", "comboCount", "groupCount")
	if count <= 0 {
		count = 1
	}
	repeatEnd := douyinBool(mapValueFold(payload, "repeatEnd"))
	isCombo := comboID != "" || mapValueFold(payload, "repeatCount") != nil || mapValueFold(payload, "comboCount") != nil || mapValueFold(payload, "groupCount") != nil
	comboKey := userID + "|" + giftID + "|" + comboID
	price := firstDouyinNumber(gift, "diamondCount")

	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	dedupeID := eventKey
	if dedupeID == "" {
		dedupeID = msgID
	}
	if dedupeID != "" && containsDouyinGiftMessageID(state.ProcessedGiftMessageIDs, dedupeID) {
		m.mu.Unlock()
		return
	}
	if dedupeID != "" {
		state.ProcessedGiftMessageIDs = append(state.ProcessedGiftMessageIDs, dedupeID)
		if len(state.ProcessedGiftMessageIDs) > douyinProcessedGiftLimit {
			state.ProcessedGiftMessageIDs = append([]string(nil), state.ProcessedGiftMessageIDs[len(state.ProcessedGiftMessageIDs)-douyinProcessedGiftLimit:]...)
		}
	}
	state.GiftEventCount++
	if state.ComboGiftCounts == nil {
		state.ComboGiftCounts = make(map[string]int64)
	}
	delta := count
	if isCombo {
		previous := state.ComboGiftCounts[comboKey]
		delta = count - previous
		if delta < 0 {
			delta = 0
		}
		if repeatEnd {
			delete(state.ComboGiftCounts, comboKey)
		} else if count > previous {
			state.ComboGiftCounts[comboKey] = count
		}
	}
	if price > 0 {
		state.DiamondAvailable = true
		state.SoundWaveAvailable = true
		if delta > 0 {
			state.DiamondTotal += price * delta
			state.EstimatedSoundWave += price * delta
		}
	}
	if delta > 0 {
		state.GiftCount += delta
	}
	state.LastUpdatedAt = capturedAt
	sessionID := state.SessionID
	store := m.liveStore
	m.mu.Unlock()
	if store != nil {
		_ = store.recordRevenue(douyinRevenueSnapshot{SessionID: sessionID, CapturedAt: capturedAt,
			DiamondDelta: price * delta, GiftCountDelta: delta, SourceEventKey: eventKey})
	}
	m.persistLiveState(liveID, false)
}

func (m *DouyinMonitor) applyDouyinFanTicket(liveID, eventKey string, capturedAt time.Time, total int64) {
	if total <= 0 {
		return
	}
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	if total > state.FanTicketTotal {
		state.FanTicketTotal = total
	}
	state.LastUpdatedAt = capturedAt
	sessionID, store, savedTotal := state.SessionID, m.liveStore, state.FanTicketTotal
	m.mu.Unlock()
	if store != nil {
		_ = store.recordRevenue(douyinRevenueSnapshot{SessionID: sessionID, CapturedAt: capturedAt,
			FanTicketTotal: savedTotal, SourceEventKey: eventKey})
	}
	m.persistLiveState(liveID, false)
}

func (m *DouyinMonitor) applyDouyinPKScores(liveID, eventKey string, capturedAt time.Time, left, right int64) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	state.PKLeftScore, state.PKRightScore = left, right
	state.LastUpdatedAt = capturedAt
	sessionID, store := state.SessionID, m.liveStore
	m.mu.Unlock()
	if store != nil {
		_ = store.recordRevenue(douyinRevenueSnapshot{SessionID: sessionID, CapturedAt: capturedAt,
			PKLeftScore: left, PKRightScore: right, SourceEventKey: eventKey})
	}
	m.persistLiveState(liveID, false)
}

func sensitiveDouyinKey(key string) bool {
	key = strings.ToLower(key)
	for _, term := range []string{"cookie", "token", "authorization", "signature", "verify", "apikey", "api_key", "credential", "session"} {
		if strings.Contains(key, term) {
			return true
		}
	}
	return false
}

func sanitizeDouyinSample(value interface{}) interface{} {
	switch node := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(node))
		for key, child := range node {
			if sensitiveDouyinKey(key) {
				result[key] = "[redacted]"
				continue
			}
			if strings.EqualFold(key, "user") {
				if user, ok := child.(map[string]interface{}); ok {
					redacted := make(map[string]interface{}, len(user))
					for userKey, userValue := range user {
						switch strings.ToLower(userKey) {
						case "id", "uid", "secuid", "sec_uid", "nickname", "displayname", "avatarurl", "avatarthumb":
							redacted[userKey] = "[redacted]"
						default:
							redacted[userKey] = sanitizeDouyinSample(userValue)
						}
					}
					result[key] = redacted
					continue
				}
			}
			result[key] = sanitizeDouyinSample(child)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(node))
		for i, child := range node {
			result[i] = sanitizeDouyinSample(child)
		}
		return result
	case string:
		lower := strings.ToLower(node)
		if strings.Contains(lower, "sessionid=") || strings.Contains(lower, "ttwid=") ||
			strings.Contains(lower, "passport_csrf_token=") || strings.Contains(lower, "odin_tt=") {
			return "[redacted]"
		}
		if parsed, err := url.Parse(node); err == nil && parsed.IsAbs() && parsed.RawQuery != "" {
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return parsed.String()
		}
		return node
	default:
		return value
	}
}

func (m *DouyinMonitor) dumpDouyinLiveSample(kind, liveID string, body map[string]interface{}) {
	dir := filepath.Join("storage", "douyin-live-stat-dumps")
	if kind == "gift" {
		dir = filepath.Join("storage", "douyin-live-gift-dumps")
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	raw, err := json.MarshalIndent(sanitizeDouyinSample(body), "", "  ")
	if err != nil {
		return
	}
	name := fmt.Sprintf("%s-%s-%d.json", kind, safeDouyinLiveFilename(liveID), time.Now().UnixNano())
	tmp, err := os.CreateTemp(dir, ".douyin-dump-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err == nil {
		_ = os.Rename(tmpPath, filepath.Join(dir, name))
		pruneDouyinDumpFiles(dir, 200)
	}
}

func pruneDouyinDumpFiles(dir string, max int) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= max {
		return
	}
	type dumpInfo struct {
		path string
		mod  time.Time
	}
	files := make([]dumpInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, dumpInfo{filepath.Join(dir, entry.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for len(files) > max {
		_ = os.Remove(files[0].path)
		files = files[1:]
	}
}
