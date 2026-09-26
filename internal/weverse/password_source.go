package weverse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	h2hPasswordSourceURL = "https://firestore.googleapis.com/v1/projects/h2h-link/databases/(default)/documents/stock_articles?pageSize=200"
	h2hPasswordSource    = "https://h2h-link.com/stock-info/?a=RhJ1OJO0pvGI9mIlFbmk"
)

var h2hPasswordLineRE = regexp.MustCompile(`^([0-9]{6})\s+([^\s：:]+)\s*[：:]\s*(.+)$`)

type PasswordSyncStatus struct {
	LastCheck       string `json:"lastCheck,omitempty"`
	LastSuccess     string `json:"lastSuccess,omitempty"`
	SourceUpdatedAt string `json:"sourceUpdatedAt,omitempty"`
	Entries         int    `json:"entries"`
	Candidates      int    `json:"candidates"`
	Imported        int    `json:"imported"`
	AlreadyKnown    int    `json:"alreadyKnown"`
	Unmatched       int    `json:"unmatched"`
	Error           string `json:"error,omitempty"`
}

type h2hPasswordEntry struct {
	Date       string
	Emoji      string
	MemberName string
	Password   string
}

type passwordCandidate struct {
	PostID   string
	MemberID string
	Date     string
	Kind     string
	URL      string
	Raw      Object
}

type firestoreString struct {
	StringValue    string `json:"stringValue"`
	TimestampValue string `json:"timestampValue"`
}

type firestoreDocument struct {
	Name       string                     `json:"name"`
	Fields     map[string]firestoreString `json:"fields"`
	UpdateTime string                     `json:"updateTime"`
}

type firestoreDocuments struct {
	Documents []firestoreDocument `json:"documents"`
}

var h2hEmojiMembers = map[string]string{
	"🍓": "JIWOO",
	"🌴": "CARMEN",
	"🎀": "YUHA",
	"🧁": "STELLA",
	"👾": "JUUN",
	"🌻": "ANA",
	"🫛": "IAN",
	"😊": "YEON",
}

func normalizeMemberName(s string) string {
	s = strings.ToUpper(s)
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(s)
}

func parseH2HPasswordBody(body string) ([]h2hPasswordEntry, error) {
	entries := []h2hPasswordEntry{}
	for lineNumber, raw := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		match := h2hPasswordLineRE.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("密码来源第 %d 行格式无法识别", lineNumber+1)
		}
		emoji := strings.NewReplacer("\uFE0F", "", "\uFE0E", "").Replace(match[2])
		member := h2hEmojiMembers[emoji]
		password := strings.TrimSpace(match[3])
		if member == "" {
			return nil, fmt.Errorf("密码来源第 %d 行成员标记无法识别", lineNumber+1)
		}
		if password == "" || len(password) > 256 {
			return nil, fmt.Errorf("密码来源第 %d 行密码长度无效（%d 字节）", lineNumber+1, len(password))
		}
		if _, err := time.Parse("060102", match[1]); err != nil {
			return nil, fmt.Errorf("密码来源第 %d 行日期无效", lineNumber+1)
		}
		entries = append(entries, h2hPasswordEntry{Date: match[1], Emoji: emoji, MemberName: member, Password: password})
	}
	if len(entries) == 0 || len(entries) > 200 {
		return nil, fmt.Errorf("密码来源条目数量异常")
	}
	return entries, nil
}

func (c *Client) fetchH2HPasswordEntries(ctx context.Context, sourceURL string) ([]h2hPasswordEntry, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Pocket48Bot/1.0 (+https://h2h-link.com/stock-info/)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("密码来源连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("密码来源返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, "", err
	}
	var payload firestoreDocuments
	if err = json.Unmarshal(body, &payload); err != nil {
		return nil, "", fmt.Errorf("密码来源返回了无法识别的数据")
	}
	for _, doc := range payload.Documents {
		if doc.Fields["groupId"].StringValue != "hearts2hearts" || doc.Fields["category"].StringValue != "weverse_password" {
			continue
		}
		title := doc.Fields["title"].StringValue
		if !strings.Contains(title, "Weverse") || !strings.Contains(title, "パスワード") {
			continue
		}
		entries, parseErr := parseH2HPasswordBody(doc.Fields["body"].StringValue)
		updated := doc.Fields["updatedAt"].TimestampValue
		if updated == "" {
			updated = doc.UpdateTime
		}
		return entries, updated, parseErr
	}
	return nil, "", fmt.Errorf("密码来源中未找到 Weverse 密码条目")
}

func passwordDate(timestamp int64) string {
	if timestamp <= 0 {
		return ""
	}
	location := time.FixedZone("KST", 9*60*60)
	return time.UnixMilli(timestamp).In(location).Format("060102")
}

func recursiveMemberID(value any, allowed map[string]bool) string {
	switch current := value.(type) {
	case map[string]any:
		if id := text(current, "memberId"); allowed[id] {
			return id
		}
		for _, child := range current {
			if id := recursiveMemberID(child, allowed); id != "" {
				return id
			}
		}
	case []any:
		for _, child := range current {
			if id := recursiveMemberID(child, allowed); id != "" {
				return id
			}
		}
	}
	return ""
}

func (c *Client) passwordCandidates(ctx context.Context, communityID int64, slug string, members []Member, entries []h2hPasswordEntry) ([]passwordCandidate, error) {
	memberByName := map[string]string{}
	allowedMembers := map[string]bool{}
	for _, member := range members {
		memberByName[normalizeMemberName(member.Name)] = member.ID
		allowedMembers[member.ID] = true
	}
	wanted := map[string]bool{}
	earliest := "999999"
	for _, entry := range entries {
		memberID := memberByName[entry.MemberName]
		if memberID == "" {
			return nil, fmt.Errorf("无法把 %s 对应到 Weverse 成员", entry.MemberName)
		}
		wanted[entry.Date+"/"+memberID] = true
		if entry.Date < earliest {
			earliest = entry.Date
		}
	}
	start, _ := time.ParseInLocation("060102", earliest, time.FixedZone("KST", 9*60*60))
	candidates := map[string]passwordCandidate{}
	posts, err := c.pagesLimit(ctx, fmt.Sprintf("/post/v1.0/community-%d/artistTabPosts?fieldSet=postsV1&pagingType=CURSOR&limit=100", communityID), "after", "publishedAt", start, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range posts {
		post := obj(item)
		locked, _ := post["locked"].(bool)
		id := text(post, "postId", "id")
		memberID := text(obj(post["author"]), "memberId", "id")
		date := passwordDate(num(post["publishedAt"]))
		if !locked || !idRE.MatchString(id) || !wanted[date+"/"+memberID] {
			continue
		}
		candidates[id] = passwordCandidate{PostID: id, MemberID: memberID, Date: date, Kind: "post", URL: "https://weverse.io/" + slug + "/artist/" + id, Raw: post}
	}
	// The member directory is the most reliable source for a newly published
	// Moment. It remains useful even when the notification feed omits or delays
	// the corresponding activity.
	for _, member := range members {
		id := member.latestMomentPostID
		date := passwordDate(member.latestMomentAt)
		if !member.latestMomentLocked || !idRE.MatchString(id) || !wanted[date+"/"+member.ID] {
			continue
		}
		candidates[id] = passwordCandidate{PostID: id, MemberID: member.ID, Date: date, Kind: "moment", URL: "https://weverse.io/" + slug + "/moment/" + member.ID + "/post/" + id}
	}

	notifications, err := c.pagesLimit(ctx, fmt.Sprintf("/noti/feed/v2.0/activities?communityId=%d&count=100&seen=false&excludeGroup=COLLECTION,CO_HOST_LIVE,PARTY,CALENDAR", communityID), "next", "time", start, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range notifications {
		notification := obj(item)
		typ := strings.ToUpper(text(notification, "activityType", "type"))
		if !strings.Contains(typ, "MOMENT") || strings.Contains(typ, "COMMENT") {
			continue
		}
		targetSlug, id, _ := notificationTarget(notification)
		date := passwordDate(num(notification["time"]))
		memberID := recursiveMemberID(notification, allowedMembers)
		if targetSlug != slug || !idRE.MatchString(id) || !wanted[date+"/"+memberID] {
			continue
		}
		if _, exists := candidates[id]; exists {
			continue
		}
		// Confirm that this is actually password-protected. Public and
		// membership-only Moments on the same date must never receive a password.
		var post Object
		err := c.call(ctx, "/post/v1.0/post-"+id+"?fieldSet=postV1", true, &post)
		if !errors.Is(err, ErrPostPassword) {
			if err == nil || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		candidates[id] = passwordCandidate{PostID: id, MemberID: memberID, Date: date, Kind: "moment", URL: "https://weverse.io/" + slug + "/moment/" + memberID + "/post/" + id}
	}
	result := make([]passwordCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Date == result[j].Date {
			return result[i].PostID < result[j].PostID
		}
		return result[i].Date < result[j].Date
	})
	return result, nil
}

func (c *Client) verifyAndSaveSourcePassword(ctx context.Context, candidate passwordCandidate, password string) (Object, error) {
	query := url.Values{"fieldSet": {"postV1"}, "lockPassword": {password}}
	var post Object
	if err := c.call(ctx, "/post/v1.0/post-"+candidate.PostID+"?"+query.Encode(), true, &post); err != nil {
		return nil, err
	}
	if text(post, "postId", "id") != candidate.PostID {
		return nil, fmt.Errorf("接口未返回目标帖子")
	}
	row := PostPassword{PostID: candidate.PostID, URL: candidate.URL, Password: password, VerifiedAt: time.Now().Format(time.RFC3339), Source: h2hPasswordSource}
	if _, err := savePostPasswordRow(c.Dir, row); err != nil {
		return nil, err
	}
	return post, nil
}

// SyncH2HPasswords imports only passwords published by the named public source.
// It never guesses passwords: a source row must match the official post date and
// member, and Weverse must accept it before it is persisted.
func (c *Client) SyncH2HPasswords(ctx context.Context, history *History, communityID int64, slug string) (PasswordSyncStatus, error) {
	return c.syncH2HPasswords(ctx, history, communityID, slug, h2hPasswordSourceURL)
}

func (c *Client) syncH2HPasswords(ctx context.Context, history *History, communityID int64, slug, sourceURL string) (status PasswordSyncStatus, resultErr error) {
	status.LastCheck = time.Now().Format(time.RFC3339)
	defer func() {
		if resultErr != nil {
			status.Error = resultErr.Error()
		}
	}()
	entries, updated, err := c.fetchH2HPasswordEntries(ctx, sourceURL)
	if err != nil {
		return status, err
	}
	status.SourceUpdatedAt = updated
	status.Entries = len(entries)
	members, err := c.Members(ctx, communityID)
	if err != nil {
		return status, err
	}
	memberIDs := map[string]string{}
	memberNames := map[string]string{}
	for _, member := range members {
		memberIDs[normalizeMemberName(member.Name)] = member.ID
		memberNames[member.ID] = member.Name
	}
	candidates, err := c.passwordCandidates(ctx, communityID, slug, members, entries)
	if err != nil {
		return status, err
	}
	status.Candidates = len(candidates)
	stored, err := LoadPostPasswords(c.Dir)
	if err != nil {
		return status, err
	}
	known := map[string]string{}
	for _, row := range stored {
		if row.Password != "" {
			known[row.PostID] = row.Password
		}
	}
	matchedEntries := map[int]bool{}
	for _, candidate := range candidates {
		entryIndexes := []int{}
		for index, entry := range entries {
			if entry.Date == candidate.Date && memberIDs[entry.MemberName] == candidate.MemberID {
				entryIndexes = append(entryIndexes, index)
			}
		}
		if len(entryIndexes) == 0 {
			continue
		}
		if savedPassword := known[candidate.PostID]; savedPassword != "" {
			// Exact comparison disambiguates multiple locked posts from the same
			// member on the same date without sending any additional requests.
			matched := -1
			for _, index := range entryIndexes {
				if !matchedEntries[index] && entries[index].Password == savedPassword {
					matched = index
					break
				}
			}
			if matched < 0 {
				for _, index := range entryIndexes {
					if !matchedEntries[index] {
						matched = index
						break
					}
				}
			}
			if matched >= 0 {
				matchedEntries[matched] = true
			}
			status.AlreadyKnown++
			continue
		}
		var post Object
		matched := -1
		for _, index := range entryIndexes {
			if matchedEntries[index] {
				continue
			}
			var verifyErr error
			post, verifyErr = c.verifyAndSaveSourcePassword(ctx, candidate, entries[index].Password)
			if errors.Is(verifyErr, ErrPostPassword) {
				continue
			}
			if errors.Is(verifyErr, ErrForbidden) || errors.Is(verifyErr, ErrNotFound) {
				break
			}
			if verifyErr != nil {
				return status, verifyErr
			}
			matched = index
			break
		}
		if matched < 0 {
			continue
		}
		matchedEntries[matched] = true
		known[candidate.PostID] = entries[matched].Password
		status.Imported++
		// A successfully supplied password makes the body accessible even when
		// the response retains locked=true as metadata.
		post["locked"] = false
		if history != nil {
			event, parseErr := eventFromPost(post, slug, communityID)
			if parseErr != nil {
				return status, parseErr
			}
			if event.ID != "" {
				event.Author = memberNames[event.MemberID]
				if err = history.Record([]Event{event}, ""); err != nil {
					return status, err
				}
			}
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	status.Unmatched = len(entries) - len(matchedEntries)
	status.LastSuccess = time.Now().Format(time.RFC3339)
	return status, nil
}
