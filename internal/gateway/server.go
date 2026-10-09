package gateway

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/message"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/outbound"
)

//go:embed web
var consoleFiles embed.FS

type DeliveryRecord struct {
	Time      string `json:"time"`
	Project   string `json:"project"`
	Event     string `json:"event"`
	Summary   string `json:"summary"`
	Target    string `json:"target"`
	Platform  string `json:"platform"`
	Status    string `json:"status"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

type Server struct {
	store     *Store
	hub       *outbound.Hub
	qq        *napcat.Client
	started   time.Time
	recordsMu sync.RWMutex
	records   []DeliveryRecord
	http      *http.Server
}

type MessageRequest struct {
	Project      string            `json:"project"`
	Event        string            `json:"event"`
	TargetIDs    []string          `json:"targetIds,omitempty"`
	LegacyTarget *LegacyTarget     `json:"legacyTarget,omitempty"`
	Segments     []message.Segment `json:"segments"`
}

type LegacyTarget struct {
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	Address  string `json:"address"`
}

func NewServer(store *Store) (*Server, error) {
	cfg := store.Get()
	hub := outbound.NewHub("qq")
	var qq *napcat.Client
	if cfg.QQ.Enabled {
		qqCfg := &config.Config{NapCatWSURL: cfg.QQ.WSURL, NapCatAccessToken: cfg.QQ.AccessToken}
		qq = napcat.NewClient(qqCfg)
		if err := qq.Connect(); err != nil {
			log.Printf("[Gateway] QQ adapter is configured but currently unavailable: %v", err)
		} else {
			hub.Register("qq", outbound.NewOneBot(qq))
		}
	}
	if cfg.Feishu.Enabled && cfg.Feishu.AppID != "" && cfg.Feishu.AppSecret != "" {
		hub.Register("feishu", outbound.NewFeishu(outbound.FeishuOptions{
			AppID: cfg.Feishu.AppID, AppSecret: cfg.Feishu.AppSecret,
			UploadConcurrency: cfg.Feishu.UploadConcurrency,
		}))
	}
	s := &Server{store: store, hub: hub, qq: qq, started: time.Now(), records: make([]DeliveryRecord, 0, 200)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/messages", s.handleMessages)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/test", s.handleTest)
	webRoot, err := fs.Sub(consoleFiles, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("/", spaHandler(webRoot))
	s.http = &http.Server{Addr: cfg.Address, Handler: s.securityHeaders(mux), ReadHeaderTimeout: 10 * time.Second}
	return s, nil
}

func (s *Server) ListenAndServe() error { return s.http.ListenAndServe() }
func (s *Server) Address() string       { return s.http.Addr }

func (s *Server) authorized(r *http.Request) bool {
	key := s.store.Get().APIKey
	if key == "" {
		return true
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(key), []byte(provided)) == 1
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if strings.HasPrefix(r.URL.Path, "/api/") && !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req MessageRequest
	if err := decode(r, &req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	targets := s.resolveTargets(req)
	if len(targets) == 0 {
		writeJSON(w, 422, map[string]string{"error": "no delivery targets matched"})
		return
	}
	started := time.Now()
	for _, target := range targets {
		deliveryTarget, err := outboundTarget(target)
		record := DeliveryRecord{Time: time.Now().Format(time.RFC3339), Project: req.Project, Event: req.Event, Summary: summarize(req.Segments), Target: target.Name, Platform: target.Platform}
		if err != nil {
			record.Status, record.Error = "failed", err.Error()
		} else {
			s.hub.Send(deliveryTarget, req.Segments)
			record.Status = "queued"
		}
		record.LatencyMS = time.Since(started).Milliseconds()
		s.addRecord(record)
	}
	writeJSON(w, 202, map[string]any{"ok": true, "targets": len(targets)})
}

func (s *Server) resolveTargets(req MessageRequest) []Target {
	cfg := s.store.Get()
	byID := make(map[string]Target, len(cfg.Targets))
	for _, target := range cfg.Targets {
		byID[target.ID] = target
	}
	ids := append([]string(nil), req.TargetIDs...)
	if len(ids) == 0 {
		for _, route := range cfg.Routes {
			if !route.Enabled {
				continue
			}
			if route.Project != "" && route.Project != "*" && route.Project != req.Project {
				continue
			}
			if route.Event != "" && route.Event != "*" && route.Event != req.Event {
				continue
			}
			if req.LegacyTarget != nil && route.LegacyAddress != "" {
				if (route.LegacyPlatform != "" && route.LegacyPlatform != req.LegacyTarget.Platform) ||
					(route.LegacyKind != "" && route.LegacyKind != req.LegacyTarget.Kind) ||
					route.LegacyAddress != req.LegacyTarget.Address {
					continue
				}
			} else if route.LegacyAddress != "" {
				continue
			}
			ids = append(ids, route.TargetIDs...)
		}
	}
	seen := map[string]bool{}
	result := make([]Target, 0, len(ids))
	for _, id := range ids {
		if target, ok := byID[id]; ok && !seen[id] {
			seen[id] = true
			result = append(result, target)
		}
	}
	if len(result) == 0 {
		if fallback := legacyFallbackTarget(req); fallback != nil {
			log.Printf("[Gateway] no route matched; falling back to legacy target platform=%s kind=%s address=%s",
				fallback.Platform, fallback.Kind, fallback.Address)
			result = append(result, *fallback)
		}
	}
	return result
}

// legacyFallbackTarget keeps the pre-gateway delivery path working. Without it
// a gateway started with empty targets/routes would silently drop every
// message, which is the default state right after deployment.
func legacyFallbackTarget(req MessageRequest) *Target {
	if req.LegacyTarget == nil {
		return nil
	}
	address := strings.TrimSpace(req.LegacyTarget.Address)
	if address == "" {
		return nil
	}
	platform := strings.TrimSpace(req.LegacyTarget.Platform)
	if platform == "" {
		platform = "qq"
	}
	kind := strings.TrimSpace(req.LegacyTarget.Kind)
	if kind == "" {
		kind = "group"
	}
	return &Target{ID: "legacy:" + platform + ":" + kind + ":" + address, Name: address, Platform: platform, Kind: kind, Address: address}
}

func outboundTarget(target Target) (outbound.Target, error) {
	kind := outbound.GroupChat
	if target.Kind == "private" {
		kind = outbound.PrivateChat
	}
	result := outbound.Target{Platform: target.Platform, Kind: kind, Address: target.Address}
	if target.Platform == "qq" {
		id, err := strconv.ParseInt(target.Address, 10, 64)
		if err != nil {
			return result, fmt.Errorf("invalid QQ target: %w", err)
		}
		result.ID = id
	}
	return result, nil
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	cfg := s.store.Get()
	cfg.APIKey = redact(cfg.APIKey)
	cfg.QQ.AccessToken, cfg.Feishu.AppSecret, cfg.Telegram.BotToken = redact(cfg.QQ.AccessToken), redact(cfg.Feishu.AppSecret), redact(cfg.Telegram.BotToken)
	s.recordsMu.RLock()
	records := append([]DeliveryRecord(nil), s.records...)
	s.recordsMu.RUnlock()
	writeJSON(w, 200, map[string]any{"config": cfg, "records": records, "uptime": time.Since(s.started).Round(time.Second).String(), "queueDepth": s.hub.QueueDepth()})
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	current := s.store.Get()
	var next Config
	if err := decode(r, &next); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	if next.QQ.AccessToken == "••••••••" {
		next.QQ.AccessToken = current.QQ.AccessToken
	}
	if next.APIKey == "••••••••" {
		next.APIKey = current.APIKey
	}
	if next.Feishu.AppSecret == "••••••••" {
		next.Feishu.AppSecret = current.Feishu.AppSecret
	}
	if next.Telegram.BotToken == "••••••••" {
		next.Telegram.BotToken = current.Telegram.BotToken
	}
	if err := s.store.Update(next); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restartRequired": true})
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	var body struct {
		TargetID string `json:"targetId"`
	}
	if err := decode(r, &body); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	req := MessageRequest{Project: "gateway", Event: "test", TargetIDs: []string{body.TargetID}, Segments: []message.Segment{message.Text("【消息出口中心|测试】\n这是一条测试消息。\n" + time.Now().Format("2006-01-02 15:04:05"))}}
	targets := s.resolveTargets(req)
	if len(targets) != 1 {
		writeJSON(w, 404, map[string]string{"error": "target not found"})
		return
	}
	t, err := outboundTarget(targets[0])
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	s.hub.Send(t, req.Segments)
	writeJSON(w, 202, map[string]bool{"ok": true})
}

func (s *Server) addRecord(record DeliveryRecord) {
	s.recordsMu.Lock()
	defer s.recordsMu.Unlock()
	s.records = append([]DeliveryRecord{record}, s.records...)
	if len(s.records) > 200 {
		s.records = s.records[:200]
	}
}
func summarize(segments []message.Segment) string {
	for _, s := range segments {
		if s.Type == "text" {
			text := strings.ReplaceAll(s.Data["text"], "\n", " ")
			if len([]rune(text)) > 60 {
				return string([]rune(text)[:60]) + "…"
			}
			return text
		}
	}
	return "媒体消息"
}
func redact(value string) string {
	if value == "" {
		return ""
	}
	return "••••••••"
}
func decode(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func spaHandler(root fs.FS) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

var _ = log.Printf
