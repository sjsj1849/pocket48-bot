package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"pocket48-bot/internal/logic"
)

var xiaohongshuPanelProfilePattern = regexp.MustCompile(`(?i)/user/profile/([a-z0-9]+)`)
var xiaohongshuPanelUserIDPattern = regexp.MustCompile(`^[a-zA-Z0-9]{16,64}$`)

type xiaohongshuPanelSubscription struct {
	GroupID         int64  `json:"groupId,omitempty"`
	UserID          string `json:"userId"`
	ProfileURL      string `json:"profileUrl,omitempty"`
	// Target is the raw panel input: internal user_id, full profile URL, or xhslink short URL.
	Target          string `json:"target,omitempty"`
	Name            string `json:"name,omitempty"`
	AtAll           bool   `json:"atAll"`
	LastNoteID      string `json:"lastNoteId,omitempty"`
	LastNoteTime    int64  `json:"lastNoteTime,omitempty"`
	LiveInitialized bool   `json:"liveInitialized,omitempty"`
	LiveActive      bool   `json:"liveActive,omitempty"`
}

type xiaohongshuStoredSubscription struct {
	UserID          string `json:"user_id"`
	ProfileURL      string `json:"profile_url,omitempty"`
	Name            string `json:"name,omitempty"`
	AtAll           bool   `json:"at_all"`
	LastNoteID      string `json:"last_note_id,omitempty"`
	LastNoteTime    int64  `json:"last_note_time,omitempty"`
	LiveInitialized bool   `json:"live_initialized,omitempty"`
	LiveActive      bool   `json:"live_active,omitempty"`
}

type xiaohongshuSubscriptionMap map[string]map[string]*xiaohongshuStoredSubscription

func (s *Server) handleXiaohongshuSubscriptions(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := readJSONFile(s.opts.ConfigPath, &raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	subs := make(xiaohongshuSubscriptionMap)
	if encoded := raw["XIAOHONGSHU_SUBSCRIPTIONS"]; len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &subs)
	}
	switch r.Method {
	case http.MethodGet:
		result := make([]xiaohongshuPanelSubscription, 0)
		for groupText, group := range subs {
			groupID, _ := strconv.ParseInt(groupText, 10, 64)
			for key, item := range group {
				if item != nil {
					id := item.UserID
					if id == "" {
						id = key
					}
					result = append(result, xiaohongshuPanelSubscription{
						GroupID:         groupID,
						UserID:          id,
						ProfileURL:      item.ProfileURL,
						Name:            item.Name,
						AtAll:           item.AtAll,
						LastNoteID:      item.LastNoteID,
						LastNoteTime:    item.LastNoteTime,
						LiveInitialized: item.LiveInitialized,
						LiveActive:      item.LiveActive,
					})
				}
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].GroupID == result[j].GroupID {
				return result[i].UserID < result[j].UserID
			}
			return result[i].GroupID < result[j].GroupID
		})
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": result})
	case http.MethodPost:
		var body xiaohongshuPanelSubscription
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		body.UserID = strings.TrimSpace(body.UserID)
		body.ProfileURL = strings.TrimSpace(body.ProfileURL)
		body.Target = strings.TrimSpace(body.Target)
		// Prefer explicit fields; fall back to raw panel input (xhslink / profile / internal id).
		resolveInput := body.UserID
		if resolveInput == "" {
			resolveInput = body.ProfileURL
		}
		if resolveInput == "" {
			resolveInput = body.Target
		}
		if body.GroupID <= 0 || resolveInput == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效 QQ 群号，以及小红书个人主页链接 / xhslink 分享链接 / 内部 user_id（不是小红书号）"})
			return
		}
		// Short red-book number (e.g. 956753385) is NOT the internal user_id used by monitoring.
		if regexp.MustCompile(`^\d{6,12}$`).MatchString(resolveInput) {
			writeJSON(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("%s 是小红书号，不是内部 user_id。请粘贴个人主页链接或 xhslink 分享链接（例如 https://xhslink.com/m/...）", resolveInput)})
			return
		}
		userID, profileURL, err := logic.ResolveXiaohongshuTarget(r.Context(), resolveInput)
		if err != nil {
			// Keep profile-path fallback when resolve fails on already-known full URLs.
			if match := xiaohongshuPanelProfilePattern.FindStringSubmatch(resolveInput); len(match) == 2 {
				userID = match[1]
				profileURL = resolveInput
				err = nil
			}
		}
		if err != nil || !xiaohongshuPanelUserIDPattern.MatchString(userID) {
			msg := "无法解析小红书目标，请使用个人主页链接、xhslink 分享链接或内部 user_id（24 位左右，不是小红书号）"
			if err != nil {
				msg = err.Error()
			}
			writeJSON(w, http.StatusBadRequest, apiError{Error: msg})
			return
		}
		body.UserID, body.ProfileURL = userID, profileURL
		groupKey := strconv.FormatInt(body.GroupID, 10)
		if subs[groupKey] == nil {
			subs[groupKey] = make(map[string]*xiaohongshuStoredSubscription)
		}
		item := subs[groupKey][body.UserID]
		if item == nil {
			item = &xiaohongshuStoredSubscription{UserID: body.UserID}
		}
		item.ProfileURL, item.AtAll = body.ProfileURL, body.AtAll
		if item.ProfileURL == "" {
			item.ProfileURL = "https://www.xiaohongshu.com/user/profile/" + body.UserID
		}
		subs[groupKey][body.UserID] = item
		if err := s.applyConfig(map[string]any{"XIAOHONGSHU_ENABLED": true, "XIAOHONGSHU_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "订阅已保存，Bot 已重新启动", "userId": body.UserID, "profileUrl": body.ProfileURL})
	case http.MethodDelete:
		var body xiaohongshuPanelSubscription
		if err := decodeJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		groupKey := strconv.FormatInt(body.GroupID, 10)
		if group := subs[groupKey]; group != nil {
			delete(group, strings.TrimSpace(body.UserID))
			if len(group) == 0 {
				delete(subs, groupKey)
			}
		}
		if err := s.applyConfig(map[string]any{"XIAOHONGSHU_SUBSCRIPTIONS": subs}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "订阅已删除，Bot 已重新启动"})
	default:
		methodNotAllowed(w)
	}
}
