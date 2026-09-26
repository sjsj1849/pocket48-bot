package melon

import "sort"

type Cursor struct {
	ArtistID string   `json:"artistId"`
	Seen     []string `json:"seen"`
	Ready    bool     `json:"ready"`
}

type Runtime struct {
	Subscriptions map[string]Cursor `json:"subscriptions"`
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
