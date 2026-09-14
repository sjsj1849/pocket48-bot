package weverse

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

type workbookSheet struct {
	name string
	rows [][]any
}

func (r Report) XLSX() ([]byte, error) {
	summary := [][]any{reportColumns()}
	matrix := [][]any{{"       主帖作者\n回复者"}}
	for _, m := range r.Members {
		matrix[0] = append(matrix[0], m.Name)
	}
	for _, m := range r.Members {
		summary = append(summary, reportMemberRow(m))
		row := []any{m.Name}
		for _, target := range r.Members {
			row = append(row, interactionCell(m.ID, target.ID, m.Teammates[target.ID]))
		}
		matrix = append(matrix, row)
	}
	details := [][]any{{"回复成员", "主帖成员", "主帖编号", "回复原文", "北京时间", "回复链接"}}
	for _, e := range r.TeammateReplies {
		details = append(details, []any{e.Author, e.Owner, e.PostID, e.Body, time.UnixMilli(e.Time).In(ReportLocation).Format("2006-01-02 15:04:05"), e.URL})
	}
	activity := [][]any{{"成员", "类型", "北京时间", "帖子编号", "回复编号", "内容原文", "被回复者", "被回复原文", "图片张数", "视频数", "链接", "被回复者ID", "被回复者类型", "帖子累计评论", "帖子累计点赞", "互动采集时间", "直播时长秒", "评论采集时间", "点赞采集时间"}}
	for _, e := range r.Events {
		kind := map[string]string{"post": "发帖", "comment": "回复", "live": "开播", "moment": "Moment"}[e.Kind]
		activity = append(activity, []any{e.Author, kind, time.UnixMilli(e.Time).In(ReportLocation).Format("2006-01-02 15:04:05"), e.PostID, e.CommentID, e.Body, e.ParentAuthor, e.ParentBody, len(e.Images), len(e.Videos), e.URL, e.ParentMemberID, e.ParentProfileType, countValue(e.PostComments), countValue(e.PostLikes), metricTime(e.MetricsAt), e.LiveDuration, metricTime(e.CommentsAt), metricTime(e.LikesAt)})
	}
	notes := [][]any{{"统计说明", "内容"}, {"社区", r.Community}, {"报表", r.DisplayTitle()}, {"开始（含）", r.Period.Start.Format(time.RFC3339)}, {"结束（不含）", r.Period.End.Format(time.RFC3339)}, {"完整覆盖", fmt.Sprint(r.Complete)}, {"口径与采集范围", r.CoverageNote()}}
	direct := [][]any{{"       被回复成员\n回复者"}}
	for _, m := range r.Members {
		direct[0] = append(direct[0], m.Name)
	}
	for _, m := range r.Members {
		row := []any{m.Name}
		for _, target := range r.Members {
			row = append(row, interactionCell(m.ID, target.ID, m.ReplyMembers[target.ID]))
		}
		direct = append(direct, row)
	}
	monthly := [][]any{append([]any{"月份"}, reportColumns()...)}
	for _, month := range r.Months {
		for _, m := range month.Members {
			monthly = append(monthly, append([]any{month.Month}, reportMemberRow(m)...))
		}
	}
	return writeWorkbook([]workbookSheet{{"成员汇总", summary}, {"队友帖回复矩阵", matrix}, {"队友帖回复明细", details}, {"全部活动", activity}, {"统计说明", notes}, {"直接回复成员矩阵", direct}, {"逐月成员数据", monthly}})
}
func xmlText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, s)
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func columnName(i int) string {
	var s string
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}
func writeWorkbook(sheets []workbookSheet) ([]byte, error) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	write := func(name, body string) error {
		w, e := z.Create(name)
		if e != nil {
			return e
		}
		_, e = w.Write([]byte(body))
		return e
	}
	types := `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`
	workbook := `<?xml version="1.0" encoding="UTF-8"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`
	rels := `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`
	for i, s := range sheets {
		types += fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
		workbook += fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlText(s.name), i+1, i+1)
		rels += fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		var body strings.Builder
		body.WriteString(`<?xml version="1.0" encoding="UTF-8"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><cols><col min="1" max="32" width="24" customWidth="1"/></cols><sheetData>`)
		for j, row := range s.rows {
			fmt.Fprintf(&body, `<row r="%d">`, j+1)
			for k, v := range row {
				cell := fmt.Sprintf("%s%d", columnName(k), j+1)
				style := ""
				if s.name == "队友帖回复矩阵" || s.name == "直接回复成员矩阵" {
					if j == k && j > 0 {
						style = ` s="1"`
					}
					if j == 0 && k == 0 {
						style = ` s="2"`
					}
				}
				switch value := v.(type) {
				case int:
					fmt.Fprintf(&body, `<c r="%s"%s><v>%d</v></c>`, cell, style, value)
				case int64:
					fmt.Fprintf(&body, `<c r="%s"%s><v>%d</v></c>`, cell, style, value)
				default:
					fmt.Fprintf(&body, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, cell, style, xmlText(fmt.Sprint(v)))
				}
			}
			body.WriteString(`</row>`)
		}
		body.WriteString(`</sheetData>`)
		if s.name == "成员汇总" {
			body.WriteString(reportExcelExtrema(s.rows, 1, len(s.rows)-1, 1))
		}
		if s.name == "逐月成员数据" {
			for start := 1; start < len(s.rows); start += len(rptMonthMembers(s.rows)) {
				size := len(rptMonthMembers(s.rows))
				if size == 0 {
					break
				}
				end := start + size - 1
				if end >= len(s.rows) {
					end = len(s.rows) - 1
				}
				body.WriteString(reportExcelExtrema(s.rows, start, end, 2))
			}
		}
		body.WriteString(`</worksheet>`)
		if e := write(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), body.String()); e != nil {
			return nil, e
		}
	}
	for _, f := range []struct{ name, body string }{{"[Content_Types].xml", types + `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/></Types>`}, {"xl/workbook.xml", workbook + `</sheets></workbook>`}, {"xl/styles.xml", reportExcelStyles}, {"xl/_rels/workbook.xml.rels", rels + `<Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`}, {"_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`}} {
		if e := write(f.name, f.body); e != nil {
			return nil, e
		}
	}
	if e := z.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}

func reportColumns() []any {
	return []any{"成员", "帖子总数", "帖子照片张数", "帖子累计评论", "帖子累计点赞", "回复总数", "回复粉丝", "回复其他成员", "回复自己", "被回复者未知", "视频数", "Moment已采集数", "直播次数", "队友帖下回复数", "已知直播时长秒", "有时长直播数", "确认单人直播时长秒", "确认单人直播数"}
}
func reportMemberRow(m MemberCount) []any {
	return []any{m.Name, m.Posts, m.PostPhotos, engagementExcelValue(m.PostComments, m.CommentPosts, m.Posts), engagementExcelValue(m.PostLikes, m.LikePosts, m.Posts), m.Replies, m.FanReplies, m.MemberReplies, m.SelfReplies, m.UnknownReplies, m.Videos, m.Moments, m.Lives, m.TeammateReplies, knownDuration(m.LiveSeconds, m.TimedLives, "未取得时长"), m.TimedLives, knownDuration(m.SoloLiveSeconds, m.SoloLives, "未确认单人"), m.SoloLives}
}
func countValue(v *int64) any {
	if v == nil {
		return "缺失"
	}
	return *v
}
func metricTime(ms int64) string {
	if ms <= 0 {
		return "未采集"
	}
	return time.UnixMilli(ms).In(ReportLocation).Format("2006-01-02 15:04:05")
}

func interactionCell(author, target string, count int) any {
	if author == target {
		return ""
	}
	if count == 0 {
		return ""
	}
	return count
}

func engagementExcelValue(value int64, known, posts int) any {
	if known == posts {
		return value
	}
	return EngagementText(value, known, posts)
}

// Each monthly block contains one row per member; derive the block size from its month key.
func rptMonthMembers(rows [][]any) []int {
	var members []int
	if len(rows) < 2 {
		return members
	}
	key := rows[1][0]
	for i := 1; i < len(rows) && rows[i][0] == key; i++ {
		members = append(members, i)
	}
	return members
}
func reportExcelExtrema(rows [][]any, start, end, firstColumn int) string {
	if start >= len(rows) || end < start {
		return ""
	}
	var out strings.Builder
	for column := firstColumn; column < len(rows[0]); column++ {
		letter := columnName(column)
		rangeName := fmt.Sprintf("$%s$%d:$%s$%d", letter, start+1, letter, end+1)
		cell := fmt.Sprintf("%s%d", letter, start+1)
		fmt.Fprintf(&out, `<conditionalFormatting sqref="%s%d:%s%d">`, letter, start+1, letter, end+1)
		for style, fn := range []string{"MAX", "MIN"} {
			formula := fmt.Sprintf("AND(ISNUMBER(%s),COUNT(%s)&gt;1,MAX(%s)&lt;&gt;MIN(%s),%s=%s(%s))", cell, rangeName, rangeName, rangeName, cell, fn, rangeName)
			fmt.Fprintf(&out, `<cfRule type="expression" dxfId="%d" priority="%d"><formula>%s</formula></cfRule>`, style, 1+(start*len(rows[0])+column)*2+style, formula)
		}
		out.WriteString(`</conditionalFormatting>`)
	}
	return out.String()
}

const reportExcelStyles = `<?xml version="1.0" encoding="UTF-8"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="2"><border/><border diagonalDown="1"><diagonal style="thin"><color rgb="FF8994A5"/></diagonal></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1"/><xf numFmtId="0" fontId="0" fillId="0" borderId="1" xfId="0" applyBorder="1" applyAlignment="1"><alignment wrapText="1" vertical="center"/></xf></cellXfs><dxfs count="2"><dxf><font><b/><color rgb="FF1D4ED8"/></font></dxf><dxf><font><b/><color rgb="FFC2410C"/></font></dxf></dxfs></styleSheet>`

func knownDuration(seconds int64, count int, missing string) any {
	if count == 0 {
		return missing
	}
	return seconds
}
