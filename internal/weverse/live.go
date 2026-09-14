package weverse

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

type TrackedLive struct {
	Event    Event  `json:"event"`
	LastSeen int64  `json:"lastSeen"`
	End      *Event `json:"end,omitempty"`
	Replay   *Event `json:"replay,omitempty"`
}

// Track only broadcasts actually observed while monitoring. A missing list item
// is not evidence of ending: query that exact post and require a terminal state.
// Persist terminal events for retries through the usual per-subscription cursor.
func (c *Client) LiveEndEvents(ctx context.Context, state *Runtime, cfg Settings, events []Event, now time.Time) ([]Event, error) {
	if state.Lives == nil {
		state.Lives = map[string]*TrackedLive{}
	}
	interested := func(e Event) bool {
		for _, s := range cfg.Subscriptions {
			if Matches(s, e) {
				return true
			}
		}
		return false
	}
	onAir := map[string]bool{}
	for _, e := range events {
		if e.Kind != "live" || !interested(e) {
			continue
		}
		key := fmt.Sprintf("%d/%s", e.CommunityID, e.PostID)
		onAir[key] = true
		if old := state.Lives[key]; old != nil {
			if old.End == nil {
				if old.Event.Body == e.Body {
					e.Translation = old.Event.Translation
				}
				old.Event = e
				old.LastSeen = now.UnixMilli()
			}
		} else {
			state.Lives[key] = &TrackedLive{Event: e, LastSeen: now.UnixMilli()}
		}
	}
	ends := []Event{}
	for key, tracked := range state.Lives {
		if !interested(tracked.Event) || tracked.LastSeen < now.Add(-7*24*time.Hour).UnixMilli() {
			delete(state.Lives, key)
			continue
		}
		if tracked.End != nil {
			ends = append(ends, *tracked.End)
			if tracked.Replay != nil {
				ends = append(ends, *tracked.Replay)
				continue
			}
		}
		if tracked.End == nil && onAir[key] {
			continue
		}
		var post Object
		if err := c.call(ctx, "/post/v1.0/post-"+tracked.Event.PostID+"?fieldSet=postV1", true, &post); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if tracked.End != nil {
				log.Printf("[Weverse] 回放检查失败: %v", err)
				continue
			}
			return nil, err
		}
		video := obj(obj(post["extension"])["video"])
		liveToVod, _ := video["liveToVod"].(bool)
		if !(str(video["type"]) == "LIVE" && str(video["status"]) == "DONE") && !liveToVod {
			continue
		}
		if tracked.End == nil {
			end := tracked.Event
			end.ID = "live_end:" + end.PostID
			end.Kind = "live_end"
			end.LiveEndedAt = num(video["onAirEndAt"])
			end.LiveDuration = num(video["playTime"])
			if end.LiveEndedAt > end.LiveStartedAt && end.LiveStartedAt > 0 {
				end.LiveDuration = (end.LiveEndedAt - end.LiveStartedAt) / 1000
			}
			end.Time = end.LiveEndedAt
			if end.Time == 0 {
				end.Time = now.UnixMilli()
			}
			// Keep the observed title/cover if the replay has not yet exposed metadata.
			media := obj(obj(post["extension"])["mediaInfo"])
			if title := plain(text(media, "title")); title != "" {
				if end.Body != title {
					end.Translation = ""
				}
				end.Body = title
			}
			tracked.End = &end
			ends = append(ends, end)
		}

		if replayReady(video) {
			replay := *tracked.End
			replay.Kind = "live_replay"
			replay.ID = "live_replay:" + replay.PostID
			replay.Time = now.UnixMilli()
			replay.LiveEndedAt = 0
			if duration := num(video["playTime"]); duration > 0 {
				replay.LiveDuration = duration
			}
			media := obj(obj(post["extension"])["mediaInfo"])
			if title := plain(text(media, "title")); title != "" {
				if replay.Body != title {
					replay.Translation = ""
				}
				replay.Body = title
			}
			cover := text(obj(media["thumbnail"]), "url")
			if cover == "" {
				cover = text(video, "thumb")
			}
			if strings.HasPrefix(cover, "https://") {
				replay.CoverURL = cover
				replay.Images = []string{cover}
			}
			tracked.Replay = &replay
			ends = append(ends, replay)
		}
	}
	return ends, nil
}

func replayReady(video Object) bool {
	liveToVod, _ := video["liveToVod"].(bool)
	return liveToVod && str(video["type"]) == "VOD" && str(video["encodingStatus"]) == "COMPLETE" && str(video["infraVideoId"]) != "" && str(video["videoId"]) != ""
}
