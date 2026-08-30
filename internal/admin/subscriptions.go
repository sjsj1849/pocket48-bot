package admin

import (
	"encoding/json"
	"net/http"
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
	GroupID int64  `json:"groupId"`
	RoomID  int64  `json:"roomId"`
	Name    string `json:"name,omitempty"`
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
	switch r.Method {
	case http.MethodGet:
		result := make([]pocketRoomSub, 0)
		var client *pocket48.Client
		if cfg, err := config.LoadConfig(s.opts.ConfigPath); err == nil && strings.TrimSpace(cfg.PocketToken) != "" {
			client = pocket48.NewClient(cfg)
		}
		for groupText, rooms := range subs {
			gid, _ := strconv.ParseInt(groupText, 10, 64)
			for _, roomID := range rooms {
				name := ""
				if client != nil {
					name = enrichPocketRoomName(client, roomID)
				}
				result = append(result, pocketRoomSub{GroupID: gid, RoomID: roomID, Name: name})
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
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs}); err != nil {
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
		oldG := body.OldGroupID
		oldR := body.OldRoomID
		// Allow 0 for old values (group 0 subscriptions are valid)
		oldGSpecified := oldR > 0 || body.OldRoomID == 0
		if !oldGSpecified || oldG <= 0 {
			oldG = body.GroupID
		}
		if oldR <= 0 {
			oldR = body.RoomID
		}
		if body.GroupID <= 0 || body.RoomID <= 0 || oldG <= 0 || oldR <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效的 QQ 群号与房间 ID"})
			return
		}
		// remove old
		ogk := strconv.FormatInt(oldG, 10)
		rooms := subs[ogk]
		next := rooms[:0]
		for _, id := range rooms {
			if id != oldR {
				next = append(next, id)
			}
		}
		if len(next) == 0 {
			delete(subs, ogk)
		} else {
			subs[ogk] = next
		}
		// add new
		ngk := strconv.FormatInt(body.GroupID, 10)
		exists := false
		for _, id := range subs[ngk] {
			if id == body.RoomID {
				exists = true
				break
			}
		}
		if !exists {
			subs[ngk] = append(subs[ngk], body.RoomID)
		}
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs}); err != nil {
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
		if err := s.writeConfigAndReloadBot(map[string]any{"GROUP_SUBSCRIPTIONS": subs}); err != nil {
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
	GroupID    int64  `json:"groupId"`
	UID        string `json:"uid"`
	Name       string `json:"name,omitempty"`
	AtAll      bool   `json:"atAll"`
	LastID     string `json:"lastId,omitempty"`
	OldGroupID int64  `json:"oldGroupId,omitempty"`
	OldUID     string `json:"oldUid,omitempty"`
}

type weiboStoredSub struct {
	UID    string `json:"uid"`
	Name   string `json:"name,omitempty"`
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
				result = append(result, weiboPanelSub{GroupID: gid, UID: id, Name: name, AtAll: item.AtAll, LastID: item.LastID})
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
		body.UID = strings.TrimSpace(body.UID)
		oldUID := strings.TrimSpace(body.OldUID)
		if oldUID == "" {
			oldUID = body.UID
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
		if body.GroupID <= 0 || !regexp.MustCompile(`^\d{5,20}$`).MatchString(body.UID) || oldUID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效 QQ 群号和微博 UID"})
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
	GroupID    int64  `json:"groupId"`
	SecUserID  string `json:"secUserId,omitempty"`
	ProfileURL string `json:"profileUrl,omitempty"`
	Target     string `json:"target,omitempty"`
	Name       string `json:"name,omitempty"`
	AtAll      bool   `json:"atAll"`
	LiveID     string `json:"liveId,omitempty"`
	OldGroupID int64  `json:"oldGroupId,omitempty"`
	OldSec     string `json:"oldSecUserId,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
	Source     string `json:"source,omitempty"`
	Status     string `json:"status,omitempty"`
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
	Disabled      bool   `json:"disabled,omitempty"`
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
					Enabled: boolPointer(!item.Disabled), Source: "config", Status: douyinSubscriptionStatus(s.opts.ConfigPath, item),
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
		if body.Enabled != nil {
			item.Disabled = !*body.Enabled
		}
		if n := strings.TrimSpace(body.Name); n != "" {
			item.Name = n
		}
		subs[gk][sec] = item
		var douyinEnabled bool
		_ = json.Unmarshal(raw["DOUYIN_ENABLED"], &douyinEnabled)
		var saveErr error
		if douyinEnabled {
			saveErr = s.writeConfigAndReloadBot(map[string]any{"DOUYIN_SUBSCRIPTIONS": subs})
		} else {
			// The master switch creates the long-lived monitor at boot, so the
			// first panel subscription requires the existing authenticated restart path.
			saveErr = s.applyConfig(map[string]any{"DOUYIN_ENABLED": true, "DOUYIN_SUBSCRIPTIONS": subs})
		}
		if saveErr != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: saveErr.Error()})
			return
		}
		message := "抖音订阅已保存并热重载"
		if !douyinEnabled {
			message = "抖音订阅已保存，已启用并重启 Bot"
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message, "secUserId": sec, "profileUrl": profile})
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
		oldG := body.OldGroupID
		if oldG <= 0 {
			oldG = body.GroupID
		}
		if body.GroupID <= 0 || oldSec == "" || oldG <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请填写有效 QQ 群号与 sec_user_id"})
			return
		}
		// resolve new sec if target provided, else keep old
		newSec := oldSec
		newProfile := ""
		resolveInput := strings.TrimSpace(body.Target)
		if resolveInput == "" {
			resolveInput = strings.TrimSpace(body.ProfileURL)
		}
		if resolveInput == "" {
			resolveInput = strings.TrimSpace(body.SecUserID)
		}
		if resolveInput != "" && resolveInput != oldSec {
			sec, profile, err := logic.ResolveDouyinTarget(r.Context(), resolveInput)
			if err != nil || sec == "" {
				msg := "无法解析抖音目标"
				if err != nil {
					msg = err.Error()
				}
				writeJSON(w, http.StatusBadRequest, apiError{Error: msg})
				return
			}
			newSec = sec
			newProfile = profile
		}
		ogk := strconv.FormatInt(oldG, 10)
		var preserved *douyinStoredSub
		if g := subs[ogk]; g != nil {
			if old := g[oldSec]; old != nil {
				cp := *old
				preserved = &cp
			}
			delete(g, oldSec)
			if len(g) == 0 {
				delete(subs, ogk)
			}
		}
		ngk := strconv.FormatInt(body.GroupID, 10)
		if subs[ngk] == nil {
			subs[ngk] = map[string]*douyinStoredSub{}
		}
		item := &douyinStoredSub{SecUserID: newSec}
		if existing := subs[ngk][newSec]; existing != nil {
			*item = *existing
			item.SecUserID = newSec
		} else if preserved != nil && newSec == oldSec {
			*item = *preserved
			item.SecUserID = newSec
		} else if preserved != nil {
			// Changing creators must not carry the old creator's live ID or
			// work cursor into the new subscription.
			item.Disabled = preserved.Disabled
		}
		if newProfile != "" {
			item.ProfileURL = newProfile
		}
		item.AtAll = body.AtAll
		if body.Enabled != nil {
			item.Disabled = !*body.Enabled
		}
		if n := strings.TrimSpace(body.Name); n != "" {
			item.Name = n
		}
		subs[ngk][newSec] = item
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
		gk := strconv.FormatInt(body.GroupID, 10)
		if group := subs[gk]; group != nil {
			delete(group, strings.TrimSpace(body.SecUserID))
			if len(group) == 0 {
				delete(subs, gk)
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

func boolPointer(value bool) *bool { return &value }

func douyinSubscriptionStatus(configPath string, item *douyinStoredSub) string {
	if item.Disabled {
		return "已停用"
	}
	if strings.TrimSpace(item.LiveID) != "" {
		path := filepath.Join(filepath.Dir(configPath), "storage", "douyin-live-sessions", safeAdminFilename(item.LiveID)+".json")
		var state struct {
			Online        bool      `json:"online"`
			LastUpdatedAt time.Time `json:"last_updated_at"`
		}
		if raw, err := os.ReadFile(path); err == nil && json.Unmarshal(raw, &state) == nil {
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

func safeAdminFilename(value string) string {
	var result strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' {
			result.WriteRune(r)
		}
	}
	return result.String()
}
