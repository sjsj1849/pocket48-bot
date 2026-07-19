package logic

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"log"
	"mime"
	"net/mail"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"pocket48-bot/internal/config"
	"pocket48-bot/internal/mailsend"
	"pocket48-bot/internal/monitor"
	"pocket48-bot/internal/napcat"
	"pocket48-bot/internal/pocket48"
	"pocket48-bot/internal/storage"
)

var qqFaceNameToID = map[string]string{
	"微笑":  "14",
	"撇嘴":  "1",
	"色":   "2",
	"发呆":  "3",
	"得意":  "4",
	"流泪":  "5",
	"害羞":  "6",
	"闭嘴":  "7",
	"睡":   "8",
	"大哭":  "9",
	"尴尬":  "10",
	"尬笑":  "10",
	"捂脸":  "10",
	"发怒":  "11",
	"调皮":  "12",
	"呲牙":  "13",
	"惊讶":  "15",
	"酷":   "16",
	"冷汗":  "96",
	"抓狂":  "18",
	"吐":   "19",
	"偷笑":  "20",
	"可爱":  "21",
	"白眼":  "22",
	"傲慢":  "23",
	"饥饿":  "24",
	"困":   "25",
	"惊恐":  "26",
	"流汗":  "27",
	"汗":   "27",
	"憨笑":  "28",
	"悠闲":  "29",
	"奋斗":  "30",
	"咒骂":  "31",
	"疑问":  "32",
	"嘘":   "33",
	"晕":   "34",
	"折磨":  "35",
	"衰":   "36",
	"骷髅":  "37",
	"敲打":  "38",
	"再见":  "39",
	"擦汗":  "97",
	"抠鼻":  "98",
	"鼓掌":  "99",
	"坏笑":  "100",
	"左哼哼": "101",
	"右哼哼": "102",
	"哈欠":  "103",
	"鄙视":  "104",
	"委屈":  "105",
	"快哭了": "106",
	"阴险":  "108",
	"亲亲":  "109",
	"可怜":  "111",
	"菜刀":  "112",
	"西瓜":  "113",
	"啤酒":  "114",
	"篮球":  "115",
	"乒乓":  "116",
	"咖啡":  "60",
	"饭":   "61",
	"猪头":  "46",
	"玫瑰":  "63",
	"凋谢":  "64",
	"嘴唇":  "67",
	"爱心":  "66",
	"心碎":  "65",
	"蛋糕":  "53",
	"闪电":  "54",
	"炸弹":  "55",
	"刀":   "56",
	"足球":  "57",
	"瓢虫":  "117",
	"便便":  "59",
	"月亮":  "75",
	"太阳":  "74",
	"礼物":  "69",
	"拥抱":  "49",
	"强":   "76",
	"弱":   "77",
	"握手":  "78",
	"胜利":  "79",
	"抱拳":  "118",
	"勾引":  "119",
	"拳头":  "120",
	"差劲":  "121",
	"爱你":  "122",
	"NO":  "123",
	"OK":  "124",
	"转圈":  "125",
	"磕头":  "126",
	"回头":  "127",
	"跳绳":  "128",
	"挥手":  "129",
	"激动":  "130",
	"街舞":  "131",
	"献吻":  "132",
	"左太极": "133",
	"右太极": "134",
}

var pocketMobilePattern = regexp.MustCompile(`^1\d{10}$`)

func (b *Bot) reply(event *napcat.Event, msg string) {
	if event.MessageType == "group" {
		b.napcat.SendGroupMessage(event.GroupID, napcat.TextSegment(msg))
	} else if event.MessageType == "private" {
		b.napcat.SendPrivateMessage(event.UserID, napcat.TextSegment(msg))
	}
}

func (b *Bot) notifyAdmins(msg string) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	// Service-status alerts only → email. Boot/shutdown/business reports use notifyAdminsQQ.
	if err := sendAdminAlertEmail(b.cfg, msg); err != nil {
		log.Printf("[alert-email] send failed: %v; message=%s", err, truncateForLog(msg, 200))
	}
}

// notifyAdminsQQ sends non-alert notices to admin QQ (startup/shutdown, reports, private-style status).
func (b *Bot) notifyAdminsQQ(msg string) {
	b.notifyQQUsers(msg, b.collectAdminRecipients()...)
}

// notifyQQUsers sends a private QQ message to the given user IDs (deduped, skips 0).
func (b *Bot) notifyQQUsers(msg string, uids ...int64) {
	msg = strings.TrimSpace(msg)
	if msg == "" || b == nil || b.napcat == nil {
		return
	}
	seen := make(map[int64]struct{}, len(uids))
	for _, uid := range uids {
		if uid == 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		b.napcat.SendPrivateMessage(uid, napcat.TextSegment(msg))
	}
}

// collectWeiboSuperCountQQRecipients returns admin QQ plus optional WEIBO_SUPER_COUNT_QQ extras.
func (b *Bot) collectWeiboSuperCountQQRecipients() []int64 {
	out := b.collectAdminRecipients()
	if b == nil || b.cfg == nil {
		return out
	}
	extra := strings.FieldsFunc(b.cfg.WeiboSuperCountQQ, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '	' || r == ';' || r == '|'
	})
	seen := make(map[int64]struct{}, len(out))
	for _, uid := range out {
		seen[uid] = struct{}{}
	}
	for _, raw := range extra {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		uid, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || uid == 0 {
			continue
		}
		if _, ok := seen[uid]; ok {
			continue
		}
		seen[uid] = struct{}{}
		out = append(out, uid)
	}
	return out
}

func truncateForLog(s string, max int) string {
	runes := []rune(strings.TrimSpace(s))
	if max <= 0 || len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "…"
}


func mailConfigFrom(cfg *config.Config) mailsend.Config {
	if cfg == nil {
		return mailsend.Config{}
	}
	return mailsend.Config{
		From:     strings.TrimSpace(cfg.AlertEmailFrom),
		To:       strings.TrimSpace(cfg.AlertEmailTo),
		SMTPHost: strings.TrimSpace(cfg.AlertEmailSMTPHost),
		SMTPPort: cfg.AlertEmailSMTPPort,
		SMTPUser: strings.TrimSpace(cfg.AlertEmailSMTPUser),
		SMTPPass: cfg.AlertEmailSMTPPassword,
		PanelURL: mailsend.NormalizePanelURL(cfg.AdminPanelURL),
	}
}

func panelLinkLine(panelURL string) string {
	if panelURL == "" {
		return ""
	}
	return "\n管理面板：" + panelURL + "\n"
}

func panelButtonHTML(panelURL string) string {
	panelURL = mailsend.NormalizePanelURL(panelURL)
	if panelURL == "" {
		return ""
	}
	return fmt.Sprintf(`<tr><td style="padding:0 32px 32px;" align="center"><a href="%s" style="display:inline-block;padding:11px 17px;background:#3478d4;color:#ffffff;text-decoration:none;border-radius:7px;font-size:14px;font-weight:650;">打开管理面板</a></td></tr>`, html.EscapeString(panelURL))
}

// sendAdminAlertEmail uses the same ALERT_EMAIL_* config (SMTP or sendmail) as pocket48-admin.
func sendAdminAlertEmail(cfg *config.Config, body string) error {
	if cfg == nil || !cfg.AlertEmailEnabled {
		return fmt.Errorf("email alert disabled")
	}
	to := strings.TrimSpace(cfg.AlertEmailTo)
	from := strings.TrimSpace(cfg.AlertEmailFrom)
	if to == "" {
		return fmt.Errorf("ALERT_EMAIL_TO empty")
	}
	if from == "" {
		return fmt.Errorf("ALERT_EMAIL_FROM empty")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("invalid ALERT_EMAIL_TO: %w", err)
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("invalid ALERT_EMAIL_FROM: %w", err)
	}

	// Title: first non-empty line; strip leading emoji for cleaner subject.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	title := "Bot 告警"
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			title = line
			break
		}
	}
	subjectTitle := strings.TrimSpace(strings.TrimLeft(title, "⚠️⚠︎ "))
	if subjectTitle == "" {
		subjectTitle = title
	}
	if len([]rune(subjectTitle)) > 48 {
		subjectTitle = string([]rune(subjectTitle)[:48]) + "…"
	}
	subject := mime.QEncoding.Encode("utf-8", "Pocket48 告警｜"+subjectTitle)

	now := time.Now()
	timeText := now.Format("2006-01-02 15:04:05 MST")
	// Detail = body without the first title line (if multi-line).
	detail := strings.TrimSpace(body)
	if idx := strings.Index(detail, "\n"); idx >= 0 {
		detail = strings.TrimSpace(detail[idx+1:])
	} else {
		detail = ""
	}
	if detail == "" {
		detail = title
	}

	panel := mailsend.NormalizePanelURL(cfg.AdminPanelURL)
	plain := fmt.Sprintf("Pocket48 告警\n\n%s\n\n详情：\n%s\n\n时间：%s%s",
		title, detail, timeText, panelLinkLine(panel))

	esc := html.EscapeString
	// Keep multi-line detail readable in HTML.
	detailHTML := strings.ReplaceAll(esc(detail), "\n", "<br/>")
	htmlBody := fmt.Sprintf(`<!doctype html>
<html><body style="margin:0;padding:0;background:#f5f7fb;color:#172033;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',Arial,sans-serif;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#f5f7fb;padding:32px 12px;"><tr><td align="center">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:600px;background:#ffffff;border:1px solid #e5eaf2;border-radius:8px;overflow:hidden;box-shadow:0 10px 30px rgba(31,52,89,.06);">
<tr><td style="height:4px;background:#3478d4;font-size:0;line-height:0;">&nbsp;</td></tr>
<tr><td style="padding:28px 32px 18px;">
<table role="presentation" cellspacing="0" cellpadding="0"><tr><td style="width:36px;height:36px;border-radius:8px;background:#3478d4;color:#fff;font-size:13px;font-weight:700;text-align:center;vertical-align:middle;">P48</td><td style="padding-left:12px;font-size:17px;font-weight:650;color:#172033;">Pocket48 Console</td></tr></table>
</td></tr>
<tr><td style="padding:4px 32px 28px;">
<span style="display:inline-block;padding:5px 9px;border-radius:6px;background:#edf4ff;color:#2466b3;font-size:12px;font-weight:650;">需要处理</span>
<h1 style="margin:14px 0 8px;font-size:25px;line-height:1.35;letter-spacing:0;color:#172033;">%s</h1>
<p style="margin:0;color:#667085;font-size:14px;line-height:1.7;">Bot 业务告警（非日常启动通知）。请根据下方详情检查对应链路。</p>
</td></tr>
<tr><td style="padding:0 32px 28px;">
<table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="border-collapse:separate;border-spacing:0;background:#f8fafc;border:1px solid #e7ebf1;border-radius:8px;">
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;width:72px;">类型</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;font-weight:650;">Bot 告警</td></tr>
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;">标题</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;">%s</td></tr>
<tr><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#7a8495;font-size:13px;vertical-align:top;">详情</td><td style="padding:15px 18px;border-bottom:1px solid #e7ebf1;color:#172033;font-size:14px;line-height:1.6;">%s</td></tr>
<tr><td style="padding:15px 18px;color:#7a8495;font-size:13px;">时间</td><td style="padding:15px 18px;color:#172033;font-size:14px;">%s</td></tr>
</table>
</td></tr>
%s
<tr><td style="padding:20px 32px;border-top:1px solid #edf0f4;color:#98a1af;font-size:12px;line-height:1.6;">这是一封由 Pocket48 Console 自动发送的告警通知。</td></tr>
</table>
</td></tr></table>
</body></html>`, esc(title), esc(title), detailHTML, esc(timeText), panelButtonHTML(panel))

	const boundary = "pocket48-bot-alert-alternative"
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, to, subject)
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, plain)
	fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", boundary, htmlBody)
	fmt.Fprintf(&buf, "--%s--\r\n", boundary)

	mc := mailConfigFrom(cfg)
	mc.From, mc.To = from, to
	if err := mailsend.Send(mc, buf.Bytes()); err != nil {
		return err
	}
	log.Printf("[alert-email] sent to=%s subject=%s", to, subjectTitle)
	return nil
}

// emailAttachment is an optional file attachment for admin report emails.
type emailAttachment struct {
	Name        string
	ContentType string
	// Text body (used when Data is empty). For binary attachments use Data.
	Text string
	// Binary body (PNG etc.). Preferred over Text when non-empty.
	Data []byte
}

// sendAdminHTMLEmail sends a general HTML report (not an "alert" badge).
// Optional attachments may be text (txt) or binary (png).
func sendAdminHTMLEmail(cfg *config.Config, subjectTitle, htmlBody, plainBody string, attachments ...emailAttachment) error {
	if cfg == nil || !cfg.AlertEmailEnabled {
		return fmt.Errorf("email alert disabled")
	}
	to := strings.TrimSpace(cfg.AlertEmailTo)
	from := strings.TrimSpace(cfg.AlertEmailFrom)
	if to == "" {
		return fmt.Errorf("ALERT_EMAIL_TO empty")
	}
	if from == "" {
		return fmt.Errorf("ALERT_EMAIL_FROM empty")
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return fmt.Errorf("invalid ALERT_EMAIL_TO: %w", err)
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("invalid ALERT_EMAIL_FROM: %w", err)
	}
	subjectTitle = strings.TrimSpace(subjectTitle)
	if subjectTitle == "" {
		subjectTitle = "Pocket48 日报"
	}
	if len([]rune(subjectTitle)) > 48 {
		subjectTitle = string([]rune(subjectTitle)[:48]) + "…"
	}
	subject := mime.QEncoding.Encode("utf-8", subjectTitle)
	if plainBody == "" {
		plainBody = subjectTitle
	}

	const mixedBoundary = "pocket48-bot-mixed"
	const altBoundary = "pocket48-bot-alt"
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\nTo: %s\r\nSubject: %s\r\n", from, to, subject)
	buf.WriteString("MIME-Version: 1.0\r\n")

	hasAttach := false
	for _, a := range attachments {
		if len(a.Data) > 0 || strings.TrimSpace(a.Text) != "" {
			hasAttach = true
			break
		}
	}

	if hasAttach {
		fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixedBoundary)
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n", mixedBoundary, altBoundary)
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", altBoundary, plainBody)
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", altBoundary, htmlBody)
		fmt.Fprintf(&buf, "--%s--\r\n", altBoundary)
		for _, a := range attachments {
			if len(a.Data) == 0 && strings.TrimSpace(a.Text) == "" {
				continue
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				if len(a.Data) > 0 {
					name = "report.png"
				} else {
					name = "report.txt"
				}
			}
			ct := strings.TrimSpace(a.ContentType)
			if ct == "" {
				if strings.HasSuffix(strings.ToLower(name), ".png") {
					ct = "image/png"
				} else if strings.HasSuffix(strings.ToLower(name), ".jpg") || strings.HasSuffix(strings.ToLower(name), ".jpeg") {
					ct = "image/jpeg"
				} else {
					ct = "text/plain; charset=UTF-8"
				}
			}
			dispName := mime.QEncoding.Encode("utf-8", name)
			if len(a.Data) > 0 {
				fmt.Fprintf(&buf, "--%s\r\nContent-Type: %s; name=%q\r\n", mixedBoundary, ct, name)
				fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%s\r\n", dispName)
				buf.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
				// RFC 2045: base64 lines <= 76 chars
				enc := base64.StdEncoding.EncodeToString(a.Data)
				for i := 0; i < len(enc); i += 76 {
					end := i + 76
					if end > len(enc) {
						end = len(enc)
					}
					buf.WriteString(enc[i:end])
					buf.WriteString("\r\n")
				}
			} else {
				fmt.Fprintf(&buf, "--%s\r\nContent-Type: %s; name=%q\r\n", mixedBoundary, ct, name)
				fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=%s\r\n", dispName)
				buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
				buf.WriteString(a.Text)
				if !strings.HasSuffix(a.Text, "\n") {
					buf.WriteString("\r\n")
				} else {
					buf.WriteString("\r\n")
				}
			}
		}
		fmt.Fprintf(&buf, "--%s--\r\n", mixedBoundary)
	} else {
		fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", altBoundary)
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", altBoundary, plainBody)
		fmt.Fprintf(&buf, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s\r\n", altBoundary, htmlBody)
		fmt.Fprintf(&buf, "--%s--\r\n", altBoundary)
	}

	mc := mailConfigFrom(cfg)
	mc.From, mc.To = from, to
	if err := mailsend.Send(mc, buf.Bytes()); err != nil {
		return err
	}
	log.Printf("[report-email] sent to=%s subject=%s attachments=%d", to, subjectTitle, len(attachments))
	return nil
}

func (b *Bot) notifyAdminsEmailReport(subject, htmlBody, plainBody string, attachments ...emailAttachment) {
	if err := sendAdminHTMLEmail(b.cfg, subject, htmlBody, plainBody, attachments...); err != nil {
		log.Printf("[report-email] send failed: %v; subject=%s", err, truncateForLog(subject, 80))
	}
}

// renderHTMLToPNG renders HTML to PNG via Playwright Chromium (scripts/html_to_png.mjs).
func renderHTMLToPNG(htmlBody string) ([]byte, error) {
	htmlBody = strings.TrimSpace(htmlBody)
	if htmlBody == "" {
		return nil, fmt.Errorf("empty html")
	}
	tmpDir, err := os.MkdirTemp("", "pocket48-report-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	htmlPath := filepath.Join(tmpDir, "report.html")
	pngPath := filepath.Join(tmpDir, "report.png")
	if err := os.WriteFile(htmlPath, []byte(htmlBody), 0o600); err != nil {
		return nil, err
	}
	// Prefer repo-relative script (bot WorkingDirectory is /root/pocket48-bot)
	script := "scripts/html_to_png.mjs"
	if _, err := os.Stat(script); err != nil {
		// fallback absolute
		script = "/root/pocket48-bot/scripts/html_to_png.mjs"
	}
	cmd := exec.Command("node", script, htmlPath, pngPath, "920")
	cmd.Env = append(os.Environ(),
		"PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("html_to_png: %w: %s", err, strings.TrimSpace(string(out)))
	}
	data, err := os.ReadFile(pngPath)
	if err != nil {
		return nil, err
	}
	if len(data) < 100 {
		return nil, fmt.Errorf("png too small (%d bytes): %s", len(data), strings.TrimSpace(string(out)))
	}
	return data, nil
}

func (b *Bot) collectAdminRecipients() []int64 {
	seen := make(map[int64]struct{})
	out := make([]int64, 0, 1+len(b.cfg.AdminQQ))
	if b.cfg.SuperAdmin != 0 {
		seen[b.cfg.SuperAdmin] = struct{}{}
		out = append(out, b.cfg.SuperAdmin)
	}
	for _, admin := range b.cfg.AdminQQ {
		if admin == 0 {
			continue
		}
		if _, ok := seen[admin]; ok {
			continue
		}
		seen[admin] = struct{}{}
		out = append(out, admin)
	}
	return out
}

type cachedUserDetail struct {
	info      *pocket48.UserDetailInfo
	expiresAt time.Time
}

type cachedRoomInfo struct {
	info      *pocket48.RoomInfo
	expiresAt time.Time
}

type GiftEventRecord struct {
	Timestamp      int64
	SpeakerUserID  int64
	SpeakerName    string
	GiftName       string
	GiftNum        int64
	ChickenLegUnit int64
	ChickenLegs    int64
	LegSource      string
}

type AnnualScoreGift struct {
	GiftID       int64
	GiftName     string
	GiftNum      int64
	ReceiverName string
	UnitScore    float64
	TotalScore   float64
}

type LiveGiftSession struct {
	LiveID        string
	LiveRoomID    int64
	LiveOwnerID   int64
	LiveOwnerName string
	StartedAt     int64
	Events        []GiftEventRecord
	ChickenLegs   int64
	AnnualScore   float64
	PeakOnline    int64
	Ended         bool
}

type qchatPendingIdentity struct {
	Account string
	SeenAt  time.Time
}

type qchatRESTIdentity struct {
	UserID   int64
	Nickname string
	SeenAt   time.Time
}

type Bot struct {
	cfg                *config.Config
	pocket             *pocket48.Client
	napcat             *napcat.Client
	weiboMonitor       *monitor.WeiboMonitor
	storage            *storage.Storage
	nimDanmaku         *NimDanmakuBridge
	weiboAuth          *WeiboAuthBridge
	douyinMonitor      *DouyinMonitor
	xiaohongshuMonitor *XiaohongshuMonitor

	lastMsgTime            map[int64]int64
	cursorLoaded           map[int64]bool
	onMicState             map[int64]bool
	onMicLastCheck         map[int64]time.Time
	userDetailCache        map[int64]cachedUserDetail
	roomInfoCache          map[int64]cachedRoomInfo
	seenMessageIDs         map[string]time.Time
	qchatOwnerIdentities   map[int64]storage.QChatIdentity
	qchatIdentityLoaded    map[int64]bool
	qchatPendingIdentities map[string]qchatPendingIdentity
	qchatRESTIdentities    map[string]qchatRESTIdentity
	roomMediaWG            sync.WaitGroup
	roomRealtimeOrderMu    sync.Mutex
	roomRealtimeTails      map[int64]chan struct{}
	pendingPocketSMSMobile string
	pocketAuthExpired      bool
	lastWeiboAuthErrorAt     time.Time
	lastWeiboAuthRestartAt  time.Time
	weiboAutoSignMu        sync.Mutex
	mu                     sync.RWMutex

	isMonitoring      bool
	isLiveMonitoring  bool
	pollingInterval   time.Duration
	fastInterval      time.Duration // fast polling interval (~300ms) when messages detected
	pollFastMode      bool          // use fastInterval temporarily
	pollFastRemaining int32         // remaining fast cycles

	memberEnterTimes map[string]time.Time // userId -> enter time, for calculating watch duration
	memberEnterMu    sync.Mutex
	liveSessions     map[int64]*LiveGiftSession // Pocket48 room id -> current live statistics
	liveSessionsMu   sync.Mutex
}

func NewBot(cfg *config.Config) *Bot {
	interval := time.Duration(cfg.PollingInterval) * time.Second
	if interval <= 0 {
		interval = 3 * time.Second
	}

	napcatClient := napcat.NewClient(cfg)
	weiboMon := monitor.NewWeiboMonitor(napcatClient)
	if cfg.WeiboCookie != "" {
		weiboMon.SetCookie(cfg.WeiboCookie)
	}
	if cfg.WeiboMWeiboCookie != "" {
		weiboMon.SetMWeiboCookie(cfg.WeiboMWeiboCookie)
	}
	if cfg.WeiboApp != nil {
		weiboMon.SetAppAuth(&monitor.WeiboAppAuth{
			RawCapture:     cfg.WeiboApp.RawCapture,
			Host:           cfg.WeiboApp.Host,
			RequestPath:    cfg.WeiboApp.RequestPath,
			RequestBody:    cfg.WeiboApp.RequestBody,
			CapturedOID:    cfg.WeiboApp.CapturedOID,
			Authorization:  cfg.WeiboApp.Authorization,
			GSID:           cfg.WeiboApp.GSID,
			Aid:            cfg.WeiboApp.Aid,
			S:              cfg.WeiboApp.S,
			XSessionID:     cfg.WeiboApp.XSessionID,
			XValidator:     cfg.WeiboApp.XValidator,
			XShanhaiPass:   cfg.WeiboApp.XShanhaiPass,
			XLogUID:        cfg.WeiboApp.XLogUID,
			XEngineType:    cfg.WeiboApp.XEngineType,
			CronetRID:      cfg.WeiboApp.CronetRID,
			SNRT:           cfg.WeiboApp.SNRT,
			AcceptLanguage: cfg.WeiboApp.AcceptLanguage,
			AcceptEncoding: cfg.WeiboApp.AcceptEncoding,
			UserAgent:      cfg.WeiboApp.UserAgent,
		})
	}

	// 初始化存储
	storageDir := "storage"
	cosDir := "/lhcos-data/bot48"
	botStorage := storage.NewStorage(storageDir, cosDir)

	if botStorage.IsCOSAvailable() {
		log.Println("✅ COS storage available, will archive messages")
	} else {
		log.Println("⚠️ COS not available, running in degraded mode")
	}
	bot := &Bot{
		cfg:                    cfg,
		pocket:                 pocket48.NewClient(cfg),
		napcat:                 napcatClient,
		weiboMonitor:           weiboMon,
		storage:                botStorage,
		nimDanmaku:             NewNimDanmakuBridge(cfg),
		weiboAuth:              NewWeiboAuthBridge(cfg),
		lastMsgTime:            make(map[int64]int64),
		cursorLoaded:           make(map[int64]bool),
		onMicState:             make(map[int64]bool),
		onMicLastCheck:         make(map[int64]time.Time),
		userDetailCache:        make(map[int64]cachedUserDetail),
		roomInfoCache:          make(map[int64]cachedRoomInfo),
		seenMessageIDs:         make(map[string]time.Time),
		qchatOwnerIdentities:   make(map[int64]storage.QChatIdentity),
		qchatIdentityLoaded:    make(map[int64]bool),
		qchatPendingIdentities: make(map[string]qchatPendingIdentity),
		qchatRESTIdentities:    make(map[string]qchatRESTIdentity),
		roomRealtimeTails:      make(map[int64]chan struct{}),
		memberEnterTimes:       make(map[string]time.Time),
		liveSessions:           make(map[int64]*LiveGiftSession),
		isMonitoring:           true,
		isLiveMonitoring:       cfg.LiveMonitoring,
		pollingInterval:        interval,
		fastInterval:           300 * time.Millisecond,
	}
	bot.douyinMonitor = NewDouyinMonitor(cfg, napcatClient, bot.notifyAdmins)
	bot.douyinMonitor.SetBrowserBridge(bot.weiboAuth)
	bot.douyinMonitor.SetRequestBotRestart(func(reason string) {
		log.Printf("[Bot] restart requested: %s", reason)
		// systemd Restart=always will bring the process back.
		go func() {
			time.Sleep(800 * time.Millisecond)
			os.Exit(1)
		}()
	})
	bot.weiboAuth.SetDouyinCallback(bot.douyinMonitor.HandleBrowserEvent)
	bot.xiaohongshuMonitor = NewXiaohongshuMonitor(cfg, napcatClient, bot.notifyAdmins)
	bot.xiaohongshuMonitor.SetBrowserBridge(bot.weiboAuth)
	bot.weiboAuth.SetXiaohongshuCallback(bot.xiaohongshuMonitor.HandleBrowserEvent)
	weiboMon.OnCookieInvalid = bot.notifyWeiboCookieInvalid
	bot.weiboAuth.SetCallbacks(
		bot.handleWeiboAuthCookies,
		bot.handleWeiboAuthQRCode,
		bot.handleWeiboAuthStatus,
		bot.handleWeiboAuthError,
	)
	if cfg.NIMRoomMessageEnabled {
		roomIDs := make(map[int64]struct{})
		for _, rooms := range cfg.GroupSubscriptions {
			for _, roomID := range rooms {
				roomIDs[roomID] = struct{}{}
			}
		}
		for roomID := range roomIDs {
			bot.loadQChatOwnerIdentity(roomID)
		}
	}

	// Set up welcome new member callback
	napcatClient.OnMemberJoin = bot.handleMemberJoin

	return bot
}

func (b *Bot) LogInfo(format string, v ...interface{}) {
	log.Printf("[INFO] "+format, v...)
}

func (b *Bot) getCachedUserDetail(userID int64) (*pocket48.UserDetailInfo, error) {
	if userID == 0 {
		return nil, nil
	}

	now := time.Now()
	b.mu.RLock()
	cached, ok := b.userDetailCache[userID]
	b.mu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.info, nil
	}

	detailInfo, err := b.pocket.GetUserDetailInfo(userID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.userDetailCache[userID] = cachedUserDetail{info: detailInfo, expiresAt: now.Add(10 * time.Minute)}
	b.mu.Unlock()
	return detailInfo, nil
}

func (b *Bot) getCachedRoomInfo(roomID int64) (*pocket48.RoomInfo, error) {
	now := time.Now()
	b.mu.RLock()
	cached, ok := b.roomInfoCache[roomID]
	b.mu.RUnlock()
	if ok && now.Before(cached.expiresAt) {
		return cached.info, nil
	}

	info, err := b.pocket.GetRoomInfoByChannelID(roomID)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	b.roomInfoCache[roomID] = cachedRoomInfo{info: info, expiresAt: now.Add(5 * time.Minute)}
	b.mu.Unlock()
	return info, nil
}





// reloadSubscriptions re-reads config.json and hot-applies fields that do not need a process restart.
// Subscription maps, report delivery, email/SMTP, poll intervals, and similar switches update here.
// NapCat URL/token, platform master switches, NIM/browser sidecar lifecycle still need full restart.
func (b *Bot) reloadSubscriptions() {
	cfg, err := config.LoadConfig(b.cfg.ConfigPath())
	if err != nil {
		b.LogInfo("热重载失败: %v", err)
		return
	}

	// Subscriptions (monitors read maps dynamically / next cycle)
	b.cfg.WeiboSubscriptions = cfg.WeiboSubscriptions
	b.cfg.GroupSubscriptions = cfg.GroupSubscriptions
	b.cfg.DouyinSubscriptions = cfg.DouyinSubscriptions
	b.cfg.XiaohongshuSubscriptions = cfg.XiaohongshuSubscriptions
	b.cfg.WeiboSuperPostSubscriptions = cfg.WeiboSuperPostSubscriptions
	b.cfg.WeiboSuperTopics = cfg.WeiboSuperTopics
	b.cfg.WeiboSuperCountTopics = cfg.WeiboSuperCountTopics
	b.cfg.WeiboSuperCountGroups = cfg.WeiboSuperCountGroups

	// Bot / alert / report (no process spawn)
	b.cfg.BoundGroupID = cfg.BoundGroupID
	b.cfg.CommandPrefix = cfg.CommandPrefix
	b.cfg.DisableGroupCommands = cfg.DisableGroupCommands
	b.cfg.MediaDelivery = cfg.MediaDelivery
	b.cfg.AdminQQ = cfg.AdminQQ
	b.cfg.SuperAdmin = cfg.SuperAdmin
	b.cfg.AdminPanelURL = cfg.AdminPanelURL
	b.cfg.AlertEmailEnabled = cfg.AlertEmailEnabled
	b.cfg.AlertEmailTo = cfg.AlertEmailTo
	b.cfg.AlertEmailFrom = cfg.AlertEmailFrom
	b.cfg.AlertEmailCooldownMinutes = cfg.AlertEmailCooldownMinutes
	b.cfg.AlertEmailSMTPHost = cfg.AlertEmailSMTPHost
	b.cfg.AlertEmailSMTPPort = cfg.AlertEmailSMTPPort
	b.cfg.AlertEmailSMTPUser = cfg.AlertEmailSMTPUser
	b.cfg.AlertEmailSMTPPassword = cfg.AlertEmailSMTPPassword

	// Weibo day-to-day toggles + cookies + report delivery
	b.cfg.WeiboCookie = cfg.WeiboCookie
	b.cfg.WeiboMWeiboCookie = cfg.WeiboMWeiboCookie
	b.cfg.WeiboBrowserRefreshMinutes = cfg.WeiboBrowserRefreshMinutes
	b.cfg.WeiboSuperAutoEnabled = cfg.WeiboSuperAutoEnabled
	b.cfg.WeiboSuperCountEnabled = cfg.WeiboSuperCountEnabled
	b.cfg.WeiboSuperCountDelivery = cfg.WeiboSuperCountDelivery
	b.cfg.WeiboSuperCountQQ = cfg.WeiboSuperCountQQ

	// Douyin / 小红书 poll + IM routing (not master enable)
	b.cfg.DouyinPollSeconds = cfg.DouyinPollSeconds
	b.cfg.DouyinIMEnabled = cfg.DouyinIMEnabled
	b.cfg.DouyinIMPrivateEnabled = cfg.DouyinIMPrivateEnabled
	b.cfg.DouyinIMGroupName = cfg.DouyinIMGroupName
	b.cfg.DouyinIMGroupNumber = cfg.DouyinIMGroupNumber
	b.cfg.XiaohongshuPollSeconds = cfg.XiaohongshuPollSeconds
	b.cfg.PollingInterval = cfg.PollingInterval

	// Pocket / NIM feature flags (read from b.cfg on each message / poll cycle)
	b.cfg.LiveMonitoring = cfg.LiveMonitoring
	b.cfg.NIMEnabled = cfg.NIMEnabled
	b.cfg.NIMRoomMessageEnabled = cfg.NIMRoomMessageEnabled
	b.cfg.NIMRoomMessagePollFallback = cfg.NIMRoomMessagePollFallback
	b.cfg.NIMLiveDanmakuEnabled = cfg.NIMLiveDanmakuEnabled
	b.cfg.NIMViewerEventEnabled = cfg.NIMViewerEventEnabled

	// Push cookies into weibo monitor if present
	if b.weiboMonitor != nil {
		if strings.TrimSpace(cfg.WeiboCookie) != "" {
			b.weiboMonitor.SetCookie(cfg.WeiboCookie)
		}
		if strings.TrimSpace(cfg.WeiboMWeiboCookie) != "" {
			b.weiboMonitor.SetMWeiboCookie(cfg.WeiboMWeiboCookie)
		}
	}

	b.LogInfo("热重载完成：订阅与可热更新配置已同步")
}

func (b *Bot) Start() error {
	// Recover in-progress live gift/score sessions before NIM live discovery.
	b.loadLiveSessionsFromDisk()

	// Connect to NapCat
	if err := b.napcat.Connect(); err != nil {
		return fmt.Errorf("failed to connect to NapCat: %v", err)
	}

	// Register Event Handlers
	b.napcat.OnGroupMessage = b.handleGroupMessage
	b.napcat.OnPrivateMessage = b.handlePrivateMessage
	// NapCat 断线/重连：后台静默自动重连，不在 QQ 私聊刷状态。
	// 面板看 bot.log 的 [NapCat] status=...；持续异常由 admin 邮件告警负责。

	// Login to Pocket48
	b.LogInfo("Checking Pocket48 login credentials...")
	if b.cfg.PocketToken != "" {
		b.LogInfo("Token found. Verifying...")
		if err := b.pocket.CheckToken(); err == nil {
			b.LogInfo("Token valid. Using existing token for authentication.")
			b.LogInfo("Pocket48 (Token Mode) logged in successfully")
			b.clearPocketAuthExpired()
			b.refreshNIMCredentials()
		} else if pocket48.IsInconclusiveTokenCheck(err) {
			// Dummy channel probe often returns "频道不存在"; keep token and continue.
			b.LogInfo("Pocket48 token check inconclusive (%v); keeping existing token", err)
			b.clearPocketAuthExpired()
			b.refreshNIMCredentials()
		} else {
			// Soft line for restart noise; only true 401003 escalates via handlePocketAuthError.
			b.LogInfo("Pocket48 token check failed: %v", err)
			if !b.tryPocketPasswordLogin() {
				b.handlePocketAuthError(err)
			}
		}
	} else if !b.tryPocketPasswordLogin() {
		b.warnPocketLoginRequired("Pocket48 Token 未配置，且账号密码自动登录不可用")
	}

	// Start Weibo monitor
	if b.migrateWeiboSuperPostSubscriptionsToBoundGroup() {
		if err := b.cfg.Save(); err != nil {
			log.Printf("Failed to migrate weibo superpost subscriptions to bound group: %v", err)
		} else {
			log.Printf("Migrated weibo superpost subscriptions from group 0 to bound group %d", b.cfg.BoundGroupID)
		}
	}

	if len(b.cfg.WeiboSubscriptions) > 0 || len(b.cfg.WeiboSuperPostSubscriptions) > 0 {
		b.LogInfo("Starting Weibo monitor...")
		for groupID, weiboConfigs := range b.cfg.WeiboSubscriptions {
			for uid, weiboConfig := range weiboConfigs {
				gid := groupID
				onNew := func(u, lastID string) {
					if b.cfg.WeiboSubscriptions[gid] != nil && b.cfg.WeiboSubscriptions[gid][u] != nil {
						b.cfg.WeiboSubscriptions[gid][u].LastID = lastID
						b.cfg.Save()
					}
				}

				if err := b.weiboMonitor.AddConfig(gid, uid, weiboConfig.AtAll, weiboConfig.LastID, onNew); err != nil {
					log.Printf("Failed to add weibo config for group %d, uid %s: %v", gid, uid, err)
				} else {
					b.LogInfo("Added weibo monitor for group %d, uid: %s", gid, uid)
				}
			}
		}
		for groupID, superPostConfigs := range b.cfg.WeiboSuperPostSubscriptions {
			for key, superPostConfig := range superPostConfigs {
				gid := groupID
				onNew := func(uid, oid, lastPostID string) {
					cfgs := b.cfg.WeiboSuperPostSubscriptions[gid]
					if cfgs != nil && cfgs[key] != nil {
						cfgs[key].LastPostID = lastPostID
						b.cfg.Save()
					}
				}
				if err := b.weiboMonitor.AddSuperPostConfig(gid, superPostConfig.UID, superPostConfig.OID, superPostConfig.Name, superPostConfig.AtAll, superPostConfig.LastPostID, onNew); err != nil {
					log.Printf("Failed to add weibo superpost config for group %d, key %s: %v", gid, key, err)
				} else {
					b.LogInfo("Added weibo superpost monitor for group %d, key: %s", gid, key)
				}
			}
		}
		b.weiboMonitor.Start()
	}
	go b.runWeiboSuperAutoSignLoop()
	go b.runWeiboSuperCountDailyPushLoop()
	go b.runWeiboAppAuthHealthCheckLoop()
	if b.cfg.WeiboBrowserAuthEnabled || b.cfg.DouyinEnabled || b.cfg.XiaohongshuEnabled {
		go b.startWeiboAuthBridge()
	}
	if b.cfg.DouyinEnabled {
		go func() {
			if err := b.douyinMonitor.Start(); err != nil {
				log.Printf("[Douyin] monitor startup failed: %v", err)
				b.notifyAdmins(fmt.Sprintf("⚠️ 抖音监控侧卡启动失败：%v", err))
			}
		}()
	}
	if b.cfg.DouyinIMEnabled && b.douyinMonitor != nil {
		b.douyinMonitor.StartIMWatchdog()
	}

	// Start Polling Loop
	go b.pollLoop()

	// Start media cache cleanup
	go b.runMediaCleanupLoop()

	// Start the NIM sidecar (live chatrooms and/or QChat room messages).
	if b.cfg.NIMEnabled || b.cfg.NIMRoomMessageEnabled {
		go b.startNIMBridge()
	}

	// Startup Notification
	startTime := time.Now()
	startTimeStr := startTime.Format("2006-01-02 15:04:05")
	lastTimeStr := "无 (首次启动)"
	if b.cfg.LastStartupTime > 0 {
		lastTimeStr = time.Unix(b.cfg.LastStartupTime, 0).Format("2006-01-02 15:04:05")
	}

	startupMsg := fmt.Sprintf("🤖 机器人已启动\n本次启动时间：%s\n上次启动时间：%s", startTimeStr, lastTimeStr)
	b.notifyAdminsQQ(startupMsg)

	// Update LastStartupTime
	b.cfg.LastStartupTime = startTime.Unix()
	b.cfg.Save()

	// Graceful Shutdown Logic
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, syscall.SIGINT, syscall.SIGTERM)
	// SIGHUP goroutine for config hot-reload (no restart needed)
	go func() {
		sighup := make(chan os.Signal, 1)
		signal.Notify(sighup, syscall.SIGHUP)
		for range sighup {
			b.reloadSubscriptions()
		}
	}()

	sig := <-stopChan
	b.LogInfo("Received signal: %v. Shutting down...", sig)
	if b.cfg.WeiboBrowserAuthEnabled || b.cfg.DouyinEnabled || b.cfg.XiaohongshuEnabled {
		b.weiboAuth.BeginStop()
	}

	// Runtime Calculation
	runTime := time.Since(startTime)
	hours := int(runTime.Hours())
	minutes := int(runTime.Minutes()) % 60
	seconds := int(runTime.Seconds()) % 60
	runTimeStr := fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)

	// Send Shutdown Notification
	shutdownMsg := fmt.Sprintf("⚠️ 机器人即将下线，服务暂时不可用。\n本次运行时间：%s", runTimeStr)
	b.notifyAdminsQQ(shutdownMsg)
	time.Sleep(1 * time.Second)

	b.cfg.Save()
	if b.cfg.NIMEnabled || b.cfg.NIMRoomMessageEnabled {
		b.nimDanmaku.Stop()
	}
	if b.cfg.WeiboBrowserAuthEnabled || b.cfg.DouyinEnabled || b.cfg.XiaohongshuEnabled {
		b.weiboAuth.Stop()
	}
	if b.douyinMonitor != nil {
		b.douyinMonitor.Stop()
	}
	b.roomMediaWG.Wait()

	return nil
}

func (b *Bot) handleGroupMessage(event *napcat.Event) {

	// Multi-group support: allow all groups

	// If group commands are disabled, don't respond to any group commands
	if b.cfg.DisableGroupCommands {
		return
	}

	// Check for at message and translate to command
	msg := strings.TrimSpace(event.RawMessage)

	// Check if message contains at bot
	if strings.Contains(msg, "[CQ:at,qq=") || strings.Contains(msg, "[CQ:at,all]") {
		cleanMsg := strings.ReplaceAll(msg, "[CQ:at,qq=3808515247]", "")
		cleanMsg = strings.ReplaceAll(cleanMsg, "[CQ:at,all]", "")
		cleanMsg = strings.TrimSpace(cleanMsg)
		if cleanMsg != "" {
			if b.tryHandleNaturalLanguage(event, cleanMsg) {
				return
			}
		}
	}

	// Check for standard command prefix
	prefix := b.cfg.CommandPrefix

	if strings.HasPrefix(msg, prefix) {
		args := parseCommandArgs(msg[len(prefix):])
		if len(args) > 0 {
			b.handleCommand(event, args)
		}
	}
}

func (b *Bot) warnPocketLoginRequired(reason string) {
	b.mu.Lock()
	if b.pocketAuthExpired {
		b.mu.Unlock()
		return
	}
	b.pocketAuthExpired = true
	b.mu.Unlock()

	log.Printf("[Pocket48] authorization unavailable: %s", reason)
	b.notifyAdmins(fmt.Sprintf("⚠️ Pocket48 授权已过期，房间消息轮询已暂停。\n原因: %s\n请手动登录：\nbot login sms <手机号>\n收到验证码后发送：bot code <验证码>", reason))
}

func (b *Bot) handlePocketAuthError(err error) bool {
	if !pocket48.IsAuthorizationExpired(err) {
		return false
	}
	b.warnPocketLoginRequired(err.Error())
	return true
}

func (b *Bot) clearPocketAuthExpired() {
	b.mu.Lock()
	b.pocketAuthExpired = false
	b.mu.Unlock()
}

func (b *Bot) isPocketAuthExpired() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.pocketAuthExpired
}

func (b *Bot) tryHandleNaturalLanguage(event *napcat.Event, msg string) bool {

	msg = strings.ReplaceAll(msg, "[CQ:at,qq=3808515247]", "")
	msg = strings.ReplaceAll(msg, "[CQ:at,all]", "")
	msg = strings.TrimSpace(msg)
	lowerMsg := strings.ToLower(msg)

	if strings.HasPrefix(lowerMsg, "bot ") || strings.HasPrefix(lowerMsg, "weibo ") {
		return false
	}

	if msg == "帮助" || msg == "?" || msg == "help" {
		helpMsg := `📖 可用命令：
• 监控房间 <房间号> - 添加口袋房间
• 监控微博 <UID> - 添加微博监控
• 监控微博 <UID> 全体 - @全体
• 监控B站 <房间号> - 添加B站直播
• 查看监控 - 查看监控列表
• 删除微博监控 / 删除B站监控
• 开启监控 / 关闭监控
• 搜索 <名字> - 搜索房间
• 登录密码 <密码> - 密码登录
• 检查微博Cookie - 检查Cookie是否可用
• 重设微博Cookie <Cookie> - 热更新微博Cookie
• 直接粘贴抓包文本（含Set-Cookie）- 自动提取并更新
或直接发送命令: bot xxx`
		b.reply(event, helpMsg)
		return true
	}

	if strings.Contains(strings.ToLower(msg), "cookie") && strings.Contains(msg, "检查") {
		b.handleCommand(event, []string{"weibo", "cookie", "check"})
		return true
	}

	if strings.Contains(msg, "检查") && strings.Contains(msg, "微博") && strings.Contains(strings.ToLower(msg), "cookie") {
		b.handleCommand(event, []string{"weibo", "cookie", "check"})
		return true
	}

	if strings.Contains(msg, "重设") && strings.Contains(strings.ToLower(msg), "cookie") {
		cookie, ok := extractWeiboCookiePayload(msg)
		if !ok {
			b.reply(event, "格式错误: 重设微博Cookie <完整Cookie或SUB值>")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.Contains(msg, "更新") && strings.Contains(strings.ToLower(msg), "cookie") {
		cookie, ok := extractWeiboCookiePayload(msg)
		if !ok {
			b.reply(event, "格式错误: 更新微博Cookie <完整Cookie或SUB值>")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.Contains(lowerMsg, "api.weibo.cn") && (strings.Contains(lowerMsg, "authorization:") || strings.Contains(lowerMsg, "wb-sut")) {
		rawText := strings.TrimSpace(msg)
		if appCfg, ok := extractWeiboAppAuthFromCaptureText(rawText); ok {
			if err := b.updateWeiboAppAuth(appCfg); err != nil {
				b.reply(event, fmt.Sprintf("[错误] 导入微博 App 抓包失败: %v", err))
				return true
			}
			b.reply(event, fmt.Sprintf("[OK] 已导入微博 App 抓包参数: %s\n[OK] 后续超话统计/动态监控可优先走 App 通道", maskWeiboAppAuth(appCfg)))
			return true
		}
	}

	if strings.Contains(msg, "Set-Cookie:") && strings.Contains(msg, "SUB=") {
		cookie, ok := extractCookieFromCaptureText(msg)
		if !ok {
			b.reply(event, "[错误] 抓包文本里未提取到有效Cookie（至少需要SUB）")
			return true
		}
		b.handleCommand(event, []string{"weibo", "cookie", "set", cookie})
		return true
	}

	if strings.HasPrefix(msg, "搜索 ") {
		name := strings.TrimPrefix(msg, "搜索 ")
		name = strings.TrimSpace(name)
		if name != "" {
			b.handleCommand(event, []string{"search", name})
			return true
		}
	}

	if strings.Contains(msg, "监控") && strings.Contains(msg, "房间") {
		roomID, err := extractNumber(msg)
		if err == nil && roomID > 0 {
			b.handleCommand(event, []string{"monitor", strconv.FormatInt(roomID, 10)})
			return true
		}
	}

	if strings.Contains(msg, "查看") && strings.Contains(msg, "监控") {
		if strings.Contains(msg, "微博") {
			b.handleCommand(event, []string{"weibo", "list"})
			return true
		}
		b.handleCommand(event, []string{"list", "channels"})
		return true
	}

	if strings.Contains(msg, "监控") && strings.Contains(msg, "微博") {
		uid, err := extractNumber(msg)
		if err == nil && uid > 0 {
			atAll := strings.Contains(msg, "全体")
			atAllStr := ""
			if atAll {
				atAllStr = "at_all"
			}
			b.handleCommand(event, []string{"weibo", "add", strconv.FormatInt(uid, 10), atAllStr})
			return true
		}
	}

	if strings.Contains(msg, "删除") && strings.Contains(msg, "微博") && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"weibo", "del"})
		return true
	}

	if (strings.Contains(msg, "开启") || strings.Contains(msg, "启动")) && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"on"})
		return true
	}

	if (strings.Contains(msg, "关闭") || strings.Contains(msg, "停止")) && strings.Contains(msg, "监控") {
		b.handleCommand(event, []string{"off"})
		return true
	}

	// Password login: "登录密码 <密码>" or "密码登录 <密码>"
	if (strings.Contains(msg, "登录") || strings.Contains(msg, "登陆")) && strings.Contains(msg, "密码") {
		// Extract password - try "登录密码 " prefix first
		password := strings.TrimPrefix(msg, "登录密码 ")
		password = strings.TrimPrefix(password, "登陆密码 ")
		password = strings.TrimPrefix(password, "密码登录 ")
		password = strings.TrimPrefix(password, "密码登陆 ")
		password = strings.TrimSpace(password)

		// Also try to extract from anywhere in the message
		if password == msg || password == "" {
			// Try to find password after 密码
			parts := strings.SplitN(msg, "密码", 2)
			if len(parts) > 1 {
				password = strings.TrimSpace(parts[1])
			}
		}

		if password == "" || password == "登录" || password == "登陆" || password == "密码" {
			b.reply(event, "请提供密码: 登录密码 <你的密码>")
			return true
		}

		b.handleCommand(event, []string{"login", "pwd", password})
		return true
	}

	return false
}

func (b *Bot) resolveTargetGroupID(event *napcat.Event) int64 {
	if event != nil && event.GroupID != 0 {
		return event.GroupID
	}
	if b.cfg.BoundGroupID != 0 {
		return b.cfg.BoundGroupID
	}
	return 0
}

func (b *Bot) handleArchiveCommand(args []string) string {
	if len(args) < 2 {
		return "用法: archive <status|retry>"
	}
	action := strings.ToLower(strings.TrimSpace(args[1]))
	switch action {
	case "status":
		cfg := b.storage.GetConfig()
		cos := "不可用"
		if b.storage.IsCOSAvailable() {
			cos = "可用"
		}
		return fmt.Sprintf("Archive状态\n- COS: %s\n- 目录: %s\n- 重试队列: %d\n- 切片阈值: %d 行 / %d 字节 / %d 秒刷新", cos, b.storage.GetArchiveDir(), b.storage.QueueLen(), cfg.MaxLines, cfg.MaxBytes, cfg.FlushInterval)
	case "retry":
		before := b.storage.QueueLen()
		if err := b.storage.RetryQueuedMessages(); err != nil {
			return fmt.Sprintf("重试失败: %v", err)
		}
		after := b.storage.QueueLen()
		return fmt.Sprintf("[OK] 已执行重试，队列 %d -> %d", before, after)
	}
	return "用法: archive <status|retry>"
}

func (b *Bot) HandleBotCommand(args []string) string {
	log.Printf("[BotCommand] 处理命令: %v", redactCommandArgs(args))
	if len(args) == 0 {
		return ""
	}

	cmd := args[0]
	switch cmd {
	case "help":
		return `📖 可用命令：
• 搜索 <名字> - 搜索房间
• 监控 <房间号> - 添加监控
• 删除监控 <房间号> - 移除监控
• 查看监控 - 查看监控列表
• 开启监控 / 关闭监控
• 监控微博 <UID> - 添加微博监控
• 删除微博监控 - 移除微博监控
• 查看微博监控 - 查看微博监控
• 年度青春盛典记分 - score <on/off> <房间ID>
• 检查微博Cookie - 检查Cookie状态
• 状态 - 查看转发状态
• 注册 - 开启消息转发
• 取消 - 关闭消息转发`

	case "list", "channels":
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		rooms := b.cfg.GroupSubscriptions[groupIDStr]

		if len(rooms) == 0 {
			return "📊 当前没有正在监控的频道。"
		}

		b.LogInfo("正在获取频道详情...")
		var sb strings.Builder
		sb.WriteString("📊 当前监控的频道:\n")

		for _, roomID := range rooms {
			info, err := b.getCachedRoomInfo(roomID)
			if err != nil {
				sb.WriteString(fmt.Sprintf("- ID: %d (获取详情失败)\n", roomID))
			} else {
				sb.WriteString(fmt.Sprintf("- ID: %d | 频道: %s | 主播: %s\n", roomID, info.ChannelName, info.OwnerName))
			}
		}
		return sb.String()

	case "search":
		if len(args) < 2 {
			return "请提供搜索关键词: 搜索 <名字>"
		}
		query := args[1]
		servers, err := b.pocket.Search(query)
		if err != nil {
			return fmt.Sprintf("搜索失败: %v", err)
		}
		if len(servers) == 0 {
			return "未找到结果: " + query
		}
		var sb strings.Builder
		sb.WriteString("🔍 搜索结果:\n")
		for _, server := range servers {
			ids, _ := b.pocket.GetChannelIDByServerID(server.ServerID)
			for _, id := range ids {
				if id >= 0 {
					roomIdStr := fmt.Sprintf("(房间ID: %d)", id)
					channelInfo, err := b.getCachedRoomInfo(id)
					if err != nil {
						sb.WriteString(fmt.Sprintf("- %s %s\n", "获取详情失败", roomIdStr))
					} else {
						sb.WriteString(fmt.Sprintf("- %s %s\n", channelInfo.ChannelName, roomIdStr))
					}
				}
			}
		}
		return sb.String()

	case "monitor":
		if len(args) < 2 {
			return "请提供房间号: 监控 <房间号>"
		}
		roomID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		if b.cfg.GroupSubscriptions == nil {
			b.cfg.GroupSubscriptions = make(map[string][]int64)
		}
		for _, id := range b.cfg.GroupSubscriptions[groupIDStr] {
			if id == roomID {
				return fmt.Sprintf("房间 %d 已经在监控列表中", roomID)
			}
		}
		b.cfg.GroupSubscriptions[groupIDStr] = append(b.cfg.GroupSubscriptions[groupIDStr], roomID)
		b.cfg.Save()
		return fmt.Sprintf("[OK] 已添加房间 %d 到监控列表", roomID)

	case "remove":
		if len(args) < 2 {
			return "请提供房间号: 删除监控 <房间号>"
		}
		roomID, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		groupIDStr := strconv.FormatInt(b.cfg.BoundGroupID, 10)
		currentRooms := b.cfg.GroupSubscriptions[groupIDStr]
		newRooms := []int64{}
		found := false
		for _, id := range currentRooms {
			if id == roomID {
				found = true
				continue
			}
			newRooms = append(newRooms, id)
		}
		if found {
			b.cfg.GroupSubscriptions[groupIDStr] = newRooms
			b.cfg.Save()
			return fmt.Sprintf("[OK] 已移除房间 %d 的监控", roomID)
		}
		return fmt.Sprintf("房间 %d 不在监控列表中", roomID)

	case "on":
		b.isMonitoring = true
		return "✅ 监控已开启"

	case "off":
		b.isMonitoring = false
		return "✅ 监控已关闭"

	case "live":
		if len(args) < 2 {
			state := "开启"
			if !b.cfg.LiveMonitoring {
				state = "关闭"
			}
			return fmt.Sprintf("全局直播监控状态: %s", state)
		}
		action := strings.ToLower(args[1])
		if action == "on" {
			b.cfg.LiveMonitoring = true
			b.cfg.Save()
			return "[OK] 全局直播监控已开启"
		} else if action == "off" {
			b.cfg.LiveMonitoring = false
			b.cfg.Save()
			return "[OK] 全局直播监控已关闭"
		}
		return "格式错误: live <on/off>"

	case "gift":
		if len(args) < 3 {
			return "格式错误: gift <on/off> <房间号>"
		}
		action := strings.ToLower(args[1])
		roomID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		roomIDStr := strconv.FormatInt(roomID, 10)
		if b.cfg.GiftSpecific == nil {
			b.cfg.GiftSpecific = make(map[string]bool)
		}
		if action == "on" {
			b.cfg.GiftSpecific[roomIDStr] = true
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的礼物回复已开启", roomID)
		} else if action == "off" {
			b.cfg.GiftSpecific[roomIDStr] = false
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的礼物回复已关闭", roomID)
		}
		return "格式错误: gift <on/off> <房间号>"

	case "score":
		if len(args) < 3 {
			return "格式错误: score <on/off> <房间号>"
		}
		action := strings.ToLower(args[1])
		roomID, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			return "无效的房间ID"
		}
		roomIDStr := strconv.FormatInt(roomID, 10)
		if b.cfg.AnnualScoreSpecific == nil {
			b.cfg.AnnualScoreSpecific = make(map[string]bool)
		}
		if action == "on" {
			b.cfg.AnnualScoreSpecific[roomIDStr] = true
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的年度青春盛典记分监控已开启", roomID)
		} else if action == "off" {
			b.cfg.AnnualScoreSpecific[roomIDStr] = false
			b.cfg.Save()
			return fmt.Sprintf("[OK] 房间 %d 的年度青春盛典记分监控已关闭", roomID)
		}
		return "格式错误: score <on/off> <房间号>"

	case "weibo":
		if len(args) < 2 {
			return "格式错误: weibo <add/del/list|cookie|super> [参数]"
		}
		action := strings.ToLower(strings.TrimSpace(args[1]))

		if action == "super" {
			evt := &napcat.Event{GroupID: b.cfg.BoundGroupID}
			return b.handleWeiboSuperCommand(evt, args)
		}

		if action == "cookie" {
			if len(args) < 3 {
				return "用法: weibo cookie <check|set|reset|import> [Cookie]"
			}
			subCmd := strings.ToLower(strings.TrimSpace(args[2]))
			switch subCmd {
			case "check":
				ok, detail, err := b.checkWeiboCookieStatus()
				if err != nil {
					return fmt.Sprintf("[错误] Cookie检查失败: %v", err)
				}
				if ok {
					return fmt.Sprintf("[OK] 微博 Cookie 可用: %s", detail)
				}
				return fmt.Sprintf("[警告] 微博 Cookie 异常: %s\n请执行: weibo cookie set <Cookie>", detail)
			case "import":
				if len(args) < 4 {
					return "格式错误: weibo cookie import <抓包文本>"
				}
				rawText := strings.TrimSpace(strings.Join(args[3:], " "))
				if appCfg, ok := extractWeiboAppAuthFromCaptureText(rawText); ok {
					if err := b.updateWeiboAppAuth(appCfg); err != nil {
						return fmt.Sprintf("[错误] 导入微博 App 抓包失败: %v", err)
					}
					masked := maskWeiboAppAuth(appCfg)
					return fmt.Sprintf("[OK] 已导入微博 App 抓包参数: %s\n[OK] 后续超话统计/动态监控可优先走 App 通道", masked)
				}
				parsedCookie, ok := extractCookieFromCaptureText(rawText)
				if !ok {
					return "[错误] 未从抓包文本中提取到有效Cookie（至少需要SUB），且未识别到有效 App 抓包"
				}
				masked, err := b.updateWeiboCookie(0, parsedCookie)
				if err != nil {
					return fmt.Sprintf("[错误] 导入微博 Cookie 失败: %v", err)
				}
				ok2, detail, checkErr := b.checkWeiboCookieStatus()
				if checkErr != nil {
					return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[提示] 更新后检查失败: %v", masked, checkErr)
				}
				if ok2 {
					return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[OK] 可用性检查: %s", masked, detail)
				}
				return fmt.Sprintf("[OK] 已从抓包文本导入Cookie: %s\n[警告] 可用性检查异常: %s", masked, detail)
			case "set", "reset":
				if len(args) < 4 {
					return "格式错误: weibo cookie set <完整Cookie或SUB值>"
				}
				cookie := strings.TrimSpace(strings.Join(args[3:], " "))
				masked, err := b.updateWeiboCookie(0, cookie)
				if err != nil {
					return fmt.Sprintf("[错误] 更新微博 Cookie 失败: %v", err)
				}
				ok, detail, checkErr := b.checkWeiboCookieStatus()
				if checkErr != nil {
					return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[提示] 检查失败: %v", masked, checkErr)
				}
				if ok {
					return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[OK] 可用: %s", masked, detail)
				}
				return fmt.Sprintf("[OK] 微博 Cookie 已更新: %s\n[警告] 异常: %s", masked, detail)
			}
			return "用法: weibo cookie <check|set|reset|import> [Cookie]"
		}

		if action == "list" {
			if b.cfg.WeiboSubscriptions == nil || len(b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]) == 0 {
				return "该群暂无微博监控"
			}
			var uids []string
			for uid := range b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] {
				uids = append(uids, uid)
			}
			return fmt.Sprintf("📊 微博监控: UID=%s", strings.Join(uids, ", "))
		}

		if action == "add" {
			if len(args) < 3 {
				return "格式错误: weibo add <UID> [at_all]"
			}
			uid := args[2]
			atAll := len(args) >= 4 && args[3] == "at_all"

			if b.cfg.WeiboSubscriptions == nil {
				b.cfg.WeiboSubscriptions = make(map[int64]map[string]*config.WeiboConfig)
			}
			if _, ok := b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]; !ok {
				b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] = make(map[string]*config.WeiboConfig)
			}
			b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][uid] = &config.WeiboConfig{
				UID:    uid,
				AtAll:  atAll,
				LastID: "",
			}
			b.cfg.Save()

			onNew := func(u, newLastID string) {
				if b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID] != nil && b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][u] != nil {
					b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID][u].LastID = newLastID
					b.cfg.Save()
				}
			}
			b.weiboMonitor.AddConfig(b.cfg.BoundGroupID, uid, atAll, "", onNew)
			b.weiboMonitor.Start()
			return fmt.Sprintf("[OK] 添加微博监控: UID=%s, @全体=%v", uid, atAll)
		}

		if action == "del" {
			if len(args) < 3 {
				delete(b.cfg.WeiboSubscriptions, b.cfg.BoundGroupID)
				b.cfg.Save()
				b.weiboMonitor.RemoveConfig(b.cfg.BoundGroupID, "")
				return "[OK] 已删除该群的所有微博监控"
			}
			uidToDel := args[2]
			if groupSubs, ok := b.cfg.WeiboSubscriptions[b.cfg.BoundGroupID]; ok {
				if _, ok := groupSubs[uidToDel]; ok {
					delete(groupSubs, uidToDel)
					if len(groupSubs) == 0 {
						delete(b.cfg.WeiboSubscriptions, b.cfg.BoundGroupID)
					}
					b.cfg.Save()
					b.weiboMonitor.RemoveConfig(b.cfg.BoundGroupID, uidToDel)
					return fmt.Sprintf("[OK] 已删除微博监控 UID: %s", uidToDel)
				}
			}
			return "未找到要删除的 UID"
		}

		return "格式错误: weibo <add/del/list|cookie|super> [参数]"

	case "archive":
		return b.handleArchiveCommand(args)

	case "status":
		return "📊 Bot 运行中"

	default:
		return "❓ 未知命令，发送「帮助」查看可用命令"
	}
}

func redactCommandArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	if len(redacted) >= 3 && strings.EqualFold(redacted[0], "login") && strings.EqualFold(redacted[1], "pwd") {
		redacted[2] = "<redacted>"
	}
	if len(redacted) >= 3 && strings.EqualFold(redacted[0], "login") && strings.EqualFold(redacted[1], "sms") {
		redacted[2] = "<redacted>"
	}
	if len(redacted) >= 2 && strings.EqualFold(redacted[0], "login") && !strings.EqualFold(redacted[1], "sms") && !strings.EqualFold(redacted[1], "pwd") {
		redacted[1] = "<redacted>"
	}
	if len(redacted) >= 2 && strings.EqualFold(redacted[0], "code") {
		redacted[1] = "<redacted>"
	}
	return redacted
}
