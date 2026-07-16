package admin

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

const vncPort = 5901

type browserStatus struct {
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	Display   string `json:"display"`
	Message   string `json:"message"`
}

func (s *Server) handleBrowserStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	display, _, err := findXDisplay()
	status := browserStatus{Available: err == nil, Display: display}
	s.vncMu.Lock()
	status.Running = s.vncCmd != nil && s.vncCmd.Process != nil && s.vncCmd.ProcessState == nil
	s.vncMu.Unlock()
	if err != nil {
		status.Message = "浏览器桌面尚未启动"
	} else if status.Running {
		status.Message = "远程浏览器会话已就绪"
	} else {
		status.Message = "浏览器桌面可用，等待打开会话"
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	display, err := s.ensureVNC()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": true, "display": display, "websocketPath": "api/browser/ws"})
}

func (s *Server) ensureVNC() (string, error) {
	display, authority, err := findXDisplay()
	if err != nil {
		return "", err
	}
	s.vncMu.Lock()
	defer s.vncMu.Unlock()
	if s.vncCmd != nil && s.vncCmd.Process != nil && s.vncCmd.ProcessState == nil && s.vncDisplay == display {
		return display, nil
	}
	if s.vncCmd != nil && s.vncCmd.Process != nil {
		_ = s.vncCmd.Process.Kill()
	}
	cmd := exec.Command("x0vncserver",
		"Display="+display,
		"localhost=1",
		"rfbport="+strconv.Itoa(vncPort),
		"SecurityTypes=None",
		"AlwaysShared=1",
		"FrameRate=24",
		"CompareFB=1",
	)
	cmd.Env = append(os.Environ(), "DISPLAY="+display, "XAUTHORITY="+authority)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("启动浏览器代理失败: %w", err)
	}
	s.vncCmd = cmd
	s.vncDisplay = display
	go func(current *exec.Cmd) {
		_ = current.Wait()
		s.vncMu.Lock()
		if s.vncCmd == current {
			s.vncCmd = nil
			s.vncDisplay = ""
		}
		s.vncMu.Unlock()
	}(cmd)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", vncPort), 250*time.Millisecond)
		if dialErr == nil {
			conn.Close()
			return display, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("浏览器代理未能监听本机端口 %d", vncPort)
}

func findXDisplay() (string, string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil || !strings.Contains(string(cmdline), "sidecar/weibo-auth/index.mjs") {
			continue
		}
		display := environValue(pid, "DISPLAY")
		if display == "" {
			continue
		}
		authority := environValue(pid, "XAUTHORITY")
		if authority == "" {
			continue
		}
		return display, authority, nil
	}
	return "", "", fmt.Errorf("没有找到正在运行的 Xvfb 浏览器桌面")
}

func environValue(pid int, key string) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
	if err != nil {
		return ""
	}
	prefix := key + "="
	for _, item := range strings.Split(string(data), "\x00") {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

var vncUpgrader = websocket.Upgrader{
	ReadBufferSize:  64 * 1024,
	WriteBufferSize: 64 * 1024,
	Subprotocols:    []string{"binary"},
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "" || strings.Contains(origin, r.Host)
	},
}

func (s *Server) handleBrowserWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if _, err := s.ensureVNC(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError{Error: err.Error()})
		return
	}
	client, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer client.Close()
	vnc, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", vncPort), 3*time.Second)
	if err != nil {
		_ = client.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseTryAgainLater, "browser proxy unavailable"))
		return
	}
	defer vnc.Close()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		buffer := make([]byte, 64*1024)
		for {
			n, readErr := vnc.Read(buffer)
			if n > 0 {
				if writeErr := client.WriteMessage(websocket.BinaryMessage, buffer[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		client.SetReadLimit(2 << 20)
		for {
			messageType, data, readErr := client.ReadMessage()
			if readErr != nil {
				return
			}
			if messageType != websocket.BinaryMessage {
				continue
			}
			if _, writeErr := vnc.Write(data); writeErr != nil {
				return
			}
		}
	}()
	<-done
}
