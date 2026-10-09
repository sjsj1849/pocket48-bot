package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/outbound"
)

type feishuChatResponse struct {
	Chats []outbound.FeishuChat `json:"chats"`
	// MyOpenID is inferred from groups the operator owns, which is the only
	// way to surface the current user's open_id without a user token.
	MyOpenID string `json:"myOpenId,omitempty"`
}

type feishuTestRequest struct {
	ChatID string `json:"chatId"`
	Kind   string `json:"kind"`
}

// loadFeishuClient builds an adapter from the saved console configuration. The
// adapter is created without its background worker because every panel action is
// a one-shot, synchronous call.
func (s *Server) loadFeishuClient() (*outbound.Feishu, error) {
	cfg, err := config.LoadConfig(s.opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	appID := strings.TrimSpace(cfg.FeishuAppID)
	appSecret := strings.TrimSpace(cfg.FeishuAppSecret)
	if appID == "" || appSecret == "" {
		return nil, errors.New("请先在「配置 → 消息出口」中填写飞书 App ID 和 App Secret")
	}
	concurrency := cfg.FeishuUploadConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}
	return outbound.NewFeishuPanel(outbound.FeishuOptions{
		AppID:             appID,
		AppSecret:         appSecret,
		UploadConcurrency: concurrency,
	}), nil
}

func (s *Server) handleFeishu(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/feishu/chats":
		s.handleFeishuChats(w, r)
	case "/api/feishu/test":
		s.handleFeishuTest(w, r)
	default:
		writeJSON(w, http.StatusNotFound, apiError{Error: "接口不存在"})
	}
}

func (s *Server) handleFeishuChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	client, err := s.loadFeishuClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	chats, err := client.ListChats(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "拉取群列表失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, feishuChatResponse{Chats: chats, MyOpenID: ownedOpenID(chats)})
}

// ownedOpenID returns the open_id that owns the most listed groups. It is the
// practical way to identify the operator for private-message routing.
func ownedOpenID(chats []outbound.FeishuChat) string {
	counts := make(map[string]int)
	for _, chat := range chats {
		if chat.OwnerID != "" {
			counts[chat.OwnerID]++
		}
	}
	best, bestCount := "", 0
	for id, count := range counts {
		if count > bestCount {
			best, bestCount = id, count
		}
	}
	return best
}

func (s *Server) handleFeishuTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var body feishuTestRequest
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	body.ChatID = strings.TrimSpace(body.ChatID)
	if body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请先选择或填写一个群"})
		return
	}
	client, err := s.loadFeishuClient()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	kind := outbound.GroupChat
	receiveType := "chat_id"
	if strings.EqualFold(body.Kind, "private") {
		kind = outbound.PrivateChat
		receiveType = "open_id"
	}
	target := outbound.Target{Platform: "feishu", Kind: kind, Address: body.ChatID}
	content := []interface{}{
		outbound.Text("【消息出口|测试】\n这是 Pocket48 控制台发出的连通性测试消息。\n时间：" + time.Now().Format("2006-01-02 15:04:05") + "\n接收方式：" + receiveType),
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := client.SendNow(ctx, target, content); err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: "发送失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "chatId": body.ChatID})
}

type outboundTargetsResponse struct {
	Targets []config.DeliveryTarget `json:"targets"`
	// Feishu chats are read live so a newly joined group appears immediately.
	FeishuChats []outbound.FeishuChat `json:"feishuChats,omitempty"`
	MyOpenID    string                `json:"myOpenId,omitempty"`
	FeishuError string                `json:"feishuError,omitempty"`
}

func (s *Server) handleOutboundTargets(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.LoadConfig(s.opts.ConfigPath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if r.Method == http.MethodGet {
		response := outboundTargetsResponse{Targets: cfg.DeliveryTargets}
		if client, clientErr := s.loadFeishuClient(); clientErr == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if chats, chatErr := client.ListChats(ctx); chatErr == nil {
				response.FeishuChats = chats
				response.MyOpenID = ownedOpenID(chats)
				applyFeishuChatNames(response.Targets, chats, response.MyOpenID)
			} else {
				response.FeishuError = chatErr.Error()
			}
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if r.Method != http.MethodPut {
		methodNotAllowed(w)
		return
	}
	var body struct {
		Targets []config.DeliveryTarget `json:"targets"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	cfg.DeliveryTargets = body.Targets
	// 只能按 key 合并写回：config.Config 是固定结构体，json.Marshal 只会输出
	// 结构体里声明过的字段，整体覆盖会把面板独有但 Bot 未建模的键（如
	// BILIBILI_ENABLED）悄悄删掉，等于改一次投递目标就回退整份配置。
	encoded, err := json.Marshal(body.Targets)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	var targetsValue any
	if err := json.Unmarshal(encoded, &targetsValue); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	if err := s.writeConfigNoRestart(map[string]any{"DELIVERY_TARGETS": targetsValue}); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, outboundTargetsResponse{Targets: cfg.DeliveryTargets})
}

// applyFeishuChatNames replaces raw chat ids with the real group names so the
// address book stays readable.
func applyFeishuChatNames(targets []config.DeliveryTarget, chats []outbound.FeishuChat, myOpenID string) {
	names := make(map[string]string, len(chats))
	for _, chat := range chats {
		names[chat.ChatID] = chat.Name
	}
	for i := range targets {
		target := &targets[i]
		if target.Platform != "feishu" {
			continue
		}
		if name := names[target.Address]; name != "" {
			target.Name = name
			continue
		}
		if target.Kind == "private" && target.Address == myOpenID {
			target.Name = "我的飞书私聊"
		}
	}
}
