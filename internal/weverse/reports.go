package weverse

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ReportSettings struct {
	Enabled         bool   `json:"enabled"`
	Weekly          bool   `json:"weekly"`
	WeeklyEnabledAt string `json:"weeklyEnabledAt,omitempty"`
	Monthly         bool   `json:"monthly"`
	FirstHalf       bool   `json:"firstHalf"`
	Annual          bool   `json:"annual"`
	SendTime        string `json:"sendTime"`
	CommunityID     int64  `json:"communityId"`
	CommunityName   string `json:"communityName"`
	EnabledAt       string `json:"enabledAt,omitempty"`
}

func LoadReportSettings(dir string) (ReportSettings, error) {
	s := ReportSettings{Monthly: true, FirstHalf: true, Annual: true, SendTime: "09:00", CommunityID: 235, CommunityName: "Hearts2Hearts"}
	e := Read(dir, "reports.json", &s)
	return s, e
}
func SaveReportSettings(dir string, s ReportSettings) error {
	if _, e := time.Parse("15:04", s.SendTime); e != nil {
		return fmt.Errorf("发送时间应为 HH:MM")
	}
	if s.CommunityID <= 0 {
		return fmt.Errorf("请选择有效社区")
	}
	old, e := LoadReportSettings(dir)
	if e != nil {
		return e
	}
	s.EnabledAt = old.EnabledAt
	s.WeeklyEnabledAt = old.WeeklyEnabledAt
	if s.Enabled && s.Weekly && (s.WeeklyEnabledAt == "" || !old.Enabled || !old.Weekly) {
		s.WeeklyEnabledAt = time.Now().Format(time.RFC3339)
	}
	if s.Enabled && (s.EnabledAt == "" || !old.Enabled) {
		s.EnabledAt = time.Now().Format(time.RFC3339)
	}
	return Write(dir, "reports.json", s)
}

var ReportLocation = time.FixedZone("Asia/Shanghai", 8*3600)

type ReportPeriod struct {
	Kind  string    `json:"kind"`
	Key   string    `json:"key"`
	Title string    `json:"title"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func NewReportPeriod(kind string, year, month int) (ReportPeriod, error) {
	if year < 2025 || year > 2100 {
		return ReportPeriod{}, fmt.Errorf("年份超出范围")
	}
	p := ReportPeriod{Kind: kind}
	switch kind {
	case "monthly":
		if month < 1 || month > 12 {
			return p, fmt.Errorf("月份应为 1–12")
		}
		p.Start = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, ReportLocation)
		p.End = p.Start.AddDate(0, 1, 0)
		p.Title = fmt.Sprintf("%d年%d月月报", year, month)
		p.Key = fmt.Sprintf("monthly-%04d-%02d", year, month)
	case "firstHalf":
		p.Start = time.Date(year, 1, 1, 0, 0, 0, 0, ReportLocation)
		p.End = time.Date(year, 7, 1, 0, 0, 0, 0, ReportLocation)
		p.Title = fmt.Sprintf("%d年上半年报", year)
		p.Key = fmt.Sprintf("firstHalf-%04d", year)
	case "annual":
		p.Start = time.Date(year, 1, 1, 0, 0, 0, 0, ReportLocation)
		p.End = p.Start.AddDate(1, 0, 0)
		p.Title = fmt.Sprintf("%d年年报", year)
		p.Key = fmt.Sprintf("annual-%04d", year)
	default:
		return p, fmt.Errorf("报表类型无效")
	}
	return p, nil
}

// NewWeeklyReportPeriod accepts any date within a week and normalizes it to
// Monday 00:00 through the following Monday 00:00 in Beijing time.
func NewWeeklyReportPeriod(date string) (ReportPeriod, error) {
	day, err := time.ParseInLocation("2006-01-02", date, ReportLocation)
	if err != nil || day.Year() < 2025 || day.Year() > 2100 {
		return ReportPeriod{}, fmt.Errorf("周报日期应为 2025–2100 年内的 YYYY-MM-DD")
	}
	offset := (int(day.Weekday()) + 6) % 7
	start := day.AddDate(0, 0, -offset)
	end := start.AddDate(0, 0, 7)
	return ReportPeriod{Kind: "weekly", Key: "weekly-" + start.Format("2006-01-02"),
		Title: fmt.Sprintf("%s 至 %s 周报", start.Format("2006-01-02"), end.AddDate(0, 0, -1).Format("2006-01-02")),
		Start: start, End: end}, nil
}

func DueReportPeriods(s ReportSettings, now time.Time) []ReportPeriod {
	if !s.Enabled {
		return nil
	}
	enabled, e := time.Parse(time.RFC3339, s.EnabledAt)
	if e != nil {
		return nil
	}
	clock, e := time.Parse("15:04", s.SendTime)
	if e != nil {
		return nil
	}
	now = now.In(ReportLocation)
	var out []ReportPeriod
	prior := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, ReportLocation).AddDate(0, -1, 0)
	monthly, _ := NewReportPeriod("monthly", prior.Year(), int(prior.Month()))
	hy := now.Year()
	if now.Month() < 7 {
		hy--
	}
	half, _ := NewReportPeriod("firstHalf", hy, 1)
	annual, _ := NewReportPeriod("annual", now.Year()-1, 1)
	thisWeek, weeklyErr := NewWeeklyReportPeriod(now.Format("2006-01-02"))
	periods := []ReportPeriod{monthly, half, annual}
	if weeklyErr == nil {
		weekly, err := NewWeeklyReportPeriod(thisWeek.Start.AddDate(0, 0, -7).Format("2006-01-02"))
		if err == nil {
			periods = append([]ReportPeriod{weekly}, periods...)
		}
	}
	for _, p := range periods {
		on := p.Kind == "weekly" && s.Weekly || p.Kind == "monthly" && s.Monthly || p.Kind == "firstHalf" && s.FirstHalf || p.Kind == "annual" && s.Annual
		due := p.End.Add(time.Duration(clock.Hour())*time.Hour + time.Duration(clock.Minute())*time.Minute)
		periodEnabled := enabled
		if p.Kind == "weekly" {
			// Adding weekly reports must not immediately back-send a completed week.
			weeklyEnabled, err := time.Parse(time.RFC3339, s.WeeklyEnabledAt)
			if err != nil {
				continue
			}
			if weeklyEnabled.After(periodEnabled) {
				periodEnabled = weeklyEnabled
			}
		}
		if on && !due.Before(periodEnabled) && !now.Before(due) {
			out = append(out, p)
		}
	}
	return out
}

type MemberCount struct {
	PostPhotos      int            `json:"postPhotos"`
	PostComments    int64          `json:"postComments"`
	PostLikes       int64          `json:"postLikes"`
	CommentPosts    int            `json:"commentPosts"`
	LikePosts       int            `json:"likePosts"`
	FanReplies      int            `json:"fanReplies"`
	MemberReplies   int            `json:"memberReplies"`
	SelfReplies     int            `json:"selfReplies"`
	UnknownReplies  int            `json:"unknownReplies"`
	ReplyMembers    map[string]int `json:"replyMembers"`
	Videos          int            `json:"videos"`
	Moments         int            `json:"moments"`
	LiveSeconds     int64          `json:"liveSeconds"`
	TimedLives      int            `json:"timedLives"`
	SoloLiveSeconds int64          `json:"soloLiveSeconds"`
	SoloLives       int            `json:"soloLives"`

	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Posts           int            `json:"posts"`
	Replies         int            `json:"replies"`
	ImageMessages   int            `json:"imageMessages"`
	Images          int            `json:"images"`
	VideoMessages   int            `json:"videoMessages"`
	Lives           int            `json:"lives"`
	TeammateReplies int            `json:"teammateReplies"`
	Teammates       map[string]int `json:"teammates"`
}
type TeammateReply struct {
	Author string `json:"author"`
	Owner  string `json:"owner"`
	PostID string `json:"postId"`
	URL    string `json:"url"`
	Time   int64  `json:"time"`
	Body   string `json:"body"`
}
type ReportMonth struct {
	Month   string        `json:"month"`
	Members []MemberCount `json:"members"`
}
type Report struct {
	Months []ReportMonth `json:"months"`

	MissingParentComments int             `json:"missingParentComments"`
	AsOf                  time.Time       `json:"asOf"`
	Period                ReportPeriod    `json:"period"`
	Community             string          `json:"community"`
	Members               []MemberCount   `json:"members"`
	TeammateReplies       []TeammateReply `json:"teammateReplies"`
	Events                []Event         `json:"events"`
	RecordingStarted      time.Time       `json:"recordingStarted"`
	UnavailablePosts      int             `json:"unavailablePosts"`
	UnknownRootReplies    int             `json:"unknownRootReplies"`
	Complete              bool            `json:"complete"`
}

func (h *History) BuildReport(s ReportSettings, p ReportPeriod) (Report, error) {
	events, e := h.Period(s.CommunityID, p.Start, p.End)
	if e != nil {
		return Report{}, e
	}
	members, e := h.Members(s.CommunityID)
	if e != nil {
		return Report{}, e
	}
	started, e := h.RecordingStarted()
	if e != nil {
		return Report{}, e
	}
	r := AggregateReport(s, p, members, events)
	r.RecordingStarted = started
	r.Complete = false
	var completed string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfill:"+p.Key).Scan(&completed)
	if finished, e := time.Parse(time.RFC3339, completed); e == nil && !finished.Before(p.End) {
		r.Complete = true
	}
	var unavailable string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfillUnavailable:"+p.Key).Scan(&unavailable)
	r.UnavailablePosts, _ = strconv.Atoi(unavailable)
	if r.UnavailablePosts > 0 {
		r.Complete = false
	}
	return r, nil
}
func AggregateReport(s ReportSettings, p ReportPeriod, members []Member, events []Event) Report {
	r := Report{AsOf: time.Now(), Period: p, Community: s.CommunityName, Members: []MemberCount{}, TeammateReplies: []TeammateReply{}, Events: []Event{}}
	index := map[string]int{}
	owners := map[string]string{}
	sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })
	for _, m := range members {
		index[m.ID] = len(r.Members)
		r.Members = append(r.Members, MemberCount{ID: m.ID, Name: m.Name, Teammates: map[string]int{}, ReplyMembers: map[string]int{}})
	}
	for _, e := range events {
		if e.Kind == "post" {
			owners[e.PostID] = e.MemberID
		}
		if e.PostContext != nil {
			owners[e.PostID] = e.PostContext.MemberID
		}
	}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e.ID] || e.CommunityID != s.CommunityID || e.Time < p.Start.UnixMilli() || e.Time >= p.End.UnixMilli() {
			continue
		}
		seen[e.ID] = true
		i, ok := index[e.MemberID]
		if !ok {
			continue
		}
		if e.Kind != "post" && e.Kind != "comment" && e.Kind != "live" && e.Kind != "moment" {
			continue
		}
		r.Events = append(r.Events, e)
		m := &r.Members[i]
		switch e.Kind {
		case "post":
			m.Posts++
			m.PostPhotos += len(e.Images)
			if e.PostComments != nil {
				m.PostComments += *e.PostComments
				m.CommentPosts++
			}
			if e.PostLikes != nil {
				m.PostLikes += *e.PostLikes
				m.LikePosts++
			}
		case "comment":
			m.Replies++
			if e.ParentMemberID == e.MemberID {
				m.SelfReplies++
			} else if _, known := index[e.ParentMemberID]; known {
				m.MemberReplies++
				m.ReplyMembers[e.ParentMemberID]++
			} else if e.ParentProfileType == "FAN" {
				m.FanReplies++
			} else {
				m.UnknownReplies++
			}
			if e.ParentContextUnavailable {
				r.MissingParentComments++
			}
			owner := owners[e.PostID]
			if owner == "" {
				r.UnknownRootReplies++
			} else if j, ok := index[owner]; ok && owner != e.MemberID {
				m.TeammateReplies++
				m.Teammates[owner]++
				r.TeammateReplies = append(r.TeammateReplies, TeammateReply{Author: m.Name, Owner: r.Members[j].Name, PostID: e.PostID, URL: e.URL, Time: e.Time, Body: e.Body})
			}
		case "live":
			m.Lives++
			if e.LiveDuration > 0 {
				m.LiveSeconds += e.LiveDuration
				m.TimedLives++
			}
			if len(e.LiveParticipants) == 1 && e.LiveParticipants[0] == e.MemberID && e.LiveDuration > 0 {
				m.SoloLives++
				m.SoloLiveSeconds += e.LiveDuration
			}
		case "moment":
			m.Moments++
		}
		if e.Kind != "live" {
			if len(e.Images) > 0 {
				m.ImageMessages++
				m.Images += len(e.Images)
			}
			if len(e.Videos) > 0 {
				m.VideoMessages++
				m.Videos += len(e.Videos)
			}
		}
	}
	sort.Slice(r.Events, func(i, j int) bool { return r.Events[i].Time < r.Events[j].Time })
	sort.Slice(r.TeammateReplies, func(i, j int) bool { return r.TeammateReplies[i].Time < r.TeammateReplies[j].Time })
	if p.Kind == "firstHalf" || p.Kind == "annual" {
		for start := p.Start; start.Before(p.End); start = start.AddDate(0, 1, 0) {
			sub, _ := NewReportPeriod("monthly", start.Year(), int(start.Month()))
			month := AggregateReport(s, sub, append([]Member(nil), members...), events)
			r.Months = append(r.Months, ReportMonth{Month: start.Format("2006-01"), Members: month.Members})
		}
	}
	return r
}
func (r Report) CoverageNote() string {
	parts := []string{"按北京时间统计。帖子照片只统计主帖照片；视频按附件数量统计，包含帖子、回复和 Moment，排除直播回放。回复按直接被回复者区分粉丝、其他成员、自己，身份不明单列；队友帖下回复另按主帖作者统计。评论/点赞是本期发布帖子的最新已采集累计值，不是本期新增互动；缺少采集值标为缺失。Moment 只计已采集的独立内容，过期历史可能无法回采。直播按发起账号计开播；团体账号发起的直播不能可靠分配到单个成员，因此不计入八位成员的场次；时长仅统计有时长数据的直播，单人时长仅在参与成员名单明确为一人时统计，未识别参与者不推测单人。", "报告生成于 " + r.AsOf.In(ReportLocation).Format("2006-01-02 15:04:05") + "；累计互动的具体采集时间见 Excel 活动明细。"}
	if r.Complete {
		parts = append(parts, "本期可见内容已完成历史回采；已删除或无权限内容可能无法恢复。")
	}
	if !r.Complete {
		parts = append(parts, "本期历史尚未完整回采，表中仅为已采集记录；缺失记录不代表成员未发布。持续记录开始于 "+r.RecordingStarted.In(ReportLocation).Format("2006-01-02 15:04:05")+"。")
	}
	if r.UnavailablePosts > 0 {
		parts = append(parts, fmt.Sprintf("%d 条历史帖子当前不可访问，其回复可能缺失。", r.UnavailablePosts))
	}
	if r.MissingParentComments > 0 {
		parts = append(parts, fmt.Sprintf("%d 条成员回复的被回复评论不可访问，已保留成员回复并计数，但相关粉丝/队友上下文无法恢复。", r.MissingParentComments))
	}
	if r.UnknownRootReplies > 0 {
		parts = append(parts, fmt.Sprintf("%d 条回复未取得主帖作者，未计入队友帖回复。", r.UnknownRootReplies))
	}
	return strings.Join(parts, "\n")
}

func (r Report) DisplayTitle() string {
	if !r.AsOf.IsZero() && r.Period.End.After(r.AsOf) {
		return r.Period.Title + "（截至" + r.AsOf.In(ReportLocation).Format("1月2日") + "）"
	}
	return r.Period.Title
}

func EngagementText(value int64, known, posts int) string {
	if posts == 0 {
		return "0"
	}
	if known == 0 {
		return "缺失"
	}
	if known < posts {
		return fmt.Sprintf("%d（缺%d帖）", value, posts-known)
	}
	return fmt.Sprint(value)
}
