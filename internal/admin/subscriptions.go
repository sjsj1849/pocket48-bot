package admin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pocket48-bot/internal/logic"
)

// --- 口袋房间订阅 ---

type pocketRoomSub struct {
	GroupID int64  `json:"groupId"`
	RoomID  int64  `json:"roomId"`
	Name    string `json:"name,omitempty"`
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
	switch r.Method {
	case http.MethodGet:
		result := make([]pocketRoomSub, 0)
		for groupText, rooms := range subs {
			gid, _ := strconv.ParseInt(groupText, 10, 64)
			for _, roomID := range rooms {
				result = append(result, pocketRoomSub{GroupID: gid, RoomID: roomID})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupID == result[j].GroupID {
				return result[i].RoomID < result[j].RoomID
			}
			return result[i].GroupID < result[j].GroupID
		})
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body pocketRoomSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		if body.GroupID <= 0 || body.RoomID <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效 QQ 群号和口袋房间 ID"})
			return
		}
		gk := strconv.FormatInt(body.GroupID, 10)
		for _, id := range subs[gk] {
			if id == body.RoomID {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "该房间已在监控列表中"})
				return
			}
		}
		subs[gk] = append(subs[gk], body.RoomID)
		if err := s.applyConfig(map[string]any{"GROUP_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "房间已添加，Bot 已重新启动"})
	case http.MethodDelete:
		var body pocketRoomSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		gk := strconv.FormatInt(body.GroupID, 10)
		rooms := subs[gk]
		next := rooms[:0]
		for _, id := range rooms {
			if id != body.RoomID {
				next = append(next, id)
			}
		}
		if len(next) == 0 {
			delete(subs, gk)
		} else {
			subs[gk] = next
		}
		if err := s.applyConfig(map[string]any{"GROUP_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "房间已删除，Bot 已重新启动"})
	default:
		methodNotAllowed(w)
	}
}

// --- 微博 UID 订阅 ---

type weiboPanelSub struct {
	GroupID int64  `json:"groupId"`
	UID     string `json:"uid"`
	AtAll   bool   `json:"atAll"`
	LastID  string `json:"lastId,omitempty"`
}

type weiboStoredSub struct {
	UID    string `json:"uid"`
	AtAll  bool   `json:"at_all"`
	LastID string `json:"last_id,omitempty"`
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
				result = append(result, weiboPanelSub{GroupID: gid, UID: id, AtAll: item.AtAll, LastID: item.LastID})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupID == result[j].GroupID {
				return result[i].UID < result[j].UID
			}
			return result[i].GroupID < result[j].GroupID
		})
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body weiboPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		body.UID = strings.TrimSpace(body.UID)
		if body.GroupID <= 0 || !regexp.MustCompile(`^\d{5,20}$`).MatchString(body.UID) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效 QQ 群号和微博 UID（纯数字）"})
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
		subs[gk][body.UID] = item
		if err := s.applyConfig(map[string]any{"WEIBO_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "微博订阅已保存，Bot 已重新启动"})
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
		if err := s.applyConfig(map[string]any{"WEIBO_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "微博订阅已删除，Bot 已重新启动"})
	default:
		methodNotAllowed(w)
	}
}

// --- 抖音创作者订阅 ---

type douyinPanelSub struct {
	GroupID    int64  `json:"groupId"`
	SecUserID  string `json:"secUserId,omitempty"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Target     string `json:"target,omitempty"`
	Name       string `json:"name,omitempty"`
	AtAll      bool   `json:"atAll"`
	LiveID     string `json:"liveId,omitempty"`
}

type douyinStoredSub struct {
	SecUserID     string `json:"sec_user_id"`
	ProfileURL    string `json:"profile_url,omitempty"`
	Name          string `json:"name,omitempty"`
	AtAll         bool   `json:"at_all"`
	LastAwemeID   string `json:"last_aweme_id,omitempty"`
	LastAwemeTime int64  `json:"last_aweme_time,omitempty"`
	LiveID        string `json:"live_id,omitempty"`
	Auto          bool   `json:"auto,omitempty"`
}

func (s *Server) handleDouyinSubscriptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	// JSON may store group keys as strings (same as xhs) — support both via generic decode into map[string]
	subs := map[string]map[string]*douyinStoredSub{}
	if encoded := raw["DOUYIN_SUBSCRIPTIONS"]; len(encoded) > 0 {
		// Try string keys first
		if err := json.Unmarshal(encoded, &subs); err != nil {
			// Fallback: int64 keys
			var asInt map[int64]map[string]*douyinStoredSub
			if err2 := json.Unmarshal(encoded, &asInt); err2 == nil {
				for gid, g := range asInt {
					subs[strconv.FormatInt(gid, 10)] = g
				}
			}
		}
	}
	switch r.Method {
	case http.MethodGet:
		result := make([]douyinPanelSub, 0)
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
				result = append(result, douyinPanelSub{
					GroupID: gid, SecUserID: id, ProfileURL: item.ProfileURL,
					Name: item.Name, AtAll: item.AtAll, LiveID: item.LiveID,
				})
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupID == result[j].GroupID {
				return result[i].SecUserID < result[j].SecUserID
			}
			return result[i].GroupID < result[j].GroupID
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
		if body.GroupID <= 0 || resolveInput == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写 QQ 群号，以及抖音主页链接或 sec_user_id"})
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
		gk := strconv.FormatInt(body.GroupID, 10)
		if subs[gk] == nil {
			subs[gk] = map[string]*douyinStoredSub{}
		}
		item := subs[gk][sec]
		if item == nil {
			item = &douyinStoredSub{SecUserID: sec}
		}
		item.SecUserID = sec
		item.ProfileURL = profile
		item.AtAll = body.AtAll
		item.Auto = false
		subs[gk][sec] = item
		if err := s.applyConfig(map[string]any{"DOUYIN_ENABLED": true, "DOUYIN_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "抖音订阅已保存，Bot 已重新启动", "secUserId": sec, "profileUrl": profile})
	case http.MethodDelete:
		var body douyinPanelSub
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		gk := strconv.FormatInt(body.GroupID, 10)
		if group := subs[gk]; group != nil {
			delete(group, strings.TrimSpace(body.SecUserID))
			if len(group) == 0 {
				delete(subs, gk)
			}
		}
		if err := s.applyConfig(map[string]any{"DOUYIN_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "抖音订阅已删除，Bot 已重新启动"})
	default:
		methodNotAllowed(w)
	}
}

