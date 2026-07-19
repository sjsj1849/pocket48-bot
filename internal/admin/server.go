package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed web/* web/assets/*
var webFiles embed.FS

const botService = "pocket48-bot.service"

type Options struct {
	Address        string
	ConfigPath     string
	LogPath        string
	PasswordPath   string
	CookiePath     string
	AlertStatePath string
}

type Server struct {
	opts       Options
	password   []byte
	httpServer *http.Server
	sessionsMu sync.Mutex
	sessions   map[string]session
	loginMu    sync.Mutex
	login      map[string]*loginAttempt
	vncMu      sync.Mutex
	vncCmd     *exec.Cmd
	vncDisplay string
}

type session struct {
	CSRF      string
	ExpiresAt time.Time
}

type loginAttempt struct {
	Count   int
	ResetAt time.Time
}

type apiError struct {
	Error string `json:"error"`
}

func New(opts Options) (*Server, error) {
	password, err := os.ReadFile(opts.PasswordPath)
	if err != nil {
		return nil, fmt.Errorf("read admin password: %w", err)
	}
	password = []byte(strings.TrimSpace(string(password)))
	if len(password) < 10 {
		return nil, errors.New("admin password must contain at least 10 characters")
	}
	s := &Server{
		opts:     opts,
		password: password,
		sessions: make(map[string]session),
		login:    make(map[string]*loginAttempt),
	}
	if strings.TrimSpace(s.opts.AlertStatePath) == "" {
		s.opts.AlertStatePath = filepath.Join(filepath.Dir(opts.ConfigPath), "storage", "admin-alert-state.json")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.Handle("/api/", s.requireSession(http.HandlerFunc(s.handleAPI)))
	mux.Handle("/", s.staticHandler())
	s.httpServer = &http.Server{
		Addr:              opts.Address,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	return s, nil
}

func (s *Server) Address() string { return s.opts.Address }

func (s *Server) ListenAndServe() error {
	go s.runAlertMonitor()
	err := s.httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' ws: wss:; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !s.allowLogin(ip) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "尝试次数过多，请稍后再试"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "请求格式无效"})
		return
	}
	provided := []byte(body.Password)
	valid := len(provided) == len(s.password) && subtle.ConstantTimeCompare(provided, s.password) == 1
	if !valid {
		time.Sleep(350 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "密码不正确"})
		return
	}
	token := randomToken(32)
	csrf := randomToken(24)
	s.sessionsMu.Lock()
	s.sessions[token] = session{CSRF: csrf, ExpiresAt: time.Now().Add(12 * time.Hour)}
	s.pruneSessionsLocked()
	s.sessionsMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "p48_admin",
		Value:    token,
		Path:     s.opts.CookiePath,
		MaxAge:   int((12 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   forwardedHTTPS(r),
	})
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": csrf})
}

func (s *Server) allowLogin(ip string) bool {
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	attempt := s.login[ip]
	if attempt == nil || now.After(attempt.ResetAt) {
		s.login[ip] = &loginAttempt{Count: 1, ResetAt: now.Add(10 * time.Minute)}
		return true
	}
	attempt.Count++
	return attempt.Count <= 8
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("p48_admin")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "请先登录"})
			return
		}
		s.sessionsMu.Lock()
		current, ok := s.sessions[cookie.Value]
		if ok && time.Now().After(current.ExpiresAt) {
			delete(s.sessions, cookie.Value)
			ok = false
		}
		s.sessionsMu.Unlock()
		if !ok {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "会话已过期"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.URL.Path != "/api/browser/ws" {
			if r.Header.Get("X-CSRF-Token") != current.CSRF {
				writeJSON(w, http.StatusForbidden, apiError{Error: "CSRF 校验失败"})
				return
			}
		}
		ctx := context.WithValue(r.Context(), sessionKey{}, current)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type sessionKey struct{}

func (s *Server) pruneSessionsLocked() {
	for token, item := range s.sessions {
		if time.Now().After(item.ExpiresAt) {
			delete(s.sessions, token)
		}
	}
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/session":
		s.handleSession(w, r)
	case "/api/auth/logout":
		s.handleLogout(w, r)
	case "/api/overview":
		s.handleOverview(w, r)
	case "/api/logs":
		s.handleLogs(w, r)
	case "/api/config":
		s.handleConfig(w, r)
	case "/api/xiaohongshu/subscriptions":
		s.handleXiaohongshuSubscriptions(w, r)
	case "/api/service/restart":
		s.handleRestart(w, r)
	case "/api/browser/status":
		s.handleBrowserStatus(w, r)
	case "/api/browser/session":
		s.handleBrowserSession(w, r)
	case "/api/browser/ws":
		s.handleBrowserWebSocket(w, r)
	default:
		writeJSON(w, http.StatusNotFound, apiError{Error: "接口不存在"})
	}
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	current, _ := r.Context().Value(sessionKey{}).(session)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "csrf": current.CSRF})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if cookie, err := r.Cookie("p48_admin"); err == nil {
		s.sessionsMu.Lock()
		delete(s.sessions, cookie.Value)
		s.sessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "p48_admin", Value: "", Path: s.opts.CookiePath, MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func forwardedHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func randomToken(size int) string {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "请求方法不支持"})
}

func (s *Server) staticHandler() http.Handler {
	root, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if path != "." {
			if _, err := fs.Stat(root, path); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		data, err := fs.ReadFile(root, "index.html")
		if err != nil {
			http.Error(w, "admin UI is not built", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(data)
	})
}

func runCommand(timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return string(output), ctx.Err()
	}
	return strings.TrimSpace(string(output)), err
}

func (s *Server) Close(ctx context.Context) error {
	s.vncMu.Lock()
	if s.vncCmd != nil && s.vncCmd.Process != nil {
		_ = s.vncCmd.Process.Kill()
	}
	s.vncMu.Unlock()
	return s.httpServer.Shutdown(ctx)
}

func init() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}
