package melon

import (
	"fmt"
	"testing"
	"time"
)

func TestMusicWaveBatchCombinesAcrossPollsAndDeduplicates(t *testing.T) {
	now := time.Unix(100, 0)
	batch := AppendMusicWaveBatch(MusicWaveBatch{}, batchEvents(1, 4), now)
	batch = AppendMusicWaveBatch(batch, batchEvents(4, 10), now.Add(5*time.Second))
	if len(batch.Events) != MusicWaveBatchSize {
		t.Fatalf("events = %d, want %d", len(batch.Events), MusicWaveBatchSize)
	}
	if !MusicWaveBatchReady(batch, now.Add(5*time.Second)) {
		t.Fatal("ten events should flush immediately")
	}
	chunk, remaining := TakeMusicWaveBatch(batch)
	if len(chunk) != MusicWaveBatchSize || len(remaining.Events) != 0 || remaining.QueuedAt != 0 {
		t.Fatalf("chunk=%d remaining=%#v", len(chunk), remaining)
	}
}

func TestMusicWaveBatchReadyImmediately(t *testing.T) {
	now := time.Unix(200, 0)
	batch := AppendMusicWaveBatch(MusicWaveBatch{}, batchEvents(1, 2), now)
	if !MusicWaveBatchReady(batch, now) {
		t.Fatal("a poll with any events should flush immediately")
	}
}

func batchEvents(first, last int) []Event {
	result := make([]Event, 0, last-first+1)
	for i := first; i <= last; i++ {
		result = append(result, Event{ID: fmt.Sprintf("event-%d", i), Body: fmt.Sprintf("message-%d", i)})
	}
	return result
}
