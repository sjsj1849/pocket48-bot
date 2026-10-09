package xmonitor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type ViralMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var ViralMembers = []ViralMember{
	{ID: "carmen", Name: "CARMEN"},
	{ID: "jiwoo", Name: "JIWOO"},
	{ID: "yuha", Name: "YUHA"},
	{ID: "stella", Name: "STELLA"},
	{ID: "juun", Name: "JUUN"},
	{ID: "a-na", Name: "A-NA"},
	{ID: "ian", Name: "IAN"},
	{ID: "ye-on", Name: "YE-ON"},
	{ID: "group", Name: "全团 / 团体"},
}

var viralAliases = map[string][]string{
	"carmen": {"carmen", "카르멘"},
	"jiwoo":  {"jiwoo", "지우"},
	"yuha":   {"yuha", "유하"},
	"stella": {"stella", "스텔라"},
	"juun":   {"juun", "주은"},
	"a-na":   {"a-na", "a_na", "에이나"},
	"ian":    {"ian", "이안", "정이안"},
	"ye-on":  {"ye-on", "ye_on", "yeon", "예온"},
}

func SuggestViralMembers(body string) []string {
	body = strings.ToLower(body)
	result := []string{}
	for _, member := range ViralMembers[:len(ViralMembers)-1] {
		for _, alias := range viralAliases[member.ID] {
			matched := false
			if regexp.MustCompile(`[a-z]`).MatchString(alias) {
				pattern := `(^|[^a-z0-9])` + regexp.QuoteMeta(alias) + `([^a-z0-9]|$)`
				matched = regexp.MustCompile(pattern).MatchString(body)
			} else {
				matched = strings.Contains(body, alias)
			}
			if matched {
				result = append(result, member.ID)
				break
			}
		}
	}
	return result
}

type ViralPost struct {
	ID               string   `json:"id"`
	AuthorUsername   string   `json:"authorUsername"`
	AuthorName       string   `json:"authorName"`
	Body             string   `json:"body"`
	URL              string   `json:"url"`
	Cover            string   `json:"cover,omitempty"`
	PostedAt         int64    `json:"postedAt"`
	LikeCount        int64    `json:"likeCount"`
	RepostCount      int64    `json:"repostCount"`
	ReplyCount       int64    `json:"replyCount"`
	QuoteCount       int64    `json:"quoteCount"`
	ViewCount        int64    `json:"viewCount"`
	QualifiesLikes   bool     `json:"qualifiesLikes"`
	QualifiesReposts bool     `json:"qualifiesReposts"`
	SuggestedMembers []string `json:"suggestedMembers"`
	ConfirmedMembers []string `json:"confirmedMembers"`
	Status           string   `json:"status"`
	FirstSeenAt      int64    `json:"firstSeenAt"`
	UpdatedAt        int64    `json:"updatedAt"`
	ConfirmedAt      int64    `json:"confirmedAt,omitempty"`
}

type ViralStatus struct {
	LastCheck      string   `json:"lastCheck,omitempty"`
	LastSuccess    string   `json:"lastSuccess,omitempty"`
	Candidates     int      `json:"candidates"`
	Stored         int      `json:"stored"`
	AuthorsScanned int      `json:"authorsScanned"`
	DiscoveredTags []string `json:"discoveredTags,omitempty"`
	Error          string   `json:"error,omitempty"`
}

func (p ViralPost) Scope() string {
	for _, id := range p.ConfirmedMembers {
		if id == "group" {
			return "非单人"
		}
	}
	if len(p.ConfirmedMembers) == 1 {
		return "单人"
	}
	if len(p.ConfirmedMembers) > 1 {
		return "非单人"
	}
	return "待确认"
}

type ViralStore struct{ db *sql.DB }

func OpenViralStore(dir string) (*ViralStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "viral.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS viral_posts (
 id TEXT PRIMARY KEY, author_username TEXT NOT NULL, author_name TEXT NOT NULL,
 body TEXT NOT NULL, url TEXT NOT NULL, cover TEXT NOT NULL, posted_at INTEGER NOT NULL,
 like_count INTEGER NOT NULL, repost_count INTEGER NOT NULL, reply_count INTEGER NOT NULL,
 quote_count INTEGER NOT NULL, view_count INTEGER NOT NULL,
 qualifies_likes INTEGER NOT NULL, qualifies_reposts INTEGER NOT NULL,
 suggested_members TEXT NOT NULL, confirmed_members TEXT NOT NULL DEFAULT '[]',
 status TEXT NOT NULL DEFAULT 'pending', first_seen_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL, confirmed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS viral_posts_status_time ON viral_posts(status,posted_at DESC);
CREATE INDEX IF NOT EXISTS viral_posts_time ON viral_posts(posted_at DESC);`); err != nil {
		db.Close()
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		db.Close()
		return nil, err
	}
	return &ViralStore{db: db}, nil
}

func (s *ViralStore) Close() error { return s.db.Close() }

func (s *ViralStore) Upsert(events []Event, minLikes, minReposts int64, now time.Time) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	stored := 0
	for _, event := range events {
		likes := event.LikeCount >= minLikes
		reposts := event.RepostCount >= minReposts
		if event.ID == "" || event.URL == "" || event.Kind == "repost" || (!likes && !reposts) {
			continue
		}
		suggested, _ := json.Marshal(SuggestViralMembers(event.Body))
		cover := ""
		for _, media := range event.Media {
			cover = media.Cover
			if cover == "" {
				cover = media.URL
			}
			if cover != "" {
				break
			}
		}
		result, err := tx.Exec(`INSERT INTO viral_posts
(id,author_username,author_name,body,url,cover,posted_at,like_count,repost_count,reply_count,quote_count,view_count,qualifies_likes,qualifies_reposts,suggested_members,first_seen_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET author_username=excluded.author_username,author_name=excluded.author_name,
body=excluded.body,url=excluded.url,cover=excluded.cover,posted_at=excluded.posted_at,
like_count=excluded.like_count,repost_count=excluded.repost_count,reply_count=excluded.reply_count,
quote_count=excluded.quote_count,view_count=excluded.view_count,qualifies_likes=excluded.qualifies_likes,
qualifies_reposts=excluded.qualifies_reposts,suggested_members=CASE WHEN viral_posts.status='pending' THEN excluded.suggested_members ELSE viral_posts.suggested_members END,
updated_at=excluded.updated_at`, event.ID, event.Author.Username, event.Author.Name, event.Body, event.URL, cover, event.Time,
			event.LikeCount, event.RepostCount, event.ReplyCount, event.QuoteCount, event.ViewCount,
			likes, reposts, string(suggested), now.UnixMilli(), now.UnixMilli())
		if err != nil {
			return 0, err
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			stored++
		}
	}
	return stored, tx.Commit()
}

type ViralFilter struct {
	Status string
	Search string
	From   int64
	To     int64
	Limit  int
}

func (s *ViralStore) List(filter ViralFilter) ([]ViralPost, error) {
	where, args := []string{"1=1"}, []any{}
	if filter.Status != "" && filter.Status != "all" {
		where, args = append(where, "status=?"), append(args, filter.Status)
	}
	if filter.Search != "" {
		where, args = append(where, "(lower(author_username) LIKE ? OR lower(author_name) LIKE ? OR lower(body) LIKE ?)"), append(args, "%"+strings.ToLower(filter.Search)+"%", "%"+strings.ToLower(filter.Search)+"%", "%"+strings.ToLower(filter.Search)+"%")
	}
	if filter.From > 0 {
		where, args = append(where, "posted_at>=?"), append(args, filter.From)
	}
	if filter.To > 0 {
		where, args = append(where, "posted_at<?"), append(args, filter.To)
	}
	limit := filter.Limit
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT id,author_username,author_name,body,url,cover,posted_at,like_count,repost_count,reply_count,quote_count,view_count,qualifies_likes,qualifies_reposts,suggested_members,confirmed_members,status,first_seen_at,updated_at,confirmed_at FROM viral_posts WHERE `+strings.Join(where, " AND ")+` ORDER BY posted_at DESC,id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts := []ViralPost{}
	for rows.Next() {
		var post ViralPost
		var likes, reposts int
		var suggested, confirmed string
		if err := rows.Scan(&post.ID, &post.AuthorUsername, &post.AuthorName, &post.Body, &post.URL, &post.Cover, &post.PostedAt,
			&post.LikeCount, &post.RepostCount, &post.ReplyCount, &post.QuoteCount, &post.ViewCount, &likes, &reposts,
			&suggested, &confirmed, &post.Status, &post.FirstSeenAt, &post.UpdatedAt, &post.ConfirmedAt); err != nil {
			return nil, err
		}
		post.QualifiesLikes, post.QualifiesReposts = likes != 0, reposts != 0
		if json.Unmarshal([]byte(suggested), &post.SuggestedMembers) != nil || json.Unmarshal([]byte(confirmed), &post.ConfirmedMembers) != nil {
			return nil, fmt.Errorf("X 高热帖子成员记录损坏")
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func validViralMembers(ids []string) ([]string, error) {
	allowed := map[string]bool{}
	order := map[string]int{}
	for i, member := range ViralMembers {
		allowed[member.ID], order[member.ID] = true, i
	}
	seen, result := map[string]bool{}, []string{}
	for _, id := range ids {
		id = strings.TrimSpace(strings.ToLower(id))
		if !allowed[id] {
			return nil, fmt.Errorf("成员归属不正确")
		}
		if !seen[id] {
			seen[id], result = true, append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return order[result[i]] < order[result[j]] })
	return result, nil
}

func (s *ViralStore) Confirm(id string, members []string, exclude bool, now time.Time) error {
	if id == "" {
		return fmt.Errorf("帖子编号不能为空")
	}
	if exclude {
		result, err := s.db.Exec(`UPDATE viral_posts SET status='excluded',confirmed_members='[]',confirmed_at=?,updated_at=? WHERE id=?`, now.UnixMilli(), now.UnixMilli(), id)
		if err != nil {
			return err
		}
		if count, _ := result.RowsAffected(); count == 0 {
			return fmt.Errorf("没有找到该帖子")
		}
		return nil
	}
	clean, err := validViralMembers(members)
	if err != nil {
		return err
	}
	if len(clean) == 0 {
		return fmt.Errorf("请至少选择一位成员或全团")
	}
	data, _ := json.Marshal(clean)
	result, err := s.db.Exec(`UPDATE viral_posts SET status='confirmed',confirmed_members=?,confirmed_at=?,updated_at=? WHERE id=?`, string(data), now.UnixMilli(), now.UnixMilli(), id)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return fmt.Errorf("没有找到该帖子")
	}
	return nil
}

func SnapshotPath(dir, id string) string {
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(id) {
		return ""
	}
	return filepath.Join(dir, "viral-snapshots", id+".png")
}

func RenderViralSnapshots(dir string, posts []ViralPost) error {
	if len(posts) == 0 {
		return nil
	}
	outDir := filepath.Join(dir, "viral-snapshots")
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return err
	}
	input, err := os.CreateTemp(dir, ".viral-snapshots-*.json")
	if err != nil {
		return err
	}
	inputPath := input.Name()
	defer os.Remove(inputPath)
	if err := input.Chmod(0600); err != nil {
		input.Close()
		return err
	}
	encoder := json.NewEncoder(input)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(posts); err != nil {
		input.Close()
		return err
	}
	if err := input.Close(); err != nil {
		return err
	}
	root := filepath.Dir(filepath.Dir(dir))
	script := filepath.Join(root, "scripts", "x_viral_snapshot.mjs")
	command := exec.Command("node", script, inputPath, outDir)
	command.Env = append(os.Environ(), "PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH=/root/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("生成 X 帖子快照失败: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
