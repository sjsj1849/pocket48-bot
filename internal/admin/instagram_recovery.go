package admin

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pocket48-bot/internal/instagram"
)

const instagramRecoveryTTL = 10 * time.Minute

type instagramRecoveryJob struct {
	ID        string
	Username  string
	Password  []byte
	Code      []byte
	Status    string
	Message   string
	CreatedAt time.Time
	ExpiresAt time.Time
}

type instagramRecoveryManager struct {
	mu          sync.Mutex
	tokenPath   string
	deviceToken []byte
	job         *instagramRecoveryJob
	deviceSeen  time.Time
	lastSync    time.Time
	deviceState string
	lastMessage string
}

func newInstagramRecoveryManager(tokenPath string) *instagramRecoveryManager {
	return &instagramRecoveryManager{tokenPath: tokenPath, deviceState: "offline"}
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func (m *instagramRecoveryManager) clearJobLocked(status, message string) {
	if m.job == nil {
		return
	}
	zeroBytes(m.job.Password)
	zeroBytes(m.job.Code)
	m.job.Password = nil
	m.job.Code = nil
	m.job.Status = status
	m.job.Message = message
}

func (m *instagramRecoveryManager) pruneLocked(now time.Time) {
	if m.job != nil && now.After(m.job.ExpiresAt) && m.job.Status != "complete" {
		m.clearJobLocked("expired", "恢复任务已过期，请重新开始")
	}
	if !m.deviceSeen.IsZero() && now.Sub(m.deviceSeen) > 3*time.Minute {
		m.deviceState = "offline"
	}
}

func (m *instagramRecoveryManager) ensureTokenLocked() error {
	if len(m.deviceToken) == 64 {
		return nil
	}
	if data, err := os.ReadFile(m.tokenPath); err == nil {
		value := strings.TrimSpace(string(data))
		if len(value) == 64 {
			if _, err := hex.DecodeString(value); err == nil {
				m.deviceToken = []byte(value)
				return nil
			}
		}
		return errors.New("invalid Instagram recovery token file")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.tokenPath), 0o700); err != nil {
		return err
	}
	value := randomToken(32)
	temporary := m.tokenPath + ".tmp"
	if err := os.WriteFile(temporary, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, m.tokenPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	m.deviceToken = []byte(value)
	return nil
}

func (m *instagramRecoveryManager) ensureToken() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ensureTokenLocked()
}

func (m *instagramRecoveryManager) authorize(provided string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ensureTokenLocked() != nil || len(provided) != len(m.deviceToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), m.deviceToken) == 1
}

func (m *instagramRecoveryManager) start(username, password string, now time.Time) error {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 254 || password == "" || len(password) > 1024 {
		return errors.New("请填写 Instagram 账号和密码")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil {
		m.clearJobLocked("cancelled", "已由新任务替换")
	}
	m.job = &instagramRecoveryJob{
		ID:        randomToken(16),
		Username:  username,
		Password:  append([]byte(nil), []byte(password)...),
		Status:    "waiting_phone",
		Message:   "等待手机上线并打开 Instagram",
		CreatedAt: now,
		ExpiresAt: now.Add(instagramRecoveryTTL),
	}
	return nil
}

func (m *instagramRecoveryManager) setCode(code string, now time.Time) error {
	code = strings.TrimSpace(code)
	if len(code) < 6 || len(code) > 16 {
		return errors.New("验证码格式无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	if m.job == nil || len(m.job.Password) == 0 || m.job.Status == "expired" {
		return errors.New("没有正在等待验证码的恢复任务")
	}
	zeroBytes(m.job.Code)
	m.job.Code = append([]byte(nil), []byte(code)...)
	m.job.Status = "code_ready"
	m.job.Message = "验证码已发送到手机"
	return nil
}

func (m *instagramRecoveryManager) cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clearJobLocked("cancelled", "恢复任务已取消")
}

func (m *instagramRecoveryManager) panelState(now time.Time) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(now)
	out := map[string]any{
		"deviceOnline": m.deviceState != "offline",
		"deviceState":  m.deviceState,
		"deviceSeenAt": timeString(m.deviceSeen),
		"lastSyncAt":   timeString(m.lastSync),
		"message":      m.lastMessage,
	}
	if m.job != nil {
		out["jobId"] = m.job.ID
		out["status"] = m.job.Status
		out["jobMessage"] = m.job.Message
		out["expiresAt"] = timeString(m.job.ExpiresAt)
	}
	return out
}

func timeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func (m *instagramRecoveryManager) devicePayload(now time.Time) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceSeen = now
	m.deviceState = "online"
	m.pruneLocked(now)
	out := map[string]any{"state": "idle"}
	if m.job == nil || len(m.job.Password) == 0 {
		return out
	}
	out["state"] = "login_required"
	out["jobId"] = m.job.ID
	out["username"] = m.job.Username
	out["password"] = string(m.job.Password)
	out["code"] = string(m.job.Code)
	out["expiresAt"] = timeString(m.job.ExpiresAt)
	return out
}

func (m *instagramRecoveryManager) report(jobID, state, message string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceSeen = now
	m.deviceState = state
	m.lastMessage = message
	m.pruneLocked(now)
	if m.job != nil && jobID == m.job.ID && len(m.job.Password) > 0 {
		m.job.Status = state
		m.job.Message = message
	}
}

func validInstagramDeviceState(state string) bool {
	switch state {
	case "online", "opening_instagram", "filling_login", "waiting_code", "submitting_code", "capturing_session", "error":
		return true
	default:
		return false
	}
}

func (m *instagramRecoveryManager) complete(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceSeen = now
	m.lastSync = now
	m.deviceState = "online"
	m.lastMessage = "手机会话已验证并保存"
	if m.job != nil {
		m.clearJobLocked("complete", "登录成功，新会话已保存")
	}
}

func (m *instagramRecoveryManager) fail(message string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deviceSeen = now
	m.deviceState = "error"
	m.lastMessage = message
	if m.job != nil {
		m.job.Status = "error"
		m.job.Message = message
	}
}

func (s *Server) authorizeInstagramDevice(w http.ResponseWriter, r *http.Request) bool {
	if !s.instagramRecovery.authorize(r.Header.Get("X-Instagram-Recovery-Token")) {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "设备认证失败"})
		return false
	}
	return true
}

func (s *Server) handleInstagramDeviceRecovery(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeInstagramDevice(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.instagramRecovery.devicePayload(time.Now()))
	case http.MethodPost:
		var input struct {
			JobID   string `json:"jobId"`
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if err := decodeJSON(r, &input); err != nil || !validInstagramDeviceState(input.State) || len(input.Message) > 240 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "设备状态无效"})
			return
		}
		s.instagramRecovery.report(input.JobID, input.State, input.Message, time.Now())
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleInstagramDeviceCandidate(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeInstagramDevice(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var input struct {
		Authorization string            `json:"authorization"`
		Headers       map[string]string `json:"headers"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "候选会话格式无效"})
		return
	}
	dir := instagram.Dir(s.opts.ConfigPath)
	cfg, err := instagram.LoadSettings(dir)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "无法读取 Instagram 配置"})
		return
	}
	client := instagram.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
	result, err := client.Call(r.Context(), map[string]any{
		"operation":     "session_mobile_apply",
		"authorization": input.Authorization,
		"headers":       input.Headers,
	})
	if err != nil {
		s.instagramRecovery.fail("候选会话验证失败，已保留原会话："+err.Error(), time.Now())
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	s.instagramRecovery.complete(time.Now())
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleInstagramRecovery(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.instagramRecovery.panelState(time.Now()))
	case http.MethodPost:
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		if err := s.instagramRecovery.start(input.Username, input.Password, time.Now()); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.instagramRecovery.panelState(time.Now()))
	case http.MethodPut:
		var input struct {
			Code string `json:"code"`
		}
		if err := decodeJSON(r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
			return
		}
		if err := s.instagramRecovery.setCode(input.Code, time.Now()); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.instagramRecovery.panelState(time.Now()))
	case http.MethodDelete:
		s.instagramRecovery.cancel()
		writeJSON(w, http.StatusOK, s.instagramRecovery.panelState(time.Now()))
	default:
		methodNotAllowed(w)
	}
}
