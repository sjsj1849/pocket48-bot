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
	"sort"
	"strings"
	"time"
)

func WeverseReportHTML(r weverse.Report) string {
	var body strings.Builder
	body.WriteString(`<section class="report-section"><div class="section-heading"><h2>成员数据总览</h2></div>`)
	if !r.Complete {
		body.WriteString(`<p class="data-warning">历史尚未完整回采，以下为已采集数量，缺失不代表未发布。</p>`)
	}
	header := `<tr class="group-head"><th rowspan="2">成员</th><th colspan="6">内容发布</th><th colspan="5">回复去向</th><th colspan="5">直播</th></tr><tr><th>帖子</th><th>照片</th><th>累计评论</th><th>累计点赞</th><th>视频</th><th>Moment</th><th>回复</th><th>被回复</th><th>粉丝</th><th>队友</th><th>自己</th><th>场次</th><th>总时长</th><th>单人时长</th><th>弹幕</th><th>发弹幕直播场次</th></tr>`
	table := func(members []weverse.MemberCount) {
		if len(members) == 0 {
			body.WriteString(`<p class="note">本期没有成员数据。</p>`)
			return
		}
		receivedReplies := map[string]int{}
		for _, member := range members {
			for targetID, count := range member.ReplyMembers {
				receivedReplies[targetID] += count
			}
		}
		rows := make([][]reportMetricCell, len(members))
		for i, m := range members {
			rows[i] = []reportMetricCell{metric(int64(m.Posts)), metric(int64(m.PostPhotos)), {Text: weverse.EngagementText(m.PostComments, m.CommentPosts, m.Posts), Value: m.PostComments, Known: m.CommentPosts == m.Posts}, {Text: weverse.EngagementText(m.PostLikes, m.LikePosts, m.Posts), Value: m.PostLikes, Known: m.LikePosts == m.Posts}, metric(int64(m.Videos)), metric(int64(m.Moments)), metric(int64(m.Replies)), metric(int64(receivedReplies[m.ID])), metric(int64(m.FanReplies)), metric(int64(m.MemberReplies)), metric(int64(m.SelfReplies)), metric(int64(m.Lives)), durationMetric(m.LiveSeconds, m.TimedLives), soloDurationMetric(m.SoloLiveSeconds, m.SoloLives, r.UnconfirmedLives > 0), metric(int64(m.LiveChats)), metric(int64(m.LiveChatLives))}
		}
		body.WriteString(`<table><thead>` + header + `</thead><tbody>`)
		for i, m := range members {
			fmt.Fprintf(&body, `<tr><td>%s</td>`, html.EscapeString(m.Name))
			for column, cell := range rows[i] {
				class, title := reportMetricRank(rows, column, cell)
				fmt.Fprintf(&body, `<td class="%s" title="%s">%s</td>`, class, title, html.EscapeString(cell.Text))
			}
			body.WriteString(`</tr>`)
		}
		body.WriteString(`</tbody><tfoot><tr><th>总和</th>`)
		for column := range rows[0] {
			var sum int64
			known, complete := false, true
			for _, row := range rows {
				if row[column].Known {
					sum += row[column].Value
					known = true
				} else {
					complete = false
				}
			}
			text := "—"
			if known {
				if column == 12 || column == 13 {
					text = formatClockDuration(sum)
				} else {
					text = fmt.Sprint(sum)
				}
				if !complete {
					text += "（已知）"
				}
			}
			fmt.Fprintf(&body, `<td>%s</td>`, html.EscapeString(text))
		}
		body.WriteString(`</tr></tfoot></table>`)
	}
	body.WriteString(`<p class="legend"><span class="metric-highest">最高</span><span class="metric-high">第二高</span><span class="metric-low">第二低</span><span class="metric-lowest">最低</span><span>并列同档，整列相同不标注</span></p>`)
	table(r.Members)
	body.WriteString(`</section>`)
	renderReplyTrend(&body, r)
	for _, month := range r.Months {
		fmt.Fprintf(&body, `<section class="report-section"><div class="section-heading"><h2>%s · 逐月对照</h2></div>`, html.EscapeString(month.Month))
		start, _ := time.ParseInLocation("2006-01", month.Month, weverse.ReportLocation)
		if start.After(r.AsOf) {
			body.WriteString(`<p class="note">尚未到统计月份</p></section>`)
			continue
		}
		table(month.Members)
		body.WriteString(`</section>`)
	}
	matrix := func(title, axis, rowAxis, kind string) {
		type matrixValue struct{ count, lives int }
		targets := make([]weverse.LiveChatTarget, 0, len(r.Members))
		if kind == "live_chat" {
			targets = append(targets, r.LiveChatTargets...)
		} else {
			for _, m := range r.Members {
				targets = append(targets, weverse.LiveChatTarget{ID: m.ID, Name: m.Name, IsMember: true})
			}
		}
		note := "连续斜线表示自己，浅灰空白格表示已采集记录中没有互动。"
		rowTotalLabel, columnTotalLabel := "回复数", "被回复数"
		if kind == "live_chat" {
			note = "单元格为“弹幕条数 / 发弹幕直播场次”；场次指该成员至少发送过 1 条弹幕的不同直播。斜线表示自己，浅灰空白格表示没有互动。"
			rowTotalLabel, columnTotalLabel = "发送弹幕数", "收到弹幕数"
		}
		valueAt := func(m weverse.MemberCount, target weverse.LiveChatTarget) matrixValue {
			if target.IsMember && m.ID == target.ID {
				return matrixValue{}
			}
			value := matrixValue{count: m.Teammates[target.ID]}
			if kind == "direct" {
				value.count = m.ReplyMembers[target.ID]
			} else if kind == "live_chat" {
				value.count = m.LiveChatHosts[target.ID]
				value.lives = m.LiveChatHostLives[target.ID]
			}
			return value
		}
		valueText := func(value matrixValue) string {
			if value.count == 0 {
				return ""
			}
			if kind == "live_chat" {
				return fmt.Sprintf("%d条 / %d场", value.count, value.lives)
			}
			return fmt.Sprint(value.count)
		}
		values := make([][]matrixValue, len(r.Members))
		rowTotals := make([]matrixValue, len(r.Members))
		columns := make([]matrixValue, len(targets))
		grand := matrixValue{}
		detailMax := 0
		for row, m := range r.Members {
			values[row] = make([]matrixValue, len(targets))
			for column, target := range targets {
				value := valueAt(m, target)
				values[row][column] = value
				rowTotals[row].count += value.count
				rowTotals[row].lives += value.lives
				columns[column].count += value.count
				columns[column].lives += value.lives
				grand.count += value.count
				grand.lives += value.lives
				if value.count > detailMax {
					detailMax = value.count
				}
			}
		}
		totalClass := func(value matrixValue, totals []matrixValue) string {
			if len(totals) == 0 {
				return ""
			}
			minValue, maxValue := totals[0].count, totals[0].count
			for _, total := range totals[1:] {
				if total.count < minValue {
					minValue = total.count
				}
				if total.count > maxValue {
					maxValue = total.count
				}
			}
			if minValue == maxValue {
				return ""
			}
			if value.count == maxValue {
				return "matrix-total-highest"
			}
			if value.count == minValue {
				return "matrix-total-lowest"
			}
			return ""
		}
		fmt.Fprintf(&body, `<section class="report-section"><h2>%s</h2><p class="note">行：%s；列：%s。%s</p><table class="interaction-matrix"><colgroup><col style="width:132px"><col span="%d"><col class="total-column"></colgroup><thead><tr><th class="matrix-corner"><span class="corner-column">%s</span><span class="corner-row">%s</span></th>`, title, rowAxis, axis, note, len(targets), axis, rowAxis)
		for _, target := range targets {
			fmt.Fprintf(&body, `<th>%s</th>`, html.EscapeString(target.Name))
		}
		fmt.Fprintf(&body, `<th class="matrix-total">%s</th></tr></thead><tbody>`, rowTotalLabel)
		for row, m := range r.Members {
			fmt.Fprintf(&body, `<tr><th scope="row">%s</th>`, html.EscapeString(m.Name))
			for column, target := range targets {
				class := ""
				if target.IsMember && m.ID == target.ID {
					class = "matrix-self"
				} else if values[row][column].count == 0 {
					class = "matrix-zero"
				} else if values[row][column].count == detailMax {
					class = "matrix-detail-highest"
				}
				fmt.Fprintf(&body, `<td class="%s">%s</td>`, class, valueText(values[row][column]))
			}
			fmt.Fprintf(&body, `<td class="matrix-total %s">%s</td></tr>`, totalClass(rowTotals[row], rowTotals), valueText(rowTotals[row]))
		}
		fmt.Fprintf(&body, `</tbody><tfoot><tr><th>%s</th>`, columnTotalLabel)
		for _, total := range columns {
			fmt.Fprintf(&body, `<td class="%s">%s</td>`, totalClass(total, columns), valueText(total))
		}
		fmt.Fprintf(&body, `<td class="matrix-grand">%s</td></tr></tfoot></table></section>`, valueText(grand))
	}
	matrix("直接回复了哪位成员", "被回复成员", "回复者", "direct")
	matrix("哪位成员在哪位队友的帖子下回复", "主帖作者", "回复者", "post")
	matrix("在哪位成员的直播中发起弹幕", "直播成员", "弹幕成员", "live_chat")
	var coverage strings.Builder
	coverage.WriteString(`<ul class="method-list">`)
	for _, item := range r.CoverageItems() {
		fmt.Fprintf(&coverage, `<li>%s</li>`, html.EscapeString(item))
	}
	coverage.WriteString(`</ul>`)
	return fmt.Sprintf(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><style>*{box-sizing:border-box}body{font-family:Arial,"Microsoft YaHei",sans-serif;background:#edf1f2;color:#20282d;margin:0;padding:24px}#report-card{max-width:1120px;margin:auto;background:#fff;padding:34px 38px;border-top:6px solid #2f6657}.report-header{display:flex;align-items:flex-end;justify-content:space-between;border-bottom:2px solid #b8c4c1;padding-bottom:22px}.report-header h1{font-size:28px;line-height:1.15;margin:5px 0 0}.report-kicker{margin:0;color:#2f6657;font-size:11px;font-weight:800;letter-spacing:1.5px}.report-meta{text-align:right;color:#5f6c72;font-size:12px;line-height:1.7}.report-section{margin-top:28px}.section-heading{display:flex;align-items:center;margin-bottom:10px;padding-left:10px;border-left:4px solid #2f6657}.section-heading h2,.report-section>h2{font-size:17px;margin:0;color:#26353b}table{width:100%%;border-collapse:collapse;table-layout:fixed;font-size:11px;border:2px solid #73838b;background:#fff}th,td{padding:9px 5px;border:1px solid #a8b4ba;text-align:center;vertical-align:middle;font-variant-numeric:tabular-nums}thead th{background:#d9e7e2;color:#263e37;font-weight:800}.group-head th{background:#2f6657;color:#fff;border-color:#668b7f;padding:8px 5px}tbody td:first-child,tbody th:first-child{background:#c8ddd5;font-weight:850;color:#203c34}tfoot th,tfoot td{background:#315f53!important;color:#fff!important;font-weight:850;border-top:2px solid #244b41}.matrix-total{background:#c8ddd5!important;color:#203c34!important;font-weight:850}.interaction-matrix tfoot th,.interaction-matrix tfoot td{background:#c8ddd5!important;color:#203c34!important}.interaction-matrix tfoot td.matrix-grand{background:#315f53!important;color:#fff!important}.metric-highest{background:#f4b8b3!important;color:#7b1f1f!important;font-weight:850}.metric-high{background:#f8d0a0!important;color:#7a3a08!important;font-weight:850}.metric-low{background:#dcebf5!important;color:#244d66!important;font-weight:800}.metric-lowest{background:#e2e5e8!important;color:#46515a!important;font-weight:800}.legend{display:flex;align-items:center;gap:7px;margin:0 0 10px;color:#69747c;font-size:11px}.legend span{padding:5px 9px;border:1px solid #c2ccd1}.legend span:last-child{border:0;padding-left:3px}.data-warning{margin:0 0 10px;padding:9px 11px;border:1px solid #e3bd73;border-left:4px solid #bd7611;background:#fff7e8;color:#7e4d0b;font-size:11px}.interaction-matrix{table-layout:fixed}.interaction-matrix th,.interaction-matrix td{height:34px;padding:6px 4px}.interaction-matrix .matrix-corner{position:relative;height:62px;padding:0}.matrix-corner:after{content:"";position:absolute;inset:-1px;background:linear-gradient(to top right,transparent calc(50%% - .9px),#66757d 50%%,transparent calc(50%% + .9px));pointer-events:none}.corner-column{position:absolute;right:7px;top:7px}.corner-row{position:absolute;left:7px;bottom:7px}.interaction-matrix .matrix-self{position:relative;background:#eef1f2!important}.interaction-matrix .matrix-self:after{content:"";position:absolute;inset:-1px;background:linear-gradient(to top right,transparent calc(50%% - .9px),#66757d 50%%,transparent calc(50%% + .9px));pointer-events:none}.interaction-matrix td.matrix-zero{background:#f3f5f6!important}.interaction-matrix td.matrix-detail-highest{background:#f4b8b3!important;color:#7b1f1f!important;font-weight:850}.interaction-matrix tbody td.matrix-total-highest,.interaction-matrix tfoot td.matrix-total-highest{background:#f4b8b3!important;color:#7b1f1f!important}.interaction-matrix tbody td.matrix-total-lowest,.interaction-matrix tfoot td.matrix-total-lowest{background:#e2e5e8!important;color:#46515a!important}.interaction-matrix .total-column{width:92px}.reply-trend .period-column{width:112px}.reply-trend .total-column{width:68px}.reply-trend th,.reply-trend td{height:30px;padding:6px 4px}.reply-trend tfoot th,.reply-trend tfoot td{background:#c8ddd5!important;color:#203c34!important;border-top-color:#8aa69d}.reply-trend tfoot td.matrix-grand{background:#315f53!important;color:#fff!important}.reply-trend .reply-highest{background:#f4b8b3!important;color:#7b1f1f;font-weight:850}.reply-trend .reply-lowest{background:#e2e5e8!important;color:#46515a;font-weight:800}.note{font-size:11px;line-height:1.65;color:#5f6c72;white-space:pre-line;margin:7px 0 10px}.methodology{margin-top:30px;padding-top:20px;border-top:2px solid #b8c4c1}.methodology h2{font-size:15px;margin:0 0 8px}.method-list{display:grid;gap:6px;margin:0;padding-left:18px;color:#5f6c72;font-size:11px;line-height:1.5}.method-list li::marker{color:#2f6657;font-weight:800}@media(max-width:700px){body{padding:8px}#report-card{padding:20px 14px}.report-header{display:block}.report-meta{text-align:left;margin-top:8px}table{font-size:10px}}</style></head><body><article id="report-card"><header class="report-header"><div><p class="report-kicker">WEVERSE ACTIVITY REPORT</p><h1>%s · %s</h1></div><div class="report-meta">%s 至 %s<br>北京时间 · %d 位成员</div></header>%s<section class="methodology"><h2>统计说明</h2>%s</section></article></body></html>`, html.EscapeString(r.Community), html.EscapeString(r.DisplayTitle()), r.Period.Start.Format("2006-01-02"), r.Period.End.Add(-time.Second).Format("2006-01-02"), len(r.Members), body.String(), coverage.String())
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

// fanWeverseReportPNG renders the report PNG once more and fans it out to the
// configured targets so the image also lands in chat groups / private chats.
func (b *Bot) fanWeverseReportPNG(r weverse.Report, imageTargets []string) {
	if b == nil || len(imageTargets) == 0 {
		return
	}
	png, err := WeverseReportPNG(r)
	if err != nil {
		log.Printf("[Weverse Report] png render for fan-out failed: %v", err)
		return
	}
	for _, targetID := range imageTargets {
		target := b.cfg.ResolveTarget(targetID)
		if target.ID == "" {
			continue
		}
		b.sendReportImage(target, png, weverseReportCardTitle(r), weverseReportTime(r))
	}
}

// weverseReportCardTitle is the Feishu card headline for a Weverse report, e.g.
// "Hearts2Hearts 周报". It matches the PNG's own title so the card and the
// image inside it read as one unit.
func weverseReportCardTitle(r weverse.Report) string {
	name := strings.TrimSpace(r.Community)
	if name == "" {
		name = "Weverse"
	}
	label := strings.TrimSpace(r.DisplayTitle())
	if label == "" {
		label = "活动报表"
	}
	return name + " " + label
}

// weverseReportTime anchors the card timestamp at the end of the report period so
// it matches the "统计时间" line printed inside the image.
func weverseReportTime(r weverse.Report) time.Time {
	if !r.Period.End.IsZero() {
		return r.Period.End
	}
	return time.Now()
}

type weverseReportState struct {
	Sent               map[string]string `json:"sent"`
	LastAttempt        map[string]string `json:"lastAttempt"`
	Prepared           map[string]string `json:"prepared,omitempty"`
	LastPrepareAttempt map[string]string `json:"lastPrepareAttempt,omitempty"`
	Error              string            `json:"error,omitempty"`
	LastSuccess        string            `json:"lastSuccess,omitempty"`
}

func (b *Bot) runWeverseReportLoop(ctx context.Context, dir string) {
	// Check frequently enough that a period ending at midnight is normally sent
	// within a few seconds. Report aggregation reads the already collected DB.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	first := true
	for {
		if !first {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
		first = false
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
		if state.Prepared == nil {
			state.Prepared = map[string]string{}
		}
		if state.LastPrepareAttempt == nil {
			state.LastPrepareAttempt = map[string]string{}
		}
		now := time.Now().In(weverse.ReportLocation)
		upcoming := weverse.DueReportPeriods(settings, now.Add(time.Hour))
		pending := make([]weverse.ReportPeriod, 0, len(upcoming))
		for _, period := range upcoming {
			key := fmt.Sprintf("%d/%s", settings.CommunityID, period.Key)
			if state.Sent[key] != "" || state.Prepared[key] != "" {
				continue
			}
			last, _ := time.Parse(time.RFC3339, state.LastPrepareAttempt[key])
			if time.Since(last) >= 15*time.Minute {
				pending = append(pending, period)
			}
		}
		if len(pending) > 0 {
			// One pass over the widest upcoming period also fills every narrower
			// report ending at the same midnight.
			preparePeriod := pending[0]
			for _, period := range pending[1:] {
				if period.Start.Before(preparePeriod.Start) {
					preparePeriod = period
				}
			}
			attemptedAt := time.Now().Format(time.RFC3339)
			for _, period := range pending {
				state.LastPrepareAttempt[fmt.Sprintf("%d/%s", settings.CommunityID, period.Key)] = attemptedAt
			}
			if e = weverse.Write(dir, "report-state.json", state); e == nil {
				h, openErr := weverse.OpenHistory(dir)
				if openErr != nil {
					e = openErr
				} else {
					monitor, loadErr := weverse.LoadSettings(dir)
					if loadErr != nil {
						e = loadErr
					} else {
						slug := ""
						for _, subscription := range monitor.Subscriptions {
							if subscription.CommunityID == settings.CommunityID {
								slug = subscription.Slug
								break
							}
						}
						if slug == "" {
							e = fmt.Errorf("未找到报表社区订阅")
						} else {
							client := weverse.NewClient(dir, monitor.ProxyURL)
							prepareCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
							_, e = client.BackfillReportMemberComments(prepareCtx, h, settings, preparePeriod, slug)
							cancel()
							client.HTTP.CloseIdleConnections()
						}
					}
					h.Close()
				}
			}
			if e != nil {
				log.Printf("[Weverse Report] 报表前成员回复核对失败，将重试: %v", e)
			} else {
				preparedAt := time.Now().Format(time.RFC3339)
				for _, period := range pending {
					state.Prepared[fmt.Sprintf("%d/%s", settings.CommunityID, period.Key)] = preparedAt
				}
				log.Printf("[Weverse Report] 已完成报表前成员回复核对: %s", preparePeriod.Key)
			}
			if writeErr := weverse.Write(dir, "report-state.json", state); writeErr != nil {
				log.Printf("[Weverse Report] 无法保存预检状态: %v", writeErr)
			}
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
				monitor, loadErr := weverse.LoadSettings(dir)
				if loadErr != nil {
					e = loadErr
				} else {
					slug := ""
					for _, subscription := range monitor.Subscriptions {
						if subscription.CommunityID == settings.CommunityID {
							slug = subscription.Slug
							break
						}
					}
					if slug == "" {
						e = fmt.Errorf("未找到报表社区订阅")
					} else {
						client := weverse.NewClient(dir, monitor.ProxyURL)
						refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
						e = client.RefreshReportPosts(refreshCtx, h, settings, period, slug)
						cancel()
						client.HTTP.CloseIdleConnections()
					}
				}
				var report weverse.Report
				if e == nil {
					report, e = h.BuildReport(settings, period)
				}
				h.Close()
				if e == nil && len(report.Members) == 0 {
					e = fmt.Errorf("尚未采集成员名单")
				}
				if e == nil {
					e = SendWeverseReport(b.cfg, report)
					if e == nil {
						// 每个团体的报表图片发往各自配置的目标，未配置则回落全局列表。
						b.fanWeverseReportPNG(report, settings.ReportImageTargets(report.Community))
					}
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
	command := exec.CommandContext(ctx, "node", script, input, output, "1180")
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

type reportTimeBucket struct {
	Start time.Time
	End   time.Time
	Label string
}

func reportReplyBuckets(r weverse.Report) []reportTimeBucket {
	end := r.Period.End
	if !r.AsOf.IsZero() && r.AsOf.Before(end) {
		asOf := r.AsOf.In(weverse.ReportLocation)
		nextDay := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, weverse.ReportLocation).AddDate(0, 0, 1)
		if nextDay.Before(end) {
			end = nextDay
		}
	}
	weekly := r.Period.Kind == "firstHalf" || r.Period.Kind == "annual"
	var buckets []reportTimeBucket
	for start := r.Period.Start; start.Before(end); {
		next := start.AddDate(0, 0, 1)
		if weekly {
			days := (8 - int(start.Weekday())) % 7
			if days == 0 {
				days = 7
			}
			next = start.AddDate(0, 0, days)
		}
		if next.After(end) {
			next = end
		}
		label := start.Format("1月2日")
		if weekly {
			last := next.AddDate(0, 0, -1)
			if start.Month() == last.Month() {
				label = fmt.Sprintf("%d月%d日–%d日", start.Month(), start.Day(), last.Day())
			} else {
				label = start.Format("1月2日") + "–" + last.Format("1月2日")
			}
		}
		buckets = append(buckets, reportTimeBucket{Start: start, End: next, Label: label})
		start = next
	}
	return buckets
}

func renderReplyTrend(body *strings.Builder, r weverse.Report) {
	buckets := reportReplyBuckets(r)
	if len(buckets) == 0 || len(r.Members) == 0 {
		return
	}
	mode := "按天统计"
	axis := "日期"
	if r.Period.Kind == "firstHalf" || r.Period.Kind == "annual" {
		mode = "按自然周统计，首尾不足一周按实际日期显示"
		axis = "周区间"
	}
	index := map[string]int{}
	for i, member := range r.Members {
		index[member.ID] = i
	}
	rows := make([][]int, len(buckets))
	totals := make([]int, len(r.Members))
	grand := 0
	for i, bucket := range buckets {
		rows[i] = make([]int, len(r.Members))
		for _, event := range r.Events {
			if event.Kind != "comment" || event.Time < bucket.Start.UnixMilli() || event.Time >= bucket.End.UnixMilli() {
				continue
			}
			if column, ok := index[event.MemberID]; ok {
				rows[i][column]++
				totals[column]++
				grand++
			}
		}
	}
	bucketTotals := make([]int, len(rows))
	for i, row := range rows {
		for _, value := range row {
			bucketTotals[i] += value
		}
	}
	extremeClass := func(value int, values []int) string {
		minValue, maxValue := values[0], values[0]
		for _, candidate := range values[1:] {
			if candidate < minValue {
				minValue = candidate
			}
			if candidate > maxValue {
				maxValue = candidate
			}
		}
		if minValue == maxValue {
			return ""
		}
		if value == maxValue {
			return "reply-highest"
		}
		if value == minValue {
			return "reply-lowest"
		}
		return ""
	}
	fmt.Fprintf(body, `<section class="report-section"><h2>成员回复趋势</h2><p class="note">%s；只统计成员本人发布的回复，不区分回复对象。每个时段仅标注最高与最低，并列同档，整行相同不标注。</p><table class="reply-trend"><colgroup><col class="period-column"><col span="%d"><col class="total-column"></colgroup><thead><tr><th>%s</th>`, mode, len(r.Members), axis)
	for _, member := range r.Members {
		fmt.Fprintf(body, `<th>%s</th>`, html.EscapeString(member.Name))
	}
	body.WriteString(`<th class="matrix-total">合计</th></tr></thead><tbody>`)
	for i, bucket := range buckets {
		minValue, maxValue := rows[i][0], rows[i][0]
		rowTotal := 0
		for _, value := range rows[i] {
			if value < minValue {
				minValue = value
			}
			if value > maxValue {
				maxValue = value
			}
			rowTotal += value
		}
		fmt.Fprintf(body, `<tr><th scope="row">%s</th>`, html.EscapeString(bucket.Label))
		for _, value := range rows[i] {
			class := ""
			if minValue != maxValue {
				if value == maxValue {
					class = "reply-highest"
				} else if value == minValue {
					class = "reply-lowest"
				}
			}
			fmt.Fprintf(body, `<td class="%s">%d</td>`, class, value)
		}
		fmt.Fprintf(body, `<td class="matrix-total %s">%d</td></tr>`, extremeClass(rowTotal, bucketTotals), rowTotal)
	}
	body.WriteString(`</tbody><tfoot><tr><th>期间总回复</th>`)
	for _, total := range totals {
		fmt.Fprintf(body, `<td class="%s">%d</td>`, extremeClass(total, totals), total)
	}
	fmt.Fprintf(body, `<td class="matrix-grand">%d</td></tr></tfoot></table></section>`, grand)
}

func reportMetricRank(rows [][]reportMetricCell, column int, cell reportMetricCell) (string, string) {
	if !cell.Known {
		return "", ""
	}
	unique := map[int64]bool{}
	for _, row := range rows {
		if row[column].Known {
			unique[row[column].Value] = true
		}
	}
	values := make([]int64, 0, len(unique))
	for value := range unique {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	if len(values) < 2 {
		return "", ""
	}
	if cell.Value == values[len(values)-1] {
		return "metric-highest", "本列最高（含并列）"
	}
	if cell.Value == values[0] {
		return "metric-lowest", "本列最低（含并列）"
	}
	if len(values) >= 3 && cell.Value == values[len(values)-2] {
		return "metric-high", "本列第二高（含并列）"
	}
	if len(values) >= 4 && cell.Value == values[1] {
		return "metric-low", "本列第二低（含并列）"
	}
	return "", ""
}

// formatClockDuration 把秒数渲染成 HH:MM:SS。
//
// 报告里直播时长同时有总时长和单人时长两列，之前的写法在「3小时20分」
// 「31分」「0分」之间来回跳，同一列的数字宽度都不一样，既没法纵向对齐，
// 也没法用肉眼比大小。统一成定宽 6 位数字加两个冒号之后，每行严格等宽，
// 排序扫一眼就能看出高低。
//
// 小时数不封顶：超过 99 小时就自然写成 100:00:00，不会被截断或进位丢数。
func formatClockDuration(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
}

// durationMetric 渲染「总时长」列。
//
// 这一列只要该场直播有时长就累加，与面板二次确认无关，所以永远算得出来：
// 没有就是 0，写成 00:00:00，不再用「缺失」把整列填成状态文案。
//
// Known 恒为 true 是有意的：原先 count==0 时返回 Known=false，
// 会让表尾「总和」行误以为这列数据不完整，永久挂上「（已知）」后缀。
func durationMetric(seconds int64, count int) reportMetricCell {
	return reportMetricCell{Text: formatClockDuration(seconds), Value: seconds, Known: true}
}

// soloDurationMetric 渲染「单人时长」列。
//
// 这一列要求该场直播已完成面板二次确认、且确认后只有 1 名参与者才累加。
// 因此唯一会出现空值的中间态就是「有直播还没确认归属」——
// 这时才写「待确认」；没有待确认直播就一律写 00:00:00。
//
// 「待确认」保持 Known=false：它确实不是最终值，不能参与高亮排名，
// 表尾总和也需要据此标注这是已知部分的合计。
func soloDurationMetric(seconds int64, count int, pending bool) reportMetricCell {
	if count > 0 {
		return durationMetric(seconds, count)
	}
	if pending {
		return reportMetricCell{Text: "待确认"}
	}
	return reportMetricCell{Text: formatClockDuration(0), Value: 0, Known: true}
}
