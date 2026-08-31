package logic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var douyinHTTPMetricPattern = regexp.MustCompile(`(?i)\\?"(user_count|user_count_str|room_user_count|room_user_count_str)\\?"\s*:\s*(?:\\?"([^"\\]*)\\?"|([0-9][0-9,.]*))`)

type douyinResolvedMetrics struct {
	OnlineCount  int64
	OnlineSource string
	TotalViewers int64
	LikeCount    int64
	Raw          map[string]interface{}
}

type douyinMetricCandidate struct {
	name  string
	value interface{}
}

func parseDouyinDisplayCount(value interface{}) int64 {
	if value == nil {
		return 0
	}
	text := strings.TrimSpace(douyinString(value))
	text = strings.NewReplacer(",", "", "，", "", " ", "", "+", "", "人", "", "次", "").Replace(text)
	if text == "" {
		return 0
	}
	multiplier := float64(1)
	switch {
	case strings.HasSuffix(text, "亿"):
		multiplier = 100_000_000
		text = strings.TrimSuffix(text, "亿")
	case strings.HasSuffix(text, "万"):
		multiplier = 10_000
		text = strings.TrimSuffix(text, "万")
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || number <= 0 || number > float64(math.MaxInt64)/multiplier {
		return 0
	}
	return int64(math.Round(number * multiplier))
}

func candidateAt(body map[string]interface{}, name string, path ...string) douyinMetricCandidate {
	return douyinMetricCandidate{name: name, value: douyinValueAtPath(body, path...)}
}

func firstMetricCandidate(candidates []douyinMetricCandidate) (int64, string) {
	for _, candidate := range candidates {
		if value := parseDouyinDisplayCount(candidate.value); value > 0 {
			return value, candidate.name
		}
	}
	return 0, ""
}

func firstExactMetricCandidate(candidates []douyinMetricCandidate) (int64, string) {
	var sentinelValue int64
	var sentinelSource string
	for _, candidate := range candidates {
		value := parseDouyinDisplayCount(candidate.value)
		if value > 1 {
			return value, candidate.name
		}
		if value == 1 && sentinelValue == 0 {
			sentinelValue, sentinelSource = value, candidate.name
		}
	}
	return sentinelValue, sentinelSource
}

func resolveOnlineCandidates(exact, display []douyinMetricCandidate) (int64, string) {
	exactValue, exactSource := firstExactMetricCandidate(exact)
	displayValue, displaySource := firstMetricCandidate(display)
	// Some HTTP payloads use 1 as a sentinel while the adjacent display field
	// carries the actual abbreviated count (for example 1 + "1.2万").
	if exactValue == 1 && displayValue > exactValue {
		return displayValue, displaySource
	}
	if exactValue > 0 {
		return exactValue, exactSource
	}
	return displayValue, displaySource
}

func resolveDouyinMetrics(method string, body map[string]interface{}) douyinResolvedMetrics {
	exactOnline := []douyinMetricCandidate{
		candidateAt(body, "user_count", "user_count"),
		candidateAt(body, "room_user_count", "room_user_count"),
		candidateAt(body, "payload.user_count", "payload", "user_count"),
		candidateAt(body, "data.user_count", "data", "user_count"),
		candidateAt(body, "payload.room.user_count", "payload", "room", "user_count"),
		candidateAt(body, "onlineUserCount", "onlineUserCount"),
		candidateAt(body, "payload.onlineUserCount", "payload", "onlineUserCount"),
		candidateAt(body, "userCount", "userCount"),
		candidateAt(body, "payload.userCount", "payload", "userCount"),
	}
	if method == "WebcastRoomUserSeqMessage" || method == "RoomUserSeqMessage" {
		exactOnline = append([]douyinMetricCandidate{
			candidateAt(body, "RoomUserSeqMessage.total", "total"),
			candidateAt(body, "RoomUserSeqMessage.payload.total", "payload", "total"),
		}, exactOnline...)
	}
	displayOnline := []douyinMetricCandidate{
		candidateAt(body, "user_count_str", "user_count_str"),
		candidateAt(body, "room_user_count_str", "room_user_count_str"),
		candidateAt(body, "payload.user_count_str", "payload", "user_count_str"),
		candidateAt(body, "data.user_count_str", "data", "user_count_str"),
		candidateAt(body, "payload.room.user_count_str", "payload", "room", "user_count_str"),
		candidateAt(body, "onlineUserForAnchor", "onlineUserForAnchor"),
		candidateAt(body, "payload.onlineUserForAnchor", "payload", "onlineUserForAnchor"),
	}
	online, onlineSource := resolveOnlineCandidates(exactOnline, displayOnline)

	totalCandidates := []douyinMetricCandidate{
		candidateAt(body, "totalUser", "totalUser"),
		candidateAt(body, "totalUserCount", "totalUserCount"),
		candidateAt(body, "audienceCount", "audienceCount"),
		candidateAt(body, "payload.audienceCount", "payload", "audienceCount"),
		candidateAt(body, "payload.totalUser", "payload", "totalUser"),
		candidateAt(body, "totalUserStr", "totalUserStr"),
		candidateAt(body, "totalPvForAnchor", "totalPvForAnchor"),
	}
	if method == "WebcastRoomStatsMessage" || method == "RoomStatsMessage" {
		totalCandidates = append([]douyinMetricCandidate{candidateAt(body, "RoomStatsMessage.total", "total")}, totalCandidates...)
	}
	total, _ := firstMetricCandidate(totalCandidates)
	likeCandidates := []douyinMetricCandidate{
		candidateAt(body, "likeCount", "likeCount"),
		candidateAt(body, "diggCount", "diggCount"),
		candidateAt(body, "payload.likeCount", "payload", "likeCount"),
	}
	likes, _ := firstMetricCandidate(likeCandidates)
	raw := make(map[string]interface{})
	for _, candidate := range append(append(exactOnline, displayOnline...), totalCandidates...) {
		if candidate.value != nil {
			raw[candidate.name] = candidate.value
		}
	}
	for _, candidate := range likeCandidates {
		if candidate.value != nil {
			raw[candidate.name] = candidate.value
		}
	}
	return douyinResolvedMetrics{OnlineCount: online, OnlineSource: onlineSource, TotalViewers: total, LikeCount: likes, Raw: raw}
}

func douyinEventKey(method string, body map[string]interface{}) string {
	id := firstDouyinString(body, "msgId", "logId", "messageId")
	if id == "" {
		if common := douyinLiveObject(body, "common"); common != nil {
			id = firstDouyinString(common, "msgId", "logId", "messageId")
		}
	}
	if id != "" {
		return method + ":" + id
	}
	raw, _ := json.Marshal(sanitizeDouyinSample(body))
	sum := sha256.Sum256(raw)
	return method + ":sha256:" + hex.EncodeToString(sum[:])
}

func douyinGiftEventKey(method string, body map[string]interface{}) string {
	base := douyinEventKey(method, body)
	payload := douyinLivePayload(body)
	gift := douyinLiveObject(body, "gift")
	if gift == nil {
		gift = douyinLiveObject(body, "giftInfo")
	}
	if gift == nil {
		gift = douyinLiveObject(body, "giftStruct")
	}
	if gift == nil {
		return base
	}
	parts := []string{
		base,
		"gift=" + firstDouyinString(gift, "id", "giftId"),
		"group=" + firstDouyinString(payload, "groupId", "comboId"),
		"repeat=" + douyinString(mapValueFold(payload, "repeatCount")),
		"combo=" + douyinString(mapValueFold(payload, "comboCount")),
		"end=" + strconv.FormatBool(douyinBool(mapValueFold(payload, "repeatEnd"))),
	}
	return strings.Join(parts, "|")
}

func (m *DouyinMonitor) currentLiveSession(liveID string) (string, *douyinLiveStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.liveStates[liveID]
	if state == nil || !state.Online {
		return "", m.liveStore
	}
	return state.SessionID, m.liveStore
}

func (m *DouyinMonitor) applyResolvedMetrics(liveID string, metrics douyinResolvedMetrics, capturedAt time.Time) {
	m.mu.Lock()
	state := m.liveStates[liveID]
	store := m.liveStore
	if state == nil || !state.Online {
		m.mu.Unlock()
		return
	}
	if metrics.OnlineCount > 0 {
		state.CurrentOnline = metrics.OnlineCount
		if metrics.OnlineCount > state.PeakOnline {
			state.PeakOnline = metrics.OnlineCount
		}
	}
	if metrics.TotalViewers > state.TotalAudience {
		state.TotalAudience = metrics.TotalViewers
	}
	if metrics.LikeCount > state.LikeCount {
		state.LikeCount = metrics.LikeCount
	}
	state.LastUpdatedAt = capturedAt
	sessionID := state.SessionID
	totalViewers, likeCount := state.TotalAudience, state.LikeCount
	m.mu.Unlock()
	raw, _ := json.Marshal(sanitizeDouyinSample(metrics.Raw))
	if store != nil {
		_ = store.recordMetric(douyinMetricSnapshot{SessionID: sessionID, CapturedAt: capturedAt,
			OnlineCount: metrics.OnlineCount, OnlineSource: metrics.OnlineSource,
			TotalViewers: totalViewers, LikeCount: likeCount, RawMetricsJSON: string(raw)})
	}
	m.persistLiveState(liveID, false)
}

func extractDouyinFanTicket(body map[string]interface{}) int64 {
	value, _ := firstMetricCandidate([]douyinMetricCandidate{
		candidateAt(body, "roomFanTicketCount", "roomFanTicketCount"),
		candidateAt(body, "roomFanTicketCountText", "roomFanTicketCountText"),
		candidateAt(body, "fanTicketCount", "fanTicketCount"),
		candidateAt(body, "payload.roomFanTicketCount", "payload", "roomFanTicketCount"),
	})
	return value
}

func extractDouyinPKScores(body map[string]interface{}) (left, right int64, ok bool) {
	left, _ = firstMetricCandidate([]douyinMetricCandidate{
		candidateAt(body, "against.leftGoalInt", "against", "leftGoalInt"),
		candidateAt(body, "against.leftGoal", "against", "leftGoal"),
		candidateAt(body, "leftScore", "leftScore"),
		candidateAt(body, "pkLeftScore", "pkLeftScore"),
		candidateAt(body, "payload.against.leftGoalInt", "payload", "against", "leftGoalInt"),
		candidateAt(body, "payload.leftScore", "payload", "leftScore"),
	})
	right, _ = firstMetricCandidate([]douyinMetricCandidate{
		candidateAt(body, "against.rightGoalInt", "against", "rightGoalInt"),
		candidateAt(body, "against.rightGoal", "against", "rightGoal"),
		candidateAt(body, "rightScore", "rightScore"),
		candidateAt(body, "pkRightScore", "pkRightScore"),
		candidateAt(body, "payload.against.rightGoalInt", "payload", "against", "rightGoalInt"),
		candidateAt(body, "payload.rightScore", "payload", "rightScore"),
	})
	return left, right, left > 0 || right > 0
}

func extractDouyinHTTPMetrics(raw []byte) douyinResolvedMetrics {
	body := make(map[string]interface{})
	for _, match := range douyinHTTPMetricPattern.FindAllSubmatch(raw, -1) {
		if len(match) < 4 {
			continue
		}
		key := strings.ToLower(string(match[1]))
		value := string(match[2])
		if value == "" {
			value = string(match[3])
		}
		if _, exists := body[key]; !exists {
			body[key] = value
		}
	}
	return resolveDouyinMetrics("HTTP", body)
}

func (m *DouyinMonitor) sampleDouyinHTTPMetrics(ctx context.Context, liveID string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://live.douyin.com/"+liveID, nil)
	if err != nil {
		return
	}
	request.Header.Set("User-Agent", "Mozilla/5.0 (compatible; pocket48-bot/1.0; +https://github.com/sjsj1849/pocket48-bot)")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return
	}
	const maxBody = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return
	}
	metrics := extractDouyinHTTPMetrics(raw)
	if metrics.OnlineCount > 0 || metrics.TotalViewers > 0 {
		metrics.OnlineSource = "http." + metrics.OnlineSource
		m.applyResolvedMetrics(liveID, metrics, time.Now())
	}
}
