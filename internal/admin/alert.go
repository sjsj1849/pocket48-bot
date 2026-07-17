package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/mail"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const alertCheckInterval = 30 * time.Second

type alertConfig struct {
	Enabled         bool   `json:"ALERT_EMAIL_ENABLED"`
	To              string `json:"ALERT_EMAIL_TO"`
	From            string `json:"ALERT_EMAIL_FROM"`
	CooldownMinutes int    `json:"ALERT_EMAIL_COOLDOWN_MINUTES"`
}

type serviceAlertState struct {
	Failures int       `json:"failures"`
	Alerted  bool      `json:"alerted"`
	LastSent time.Time `json:"lastSent,omitempty"`
}

type alertStateFile struct {
	Services map[string]serviceAlertState `json:"services"`
}

func (s *Server) runAlertMonitor() {
	// Let the bot finish its startup sequence before judging component health.
	time.Sleep(alertCheckInterval)
	ticker := time.NewTicker(alertCheckInterval)
	defer ticker.Stop()
	for {
		s.checkServiceAlerts(time.Now())
		<-ticker.C
	}
}

func (s *Server) checkServiceAlerts(now time.Time) {
	cfg, err := loadAlertConfig(s.opts.ConfigPath)
	if err != nil {
		log.Printf("[email-alert] read config: %v", err)
		return
	}
	if !cfg.Enabled || strings.TrimSpace(cfg.To) == "" {
		return
	}
	if cfg.From == "" {
		cfg.From = "pocket48@jiufeng.cloud"
	}
	if cfg.CooldownMinutes <= 0 {
		cfg.CooldownMinutes = 60
	}
	if _, err := mail.ParseAddress(cfg.To); err != nil {
		log.Printf("[email-alert] invalid recipient: %v", err)
		return
	}
	if _, err := mail.ParseAddress(cfg.From); err != nil {
		log.Printf("[email-alert] invalid sender: %v", err)
		return
	}

	lines, _ := tailLines(s.opts.LogPath, 5000)
	services := buildServiceStates(lines)
	state := s.loadAlertState()
	changed := false
	for _, service := range services {
		if service.ID != "bot" && service.LastTime == "" {
			continue
		}
		current := state.Services[service.ID]
		unhealthy := service.Status != "healthy"
		if unhealthy {
			current.Failures++
			if current.Failures >= 2 && !current.Alerted && now.Sub(current.LastSent) >= time.Duration(cfg.CooldownMinutes)*time.Minute {
				if err := sendServiceEmail(cfg, service, false, now); err != nil {
					log.Printf("[email-alert] send offline alert for %s: %v", service.ID, err)
				} else {
					current.Alerted = true
					current.LastSent = now
					log.Printf("[email-alert] offline alert sent service=%s to=%s", service.ID, cfg.To)
				}
			}
		} else {
			current.Failures = 0
			if current.Alerted {
				if err := sendServiceEmail(cfg, service, true, now); err != nil {
					log.Printf("[email-alert] send recovery alert for %s: %v", service.ID, err)
				} else {
					current.Alerted = false
					current.LastSent = now
					log.Printf("[email-alert] recovery alert sent service=%s to=%s", service.ID, cfg.To)
				}
			}
		}
		if state.Services[service.ID] != current {
			state.Services[service.ID] = current
			changed = true
		}
	}
	if changed {
		if err := s.saveAlertState(state); err != nil {
			log.Printf("[email-alert] save state: %v", err)
		}
	}
}

func loadAlertConfig(path string) (alertConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return alertConfig{}, err
	}
	var cfg alertConfig
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

func (s *Server) loadAlertState() alertStateFile {
	state := alertStateFile{Services: make(map[string]serviceAlertState)}
	data, err := os.ReadFile(s.opts.AlertStatePath)
	if err == nil {
		_ = json.Unmarshal(data, &state)
	}
	if state.Services == nil {
		state.Services = make(map[string]serviceAlertState)
	}
	return state
}

func (s *Server) saveAlertState(state alertStateFile) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.AlertStatePath), 0700); err != nil {
		return err
	}
	tmp := s.opts.AlertStatePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.opts.AlertStatePath)
}

func sendServiceEmail(cfg alertConfig, service serviceState, recovered bool, now time.Time) error {
	status := "OFFLINE"
	title := "服务异常"
	if recovered {
		status = "RECOVERED"
		title = "服务恢复"
	}
	subject := fmt.Sprintf("[Pocket48] %s: %s", status, service.Name)
	body := fmt.Sprintf("Pocket48 %s通知\n\n服务：%s\n状态：%s\n说明：%s\n时间：%s\n主机：mail.jiufeng.cloud\n", title, service.Name, service.StatusText, service.LastEvent, now.Format("2006-01-02 15:04:05 MST"))
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", cfg.From, cfg.To, subject)
	message.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(body)
	cmd := exec.Command("/usr/sbin/sendmail", "-t", "-oi", "-f", cfg.From)
	cmd.Stdin = &message
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sendmail: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
