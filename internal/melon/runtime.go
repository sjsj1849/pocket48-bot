package melon

import (
	"sort"
	"time"
)

const (
	MusicWaveBatchSize = 10
)

type Cursor struct {
	ArtistID string   `json:"artistId"`
	Seen     []string `json:"seen"`
	Ready    bool     `json:"ready"`
}

type Runtime struct {
	Subscriptions    map[string]Cursor         `json:"subscriptions"`
	MusicWaveBatches map[string]MusicWaveBatch `json:"musicWaveBatches,omitempty"`
}

type MusicWaveBatch struct {
	Events   []Event `json:"events"`
	QueuedAt int64   `json:"queuedAt"`
}

// AppendMusicWaveBatch keeps events across polling cycles. Persisting this in
// music-wave-state.json prevents a restart during the batching window from
// losing messages whose cursor has already advanced.
func AppendMusicWaveBatch(batch MusicWaveBatch, events []Event, now time.Time) MusicWaveBatch {
	seen := make(map[string]bool, len(batch.Events)+len(events))
	result := append([]Event(nil), batch.Events...)
	for _, event := range result {
		seen[event.ID] = true
	}
	for _, event := range events {
		if event.ID == "" || seen[event.ID] {
			continue
		}
		result = append(result, event)
		seen[event.ID] = true
	}
	if len(result) > 0 && batch.QueuedAt == 0 {
		batch.QueuedAt = now.UnixMilli()
	}
	batch.Events = result
	return batch
}

func MusicWaveBatchReady(batch MusicWaveBatch, _ time.Time) bool {
	// Send as soon as a poll yields messages. One poll's events are merged into
	// a single card (bounded by MusicWaveBatchSize); we no longer wait for a full
	// batch of 10 or a 30s window to elapse.
	return len(batch.Events) > 0
}

func TakeMusicWaveBatch(batch MusicWaveBatch) ([]Event, MusicWaveBatch) {
	count := len(batch.Events)
	if count > MusicWaveBatchSize {
		count = MusicWaveBatchSize
	}
	result := append([]Event(nil), batch.Events[:count]...)
	batch.Events = append([]Event(nil), batch.Events[count:]...)
	if len(batch.Events) == 0 {
		batch.QueuedAt = 0
	}
	return result, batch
}

func Pending(cursor Cursor, sub Subscription, events []Event) []Event {
	if !cursor.Ready || cursor.ArtistID != sub.ArtistID {
		return nil
	}
	known := map[string]bool{}
	for _, id := range cursor.Seen {
		known[id] = true
	}
	result := []Event{}
	for _, event := range events {
		if !known[event.ID] && Matches(sub, event) {
			result = append(result, event)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Time < result[j].Time })
	return result
}

func Advance(cursor Cursor, sub Subscription, events []Event) Cursor {
	if !cursor.Ready || cursor.ArtistID != sub.ArtistID {
		cursor = Cursor{ArtistID: sub.ArtistID, Ready: true}
	}
	known := map[string]bool{}
	for _, id := range cursor.Seen {
		known[id] = true
	}
	for _, event := range events {
		if event.ID != "" && !known[event.ID] {
			cursor.Seen = append(cursor.Seen, event.ID)
			known[event.ID] = true
		}
	}
	if len(cursor.Seen) > 4096 {
		cursor.Seen = cursor.Seen[len(cursor.Seen)-4096:]
	}
	return cursor
}
