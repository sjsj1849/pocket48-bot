package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pocket48-bot/internal/mailsend"
)

const (
	alertCheckInterval = 30 * time.Second
	// 持续异常才邮件：约 5 分钟（10 次 × 30s）。短时重连/计划重启可自愈，不打扰。
	alertOfflineFailures = 10
)

type alertConfig struct {
	Enabled         bool   `json:"ALERT_EMAIL_ENABLED"`
	To              string `json:"ALERT_EMAIL_TO"`
	From            string `json:"ALERT_EMAIL_FROM"`
	CooldownMinutes int    `json:"ALERT_EMAIL_COOLDOWN_MINUTES"`
	SMTPHost        string `json:"ALERT_EMAIL_SMTP_HOST"`
	SMTPPort        int    `json:"ALERT_EMAIL_SMTP_PORT"`
	SMTPUser        string `json:"ALERT_EMAIL_SMTP_USER"`
	SMTPPassword    string `json:"ALERT_EMAIL_SMTP_PASSWORD"`
	PanelURL        string `json:"ADMIN_PANEL_URL"`
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
		log.Printf("[email-alert] ALERT_EMAIL_FROM empty")
		return
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
		if !shouldAlertService(service) {
			continue
		}
		current := state.Services[service.ID]
		unhealthy := service.Status != "healthy"
		if unhealthy {
			current.Failures++
			// 只在持续异常且未告警、过冷却时发邮件；短时自愈不会触达阈值。
			if current.Failures >= alertOfflineFailures && !current.Alerted && now.Sub(current.LastSent) >= time.Duration(cfg.CooldownMinutes)*time.Minute {
				if err := sendServiceEmail(cfg, service, false, now); err != nil {
					log.Printf("[email-alert] send offline alert for %s: %v", service.ID, err)
				} else {
					current.Alerted = true
					current.LastSent = now
					log.Printf("[email-alert] offline alert sent service=%s failures=%d to=%s", service.ID, current.Failures, cfg.To)
				}
			}
		} else {
			// 已恢复：清状态，不发邮件（能自愈的不打扰；只有恢复失败需人工才发 offline）。
			if current.Failures > 0 || current.Alerted {
				log.Printf("[email-alert] service=%s recovered silently (failures was %d, alerted=%v; no email)", service.ID, current.Failures, current.Alerted)
			}
			current.Failures = 0
			current.Alerted = false
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

// shouldAlertService gates the generic admin offline email path.
// Douyin IM is excluded: bot-side watchdog does sidecar restart → bot restart →
// email only after auto-heal fails. Admin must not mail on "重连中".
func shouldAlertService(service serviceState) bool {
	if service.ID == "douyin_im" {
		return false
	}
	if service.ID != "bot" && service.LastTime == "" {
		return false
	}
	return true
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
	message := buildServiceEmail(cfg, service, recovered, now)
	return mailsend.Send(mailsend.Config{
		From:     cfg.From,
		To:       cfg.To,
		SMTPHost: cfg.SMTPHost,
		SMTPPort: cfg.SMTPPort,
		SMTPUser: cfg.SMTPUser,
		SMTPPass: cfg.SMTPPassword,
		PanelURL: mailsend.NormalizePanelURL(cfg.PanelURL),
	}, message)
}

func buildServiceEmail(cfg alertConfig, service serviceState, recovered bool, now time.Time) []byte {
	// recovered 路径已停用（自愈不发信）；保留参数仅为兼容调用签名/测试。
	title := "服务持续异常，需人工处理"
	badge := "需要处理"
	observeMinutes := (alertOfflineFailures * int(alertCheckInterval/time.Second)) / 60
	if observeMinutes < 1 {
		observeMinutes = 1
	}
	summary := fmt.Sprintf("监控确认该服务已持续约 %d 分钟处于非健康状态（已超过自动恢复观察窗口），请人工检查。", observeMinutes)
	if recovered {
		title = "服务已经恢复"
		badge = "已恢复"
		summary = "监控确认服务已恢复正常，实时链路正在继续运行。"
	}
	subject := fmt.Sprintf("Pocket48 %s｜%s", choose(recovered, "服务恢复", "服务异常"), service.Name)
	timeText := now.Format("2006-01-02 15:04:05 MST")
	panel := mailsend.NormalizePanelURL(cfg.PanelURL)
	panelPlain := ""
	if panel != "" {
		panelPlain = "\n管理面板：" + panel + "\n"
	}
	plain := fmt.Sprintf("Pocket48 %s\n\n%s\n\n服务：%s\n状态：%s\n说明：%s\n时间：%s\n%s", title, summary, service.Name, service.StatusText, service.LastEvent, timeText, panelPlain)
	escape := html.EscapeString
	htmlBody := fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:0;background:#f5f7fb;color:#172033;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#f5f7fb;padding:32px 12px;"><tr><td align="center">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:600px;background:#ffffff;border:1px solid #e5eaf2;border-radius:8px;overflow:hidden;box-shadow:0 10px 30px rgba(31,52,89,.06);">
<tr><td style="height:4px;background:#3478d4;font-size:0;line-height:0;">&nbsp;</td></tr>
<tr><td style="padding:28px 32px 18px;">
<table role="presentation" cellspacing="0" cellpadding="0"><tr><td style="width:36px;height:36px;border-radius:8px;background:#3478d4;color:#fff;font-size:13px;font-weight:700;text-align:center;vertical-align:middle;">P48</td><td style="padding-left:12px;font-size:17px;font-weight:650;color:#172033;">Pocket48 Console</td></tr></table>
</td></tr>
<tr><td style="padding:4px 32px 28px;">
<span style="display:inline-block;padding:5px 9px;border-radius:6px;background:#edf4ff;color:#2466b3;font-size:12px;font-weight:650;">%s</span>
<h1 style="margin:14px 0 8px;font-size:25px;line-height:1.35;letter-spacing:0;color:#172033;">%s</h1>
<p style="margin:0;color:#667085;font-size:14px;line-height:1.7;">%s</p>
</td></tr>
<tr><td style="padding:0 32px 28px;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="border-collapse:separate;border-spacing:0;background:#f8fafc;border:1px solid #e7ebf1;border-radius:8px;">
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;width:72px;">服务</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;font-weight:650;">%s</td></tr>
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;">状态</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;">%s</td></tr>
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;">说明</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;line-height:1.6;">%s</td></tr>
<tr><td style="padding:15px 18px;color:#7a8495;font-size:13px;">时间</td><td style="padding:15px 18px;color:#172033;font-size:14px;">%s</td></tr>
</table>
</td></tr>
%s
<tr><td style="padding:20px 32px;border-top:1px solid #edf0f4;color:#98a1af;font-size:12px;line-height:1.6;">这是一封由 Pocket48 Console 自动发送的服务状态通知。</td></tr>
</table>
</td></tr></table>
</body></html>`, escape(badge), escape(title), escape(summary), escape(service.Name), escape(service.StatusText), escape(service.LastEvent), escape(timeText), panelButtonRow(panel))

	const boundary = "pocket48-alert-alternative"
	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", cfg.From, cfg.To, mime.QEncoding.Encode("UTF-8", subject))
	message.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&message, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&message, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, plain)
	fmt.Fprintf(&message, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, htmlBody)
	fmt.Fprintf(&message, "--%s--\r\n", boundary)
	return message.Bytes()
}

func panelButtonRow(panelURL string) string {
	panelURL = mailsend.NormalizePanelURL(panelURL)
	if panelURL == "" {
		return ""
	}
	return fmt.Sprintf(`<tr><td style="padding:0 32px 32px;" align="center"><a href="%s" style="display:inline-block;padding:11px 17px;background:#3478d4;color:#ffffff;text-decoration:none;border-radius:7px;font-size:14px;font-weight:650;">打开管理面板</a></td></tr>`, html.EscapeString(panelURL))
}
