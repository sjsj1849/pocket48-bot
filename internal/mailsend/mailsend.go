// Package mailsend delivers raw RFC822 messages via SMTP (preferred) or local sendmail.
// Windows / any host without sendmail should fill ALERT_EMAIL_SMTP_* and use SMTP auth.
package mailsend

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Config holds outbound mail settings (subset of bot ALERT_EMAIL_* keys).
type Config struct {
	From     string
	To       string
	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	// PanelURL is optional; callers embed it in the message body themselves.
	PanelURL string
}

// Send delivers a complete message (headers + body). Prefer SMTP when host is set;
// otherwise try /usr/sbin/sendmail (Linux/macOS with MTA). Windows needs SMTP.
func Send(cfg Config, message []byte) error {
	host := strings.TrimSpace(cfg.SMTPHost)
	if host != "" {
		return sendSMTP(cfg, message)
	}
	return sendSendmail(cfg.From, message)
}

func sendSendmail(from string, message []byte) error {
	path := "/usr/sbin/sendmail"
	if _, err := os.Stat(path); err != nil {
		path = "sendmail"
	}
	args := []string{"-t", "-oi"}
	if strings.TrimSpace(from) != "" {
		args = append(args, "-f", from)
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(string(message))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("sendmail unavailable or failed (%w: %s); set ALERT_EMAIL_SMTP_HOST/PORT/USER/PASSWORD for cross-platform SMTP", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sendSMTP(cfg Config, message []byte) error {
	host := strings.TrimSpace(cfg.SMTPHost)
	port := cfg.SMTPPort
	if port <= 0 {
		port = 587
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	user := strings.TrimSpace(cfg.SMTPUser)
	if user == "" {
		user = strings.TrimSpace(cfg.From)
	}
	pass := cfg.SMTPPass
	from := strings.TrimSpace(cfg.From)
	if from == "" {
		return fmt.Errorf("ALERT_EMAIL_FROM empty")
	}

	// Port 465: implicit TLS. Others: plain dial then STARTTLS when supported.
	var (
		client *smtp.Client
		err    error
	)
	if port == 465 {
		tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		conn, dialErr := tls.DialWithDialer(&net.Dialer{Timeout: 20 * time.Second}, "tcp", addr, tlsCfg)
		if dialErr != nil {
			return fmt.Errorf("smtp tls dial %s: %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return err
		}
	} else {
		conn, dialErr := net.DialTimeout("tcp", addr, 20*time.Second)
		if dialErr != nil {
			return fmt.Errorf("smtp dial %s: %w", addr, dialErr)
		}
		client, err = smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return err
		}
		// Local postfix (127.0.0.1/localhost) often offers STARTTLS with a
		// non-matching cert; skip STARTTLS for loopback so port 25 works.
		if ok, _ := client.Extension("STARTTLS"); ok && !isLoopbackSMTPHost(host) {
			tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
			if err := client.StartTLS(tlsCfg); err != nil {
				_ = client.Close()
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	defer client.Close()

	// Only AUTH when a password is configured (local MTA usually needs none).
	if pass != "" {
		auth := smtp.PlainAuth("", user, pass, host)
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	to := strings.TrimSpace(cfg.To)
	if to == "" {
		return fmt.Errorf("ALERT_EMAIL_TO empty")
	}
	// Support comma-separated recipients.
	for _, rcpt := range strings.Split(to, ",") {
		rcpt = strings.TrimSpace(rcpt)
		if rcpt == "" {
			continue
		}
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT %s: %w", rcpt, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(message); err != nil {
		_ = w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func isLoopbackSMTPHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	return h == "localhost" || h == "127.0.0.1" || h == "::1" || h == "[::1]"
}

// NormalizePanelURL returns a trimmed panel base URL or empty.
func NormalizePanelURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}
