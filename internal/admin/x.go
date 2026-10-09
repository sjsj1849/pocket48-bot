package admin

import (
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"pocket48-bot/internal/xmonitor"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleX(w http.ResponseWriter, r *http.Request) {
	dir := xmonitor.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	cfg, err := xmonitor.LoadSettings(dir)
	if err != nil {
		fail(fmt.Errorf("无法读取 X 配置"))
		return
	}
	client := xmonitor.Client{Dir: dir, ProxyURL: cfg.ProxyURL}
	switch r.URL.Path {
	case "/api/x/settings":
		if r.Method == http.MethodGet {
			var session xBrowserSession
			_ = xmonitor.Read(dir, "session.json", &session)
			var status xmonitor.Status
			_ = xmonitor.Read(dir, "status.json", &status)
			var viralStatus xmonitor.ViralStatus
			_ = xmonitor.Read(dir, "viral-status.json", &viralStatus)
			writeJSON(w, 200, map[string]any{"settings": cfg, "sessionConfigured": validXBrowserSession(session), "status": status, "viralStatus": viralStatus})
			return
		}
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		var next xmonitor.Settings
		if err := decodeJSON(r, &next); err != nil {
			fail(err)
			return
		}
		// Validate before network calls, then resolve stable user IDs server-side.
		if err := xmonitor.ValidateSettings(next); err != nil {
			fail(err)
			return
		}
		users := map[string]xmonitor.User{}
		for i := range next.Subscriptions {
			sub := &next.Subscriptions[i]
			name, _ := xmonitor.Username(sub.Username)
			user, found := users[name]
			if !found {
				for _, old := range cfg.Subscriptions {
					if old.ID == sub.ID && old.Username == name && old.UserID != "" && old.UserID == sub.UserID {
						user = xmonitor.User{ID: old.UserID, Username: old.Username, Name: old.Name}
						found = true
						break
					}
				}
			}
			if !found {
				var e error
				user, e = client.Lookup(r.Context(), name)
				if e != nil {
					fail(e)
					return
				}
				users[name] = user
			}
			sub.Username = user.Username
			sub.UserID = user.ID
			sub.Name = user.Name
		}
		if err := xmonitor.SaveSettings(dir, next); err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"settings": next})
	case "/api/x/search":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		q := r.URL.Query().Get("q")
		if name, e := xmonitor.Username(q); e == nil {
			user, e := client.Lookup(r.Context(), name)
			if e == nil {
				writeJSON(w, 200, map[string]any{"users": []xmonitor.User{user}})
				return
			}
			var ce *xmonitor.Error
			if !errors.As(e, &ce) || ce.Code != "user_unavailable" {
				fail(e)
				return
			}
		}
		if len(q) == 0 || len(q) > 100 {
			fail(fmt.Errorf("请输入用户名、主页链接或昵称（最多 100 字节）"))
			return
		}
		result, e := client.Call(r.Context(), map[string]any{"operation": "search", "query": q, "limit": 10})
		if e != nil {
			fail(e)
			return
		}
		if result.Users == nil {
			result.Users = []xmonitor.User{}
		}
		writeJSON(w, 200, map[string]any{"users": result.Users})
	case "/api/x/preview":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Username string `json:"username"`
		}
		if e := decodeJSON(r, &input); e != nil {
			fail(e)
			return
		}
		user, e := client.Lookup(r.Context(), input.Username)
		if e != nil {
			fail(e)
			return
		}
		events, e := client.Timeline(r.Context(), user.ID, 10)
		if e != nil {
			fail(e)
			return
		}
		writeJSON(w, 200, map[string]any{"events": events, "message": "只读测试完成，未发送 QQ 消息"})
	case "/api/x/viral", "/api/x/viral/confirm", "/api/x/viral/snapshot", "/api/x/viral/export.csv", "/api/x/viral/report.html":
		s.handleXViral(w, r, dir)
	default:
		writeJSON(w, 404, apiError{Error: "接口不存在"})
	}
}

func viralFilter(r *http.Request) (xmonitor.ViralFilter, error) {
	filter := xmonitor.ViralFilter{Status: r.URL.Query().Get("status"), Search: strings.TrimSpace(r.URL.Query().Get("search")), Limit: 1000}
	if filter.Status == "" {
		filter.Status = "pending"
	}
	if filter.Status != "pending" && filter.Status != "confirmed" && filter.Status != "excluded" && filter.Status != "all" {
		return filter, fmt.Errorf("筛选状态不正确")
	}
	location := time.FixedZone("CST", 8*3600)
	if value := r.URL.Query().Get("from"); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02", value, location)
		if err != nil {
			return filter, fmt.Errorf("开始日期格式不正确")
		}
		filter.From = parsed.UnixMilli()
	}
	if value := r.URL.Query().Get("to"); value != "" {
		parsed, err := time.ParseInLocation("2006-01-02", value, location)
		if err != nil {
			return filter, fmt.Errorf("结束日期格式不正确")
		}
		filter.To = parsed.AddDate(0, 0, 1).UnixMilli()
	}
	return filter, nil
}

func openViralPosts(dir string, filter xmonitor.ViralFilter) ([]xmonitor.ViralPost, error) {
	store, err := xmonitor.OpenViralStore(dir)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.List(filter)
}

func (s *Server) handleXViral(w http.ResponseWriter, r *http.Request, dir string) {
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	switch r.URL.Path {
	case "/api/x/viral":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		filter, err := viralFilter(r)
		if err != nil {
			fail(err)
			return
		}
		posts, err := openViralPosts(dir, filter)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"posts": posts, "members": xmonitor.ViralMembers})
	case "/api/x/viral/confirm":
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			ID      string   `json:"id"`
			Members []string `json:"members"`
			Exclude bool     `json:"exclude"`
		}
		if err := decodeJSON(r, &input); err != nil {
			fail(err)
			return
		}
		store, err := xmonitor.OpenViralStore(dir)
		if err == nil {
			defer store.Close()
			err = store.Confirm(input.ID, input.Members, input.Exclude, time.Now())
		}
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	case "/api/x/viral/snapshot":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		path := xmonitor.SnapshotPath(dir, r.URL.Query().Get("id"))
		if path == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "帖子编号不正确"})
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=300")
		http.ServeFile(w, r, path)
	case "/api/x/viral/export.csv", "/api/x/viral/report.html":
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		filter, err := viralFilter(r)
		if err != nil {
			fail(err)
			return
		}
		if r.URL.Query().Get("status") == "" {
			filter.Status = "confirmed"
		}
		posts, err := openViralPosts(dir, filter)
		if err != nil {
			fail(err)
			return
		}
		if r.URL.Path == "/api/x/viral/export.csv" {
			writeXViralCSV(w, posts)
		} else {
			writeXViralHTML(w, dir, posts)
		}
	}
}

func viralMemberNames(ids []string) string {
	names := map[string]string{}
	for _, member := range xmonitor.ViralMembers {
		names[member.ID] = member.Name
	}
	result := []string{}
	for _, id := range ids {
		if names[id] != "" {
			result = append(result, names[id])
		}
	}
	return strings.Join(result, "、")
}

func viralQualification(post xmonitor.ViralPost) string {
	if post.QualifiesLikes && post.QualifiesReposts {
		return "万赞、万转"
	}
	if post.QualifiesLikes {
		return "万赞"
	}
	return "万转"
}

func writeXViralCSV(w http.ResponseWriter, posts []xmonitor.ViralPost) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="x-viral-posts.csv"`)
	_, _ = w.Write([]byte{0xef, 0xbb, 0xbf})
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"发布时间", "作者", "账号", "单人/非单人", "所属成员", "达标类型", "点赞数", "转发数", "回复数", "引用数", "浏览数", "正文", "原帖链接"})
	location := time.FixedZone("CST", 8*3600)
	for _, post := range posts {
		_ = writer.Write([]string{time.UnixMilli(post.PostedAt).In(location).Format("2006-01-02 15:04:05"), post.AuthorName, "@" + post.AuthorUsername, post.Scope(), viralMemberNames(post.ConfirmedMembers), viralQualification(post), strconv.FormatInt(post.LikeCount, 10), strconv.FormatInt(post.RepostCount, 10), strconv.FormatInt(post.ReplyCount, 10), strconv.FormatInt(post.QuoteCount, 10), strconv.FormatInt(post.ViewCount, 10), post.Body, post.URL})
	}
	writer.Flush()
}

func writeXViralHTML(w http.ResponseWriter, dir string, posts []xmonitor.ViralPost) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="x-viral-posts.html"`)
	_, _ = fmt.Fprint(w, `<!doctype html><html><head><meta charset="utf-8"><title>X 高热帖子</title><style>body{max-width:1120px;margin:0 auto;padding:32px;font-family:Arial,"Microsoft YaHei",sans-serif;color:#172033}h1{font-size:28px}.item{display:grid;grid-template-columns:minmax(360px,700px) 1fr;gap:22px;padding:24px 0;border-top:1px solid #dfe4e9}.item img{width:100%;border:1px solid #dfe4e9}.data{line-height:1.8}.data strong{font-size:18px}.tag{display:inline-block;padding:4px 7px;margin-right:6px;background:#fff0eb;color:#9c321c}a{color:#185fa5}@media(max-width:760px){.item{grid-template-columns:1fr}}</style></head><body><h1>X 非官方高热帖子</h1>`)
	for _, post := range posts {
		image := ""
		if data, err := os.ReadFile(xmonitor.SnapshotPath(dir, post.ID)); err == nil {
			image = `<img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(data) + `" alt="帖子快照">`
		}
		fmt.Fprintf(w, `<section class="item"><div>%s</div><div class="data"><strong>%s</strong><p><span class="tag">%s</span><span class="tag">%s</span></p><p>成员：%s<br>点赞：%d<br>转发：%d</p><a href="%s">打开原帖</a></div></section>`, image, html.EscapeString(post.AuthorName), post.Scope(), viralQualification(post), html.EscapeString(viralMemberNames(post.ConfirmedMembers)), post.LikeCount, post.RepostCount, html.EscapeString(post.URL))
	}
	_, _ = fmt.Fprint(w, `</body></html>`)
}

func xService(configPath string, now time.Time) *serviceState {
	cfg, err := xmonitor.LoadSettings(xmonitor.Dir(configPath))
	card := &serviceState{ID: "x", Name: "X", Subtitle: "指定账号帖子、图片与视频", Status: "attention", StatusText: "未启用", Uptime: "—", Detail: fmt.Sprintf("每 %d 秒扫描", cfg.PollSeconds), LastEvent: "可在配置页启用 X 监控", LastTime: "—"}
	if err != nil {
		card.Status = "down"
		card.StatusText = "配置异常"
		card.LastEvent = "无法读取 X 配置"
		return card
	}
	if !cfg.Enabled {
		return card
	}
	count := 0
	for _, sub := range cfg.Subscriptions {
		if sub.Enabled {
			count++
		}
	}
	if count == 0 {
		card.StatusText = "待配置"
		card.LastEvent = "没有启用的 X 订阅"
		return card
	}
	card.StatusText = "检查中"
	card.LastEvent = "等待首次扫描"
	var status xmonitor.Status
	if xmonitor.Read(xmonitor.Dir(configPath), "status.json", &status) != nil {
		card.Status = "down"
		card.StatusText = "状态异常"
		card.LastEvent = "无法读取扫描状态"
		return card
	}
	checked, e := time.Parse(time.RFC3339, status.LastCheck)
	if e != nil {
		return card
	}
	card.LastTime = checked.Local().Format("15:04:05")
	if status.Error != "" {
		card.Status = "down"
		card.StatusText = "扫描异常"
		card.LastEvent = status.Error
		if xAutoRecoveringError(status.ErrorCode) {
			card.Status = "attention"
			card.StatusText = "自动恢复中"
			card.SuppressAlert = true
			if retry, err := time.Parse(time.RFC3339, status.NextRetryAt); err == nil {
				card.LastEvent = fmt.Sprintf("%s；%s 自动重试", status.Error, retry.Local().Format("15:04:05"))
			}
		}
		return card
	}
	if now.Sub(checked) > time.Duration(cfg.PollSeconds)*time.Second+4*time.Minute {
		card.Status = "down"
		card.StatusText = "扫描超时"
		card.LastEvent = "长时间没有完成扫描，请检查主控服务"
		return card
	}
	if status.LastCheck == status.LastSuccess {
		card.Status = "healthy"
		card.StatusText = "运行中"
	}
	card.LastEvent = fmt.Sprintf("扫描完成：%d 条帖子，%d 个订阅；新内容 %d 条已入推送队列", status.Events, count, status.Forwarded)
	return card
}

func xAutoRecoveringError(code string) bool {
	switch code {
	case "account_unavailable", "timeout", "collection_failed", "timeline_unavailable":
		return true
	default:
		return false
	}
}
