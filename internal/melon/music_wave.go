package melon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const Hearts2HeartsMusicWaveURL = "https://kko.to/_hCvnuw5WK"

type musicWaveResponse struct {
	ArtistComments []struct {
		CommentSeq    int64  `json:"COMMENTSEQ"`
		ArtistID      int64  `json:"ARTISTID"`
		ArtistComment string `json:"ARTISTCOMMENT"`
		AttachImages  []struct {
			ImageURL string `json:"IMAGEURL"`
		} `json:"ATTACHIMAGES"`
	} `json:"artistComments"`
}

type musicWaveComment struct {
	ImageURL    string `json:"imgUrl"`
	MessageType string `json:"messageType"`
	RegDate     string `json:"regDate"`
	ArtistID    string `json:"artistId"`
	ArtistName  string `json:"artistName"`
	Comment     string `json:"comment"`
}

func musicWaveURL(artistID string) string {
	if artistID == Hearts2HeartsArtistID {
		return Hearts2HeartsMusicWaveURL
	}
	return "https://musicwave.melon.com/"
}

func parseMusicWave(artistID string, data []byte) ([]Event, error) {
	var payload musicWaveResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("解析 Melon Music Wave 失败: %w", err)
	}
	events := make([]Event, 0, len(payload.ArtistComments))
	for _, raw := range payload.ArtistComments {
		var comment musicWaveComment
		if raw.CommentSeq <= 0 || json.Unmarshal([]byte(raw.ArtistComment), &comment) != nil {
			continue
		}
		// The channel also emits group-level closing notices. The requested feed is
		// the members' actual chat, whose artist id differs from the group channel id.
		if comment.MessageType != "artist" || strings.TrimSpace(comment.Comment) == "" || comment.ArtistID == artistID {
			continue
		}
		when, _ := strconv.ParseInt(comment.RegDate, 10, 64)
		images := make([]string, 0, len(raw.AttachImages))
		for _, attachment := range raw.AttachImages {
			if attachment.ImageURL != "" {
				images = append(images, cleanImage(attachment.ImageURL))
			}
		}
		events = append(events, Event{
			ID:       fmt.Sprintf("music_wave:%d", raw.CommentSeq),
			Kind:     "music_wave",
			Title:    "Music Wave",
			Body:     strings.TrimSpace(comment.Comment),
			URL:      musicWaveURL(artistID),
			Time:     when,
			Images:   images,
			ArtistID: artistID,
			Author:   strings.TrimSpace(comment.ArtistName),
			Avatar:   cleanImage(comment.ImageURL),
		})
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Time == events[j].Time {
			return events[i].ID < events[j].ID
		}
		return events[i].Time < events[j].Time
	})
	return events, nil
}

func (c Client) MusicWave(ctx context.Context, artistID string) ([]Event, error) {
	id, err := NormalizeArtistID(artistID)
	if err != nil {
		return nil, err
	}
	// Melon's edge occasionally serves the same URL for roughly five minutes even
	// though the response says no-cache. A changing query keeps live chat polling
	// on the current response instead of releasing dozens of messages at once.
	endpoint := "https://musicwave.melon.com/m6/musicwave/chat/amessage/list.json?type=artistrep&id=" + url.QueryEscape(id) + "&startNo=1&_=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	data, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return parseMusicWave(id, data)
}
