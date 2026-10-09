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
	// ImageTargets is the global fallback list used when a community has no
	// dedicated entry in CommunityImageTargets.
	ImageTargets []string `json:"imageTargets,omitempty"`
	// CommunityImageTargets maps a normalized community key (see
	// NormalizeCommunityKey) to the targets that community's report PNG should be
	// sent to. This is what lets Hearts2Hearts and any future community fan out
	// to different groups / private chats.
	CommunityImageTargets map[string][]string `json:"communityImageTargets,omitempty"`
}

// NormalizeCommunityKey is the stable key used by CommunityImageTargets.
func NormalizeCommunityKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ReportImageTargets returns the fan-out targets for one community's report PNG:
// its own list when configured, otherwise the global fallback.
func (s ReportSettings) ReportImageTargets(community string) []string {
	if list, ok := s.CommunityImageTargets[NormalizeCommunityKey(community)]; ok && len(list) > 0 {
		return list
	}
	return s.ImageTargets
}

func LoadReportSettings(dir string) (ReportSettings, error) {
	s := ReportSettings{Monthly: true, FirstHalf: true, Annual: true, SendTime: "00:00", CommunityID: 235, CommunityName: "Hearts2Hearts"}
	e := Read(dir, "reports.json", &s)
	// Scheduled reports are always released as soon as the reporting period ends.
	// Keep the field in the JSON contract so older admin clients remain compatible.
	s.SendTime = "00:00"
	return s, e
}
func SaveReportSettings(dir string, s ReportSettings) error {
	s.SendTime = "00:00"
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
		due := p.End
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
	PostPhotos        int            `json:"postPhotos"`
	PostComments      int64          `json:"postComments"`
	PostLikes         int64          `json:"postLikes"`
	CommentPosts      int            `json:"commentPosts"`
	LikePosts         int            `json:"likePosts"`
	FanReplies        int            `json:"fanReplies"`
	MemberReplies     int            `json:"memberReplies"`
	SelfReplies       int            `json:"selfReplies"`
	UnknownReplies    int            `json:"unknownReplies"`
	ReplyMembers      map[string]int `json:"replyMembers"`
	Videos            int            `json:"videos"`
	Moments           int            `json:"moments"`
	LiveSeconds       int64          `json:"liveSeconds"`
	TimedLives        int            `json:"timedLives"`
	SoloLiveSeconds   int64          `json:"soloLiveSeconds"`
	SoloLives         int            `json:"soloLives"`
	LiveChats         int            `json:"liveChats"`
	LiveChatLives     int            `json:"liveChatLives"`
	LiveChatHosts     map[string]int `json:"liveChatHosts"`
	LiveChatHostLives map[string]int `json:"liveChatHostLives"`

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
	Month            string        `json:"month"`
	Members          []MemberCount `json:"members"`
	UnconfirmedLives int           `json:"unconfirmedLives"`
}
type LiveChatTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	IsMember bool   `json:"isMember"`
}
type Report struct {
	Months []ReportMonth `json:"months"`

	UnconfirmedLives        int              `json:"unconfirmedLives"`
	MissingParentComments   int              `json:"missingParentComments"`
	AsOf                    time.Time        `json:"asOf"`
	Period                  ReportPeriod     `json:"period"`
	Community               string           `json:"community"`
	Members                 []MemberCount    `json:"members"`
	TeammateReplies         []TeammateReply  `json:"teammateReplies"`
	Events                  []Event          `json:"events"`
	LiveChatTargets         []LiveChatTarget `json:"liveChatTargets"`
	RecordingStarted        time.Time        `json:"recordingStarted"`
	RestrictedPosts         int              `json:"restrictedPosts"`
	RestrictedContextPosts  int              `json:"restrictedContextPosts"`
	UnavailablePosts        int              `json:"unavailablePosts"`
	UnavailableContextPosts int              `json:"unavailableContextPosts"`
	UnknownRootReplies      int              `json:"unknownRootReplies"`
	Complete                bool             `json:"complete"`
}

func (h *History) BuildReport(s ReportSettings, p ReportPeriod) (Report, error) {
	events, e := h.Period(s.CommunityID, p.Start, p.End)
	if e != nil {
		return Report{}, e
	}
	if e = h.applyLiveAssignments(s.CommunityID, events); e != nil {
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
	if _, e := time.Parse(time.RFC3339, completed); e == nil {
		r.Complete = true
	}
	var unavailable string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfillUnavailable:"+p.Key).Scan(&unavailable)
	r.UnavailablePosts, _ = strconv.Atoi(unavailable)
	var unavailableContext string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfillUnavailableContext:"+p.Key).Scan(&unavailableContext)
	r.UnavailableContextPosts, _ = strconv.Atoi(unavailableContext)
	var restricted string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfillRestricted:"+p.Key).Scan(&restricted)
	r.RestrictedPosts, _ = strconv.Atoi(restricted)
	var restrictedContext string
	_ = h.db.QueryRow("SELECT value FROM metadata WHERE key=?", "backfillRestrictedContext:"+p.Key).Scan(&restrictedContext)
	r.RestrictedContextPosts, _ = strconv.Atoi(restrictedContext)
	if r.UnavailablePosts > 0 {
		r.Complete = false
	}
	return r, nil
}
func AggregateReport(s ReportSettings, p ReportPeriod, members []Member, events []Event) Report {
	r := Report{AsOf: time.Now(), Period: p, Community: s.CommunityName, Members: []MemberCount{}, TeammateReplies: []TeammateReply{}, Events: []Event{}}
	events = append([]Event(nil), events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Time < events[j].Time })
	index := map[string]int{}
	owners := map[string]string{}
	ageOrder := map[string]int{"CARMEN": 0, "JIWOO": 1, "YUHA": 2, "STELLA": 3, "JUUN": 4, "A-NA": 5, "IAN": 6, "YE-ON": 7}
	sort.SliceStable(members, func(i, j int) bool {
		left, leftKnown := ageOrder[strings.ToUpper(strings.TrimSpace(members[i].Name))]
		right, rightKnown := ageOrder[strings.ToUpper(strings.TrimSpace(members[j].Name))]
		if leftKnown != rightKnown {
			return leftKnown
		}
		if leftKnown {
			return left < right
		}
		return members[i].Name < members[j].Name
	})
	for _, m := range members {
		if community := strings.TrimSpace(s.CommunityName); community != "" && strings.EqualFold(strings.TrimSpace(m.Name), community) {
			continue
		}
		index[m.ID] = len(r.Members)
		r.Members = append(r.Members, MemberCount{ID: m.ID, Name: m.Name, Teammates: map[string]int{}, ReplyMembers: map[string]int{}, LiveChatHosts: map[string]int{}, LiveChatHostLives: map[string]int{}})
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
	liveChatLives := map[string]map[string]bool{}
	liveChatHostLives := map[string]bool{}
	liveChatTargets := map[string]LiveChatTarget{}
	type liveRun struct {
		end       int64
		timed     bool
		soloTimed bool
	}
	liveRuns := map[string]*liveRun{}
	for _, m := range r.Members {
		liveChatTargets[m.ID] = LiveChatTarget{ID: m.ID, Name: m.Name, IsMember: true}
	}
	for _, e := range events {
		if seen[e.ID] || e.CommunityID != s.CommunityID || e.Time < p.Start.UnixMilli() || e.Time >= p.End.UnixMilli() {
			continue
		}
		seen[e.ID] = true
		if e.Kind == "live" && !e.LiveParticipantsConfirmed {
			r.UnconfirmedLives++
		}
		i, ok := index[e.MemberID]
		if !ok && e.Kind != "live" {
			continue
		}
		if e.Kind != "post" && e.Kind != "comment" && e.Kind != "live" && e.Kind != "live_chat" && e.Kind != "moment" {
			continue
		}
		r.Events = append(r.Events, e)
		var m *MemberCount
		if ok {
			m = &r.Members[i]
		}
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
			targetID := e.ParentMemberID
			targetType := e.ParentProfileType
			if targetID == "" && e.PostContext != nil {
				targetID = e.PostContext.MemberID
				if !e.PostContext.AuthorIsArtist {
					targetType = "FAN"
				}
			}
			if targetID == e.MemberID {
				m.SelfReplies++
			} else if _, known := index[targetID]; known {
				m.MemberReplies++
				m.ReplyMembers[targetID]++
			} else if targetType == "FAN" {
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
			participants := []string{e.MemberID}
			if e.LiveParticipantsConfirmed {
				participants = e.LiveParticipants
			}
			participantKey := append([]string(nil), participants...)
			sort.Strings(participantKey)
			startedAt := e.LiveStartedAt
			if startedAt <= 0 {
				startedAt = e.Time
			}
			day := time.UnixMilli(startedAt).In(ReportLocation).Format("2006-01-02")
			key := day + "\x00" + strings.Join(participantKey, "\x00")
			endAt := startedAt + e.LiveDuration*1000
			if endAt < startedAt {
				endAt = startedAt
			}
			run, merged := liveRuns[key]
			if !merged || startedAt > run.end+int64(30*time.Minute/time.Millisecond) {
				run = &liveRun{end: endAt}
				liveRuns[key] = run
				merged = false
			} else if endAt > run.end {
				run.end = endAt
			}
			for _, participantID := range participants {
				participantIndex, known := index[participantID]
				if !known {
					continue
				}
				participant := &r.Members[participantIndex]
				if !merged {
					participant.Lives++
				}
				if e.LiveDuration > 0 {
					participant.LiveSeconds += e.LiveDuration
					if !run.timed {
						participant.TimedLives++
					}
				}
				if e.LiveParticipantsConfirmed && len(participants) == 1 && e.LiveDuration > 0 {
					if !run.soloTimed {
						participant.SoloLives++
					}
					participant.SoloLiveSeconds += e.LiveDuration
				}
			}
			if e.LiveDuration > 0 {
				run.timed = true
				if e.LiveParticipantsConfirmed && len(participants) == 1 {
					run.soloTimed = true
				}
			}
		case "live_chat":
			m.LiveChats++
			hostIDs := []string{e.LiveHostMemberID}
			if e.LiveParticipantsConfirmed {
				hostIDs = e.LiveParticipants
			}
			if len(hostIDs) == 0 || hostIDs[0] == "" {
				hostIDs = []string{"__unknown_live_host__"}
			}
			for _, hostID := range hostIDs {
				if hostID == e.MemberID {
					continue
				}
				m.LiveChatHosts[hostID]++
				if _, known := liveChatTargets[hostID]; !known {
					name := strings.TrimSpace(e.LiveHostAuthor)
					if name == "" {
						name = "未识别发起账号"
					} else {
						name = "团体账号：" + name
					}
					liveChatTargets[hostID] = LiveChatTarget{ID: hostID, Name: name}
				}
				hostLiveKey := e.MemberID + "\x00" + hostID + "\x00" + e.PostID
				if !liveChatHostLives[hostLiveKey] {
					liveChatHostLives[hostLiveKey] = true
					m.LiveChatHostLives[hostID]++
				}
			}
			if liveChatLives[e.MemberID] == nil {
				liveChatLives[e.MemberID] = map[string]bool{}
			}
			if !liveChatLives[e.MemberID][e.PostID] {
				liveChatLives[e.MemberID][e.PostID] = true
				m.LiveChatLives++
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
	for _, m := range r.Members {
		r.LiveChatTargets = append(r.LiveChatTargets, liveChatTargets[m.ID])
		delete(liveChatTargets, m.ID)
	}
	extraTargets := make([]LiveChatTarget, 0, len(liveChatTargets))
	for _, target := range liveChatTargets {
		extraTargets = append(extraTargets, target)
	}
	sort.Slice(extraTargets, func(i, j int) bool {
		if extraTargets[i].Name == extraTargets[j].Name {
			return extraTargets[i].ID < extraTargets[j].ID
		}
		return extraTargets[i].Name < extraTargets[j].Name
	})
	r.LiveChatTargets = append(r.LiveChatTargets, extraTargets...)
	sort.Slice(r.Events, func(i, j int) bool { return r.Events[i].Time < r.Events[j].Time })
	sort.Slice(r.TeammateReplies, func(i, j int) bool { return r.TeammateReplies[i].Time < r.TeammateReplies[j].Time })
	if p.Kind == "firstHalf" || p.Kind == "annual" {
		for start := p.Start; start.Before(p.End); start = start.AddDate(0, 1, 0) {
			sub, _ := NewReportPeriod("monthly", start.Year(), int(start.Month()))
			month := AggregateReport(s, sub, append([]Member(nil), members...), events)
			r.Months = append(r.Months, ReportMonth{Month: start.Format("2006-01"), Members: month.Members, UnconfirmedLives: month.UnconfirmedLives})
		}
	}
	return r
}
func (r Report) CoverageItems() []string {
	parts := []string{
		"统计时间均为北京时间；实时记录开始于 " + r.RecordingStarted.In(ReportLocation).Format("2006-01-02 15:04") + "。此前可见的帖子、回复、直播及直播弹幕已做历史回采。",
		"Moment 仅在发布后 24 小时内开放评论和 Cheer；超过后不一定能从历史列表重新发现，因此较早 Moment 可能缺失。",
		"累计评论和累计点赞只统计本期发布的帖子；成员回复按回复发生时间统计，包含成员在过往帖子下的新回复。",
		"成员回复按直接对象分为粉丝、成员和自己；队友帖子回复另按主帖作者统计。",
		"直播时长按面板确认的所属成员统计。相同成员同日、前后间隔不超过 30 分钟的连续开播合并为 1 场；总时长没有数据时记为 00:00:00，单人时长在直播完成归属确认前显示待确认。",
		"发弹幕直播场次指至少发送过 1 条弹幕的不同直播，不表示本人参与或客串直播。",
		"报告生成于 " + r.AsOf.In(ReportLocation).Format("2006-01-02 15:04") + "；详细记录与采集时间见 Excel。",
	}
	if r.UnavailablePosts > 0 {
		parts = append(parts, fmt.Sprintf("本期有 %d 条帖子或直播当前不可访问，相关内容可能缺失。", r.UnavailablePosts))
	}
	if r.RestrictedPosts > 0 {
		parts = append(parts, fmt.Sprintf("本期有 %d 条会员专属帖子：基础记录及列表可见的互动数已统计，正文和完整回复上下文无权限读取。", r.RestrictedPosts))
	}
	if r.RestrictedContextPosts > 0 {
		parts = append(parts, fmt.Sprintf("另有 %d 条本期以前的会员专属帖子，基础记录可识别，但无权限检查其中是否新增本期成员回复。", r.RestrictedContextPosts))
	}
	if r.UnavailableContextPosts > 0 {
		parts = append(parts, fmt.Sprintf("另有 %d 条本期以前的其他历史主帖当前不可访问，无法检查其中是否新增本期成员回复；不计作本期发帖缺失。", r.UnavailableContextPosts))
	}
	if r.MissingParentComments > 0 {
		parts = append(parts, fmt.Sprintf("%d 条成员回复的被回复评论不可访问，已保留成员回复并计数，但相关粉丝/队友上下文无法恢复。", r.MissingParentComments))
	}
	if r.UnknownRootReplies > 0 {
		parts = append(parts, fmt.Sprintf("%d 条回复未取得主帖作者，未计入队友帖回复。", r.UnknownRootReplies))
	}
	return parts

}

func (r Report) CoverageNote() string {
	return strings.Join(r.CoverageItems(), "\n")
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
