package melon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const baseURL = "https://www.melon.com"
const mobileBaseURL = "https://m2.melon.com"

type Client struct {
	ProxyURL string
}

var (
	tagRE         = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE       = regexp.MustCompile(`\s+`)
	artistNameRE  = regexp.MustCompile(`(?s)<p class="title_atist"><strong[^>]*>.*?</strong>(.*?)</p>`)
	artistImageRE = regexp.MustCompile(`(?s)<span[^>]+id="artistImgArea"[^>]*>.*?<img[^>]+src="([^"]+)"`)
	boxRE         = regexp.MustCompile(`(?s)<div class="box_act ([^"]+)".*?</div>\s*<!-- ▲▲▲`)
	descRE        = regexp.MustCompile(`(?s)<p class="box_desc"[^>]*>(.*?)</p>`)
	dateRE        = regexp.MustCompile(`(\d{4})년\s*(\d{1,2})월\s*(\d{1,2})일`)
	imageRE       = regexp.MustCompile(`<img[^>]+src="(https://cdnimg\.melon\.co\.kr/[^"]+)"`)
	mstoryRE      = regexp.MustCompile(`goMstoryDetail\(['"]?(\d+)`)
	albumRE       = regexp.MustCompile(`goAlbumDetail\(['"]?(\d+)`)
	mvRE          = regexp.MustCompile(`goMvDetail\([^,]+,\s*['"](\d+)`)
	photoRE       = regexp.MustCompile(`goPhotoBtmDetail\(['"]?(\d+)['"]?,['"](\d+)`)
)

func (c Client) httpClient() (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(c.ProxyURL) != "" {
		proxy, err := url.Parse(c.ProxyURL)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Transport: transport, Timeout: 25 * time.Second}, nil
}

func (c Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	client, err := c.httpClient()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/131.0 Safari/537.36")
	req.Header.Set("Accept-Language", "ko-KR,ko;q=0.9,en;q=0.7")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Melon 请求失败: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Melon 返回 HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(data) == 8<<20 {
		return nil, fmt.Errorf("Melon 响应超过大小限制")
	}
	return data, nil
}

func cleanText(value string) string {
	value = tagRE.ReplaceAllString(value, " ")
	value = html.UnescapeString(value)
	return strings.TrimSpace(spaceRE.ReplaceAllString(value, " "))
}

func cleanImage(value string) string {
	value = html.UnescapeString(value)
	if index := strings.Index(value, "/melon/resize/"); index >= 0 {
		value = value[:index]
	}
	return value
}

func (c Client) Lookup(ctx context.Context, artistID string) (Artist, error) {
	id, err := NormalizeArtistID(artistID)
	if err != nil {
		return Artist{}, err
	}
	rawURL := baseURL + "/artist/detail.htm?artistId=" + url.QueryEscape(id)
	data, err := c.get(ctx, rawURL)
	if err != nil {
		return Artist{}, err
	}
	match := artistNameRE.FindSubmatch(data)
	if len(match) < 2 || cleanText(string(match[1])) == "" {
		return Artist{}, fmt.Errorf("未找到该 Melon 艺人")
	}
	artist := Artist{ID: id, Name: cleanText(string(match[1])), URL: rawURL}
	if image := artistImageRE.FindSubmatch(data); len(image) > 1 {
		artist.Avatar = cleanImage(string(image[1]))
	}
	return artist, nil
}

func eventTime(block string) int64 {
	match := dateRE.FindStringSubmatch(block)
	if len(match) != 4 {
		return 0
	}
	year, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	day, _ := strconv.Atoi(match[3])
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.FixedZone("KST", 9*3600)).UnixMilli()
}

func uniqueImages(block string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, match := range imageRE.FindAllStringSubmatch(block, -1) {
		image := cleanImage(match[1])
		if image != "" && !seen[image] {
			seen[image] = true
			result = append(result, image)
		}
	}
	return result
}

func parseTimeline(artistID string, data []byte) []Event {
	events := []Event{}
	for _, match := range boxRE.FindAllSubmatch(data, -1) {
		className, block := string(match[1]), string(match[0])
		event := Event{ArtistID: artistID, Time: eventTime(block), Images: uniqueImages(block)}
		if desc := descRE.FindStringSubmatch(block); len(desc) > 1 {
			event.Body = cleanText(desc[1])
		}
		switch {
		case strings.Contains(className, "musicstory"):
			id := mstoryRE.FindStringSubmatch(block)
			if len(id) < 2 {
				continue
			}
			event.ID, event.Kind, event.Title = "magazine:"+id[1], "magazine", "Melon 杂志"
			event.URL = baseURL + "/musicstory/detail.htm?mstorySeq=" + id[1]
		case strings.Contains(className, "album"):
			id := albumRE.FindStringSubmatch(block)
			if len(id) < 2 {
				continue
			}
			event.ID, event.Kind, event.Title = "album:"+id[1], "album", "新专辑"
			event.URL = baseURL + "/album/detail.htm?albumId=" + id[1]
		case strings.Contains(className, "vdo"):
			id := mvRE.FindStringSubmatch(block)
			if len(id) < 2 {
				continue
			}
			event.ID, event.Kind, event.Title = "video:"+id[1], "video", "新视频"
			event.URL = baseURL + "/video/detail2.htm?mvId=" + id[1]
		case strings.Contains(className, "photo"):
			id := photoRE.FindStringSubmatch(block)
			if len(id) < 3 {
				continue
			}
			event.ID, event.Kind, event.Title = "photo:"+id[2], "photo", "新照片 / Story"
			event.URL = baseURL + "/artist/photo.htm?artistId=" + artistID
		default:
			continue
		}
		if event.Body == "" {
			event.Body = event.Title
		}
		events = append(events, event)
	}
	return events
}

type artistNoteListResponse struct {
	Response struct {
		ArtistNotes []struct {
			AlbumID     string `json:"ALBUMID"`
			ArtistID    string `json:"ARTISTID"`
			ArtistNote  string `json:"ARTISTNOTE"`
			ArtistName  string `json:"ARTISTNAME"`
			ArtistImage string `json:"ARTISTIMG"`
			IssueDate   string `json:"ISSUEDATE"`
		} `json:"ARTISTNOTELIST"`
	} `json:"response"`
}

func parseIssueDate(value string) int64 {
	parsed, err := time.ParseInLocation("2006.01.02", value, time.FixedZone("KST", 9*3600))
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func parseAlbumArtistNotes(subscribedArtistID, albumID, albumImage string, data []byte) ([]Event, error) {
	var payload artistNoteListResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("解析 Melon Artist Note 失败: %w", err)
	}
	events := make([]Event, 0, len(payload.Response.ArtistNotes))
	for _, note := range payload.Response.ArtistNotes {
		body := strings.TrimSpace(note.ArtistNote)
		if body == "" || (note.AlbumID != "" && note.AlbumID != albumID) {
			continue
		}
		digest := sha256.Sum256([]byte(note.ArtistID + "\x00" + body))
		title := "Artist Note"
		if note.ArtistName != "" {
			title += " · " + note.ArtistName
		}
		images := []string{}
		if note.ArtistImage != "" {
			images = append(images, cleanImage(note.ArtistImage))
		}
		if albumImage != "" {
			images = append(images, cleanImage(albumImage))
		}
		events = append(events, Event{
			ID:       "artist_note:" + albumID + ":" + note.ArtistID + ":" + hex.EncodeToString(digest[:6]),
			Kind:     "artist_note",
			Title:    title,
			Body:     body,
			URL:      baseURL + "/album/detail.htm?albumId=" + albumID,
			Time:     parseIssueDate(note.IssueDate),
			Images:   images,
			ArtistID: subscribedArtistID,
			Author:   strings.TrimSpace(note.ArtistName),
		})
	}
	return events, nil
}

func (c Client) albumArtistNotes(ctx context.Context, artistID, albumID, albumImage string) ([]Event, error) {
	endpoint := mobileBaseURL + "/m6/v1/album/artistNote/list.json?albumId=" + url.QueryEscape(albumID)
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return parseAlbumArtistNotes(artistID, albumID, albumImage, data)
}

func (c Client) Timeline(ctx context.Context, artistID string) ([]Event, error) {
	id, err := NormalizeArtistID(artistID)
	if err != nil {
		return nil, err
	}
	endpoint := baseURL + "/artist/listTimeline.htm?artistId=" + url.QueryEscape(id)
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	events := parseTimeline(id, data)
	if len(events) == 0 {
		return nil, fmt.Errorf("Melon 时间线没有返回可识别内容")
	}
	seenAlbums := map[string]bool{}
	albumRequests, albumFailures := 0, 0
	for _, event := range events {
		if event.Kind != "album" {
			continue
		}
		albumID := strings.TrimPrefix(event.ID, "album:")
		if albumID == "" || seenAlbums[albumID] {
			continue
		}
		seenAlbums[albumID] = true
		albumRequests++
		albumImage := ""
		if len(event.Images) > 0 {
			albumImage = event.Images[0]
		}
		notes, noteErr := c.albumArtistNotes(ctx, id, albumID, albumImage)
		if noteErr != nil {
			albumFailures++
			continue
		}
		events = append(events, notes...)
	}
	if albumRequests > 0 && albumFailures == albumRequests {
		return nil, fmt.Errorf("Melon Artist Note 接口暂时不可用")
	}
	return events, nil
}
