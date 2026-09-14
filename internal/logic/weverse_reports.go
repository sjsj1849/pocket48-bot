package logic

import (
	"context"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"pocket48-bot/internal/config"
	"pocket48-bot/internal/weverse"
	"strings"
	"time"
)

func WeverseReportHTML(r weverse.Report) string {
	var body strings.Builder
	if !r.Complete {
		body.WriteString(`<p class="note" style="color:#a15c00">历史尚未完整回采：以下为已采集数量，缺失不代表未发布。</p>`)
	}
	header := `<tr><th>成员</th><th>帖子</th><th>帖内照片</th><th>累计评论</th><th>累计点赞</th><th>回复</th><th>粉丝</th><th>队友</th><th>自己</th><th>未知</th><th>视频</th><th>Moment</th><th>直播</th></tr>`
	table := func(members []weverse.MemberCount) {
		rows := make([][]reportMetricCell, len(members))
		for i, m := range members {
			rows[i] = []reportMetricCell{metric(int64(m.Posts)), metric(int64(m.PostPhotos)), {Text: weverse.EngagementText(m.PostComments, m.CommentPosts, m.Posts), Value: m.PostComments, Known: m.CommentPosts == m.Posts}, {Text: weverse.EngagementText(m.PostLikes, m.LikePosts, m.Posts), Value: m.PostLikes, Known: m.LikePosts == m.Posts}, metric(int64(m.Replies)), metric(int64(m.FanReplies)), metric(int64(m.MemberReplies)), metric(int64(m.SelfReplies)), metric(int64(m.UnknownReplies)), metric(int64(m.Videos)), {Text: fmt.Sprintf("%d*", m.Moments), Value: int64(m.Moments), Known: true}, metric(int64(m.Lives))}
		}
		body.WriteString(`<table><thead>` + header + `</thead><tbody>`)
		for i, m := range members {
			fmt.Fprintf(&body, `<tr><td>%s</td>`, html.EscapeString(m.Name))
			for column, cell := range rows[i] {
				min, max, found := int64(0), int64(0), false
				for _, row := range rows {
					value := row[column]
					if !value.Known {
						continue
					}
					if !found || value.Value < min {
						min = value.Value
					}
					if !found || value.Value > max {
						max = value.Value
					}
					found = true
				}
				class, title := "", ""
				if cell.Known && found && min != max {
					if cell.Value == max {
						class = "metric-max"
						title = "本列最高（含并列）"
					} else if cell.Value == min {
						class = "metric-min"
						title = "本列最低（含并列）"
					}
				}
				fmt.Fprintf(&body, `<td class="%s" title="%s">%s</td>`, class, title, html.EscapeString(cell.Text))
			}
			body.WriteString(`</tr>`)
		}
		body.WriteString(`</tbody></table>`)
	}
	body.WriteString(`<p class="note"><span class="metric-max">蓝色：本列最高</span>　<span class="metric-min">橙色：本列最低</span>；并列均标注，整列相同不标注；缺失或部分互动值不参与比较。</p>`)
	table(r.Members)
	for _, month := range r.Months {
		fmt.Fprintf(&body, `<h2>%s · 逐月对照</h2>`, html.EscapeString(month.Month))
		start, _ := time.ParseInLocation("2006-01", month.Month, weverse.ReportLocation)
		if start.After(r.AsOf) {
			body.WriteString(`<p class="note">尚未到统计月份</p>`)
			continue
		}
		table(month.Members)
	}
	matrix := func(title, axis string, direct bool) {
		fmt.Fprintf(&body, `<h2>%s</h2><p class="note">行：回复者；列：%s。连续斜线表示自己，空白表示已采集记录中没有互动。</p><table class="interaction-matrix"><colgroup><col style="width:140px"><col span="8"></colgroup><thead><tr><th class="matrix-corner"><span class="corner-column">%s</span><span class="corner-row">回复者</span></th>`, title, axis, axis)
		for _, m := range r.Members {
			fmt.Fprintf(&body, `<th>%s</th>`, html.EscapeString(m.Name))
		}
		body.WriteString(`</tr></thead><tbody>`)
		for _, m := range r.Members {
			fmt.Fprintf(&body, `<tr><th scope="row">%s</th>`, html.EscapeString(m.Name))
			for _, target := range r.Members {
				text := ""
				class := ""
				if m.ID == target.ID {
					text = ""
					class = "matrix-self"
				} else {
					count := m.Teammates[target.ID]
					if direct {
						count = m.ReplyMembers[target.ID]
					}
					if count > 0 {
						text = fmt.Sprint(count)
					}
				}
				fmt.Fprintf(&body, `<td class="%s">%s</td>`, class, text)
			}
			body.WriteString(`</tr>`)
		}
		body.WriteString(`</tbody></table>`)
	}
	matrix("直接回复了哪位成员", "被回复成员", true)
	matrix("在哪位队友的帖子下回复", "主帖作者", false)
	body.WriteString(`<h2>直播时长</h2><table><tr><th>成员</th><th>已知直播时长 / 有时长场次</th><th>已确认单人直播时长 / 场次</th></tr>`)
	for _, m := range r.Members {
		total, solo := "未取得时长", "未确认单人"
		if m.TimedLives > 0 {
			total = fmt.Sprintf("%d分%d秒 / %d场", m.LiveSeconds/60, m.LiveSeconds%60, m.TimedLives)
		}
		if m.SoloLives > 0 {
			solo = fmt.Sprintf("%d分%d秒 / %d场", m.SoloLiveSeconds/60, m.SoloLiveSeconds%60, m.SoloLives)
		}
		durationClass := func(seconds int64, solo bool) string {
			min, max, found := int64(0), int64(0), false
			for _, row := range r.Members {
				value, count := row.LiveSeconds, row.TimedLives
				if solo {
					value, count = row.SoloLiveSeconds, row.SoloLives
				}
				if count == 0 {
					continue
				}
				if !found || value < min {
					min = value
				}
				if !found || value > max {
					max = value
				}
				found = true
			}
			if min == max {
				return ""
			}
			if seconds == max {
				return "metric-max"
			}
			if seconds == min {
				return "metric-min"
			}
			return ""
		}
		totalClass, soloClass := "", ""
		if m.TimedLives > 0 {
			totalClass = durationClass(m.LiveSeconds, false)
		}
		if m.SoloLives > 0 {
			soloClass = durationClass(m.SoloLiveSeconds, true)
		}
		fmt.Fprintf(&body, `<tr><td>%s</td><td class="%s">%s</td><td class="%s">%s</td></tr>`, html.EscapeString(m.Name), totalClass, total, soloClass, solo)
	}
	body.WriteString(`</table>`)
	return fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>body{font-family:Arial,"Microsoft YaHei",sans-serif;background:#f5f7fb;color:#172033;margin:0;padding:16px}#report-card{max-width:880px;margin:auto;background:white;padding:20px}h1{font-size:23px}h2{font-size:17px;margin:24px 0 12px}table{width:100%%;border-collapse:collapse;font-size:11px}th,td{padding:9px 4px;border-bottom:1px solid #e7ebf1;text-align:right}th{background:#edf4ff}td:first-child,th:first-child{text-align:left}tr:nth-child(even){background:#f8fafc}.metric-max{color:#1d4ed8;background:#eff6ff;font-weight:800}.metric-min{color:#c2410c;background:#fff7ed;font-weight:800}.interaction-matrix{table-layout:fixed}.interaction-matrix th,.interaction-matrix td{text-align:center;border:1px solid #dce3ee;height:25px}.interaction-matrix th:first-child{text-align:left}.interaction-matrix .matrix-corner{position:relative;height:58px;padding:0;background:#edf4ff}.matrix-corner:after{content:"";position:absolute;inset:0;background:linear-gradient(to top right,transparent calc(50%% - .6px),#8994a5 50%%,transparent calc(50%% + .6px));pointer-events:none}.corner-column{position:absolute;right:8px;top:8px}.corner-row{position:absolute;left:8px;bottom:8px}.interaction-matrix tbody{position:relative}.interaction-matrix tbody:after{content:"";position:absolute;top:0;bottom:0;left:140px;right:0;pointer-events:none;background-image:url("data:image/svg+xml,%%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100' preserveAspectRatio='none'%%3E%%3Cpath d='M0 0L100 100' stroke='%%238994a5' stroke-width='1.3' vector-effect='non-scaling-stroke'/%%3E%%3C/svg%%3E");background-size:100%% 100%%;z-index:1}.interaction-matrix .matrix-self{background:#eef1f5}.note{font-size:12px;line-height:1.8;color:#667085;white-space:pre-line}ul{line-height:1.8;font-size:13px}</style></head><body><article id="report-card"><h1>%s · %s</h1><p class="note">%s 至 %s（北京时间） · %d 位成员
* Moment 为已采集数量；历史可能过期。整张图中各月均采用同一统计口径。</p>%s<h2>统计范围</h2><p class="note">%s</p><p class="note">Excel 含逐月成员数据、直接回复矩阵、队友主帖矩阵、活动原文和累计互动采集时间。</p></article></body></html>`, html.EscapeString(r.Community), html.EscapeString(r.DisplayTitle()), r.Period.Start.Format("2006-01-02"), r.Period.End.Add(-time.Second).Format("2006-01-02"), len(r.Members), body.String(), html.EscapeString(r.CoverageNote()))
}
func SendWeverseReport(cfg *config.Config, r weverse.Report) error {
	xlsx, e := r.XLSX()
	if e != nil {
		return e
	}
	body := WeverseReportHTML(r)
	png, e := renderWeverseReportPNG(body)
	if e != nil {
		return e
	}
	return sendAdminHTMLEmail(cfg, r.Community+" Weverse "+r.DisplayTitle(), body, r.Community+" "+r.DisplayTitle()+"\n"+r.CoverageNote(), emailAttachment{Name: "weverse-" + r.Period.Key + ".xlsx", ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: xlsx}, emailAttachment{Name: "weverse-" + r.Period.Key + ".png", ContentType: "image/png", Data: png})
}

type weverseReportState struct {
	Sent        map[string]string `json:"sent"`
	LastAttempt map[string]string `json:"lastAttempt"`
	Error       string            `json:"error,omitempty"`
	LastSuccess string            `json:"lastSuccess,omitempty"`
}

func (b *Bot) runWeverseReportLoop(ctx context.Context, dir string) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		settings, e := weverse.LoadReportSettings(dir)
		if e != nil || !settings.Enabled {
			continue
		}
		var state weverseReportState
		if e = weverse.Read(dir, "report-state.json", &state); e != nil {
			log.Printf("[Weverse Report] 无法读取状态: %v", e)
			continue
		}
		if state.Sent == nil {
			state.Sent = map[string]string{}
		}
		if state.LastAttempt == nil {
			state.LastAttempt = map[string]string{}
		}
		for _, period := range weverse.DueReportPeriods(settings, time.Now()) {
			key := fmt.Sprintf("%d/%s", settings.CommunityID, period.Key)
			if state.Sent[key] != "" {
				continue
			}
			last, _ := time.Parse(time.RFC3339, state.LastAttempt[key])
			if time.Since(last) < time.Hour {
				continue
			}
			state.LastAttempt[key] = time.Now().Format(time.RFC3339)
			if e = weverse.Write(dir, "report-state.json", state); e != nil {
				break
			}
			h, e := weverse.OpenHistory(dir)
			if e == nil {
				var report weverse.Report
				report, e = h.BuildReport(settings, period)
				if e == nil && !report.Complete {
					monitor, readErr := weverse.LoadSettings(dir)
					e = readErr
					slug := ""
					for _, sub := range monitor.Subscriptions {
						if sub.CommunityID == settings.CommunityID {
							slug = sub.Slug
							break
						}
					}
					if e == nil && slug == "" {
						e = fmt.Errorf("报表社区尚未订阅")
					}
					if e == nil {
						callCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
						client := weverse.NewClient(dir, monitor.ProxyURL)
						e = client.BackfillReport(callCtx, h, settings, period, slug, nil)
						cancel()
						client.HTTP.CloseIdleConnections()
						if e == nil {
							report, e = h.BuildReport(settings, period)
						}
					}
				}
				h.Close()
				if e == nil && len(report.Members) == 0 {
					e = fmt.Errorf("尚未采集成员名单")
				}
				if e == nil {
					e = SendWeverseReport(b.cfg, report)
				}
			}
			if e != nil {
				state.Error = e.Error()
				log.Printf("[Weverse Report] 邮件发送失败，将重试: %v", e)
			} else {
				state.Error = ""
				state.LastSuccess = time.Now().Format(time.RFC3339)
				state.Sent[key] = state.LastSuccess
				log.Printf("[Weverse Report] 已发送 %s 邮件和附件", period.Key)
			}
			if e = weverse.Write(dir, "report-state.json", state); e != nil {
				log.Printf("[Weverse Report] 无法保存状态: %v", e)
				break
			}
		}
	}
}

func WeverseReportPNG(r weverse.Report) ([]byte, error) {
	return renderWeverseReportPNG(WeverseReportHTML(r))
}

func renderWeverseReportPNG(body string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "weverse-report-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "report.html"), filepath.Join(dir, "report.png")
	if err = os.WriteFile(input, []byte(body), 0600); err != nil {
		return nil, err
	}
	script := "scripts/weverse_report_to_png.mjs"
	if _, err := os.Stat(script); err != nil {
		script = "/root/pocket48-bot/scripts/weverse_report_to_png.mjs"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "node", script, input, output, "920")
	command.Env = append(os.Environ(), "PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome")
	if _, err = command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("Weverse 报表图片生成失败：%w", err)
	}
	data, err := os.ReadFile(output)
	if err == nil && len(data) < 100 {
		return nil, fmt.Errorf("Weverse 报表图片为空")
	}
	return data, err
}

type reportMetricCell struct {
	Text  string
	Value int64
	Known bool
}

func metric(value int64) reportMetricCell {
	return reportMetricCell{Text: fmt.Sprint(value), Value: value, Known: true}
}
