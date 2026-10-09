package admin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/logic"
	"pocket48-bot/internal/pocket48"
)

// --- 口袋房间订阅 ---

type pocketRoomSub struct {
	GroupID     int64    `json:"groupId"`
	TargetIDs   []string `json:"targetIds,omitempty"`
	RoomID      int64    `json:"roomId"`
	Name        string   `json:"name,omitempty"`
	VisitNotify bool     `json:"visitNotify"`
	KeepHistory bool     `json:"keepHistory"`
	// Edit: move room/group. Old* used by PUT.
	OldGroupID int64 `json:"oldGroupId,omitempty"`
	OldRoomID  int64 `json:"oldRoomId,omitempty"`
}

func (s *Server) handlePocketRoomSubscriptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	subs := map[string][]int64{}
	if encoded := raw["GROUP_SUBSCRIPTIONS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &subs)
	}
	features := map[string]*config.PocketMemberFeatureConfig{}
	if encoded := raw["POCKET_MEMBER_FEATURES"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &features)
	}
	switch r.Method {
	case http.MethodGet:
		result := make([]pocketRoomSub, 0)
		var client *pocket48.Client
		if cfg, err := config.LoadConfig(s.opts.ConfigPath); err == nil && strings.TrimSpace(cfg.PocketToken) != "" {
			client = pocket48.NewClient(cfg)
		}
		// Aggregate by room id so a room fanning out to several targets (QQ +
		// Feishu, groups + private) shows as one entry with a target list.
		byRoom := map[int64]*pocketRoomSub{}
		order := make([]int64, 0)
		for targetText, rooms := range subs {
			targetID := targetText
			gid, _ := strconv.ParseInt(targetText, 10, 64)
			if strings.Contains(targetText, ":") {
				gid = qqGroupFromTargetID(targetText)
			}
			for _, roomID := range rooms {
				item := byRoom[roomID]
				if item == nil {
					item = &pocketRoomSub{RoomID: roomID, GroupID: gid}
					if client != nil {
						item.Name = enrichPocketRoomName(client, roomID)
					}
					byRoom[roomID] = item
					order = append(order, roomID)
				}
				if targetID != "" && !stringInSlice(targetID, item.TargetIDs) {
					item.TargetIDs = append(item.TargetIDs, targetID)
				}
			}
		}
		for _, roomID := range order {
			item := byRoom[roomID]
			feature := features[strconv.FormatInt(roomID, 10)]
			if feature != nil {
				item.VisitNotify = feature.VisitNotify
				item.KeepHistory = feature.KeepHistory
			}
			result = append(result, *item)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].RoomID < result[j].RoomID })
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body pocketRoomSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		if body.RoomID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效口袋房间 ID"})
			return
		}
		targets := pocketTargetIDs(body.TargetIDs, body.GroupID)
		if len(targets) == 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择至少一个投递目标"})
			return
		}
		for _, tk := range targets {
			already := false
			for _, id := range subs[tk] {
				if id == body.RoomID {
					already = true
					break
				}
			}
			if !already {
				subs[tk] = append(subs[tk], body.RoomID)
			}
		}
		featureKey := strconv.FormatInt(body.RoomID, 10)
		features[featureKey] = &config.PocketMemberFeatureConfig{VisitNotify: body.VisitNotify, KeepHistory: body.KeepHistory}
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs, "POCKET_MEMBER_FEATURES": features}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "房间已添加"})
	case http.MethodPut:
		var body pocketRoomSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oldR := body.OldRoomID
		if oldR <= 0 {
			oldR = body.RoomID
		}
		if body.RoomID <= 0 || oldR <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效的房间 ID"})
			return
		}
		targets := pocketTargetIDs(body.TargetIDs, body.GroupID)
		if len(targets) == 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择至少一个投递目标"})
			return
		}
		// Remove the room from every target, then re-add it to the chosen set.
		for tk, rooms := range subs {
			next := rooms[:0]
			for _, id := range rooms {
				if id != oldR {
					next = append(next, id)
				}
			}
			if len(next) == 0 {
				delete(subs, tk)
			} else {
				subs[tk] = next
			}
		}
		for _, tk := range targets {
			exists := false
			for _, id := range subs[tk] {
				if id == body.RoomID {
					exists = true
					break
				}
			}
			if !exists {
				subs[tk] = append(subs[tk], body.RoomID)
			}
		}
		oldFeatureKey := strconv.FormatInt(oldR, 10)
		newFeatureKey := strconv.FormatInt(body.RoomID, 10)
		if oldFeatureKey != newFeatureKey {
			delete(features, oldFeatureKey)
		}
		features[newFeatureKey] = &config.PocketMemberFeatureConfig{VisitNotify: body.VisitNotify, KeepHistory: body.KeepHistory}
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs, "POCKET_MEMBER_FEATURES": features}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "房间订阅已更新"})
	case http.MethodDelete:
		var body pocketRoomSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		if body.RoomID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效房间 ID"})
			return
		}
		// Remove the room from every target it is fanned out to.
		for tk, rooms := range subs {
			next := rooms[:0]
			for _, id := range rooms {
				if id != body.RoomID {
					next = append(next, id)
				}
			}
			if len(next) == 0 {
				delete(subs, tk)
			} else {
				subs[tk] = next
			}
		}
		delete(features, strconv.FormatInt(body.RoomID, 10))
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs, "POCKET_MEMBER_FEATURES": features}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "房间已删除"})
	default:
		methodNotAllowed(w)
	}
}

// --- 微博 UID 订阅 ---

type weiboPanelSub struct {
	GroupID    int64    `json:"groupId"`
	TargetIDs  []string `json:"targetIds,omitempty"`
	UID        string   `json:"uid"`
	Name       string   `json:"name,omitempty"`
	AtAll      bool     `json:"atAll"`
	LastID     string   `json:"lastId,omitempty"`
	OldGroupID int64    `json:"oldGroupId,omitempty"`
	OldUID     string   `json:"oldUid,omitempty"`
}

type weiboStoredSub struct {
	UID       string   `json:"uid"`
	TargetIDs []string `json:"targetIds,omitempty"`
	Name      string   `json:"name,omitempty"`
	AtAll     bool     `json:"at_all"`
	LastID    string   `json:"last_id,omitempty"`
}

var weiboUIDPattern = regexp.MustCompile(`^\d{5,20}$`)
var weiboUIDContainerPattern = regexp.MustCompile(`^(?:100505|107603)(\d{5,20})(?:\D.*)?$`)

// normalizeWeiboUID accepts the legacy numeric UID as well as common desktop
// and mobile Weibo profile/post links whose path or query contains the UID.
func normalizeWeiboUID(raw string) string {
	raw = strings.TrimSpace(raw)
	if weiboUIDPattern.MatchString(raw) {
		return raw
	}
	// A copied link can be surrounded by explanatory text. Pull out the URL so
	// url.Parse still sees the correct host and path.
	if start := strings.Index(raw, "http://"); start >= 0 {
		raw = strings.Fields(raw[start:])[0]
	} else if start := strings.Index(raw, "https://"); start >= 0 {
		raw = strings.Fields(raw[start:])[0]
	} else if strings.HasPrefix(strings.ToLower(raw), "weibo.com/") ||
		strings.HasPrefix(strings.ToLower(raw), "www.weibo.com/") ||
		strings.HasPrefix(strings.ToLower(raw), "m.weibo.cn/") ||
		strings.HasPrefix(strings.ToLower(raw), "weibo.cn/") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(strings.TrimRight(raw, "，。；;、)]}＞>"))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host != "weibo.com" && host != "weibo.cn" && !strings.HasSuffix(host, ".weibo.com") && !strings.HasSuffix(host, ".weibo.cn") {
		return ""
	}
	query := parsed.Query()
	for _, key := range []string{"uid", "value"} {
		if candidate := strings.TrimSpace(query.Get(key)); weiboUIDPattern.MatchString(candidate) {
			return candidate
		}
	}
	if containerID := strings.TrimSpace(query.Get("containerid")); containerID != "" {
		if match := weiboUIDContainerPattern.FindStringSubmatch(containerID); len(match) == 2 {
			return match[1]
		}
	}
	parts := strings.FieldsFunc(parsed.EscapedPath(), func(r rune) bool { return r == '/' })
	for index, part := range parts {
		candidate, unescapeErr := url.PathUnescape(part)
		if unescapeErr != nil {
			continue
		}
		// Older desktop profile links use /p/100505{uid}/home.
		if match := weiboUIDContainerPattern.FindStringSubmatch(candidate); len(match) == 2 {
			return match[1]
		}
		if !weiboUIDPattern.MatchString(candidate) {
			continue
		}
		// /u/{uid}, /profile/{uid}, and desktop /{uid}/{post-id} links.
		if index == 0 || (index > 0 && (parts[index-1] == "u" || parts[index-1] == "profile")) {
			return candidate
		}
	}
	return ""
}

func (s *Server) handleWeiboSubscriptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	// groupID string -> uid -> cfg
	subs := map[string]map[string]*weiboStoredSub{}
	if encoded := raw["WEIBO_SUBSCRIPTIONS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &subs)
	}
	switch r.Method {
	case http.MethodGet:
		result := make([]weiboPanelSub, 0)
		cookie := weiboCookieFromRaw(raw)
		nameDirty := false
		for groupText, group := range subs {
			gid, _ := strconv.ParseInt(groupText, 10, 64)
			for uid, item := range group {
				if item == nil {
					continue
				}
				id := item.UID
				if id == "" {
					id = uid
				}
				name := strings.TrimSpace(item.Name)
				if name == "" && cookie != "" {
					if fetched := fetchWeiboScreenName(id, cookie); fetched != "" {
						name = fetched
						item.Name = fetched
						nameDirty = true
					}
				}
				result = append(result, weiboPanelSub{GroupID: gid, TargetIDs: item.TargetIDs, UID: id, Name: name, AtAll: item.AtAll, LastID: item.LastID})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupID == result[j].GroupID {
				return result[i].UID < result[j].UID
			}
			return result[i].GroupID < result[j].GroupID
		})
		if nameDirty {
			// persist discovered nicknames without restart spam: write only subscriptions
			_ = s.writeConfigNoRestart(map[string]any{"WEIBO_SUBSCRIPTIONS": subs})
		}
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body weiboPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		body.UID = normalizeWeiboUID(body.UID)
		if body.GroupID <= 0 {
			if ids := qqGroupIDsFromTargets(body.TargetIDs); len(ids) > 0 {
				body.GroupID = ids[0]
			}
		}
		if body.GroupID <= 0 || body.UID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择投递目标（至少一个 QQ 群），以及微博 UID 或包含 UID 的完整微博链接"})
			return
		}
		gk := strconv.FormatInt(body.GroupID, 10)
		if subs[gk] == nil {
			subs[gk] = map[string]*weiboStoredSub{}
		}
		item := subs[gk][body.UID]
		if item == nil {
			item = &weiboStoredSub{UID: body.UID}
		}
		item.UID = body.UID
		item.AtAll = body.AtAll
		item.TargetIDs = body.TargetIDs
		if n := strings.TrimSpace(body.Name); n != "" {
			item.Name = n
		}
		subs[gk][body.UID] = item
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "微博订阅已保存"})
	case http.MethodPut:
		var body weiboPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		body.UID = normalizeWeiboUID(body.UID)
		oldUID := strings.TrimSpace(body.OldUID)
		if oldUID == "" {
			oldUID = body.UID
		}
		if body.UID != "" && body.UID != oldUID {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "微博 UID 是账号身份，不能在编辑时修改；请删除后重新添加"})
			return
		}
		oldG := body.OldGroupID
		// If oldUID is set and oldG is 0, it's legit (group 0 items exist, e.g. from QQ commands without specifying group).
		// Only use new GroupID as old when oldG wasn't literally sent (both 0 and missing).
		oldGSpecified := oldUID != "" && (body.OldGroupID > 0 || body.OldGroupID == 0)
		if oldUID == "" {
			oldUID = body.UID
		}
		if !oldGSpecified {
			oldG = body.GroupID
		}
		if body.GroupID <= 0 {
			if ids := qqGroupIDsFromTargets(body.TargetIDs); len(ids) > 0 {
				body.GroupID = ids[0]
			}
		}
		if body.GroupID <= 0 || body.UID == "" || oldUID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择投递目标（至少一个 QQ 群），以及微博 UID 或完整微博链接"})
			return
		}
		ogk := strconv.FormatInt(oldG, 10)
		var preservedLast string
		var preservedName string
		if g := subs[ogk]; g != nil {
			if old := g[oldUID]; old != nil {
				preservedLast = old.LastID
				preservedName = old.Name
			}
			delete(g, oldUID)
			if len(g) == 0 {
				delete(subs, ogk)
			}
		}
		ngk := strconv.FormatInt(body.GroupID, 10)
		if subs[ngk] == nil {
			subs[ngk] = map[string]*weiboStoredSub{}
		}
		item := subs[ngk][body.UID]
		if item == nil {
			item = &weiboStoredSub{UID: body.UID, LastID: preservedLast, Name: preservedName}
		}
		item.UID = body.UID
		item.AtAll = body.AtAll
		item.TargetIDs = body.TargetIDs
		if n := strings.TrimSpace(body.Name); n != "" {
			item.Name = n
		} else if item.Name == "" {
			item.Name = preservedName
		}
		if item.LastID == "" {
			item.LastID = preservedLast
		}
		subs[ngk][body.UID] = item
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "微博订阅已更新"})
	case http.MethodDelete:
		var body weiboPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		gk := strconv.FormatInt(body.GroupID, 10)
		if group := subs[gk]; group != nil {
			delete(group, strings.TrimSpace(body.UID))
			if len(group) == 0 {
				delete(subs, gk)
			}
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"WEIBO_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "微博订阅已删除"})
	default:
		methodNotAllowed(w)
	}
}

// --- 抖音创作者订阅 ---

type douyinPanelSub struct {
	GroupID      int64
	TargetIDs    []string `json:"targetIds,omitempty"`
	GroupIDs     []int64  `json:"groupIds,omitempty"`
	SecUserID    string   `json:"secUserId,omitempty"`
	ProfileURL   string   `json:"profileUrl,omitempty"`
	Target       string   `json:"target,omitempty"`
	Name         string   `json:"name,omitempty"`
	AtAll        bool     `json:"atAll"`
	LiveID       string   `json:"liveId,omitempty"`
	OldGroupID   int64    `json:"oldGroupId,omitempty"`
	OldGroupIDs  []int64  `json:"oldGroupIds,omitempty"`
	OldSec       string   `json:"oldSecUserId,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
	WorksEnabled *bool    `json:"worksEnabled,omitempty"`
	LiveEnabled  *bool    `json:"liveEnabled,omitempty"`
	Source       string   `json:"source,omitempty"`
	Status       string   `json:"status,omitempty"`
}

type douyinStoredSub struct {
	TargetIDs     []string `json:"targetIds,omitempty"`
	SecUserID     string   `json:"sec_user_id"`
	ProfileURL    string   `json:"profile_url,omitempty"`
	Name          string   `json:"name,omitempty"`
	NameManual    bool     `json:"name_manual,omitempty"`
	AtAll         bool     `json:"at_all"`
	LastAwemeID   string   `json:"last_aweme_id,omitempty"`
	LastAwemeTime int64    `json:"last_aweme_time,omitempty"`
	LiveID        string   `json:"live_id,omitempty"`
	Auto          bool     `json:"auto,omitempty"`
	Disabled      bool     `json:"disabled,omitempty"`
	WorksDisabled bool     `json:"works_disabled,omitempty"`
	LiveDisabled  bool     `json:"live_disabled,omitempty"`
}

func loadDouyinSubs(encoded json.RawMessage) map[string]map[string]*douyinStoredSub {
	subs := map[string]map[string]*douyinStoredSub{}
	if len(encoded) == 0 {
		return subs
	}
	if err := json.Unmarshal(encoded, &subs); err != nil {
		var asInt map[int64]map[string]*douyinStoredSub
		if err2 := json.Unmarshal(encoded, &asInt); err2 == nil {
			for gid, g := range asInt {
				subs[strconv.FormatInt(gid, 10)] = g
			}
		}
	}
	return subs
}

func (s *Server) handleDouyinSubscriptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	subs := loadDouyinSubs(raw["DOUYIN_SUBSCRIPTIONS"])
	switch r.Method {
	case http.MethodGet:
		cachedNames := loadDouyinCachedNames(raw, s.opts.ConfigPath)
		byCreator := make(map[string]*douyinPanelSub)
		for groupText, group := range subs {
			gid, _ := strconv.ParseInt(groupText, 10, 64)
			for key, item := range group {
				if item == nil {
					continue
				}
				id := item.SecUserID
				if id == "" {
					id = key
				}
				name := item.Name
				if strings.TrimSpace(name) == "" {
					name = cachedNames[id]
				}
				panel := byCreator[id]
				if panel == nil {
					panel = &douyinPanelSub{
						GroupID: gid, SecUserID: id, ProfileURL: item.ProfileURL,
						Name: name, AtAll: item.AtAll, LiveID: item.LiveID,
						TargetIDs: item.TargetIDs,
						Enabled:   boolPointer(!item.Disabled), WorksEnabled: boolPointer(!item.WorksDisabled), LiveEnabled: boolPointer(!item.LiveDisabled),
						Source: "config", Status: douyinSubscriptionStatus(s.opts.ConfigPath, item),
					}
					byCreator[id] = panel
				} else {
					panel.AtAll = panel.AtAll || item.AtAll
					*panel.Enabled = *panel.Enabled && !item.Disabled
					*panel.WorksEnabled = *panel.WorksEnabled && !item.WorksDisabled
					*panel.LiveEnabled = *panel.LiveEnabled && !item.LiveDisabled
					if panel.Name == "" {
						panel.Name = name
					}
					if panel.LiveID == "" {
						panel.LiveID = item.LiveID
					}
					panel.TargetIDs = mergeUniqueStrings(panel.TargetIDs, item.TargetIDs)
				}
				panel.GroupIDs = append(panel.GroupIDs, gid)
			}
		}
		result := make([]douyinPanelSub, 0, len(byCreator))
		for _, panel := range byCreator {
			sort.Slice(panel.GroupIDs, func(i, j int) bool { return panel.GroupIDs[i] < panel.GroupIDs[j] })
			panel.GroupID = panel.GroupIDs[0]
			result = append(result, *panel)
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Name == result[j].Name {
				return result[i].SecUserID < result[j].SecUserID
			}
			return result[i].Name < result[j].Name
		})
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body douyinPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		resolveInput := strings.TrimSpace(body.SecUserID)
		if resolveInput == "" {
			resolveInput = strings.TrimSpace(body.ProfileURL)
		}
		if resolveInput == "" {
			resolveInput = strings.TrimSpace(body.Target)
		}
		groupIDs := normalizeDouyinGroupIDs(body.GroupIDs, body.GroupID)
		if len(groupIDs) == 0 {
			groupIDs = qqGroupIDsFromTargets(body.TargetIDs)
		}
		if len(groupIDs) == 0 || resolveInput == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择投递目标（至少一个 QQ 群），以及抖音主页链接或 sec_user_id"})
			return
		}
		sec, profile, err := logic.ResolveDouyinTarget(r.Context(), resolveInput)
		if err != nil || sec == "" {
			msg := "无法解析抖音目标"
			if err != nil {
				msg = err.Error()
			}
			writeJSON(w, http.StatusBadRequest, apiError{Error: msg})
			return
		}
		for _, groupID := range groupIDs {
			gk := strconv.FormatInt(groupID, 10)
			if subs[gk] == nil {
				subs[gk] = map[string]*douyinStoredSub{}
			}
			item := subs[gk][sec]
			if item == nil {
				item = &douyinStoredSub{SecUserID: sec}
			}
			item.SecUserID = sec
			item.TargetIDs = body.TargetIDs
			item.ProfileURL = profile
			item.AtAll = body.AtAll
			item.Auto = false
			if body.Enabled != nil {
				item.Disabled = !*body.Enabled
			}
			if body.WorksEnabled != nil {
				item.WorksDisabled = !*body.WorksEnabled
			}
			if body.LiveEnabled != nil {
				item.LiveDisabled = !*body.LiveEnabled
			}
			if n := strings.TrimSpace(body.Name); n != "" {
				item.Name = n
				item.NameManual = true
			}
			subs[gk][sec] = item
		}
		if saveErr := s.writeConfigAndReloadBot(map[string]any{"DOUYIN_SUBSCRIPTIONS": subs}); saveErr != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: saveErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "抖音订阅已保存并热重载", "secUserId": sec, "profileUrl": profile})
	case http.MethodPut:
		var body douyinPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		oldSec := strings.TrimSpace(body.OldSec)
		if oldSec == "" {
			oldSec = strings.TrimSpace(body.SecUserID)
		}
		groupIDs := normalizeDouyinGroupIDs(body.GroupIDs, body.GroupID)
		if len(groupIDs) == 0 {
			groupIDs = qqGroupIDsFromTargets(body.TargetIDs)
		}
		oldGroupIDs := normalizeDouyinGroupIDs(body.OldGroupIDs, body.OldGroupID)
		if len(oldGroupIDs) == 0 {
			oldGroupIDs = douyinGroupsForCreator(subs, oldSec)
		}
		if len(groupIDs) == 0 || oldSec == "" || len(oldGroupIDs) == 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请选择投递目标（至少一个 QQ 群）与 sec_user_id"})
			return
		}
		if candidate := strings.TrimSpace(body.SecUserID); candidate != "" && candidate != oldSec {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "User ID 是创作者身份，不能在编辑时修改；请删除后重新添加"})
			return
		}
		var preserved *douyinStoredSub
		for _, oldGroupID := range oldGroupIDs {
			ogk := strconv.FormatInt(oldGroupID, 10)
			if g := subs[ogk]; g != nil {
				if old := g[oldSec]; old != nil && preserved == nil {
					cp := *old
					preserved = &cp
				}
				delete(g, oldSec)
				if len(g) == 0 {
					delete(subs, ogk)
				}
			}
		}
		if preserved == nil {
			writeJSON(w, http.StatusNotFound, apiError{Error: "未找到要编辑的抖音创作者订阅"})
			return
		}
		for _, groupID := range groupIDs {
			ngk := strconv.FormatInt(groupID, 10)
			if subs[ngk] == nil {
				subs[ngk] = map[string]*douyinStoredSub{}
			}
			item := *preserved
			item.SecUserID = oldSec
			item.AtAll = body.AtAll
			item.TargetIDs = body.TargetIDs
			if body.Enabled != nil {
				item.Disabled = !*body.Enabled
			}
			if body.WorksEnabled != nil {
				item.WorksDisabled = !*body.WorksEnabled
			}
			if body.LiveEnabled != nil {
				item.LiveDisabled = !*body.LiveEnabled
			}
			if n := strings.TrimSpace(body.Name); n != "" {
				item.Name = n
				item.NameManual = true
			}
			subs[ngk][oldSec] = &item
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"DOUYIN_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "抖音订阅已更新并热重载"})
	case http.MethodDelete:
		var body douyinPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		sec := strings.TrimSpace(body.SecUserID)
		groupIDs := normalizeDouyinGroupIDs(body.GroupIDs, body.GroupID)
		if len(groupIDs) == 0 {
			groupIDs = douyinGroupsForCreator(subs, sec)
		}
		for _, groupID := range groupIDs {
			gk := strconv.FormatInt(groupID, 10)
			if group := subs[gk]; group != nil {
				delete(group, sec)
				if len(group) == 0 {
					delete(subs, gk)
				}
			}
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"DOUYIN_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "抖音订阅已删除并热重载"})
	default:
		methodNotAllowed(w)
	}
}

func normalizeDouyinGroupIDs(groupIDs []int64, fallback int64) []int64 {
	seen := make(map[int64]struct{}, len(groupIDs)+1)
	result := make([]int64, 0, len(groupIDs)+1)
	for _, groupID := range groupIDs {
		if groupID <= 0 {
			continue
		}
		if _, exists := seen[groupID]; exists {
			continue
		}
		seen[groupID] = struct{}{}
		result = append(result, groupID)
	}
	if len(result) == 0 && fallback > 0 {
		result = append(result, fallback)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func douyinGroupsForCreator(subs map[string]map[string]*douyinStoredSub, secUserID string) []int64 {
	result := make([]int64, 0)
	for groupText, group := range subs {
		if group[secUserID] == nil {
			continue
		}
		if groupID, err := strconv.ParseInt(groupText, 10, 64); err == nil && groupID > 0 {
			result = append(result, groupID)
		}
	}
	return normalizeDouyinGroupIDs(result, 0)
}

func loadDouyinCachedNames(raw map[string]json.RawMessage, configPath string) map[string]string {
	result := make(map[string]string)
	var profileDir string
	_ = json.Unmarshal(raw["BROWSER_PROFILE_DIR"], &profileDir)
	if strings.TrimSpace(profileDir) == "" {
		_ = json.Unmarshal(raw["WEIBO_BROWSER_PROFILE_DIR"], &profileDir)
	}
	if strings.TrimSpace(profileDir) == "" {
		profileDir = "storage/weibo-browser-profile"
	}
	paths := []string{filepath.Join(profileDir, "douyin-contact-cache.json")}
	if !filepath.IsAbs(profileDir) {
		paths = append(paths, filepath.Join(filepath.Dir(configPath), profileDir, "douyin-contact-cache.json"))
	}
	var payload struct {
		Contacts []struct {
			SecUID     string `json:"secUid"`
			Nickname   string `json:"nickname"`
			RemarkName string `json:"remarkName"`
		} `json:"contacts"`
	}
	for _, path := range paths {
		rawCache, err := os.ReadFile(path)
		if err == nil && json.Unmarshal(rawCache, &payload) == nil {
			break
		}
	}
	for _, contact := range payload.Contacts {
		name := strings.TrimSpace(contact.RemarkName)
		if name == "" {
			name = strings.TrimSpace(contact.Nickname)
		}
		if sec := strings.TrimSpace(contact.SecUID); sec != "" && name != "" {
			result[sec] = name
		}
	}
	return result
}

func boolPointer(value bool) *bool { return &value }

func douyinSubscriptionStatus(configPath string, item *douyinStoredSub) string {
	if item.Disabled {
		return "已停用"
	}
	if item.WorksDisabled && item.LiveDisabled {
		return "作品与直播监控均已关闭"
	}
	if item.LiveDisabled {
		return "仅监控作品"
	}
	if strings.TrimSpace(item.LiveID) != "" {
		dir := filepath.Join(filepath.Dir(configPath), "storage", "douyin-live-sessions")
		var state struct {
			LiveID        string    `json:"live_id"`
			Online        bool      `json:"online"`
			LastUpdatedAt time.Time `json:"last_updated_at"`
		}
		found := false
		if entries, err := os.ReadDir(dir); err == nil {
			for _, entry := range entries {
				if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				var candidate struct {
					LiveID        string    `json:"live_id"`
					Online        bool      `json:"online"`
					LastUpdatedAt time.Time `json:"last_updated_at"`
				}
				raw, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
				if readErr == nil && json.Unmarshal(raw, &candidate) == nil && candidate.LiveID == item.LiveID && (!found || candidate.LastUpdatedAt.After(state.LastUpdatedAt)) {
					state.LiveID, state.Online, state.LastUpdatedAt = candidate.LiveID, candidate.Online, candidate.LastUpdatedAt
					found = true
				}
			}
		}
		if found {
			when := ""
			if !state.LastUpdatedAt.IsZero() {
				when = " · 更新于 " + state.LastUpdatedAt.Local().Format("01-02 15:04")
			}
			if state.Online {
				return "直播监控中" + when
			}
			return "最近一场已结束" + when
		}
		return "已解析直播间，等待/正在监控"
	}
	if strings.TrimSpace(item.Name) != "" {
		return "主页已解析，等待直播间"
	}
	return "等待首次解析"
}

// mergeUniqueStrings appends b's items to a, skipping any that are already
// present, and returns the combined list. Used to fan a creator's targets in
// across multiple group subscriptions.
func mergeUniqueStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range a {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range b {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// qqGroupIDsFromTargets extracts the numeric QQ group ids embedded in explicit
// target ids ("qq:group:<id>"). It lets the douyin subscription handler derive
// its legacy group-key map from the fan-out targets chosen in the UI.
func qqGroupIDsFromTargets(targetIDs []string) []int64 {
	var ids []int64
	seen := make(map[int64]bool)
	for _, targetID := range targetIDs {
		targetID = strings.TrimSpace(targetID)
		if !strings.HasPrefix(targetID, "qq:group:") {
			continue
		}
		raw := strings.TrimPrefix(targetID, "qq:group:")
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil && id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// pocketTargetIDs returns the target ids a room subscription should fan out to.
// It prefers the explicit targetIds list and falls back to the legacy numeric
// group id for backward compatibility.
func pocketTargetIDs(targetIDs []string, groupID int64) []string {
	clean := make([]string, 0, len(targetIDs)+1)
	seen := make(map[string]bool)
	for _, id := range targetIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		clean = append(clean, id)
	}
	if len(clean) == 0 && groupID > 0 {
		clean = append(clean, strconv.FormatInt(groupID, 10))
	}
	return clean
}

// qqGroupFromTargetID extracts the numeric QQ group id from a "qq:group:<id>"
// target id, or returns 0 if it is not a QQ group target.
func qqGroupFromTargetID(targetID string) int64 {
	targetID = strings.TrimSpace(targetID)
	if !strings.HasPrefix(targetID, "qq:group:") {
		return 0
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(targetID, "qq:group:"), 10, 64)
	return id
}

func stringInSlice(needle string, haystack []string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
