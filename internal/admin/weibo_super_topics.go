package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- 超话日报 topics (WEIBO_SUPER_COUNT_TOPICS + GROUPS) ---

type weiboSuperCountTopicPanel struct {
	OID        string `json:"oid"`
	Name       string `json:"name,omitempty"`
	GroupKey   string `json:"groupKey,omitempty"`  // internal key e.g. xjjmen
	GroupName  string `json:"groupName,omitempty"` // display name e.g. X姐姐们
	ReportSign int    `json:"reportSign,omitempty"`
}

type weiboSuperCountTopicStored struct {
	OID        string `json:"oid"`
	Name       string `json:"name,omitempty"`
	ReportSign int    `json:"report_sign,omitempty"`
	GroupName  string `json:"group_name,omitempty"` // stores key or legacy display text
}

type weiboSuperCountGroupStored struct {
	Name string `json:"name"`
}

type weiboSuperCountImageGroupPanel struct {
	Key       string   `json:"key,omitempty"`
	Name      string   `json:"name"`
	GroupKeys []string `json:"groupKeys"`
	// TargetIDs: this image only goes to these delivery targets (6 选 N，可全不选)。
	TargetIDs []string `json:"targetIds,omitempty"`
}

type weiboSuperCountImageGroupStored struct {
	Name      string   `json:"name"`
	GroupKeys []string `json:"group_keys"`
	TargetIDs []string `json:"target_ids,omitempty"`
}

type weiboSuperCountGroupPanel struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	TopicCount      int      `json:"topicCount"`
	ImageGroupNames []string `json:"imageGroupNames"`
}

var oidPattern = regexp.MustCompile(`^(?:100808)?[0-9a-fA-F]{32,40}$`)
var oidInLinkPattern = regexp.MustCompile(`(?i)100808[0-9a-f]{32,40}`)

func normalizeSuperOID(raw string) string {
	raw = strings.TrimSpace(raw)
	// Super-topic links commonly carry the OID either in /p/100808… or in a
	// containerid query parameter. Match it before parsing prefixes so callers
	// can paste the complete desktop/mobile URL (including URL-encoded values).
	if decoded, err := url.QueryUnescape(raw); err == nil {
		raw = decoded
	}
	if oid := oidInLinkPattern.FindString(raw); oid != "" {
		return strings.ToLower(oid)
	}
	// strip common prefixes
	raw = strings.TrimPrefix(raw, "1022:")
	if strings.HasPrefix(raw, "100808") {
		// keep as-is after lower
		return strings.ToLower(raw)
	}
	raw = strings.ToLower(raw)
	if raw == "" {
		return ""
	}
	// bare hex
	if matched, _ := regexp.MatchString(`^[0-9a-f]{32,40}$`, raw); matched {
		return "100808" + raw
	}
	return raw
}

func loadCountGroups(raw map[string]json.RawMessage) map[string]*weiboSuperCountGroupStored {
	groups := map[string]*weiboSuperCountGroupStored{}
	if encoded := raw["WEIBO_SUPER_COUNT_GROUPS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &groups)
	}
	return groups
}

func loadCountImageGroups(raw map[string]json.RawMessage) map[string]*weiboSuperCountImageGroupStored {
	plans := map[string]*weiboSuperCountImageGroupStored{}
	if encoded := raw["WEIBO_SUPER_COUNT_IMAGE_GROUPS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &plans)
	}
	return plans
}

func countImageGroupList(plans map[string]*weiboSuperCountImageGroupStored) []weiboSuperCountImageGroupPanel {
	result := make([]weiboSuperCountImageGroupPanel, 0, len(plans))
	for key, plan := range plans {
		if plan == nil {
			continue
		}
		result = append(result, weiboSuperCountImageGroupPanel{Key: key, Name: plan.Name, GroupKeys: plan.GroupKeys, TargetIDs: plan.TargetIDs})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Key < result[j].Key
		}
		return result[i].Name < result[j].Name
	})
	return result
}

func nextCountImageGroupKey(plans map[string]*weiboSuperCountImageGroupStored) string {
	for i := 1; ; i++ {
		key := fmt.Sprintf("image-%d", i)
		if _, exists := plans[key]; !exists {
			return key
		}
	}
}

func countGroupReference(groups map[string]*weiboSuperCountGroupStored, topics map[string]*weiboSuperCountTopicStored, plans map[string]*weiboSuperCountImageGroupStored, key string) (int, []string) {
	key = strings.TrimSpace(key)
	display := key
	if group := groups[key]; group != nil && strings.TrimSpace(group.Name) != "" {
		display = strings.TrimSpace(group.Name)
	}
	topicCount := 0
	for _, topic := range topics {
		if topic == nil {
			continue
		}
		stored := strings.TrimSpace(topic.GroupName)
		if stored == key || stored == display {
			topicCount++
		}
	}
	imageNames := []string{}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		for _, stored := range plan.GroupKeys {
			if strings.TrimSpace(stored) == key || strings.TrimSpace(stored) == display {
				name := strings.TrimSpace(plan.Name)
				if name == "" {
					name = "未命名出图方案"
				}
				imageNames = append(imageNames, name)
				break
			}
		}
	}
	sort.Strings(imageNames)
	return topicCount, imageNames
}

func (s *Server) handleWeiboSuperCountGroups(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	groups := loadCountGroups(raw)
	topics := map[string]*weiboSuperCountTopicStored{}
	if encoded := raw["WEIBO_SUPER_COUNT_TOPICS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &topics)
	}
	plans := loadCountImageGroups(raw)

	if r.Method == http.MethodGet {
		result := make([]weiboSuperCountGroupPanel, 0, len(groups))
		for key, group := range groups {
			name := key
			if group != nil && strings.TrimSpace(group.Name) != "" {
				name = strings.TrimSpace(group.Name)
			}
			topicCount, imageNames := countGroupReference(groups, topics, plans, key)
			result = append(result, weiboSuperCountGroupPanel{Key: key, Name: name, TopicCount: topicCount, ImageGroupNames: imageNames})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Name == result[j].Name {
				return result[i].Key < result[j].Key
			}
			return result[i].Name < result[j].Name
		})
		writeJSON(w, http.StatusOK, map[string]any{"groups": result})
		return
	}
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择要删除的日报分组"})
		return
	}
	key := strings.TrimSpace(body.Key)
	group := groups[key]
	if group == nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "日报分组不存在或已删除"})
		return
	}
	topicCount, imageNames := countGroupReference(groups, topics, plans, key)
	if topicCount > 0 || len(imageNames) > 0 {
		parts := []string{}
		if topicCount > 0 {
			parts = append(parts, fmt.Sprintf("%d 个超话日报配置", topicCount))
		}
		if len(imageNames) > 0 {
			parts = append(parts, "图片组合："+strings.Join(imageNames, "、"))
		}
		writeJSON(w, http.StatusConflict, apiError{Error: "该分组仍被" + strings.Join(parts, "；") + "引用，请先迁移或移除引用"})
		return
	}
	delete(groups, key)
	if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUPER_COUNT_GROUPS": groups}); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "日报分组已删除"})
}

// handleWeiboSuperCountImageGroups manages how existing report groups are
// combined into PNG attachments. It intentionally does not change email table
// grouping or QQ delivery.
func (s *Server) handleWeiboSuperCountImageGroups(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	groups := loadCountGroups(raw)
	plans := loadCountImageGroups(raw)
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"imageGroups": countImageGroupList(plans)})
		return
	}
	if r.Method == http.MethodDelete {
		var body weiboSuperCountImageGroupPanel
		if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Key) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择要删除的出图方案"})
			return
		}
		delete(plans, strings.TrimSpace(body.Key))
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUPER_COUNT_IMAGE_GROUPS": plans}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "出图方案已删除"})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var body weiboSuperCountImageGroupPanel
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写图片名称"})
		return
	}
	seen := make(map[string]struct{}, len(body.GroupKeys))
	cleanKeys := make([]string, 0, len(body.GroupKeys))
	for _, rawKey := range body.GroupKeys {
		key := strings.TrimSpace(rawKey)
		if _, exists := groups[key]; !exists {
			writeJSON(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("日报分组 %q 不存在", key)})
			return
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		cleanKeys = append(cleanKeys, key)
	}
	if len(cleanKeys) == 0 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "每张图片至少选择一个日报分组"})
		return
	}
	// 每张图可独立选择发送去向（0 或多个）。空表示不单独发送、沿用全局列表。
	seenTargets := make(map[string]struct{}, len(body.TargetIDs))
	cleanTargets := make([]string, 0, len(body.TargetIDs))
	for _, rawTarget := range body.TargetIDs {
		targetID := strings.TrimSpace(rawTarget)
		if targetID == "" {
			continue
		}
		if _, duplicate := seenTargets[targetID]; duplicate {
			continue
		}
		seenTargets[targetID] = struct{}{}
		cleanTargets = append(cleanTargets, targetID)
	}
	key := strings.TrimSpace(body.Key)
	if r.Method == http.MethodPost {
		key = nextCountImageGroupKey(plans)
	} else if _, exists := plans[key]; key == "" || !exists {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "要编辑的出图方案不存在"})
		return
	}
	plans[key] = &weiboSuperCountImageGroupStored{Name: body.Name, GroupKeys: cleanKeys, TargetIDs: cleanTargets}
	if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUPER_COUNT_IMAGE_GROUPS": plans}); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "出图方案已保存", "key": key})
}

func resolveCountGroupDisplay(groups map[string]*weiboSuperCountGroupStored, key string) (groupKey, display string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", ""
	}
	if g := groups[key]; g != nil && strings.TrimSpace(g.Name) != "" {
		return key, g.Name
	}
	// maybe key itself is already a display name used as key (e.g. 石榴大姐)
	return key, key
}

// resolveGroupKeyForWrite: panel may send display name or key.
// Prefer matching existing group by display name; else use text as key and ensure groups entry.
func resolveGroupKeyForWrite(groups map[string]*weiboSuperCountGroupStored, input string) (map[string]*weiboSuperCountGroupStored, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return groups, ""
	}
	if groups == nil {
		groups = map[string]*weiboSuperCountGroupStored{}
	}
	// exact key hit
	if g := groups[input]; g != nil {
		return groups, input
	}
	// match by display name
	for k, g := range groups {
		if g != nil && strings.TrimSpace(g.Name) == input {
			return groups, k
		}
	}
	// new group: use input as both key and display name (Chinese keys ok, e.g. 石榴大姐)
	groups[input] = &weiboSuperCountGroupStored{Name: input}
	return groups, input
}

func (s *Server) handleWeiboSuperCountTopics(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	topics := map[string]*weiboSuperCountTopicStored{}
	if encoded := raw["WEIBO_SUPER_COUNT_TOPICS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &topics)
	}
	groups := loadCountGroups(raw)

	switch r.Method {
	case http.MethodGet:
		result := make([]weiboSuperCountTopicPanel, 0, len(topics))
		for key, item := range topics {
			if item == nil {
				continue
			}
			oid := item.OID
			if oid == "" {
				oid = key
			}
			gk, display := resolveCountGroupDisplay(groups, item.GroupName)
			result = append(result, weiboSuperCountTopicPanel{
				OID: oid, Name: item.Name, GroupKey: gk, GroupName: display, ReportSign: item.ReportSign,
			})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupName == result[j].GroupName {
				if result[i].Name == result[j].Name {
					return result[i].OID < result[j].OID
				}
				return result[i].Name < result[j].Name
			}
			return result[i].GroupName < result[j].GroupName
		})
		// also return groups for panel dropdown
		groupList := make([]map[string]string, 0, len(groups))
		for k, g := range groups {
			name := k
			if g != nil && g.Name != "" {
				name = g.Name
			}
			groupList = append(groupList, map[string]string{"key": k, "name": name})
		}
		sort.Slice(groupList, func(i, j int) bool { return groupList[i]["name"] < groupList[j]["name"] })
		writeJSON(w, http.StatusOK, map[string]any{"topics": result, "groups": groupList})

	case http.MethodPost, http.MethodPut:
		var body weiboSuperCountTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		if oid == "" || !oidPattern.MatchString(oid) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效超话 OID（100808…）或完整微博超话链接"})
			return
		}
		// group input prefers display name field
		groupInput := strings.TrimSpace(body.GroupName)
		if groupInput == "" {
			groupInput = strings.TrimSpace(body.GroupKey)
		}
		groups, gk := resolveGroupKeyForWrite(groups, groupInput)

		item := topics[oid]
		if item == nil {
			// also try bare key variants
			for k, v := range topics {
				if normalizeSuperOID(k) == oid || (v != nil && normalizeSuperOID(v.OID) == oid) {
					item = v
					// rekey to normalized oid later
					delete(topics, k)
					break
				}
			}
		}
		if item == nil {
			item = &weiboSuperCountTopicStored{OID: oid}
		}
		item.OID = oid
		if name := strings.TrimSpace(body.Name); name != "" {
			item.Name = name
		}
		if gk != "" {
			item.GroupName = gk
		}
		if body.ReportSign >= 0 {
			item.ReportSign = body.ReportSign
		}
		topics[oid] = item
		if err := s.writeConfigAndReloadBot(map[string]any{
			"WEIBO_SUPER_COUNT_ENABLED": true,
			"WEIBO_SUPER_COUNT_TOPICS":  topics,
			"WEIBO_SUPER_COUNT_GROUPS":  groups,
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话日报项已保存", "oid": oid})

	case http.MethodDelete:
		var body weiboSuperCountTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		delete(topics, oid)
		delete(topics, strings.TrimSpace(body.OID))
		for k := range topics {
			if normalizeSuperOID(k) == oid {
				delete(topics, k)
			}
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUPER_COUNT_TOPICS": topics}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话日报项已删除"})

	default:
		methodNotAllowed(w)
	}
}

// --- 超话自动签到 topics (WEIBO_SUPER_TOPICS, global bucket group 0) ---

type weiboSuperSignTopicPanel struct {
	OID            string `json:"oid"`
	Name           string `json:"name,omitempty"`
	LastSignDate   string `json:"lastSignDate,omitempty"`
	LastSignStatus string `json:"lastSignStatus,omitempty"`
	LastSignRank   int    `json:"lastSignRank,omitempty"`
}

type weiboSuperSignTopicStored struct {
	OID            string `json:"oid"`
	Name           string `json:"name,omitempty"`
	LastSignDate   string `json:"last_sign_date,omitempty"`
	LastSignStatus string `json:"last_sign_status,omitempty"`
	LastSignRank   int    `json:"last_sign_rank,omitempty"`
}

func (s *Server) handleWeiboSuperSignTopics(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	// GroupID string -> oid -> topic  (JSON often uses string keys)
	subs := map[string]map[string]*weiboSuperSignTopicStored{}
	if encoded := raw["WEIBO_SUPER_TOPICS"]; len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &subs); err != nil {
			var asInt map[int64]map[string]*weiboSuperSignTopicStored
			if err2 := json.Unmarshal(encoded, &asInt); err2 == nil {
				for gid, g := range asInt {
					subs[strconv.FormatInt(gid, 10)] = g
				}
			}
		}
	}
	// ensure global "0" bucket
	if subs["0"] == nil {
		subs["0"] = map[string]*weiboSuperSignTopicStored{}
	}

	switch r.Method {
	case http.MethodGet:
		// flatten all groups but prefer showing global+others with note
		result := make([]weiboSuperSignTopicPanel, 0)
		seen := map[string]bool{}
		// prefer 0 first
		order := []string{"0"}
		for gk := range subs {
			if gk != "0" {
				order = append(order, gk)
			}
		}
		for _, gk := range order {
			for key, item := range subs[gk] {
				if item == nil {
					continue
				}
				oid := item.OID
				if oid == "" {
					oid = key
				}
				noid := normalizeSuperOID(oid)
				if seen[noid] {
					continue
				}
				seen[noid] = true
				result = append(result, weiboSuperSignTopicPanel{
					OID: oid, Name: item.Name, LastSignDate: item.LastSignDate,
					LastSignStatus: item.LastSignStatus, LastSignRank: item.LastSignRank,
				})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Name == result[j].Name {
				return result[i].OID < result[j].OID
			}
			return result[i].Name < result[j].Name
		})
		writeJSON(w, http.StatusOK, map[string]any{"topics": result})

	case http.MethodPost, http.MethodPut:
		var body weiboSuperSignTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		if oid == "" || !oidPattern.MatchString(oid) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效超话 OID（100808…）或完整微博超话链接"})
			return
		}
		// remove old oid variants from all groups, keep last_* if same oid
		var preserved *weiboSuperSignTopicStored
		for gk, g := range subs {
			for k, item := range g {
				if item == nil {
					continue
				}
				cur := item.OID
				if cur == "" {
					cur = k
				}
				if normalizeSuperOID(cur) == oid || k == body.OID {
					if preserved == nil {
						cp := *item
						preserved = &cp
					}
					delete(g, k)
				}
			}
			if len(g) == 0 && gk != "0" {
				delete(subs, gk)
			}
		}
		if subs["0"] == nil {
			subs["0"] = map[string]*weiboSuperSignTopicStored{}
		}
		item := &weiboSuperSignTopicStored{OID: oid}
		if preserved != nil {
			*item = *preserved
			item.OID = oid
		}
		if n := strings.TrimSpace(body.Name); n != "" {
			item.Name = n
		}
		subs["0"][oid] = item
		if err := s.writeConfigAndReloadBot(map[string]any{
			"WEIBO_SUPER_AUTO_ENABLED": true,
			"WEIBO_SUPER_TOPICS":       subs,
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话签到项已保存", "oid": oid})

	case http.MethodDelete:
		var body weiboSuperSignTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		for gk, g := range subs {
			for k, item := range g {
				cur := k
				if item != nil && item.OID != "" {
					cur = item.OID
				}
				if normalizeSuperOID(cur) == oid || k == strings.TrimSpace(body.OID) {
					delete(g, k)
				}
			}
			if len(g) == 0 && gk != "0" {
				delete(subs, gk)
			}
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUPER_TOPICS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话签到项已删除"})

	default:
		methodNotAllowed(w)
	}
}

// --- Unified super-topic management (sign-in + daily report) ---

type weiboUnifiedSuperTopicPanel struct {
	OID            string `json:"oid"`
	OldOID         string `json:"oldOid,omitempty"`
	Name           string `json:"name,omitempty"`
	SignEnabled    bool   `json:"signEnabled"`
	ReportEnabled  bool   `json:"reportEnabled"`
	GroupKey       string `json:"groupKey,omitempty"`
	GroupName      string `json:"groupName,omitempty"`
	ReportSign     int    `json:"reportSign,omitempty"`
	LastSignDate   string `json:"lastSignDate,omitempty"`
	LastSignStatus string `json:"lastSignStatus,omitempty"`
	LastSignRank   int    `json:"lastSignRank,omitempty"`
}

func loadSuperSignSubscriptions(raw map[string]json.RawMessage) map[string]map[string]*weiboSuperSignTopicStored {
	subs := map[string]map[string]*weiboSuperSignTopicStored{}
	if encoded := raw["WEIBO_SUPER_TOPICS"]; len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &subs); err != nil {
			var asInt map[int64]map[string]*weiboSuperSignTopicStored
			if err2 := json.Unmarshal(encoded, &asInt); err2 == nil {
				for gid, group := range asInt {
					subs[strconv.FormatInt(gid, 10)] = group
				}
			}
		}
	}
	if subs["0"] == nil {
		subs["0"] = map[string]*weiboSuperSignTopicStored{}
	}
	return subs
}

func removeSuperSignTopic(subs map[string]map[string]*weiboSuperSignTopicStored, oid string) *weiboSuperSignTopicStored {
	var preserved *weiboSuperSignTopicStored
	for groupKey, group := range subs {
		for key, item := range group {
			candidate := key
			if item != nil && strings.TrimSpace(item.OID) != "" {
				candidate = item.OID
			}
			if normalizeSuperOID(candidate) != oid {
				continue
			}
			if item != nil && preserved == nil {
				copy := *item
				preserved = &copy
			}
			delete(group, key)
		}
		if len(group) == 0 && groupKey != "0" {
			delete(subs, groupKey)
		}
	}
	return preserved
}

func removeSuperCountTopic(topics map[string]*weiboSuperCountTopicStored, oid string) *weiboSuperCountTopicStored {
	var preserved *weiboSuperCountTopicStored
	for key, item := range topics {
		candidate := key
		if item != nil && strings.TrimSpace(item.OID) != "" {
			candidate = item.OID
		}
		if normalizeSuperOID(candidate) != oid {
			continue
		}
		if item != nil && preserved == nil {
			copy := *item
			preserved = &copy
		}
		delete(topics, key)
	}
	return preserved
}

func (s *Server) handleWeiboUnifiedSuperTopics(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	signSubs := loadSuperSignSubscriptions(raw)
	countTopics := map[string]*weiboSuperCountTopicStored{}
	if encoded := raw["WEIBO_SUPER_COUNT_TOPICS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &countTopics)
	}
	groups := loadCountGroups(raw)

	switch r.Method {
	case http.MethodGet:
		byOID := map[string]*weiboUnifiedSuperTopicPanel{}
		for _, group := range signSubs {
			for key, item := range group {
				if item == nil {
					continue
				}
				oid := item.OID
				if oid == "" {
					oid = key
				}
				oid = normalizeSuperOID(oid)
				if oid == "" {
					continue
				}
				panel := byOID[oid]
				if panel == nil {
					panel = &weiboUnifiedSuperTopicPanel{OID: oid}
					byOID[oid] = panel
				}
				panel.SignEnabled = true
				if panel.Name == "" {
					panel.Name = item.Name
				}
				panel.LastSignDate = item.LastSignDate
				panel.LastSignStatus = item.LastSignStatus
				panel.LastSignRank = item.LastSignRank
			}
		}
		for key, item := range countTopics {
			if item == nil {
				continue
			}
			oid := item.OID
			if oid == "" {
				oid = key
			}
			oid = normalizeSuperOID(oid)
			if oid == "" {
				continue
			}
			panel := byOID[oid]
			if panel == nil {
				panel = &weiboUnifiedSuperTopicPanel{OID: oid}
				byOID[oid] = panel
			}
			panel.ReportEnabled = true
			if panel.Name == "" {
				panel.Name = item.Name
			}
			panel.GroupKey, panel.GroupName = resolveCountGroupDisplay(groups, item.GroupName)
			panel.ReportSign = item.ReportSign
		}
		result := make([]weiboUnifiedSuperTopicPanel, 0, len(byOID))
		for _, item := range byOID {
			result = append(result, *item)
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupName == result[j].GroupName {
				if result[i].Name == result[j].Name {
					return result[i].OID < result[j].OID
				}
				return result[i].Name < result[j].Name
			}
			return result[i].GroupName < result[j].GroupName
		})
		groupList := make([]map[string]string, 0, len(groups))
		for key, item := range groups {
			name := key
			if item != nil && strings.TrimSpace(item.Name) != "" {
				name = item.Name
			}
			groupList = append(groupList, map[string]string{"key": key, "name": name})
		}
		sort.Slice(groupList, func(i, j int) bool { return groupList[i]["name"] < groupList[j]["name"] })
		writeJSON(w, http.StatusOK, map[string]any{"topics": result, "groups": groupList})

	case http.MethodPost, http.MethodPut:
		var body weiboUnifiedSuperTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		if oid == "" || !oidPattern.MatchString(oid) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效超话 OID（100808…）或完整微博超话链接"})
			return
		}
		if r.Method == http.MethodPut {
			oldOID := normalizeSuperOID(body.OldOID)
			if oldOID == "" {
				oldOID = oid
			}
			if oldOID != oid {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "超话 OID 是身份标识，不能在编辑时修改；请删除后重新添加"})
				return
			}
		}
		if !body.SignEnabled && !body.ReportEnabled {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "自动签到和超话日报至少启用一项"})
			return
		}
		preservedSign := removeSuperSignTopic(signSubs, oid)
		preservedCount := removeSuperCountTopic(countTopics, oid)
		name := strings.TrimSpace(body.Name)
		if name == "" && preservedSign != nil {
			name = preservedSign.Name
		}
		if name == "" && preservedCount != nil {
			name = preservedCount.Name
		}
		if body.SignEnabled {
			item := &weiboSuperSignTopicStored{OID: oid}
			if preservedSign != nil {
				*item = *preservedSign
			}
			item.OID = oid
			item.Name = name
			signSubs["0"][oid] = item
		}
		if body.ReportEnabled {
			item := &weiboSuperCountTopicStored{OID: oid}
			if preservedCount != nil {
				*item = *preservedCount
			}
			item.OID = oid
			item.Name = name
			groupInput := strings.TrimSpace(body.GroupName)
			if groupInput == "" {
				groupInput = strings.TrimSpace(body.GroupKey)
			}
			if groupInput != "" {
				var groupKey string
				groups, groupKey = resolveGroupKeyForWrite(groups, groupInput)
				item.GroupName = groupKey
			}
			countTopics[oid] = item
		}
		if err := s.writeConfigAndReloadBot(map[string]any{
			"WEIBO_SUPER_TOPICS":       signSubs,
			"WEIBO_SUPER_COUNT_TOPICS": countTopics,
			"WEIBO_SUPER_COUNT_GROUPS": groups,
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话订阅已更新并热重载", "oid": oid})

	case http.MethodDelete:
		var body weiboUnifiedSuperTopicPanel
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oid := normalizeSuperOID(body.OID)
		if oid == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写超话 OID"})
			return
		}
		removeSuperSignTopic(signSubs, oid)
		removeSuperCountTopic(countTopics, oid)
		if err := s.writeConfigAndReloadBot(map[string]any{
			"WEIBO_SUPER_TOPICS":       signSubs,
			"WEIBO_SUPER_COUNT_TOPICS": countTopics,
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "超话订阅已删除并热重载"})

	default:
		methodNotAllowed(w)
	}
}

// --- Weibo nickname enrichment ---

var weiboHTTPClient = &http.Client{Timeout: 8 * time.Second}

func fetchWeiboScreenName(uid, cookie string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	u := fmt.Sprintf("https://m.weibo.cn/api/container/getIndex?type=uid&value=%s&containerid=100505%s", url.QueryEscape(uid), url.QueryEscape(uid))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")
	req.Header.Set("Referer", "https://m.weibo.cn/u/"+uid)
	if strings.TrimSpace(cookie) != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := weiboHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed struct {
		OK   int `json:"ok"`
		Data struct {
			UserInfo struct {
				ScreenName string `json:"screen_name"`
			} `json:"userInfo"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.OK != 1 {
		return ""
	}
	return strings.TrimSpace(parsed.Data.UserInfo.ScreenName)
}

func weiboCookieFromRaw(raw map[string]json.RawMessage) string {
	// prefer m.weibo cookie for m.weibo.cn API
	for _, key := range []string{"WEIBO_MWEIBO_COOKIE", "WEIBO_COOKIE"} {
		if encoded := raw[key]; len(encoded) > 0 {
			var s string
			if json.Unmarshal(encoded, &s) == nil && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}
