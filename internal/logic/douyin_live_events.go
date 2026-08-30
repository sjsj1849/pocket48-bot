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

// applyDouyinGift uses only gift.diamondCount as a unit price. repeatCount,
// comboCount and groupCount are treated as cumulative combo counters.
func (m *DouyinMonitor) applyDouyinGift(liveID string, body map[string]interface{}) {
	gift := findDouyinObject(body, "gift")
	if gift == nil {
		return
	}
	common := findDouyinObject(body, "common")
	user := findDouyinObject(body, "user")
	msgID := ""
	if common != nil {
		msgID = firstDouyinString(common, "msgId", "logId")
	}
	if msgID == "" {
		msgID = firstDouyinNestedString(body, "msgId", "logId")
	}
	giftID := firstDouyinString(gift, "id")
	userID := ""
	if user != nil {
		userID = firstDouyinString(user, "id", "secUid")
	}
	comboID := firstDouyinNestedString(body, "comboId", "groupId")
	count := firstDouyinNestedNumber(body, "repeatCount", "comboCount", "groupCount")
	if count <= 0 {
		count = 1
	}
	repeatEnd := douyinBool(findDouyinValue(body, "repeatEnd"))
	isCombo := comboID != "" || findDouyinValue(body, "repeatCount") != nil || findDouyinValue(body, "comboCount") != nil || findDouyinValue(body, "groupCount") != nil
	comboKey := userID + "|" + giftID + "|" + comboID
	price := firstDouyinNumber(gift, "diamondCount")

	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	if msgID != "" && containsDouyinGiftMessageID(state.ProcessedGiftMessageIDs, msgID) {
		m.mu.Unlock()
		return
	}
	if msgID != "" {
		state.ProcessedGiftMessageIDs = append(state.ProcessedGiftMessageIDs, msgID)
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
		state.SoundWaveAvailable = true
		if delta > 0 {
			state.EstimatedSoundWave += price * delta
		}
	}
	state.LastUpdatedAt = time.Now()
	m.mu.Unlock()
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
