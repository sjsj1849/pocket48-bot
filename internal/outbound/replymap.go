package outbound

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// ReplyMap maps a business "source message id" (e.g. a Weverse comment id) to
// the platform message id an adapter produced when it delivered that message.
// It lets a later reply, which references the parent source id, be threaded
// under the original message as a native reply instead of a detached message.
//
// Entries are bounded so an idle bot does not grow the map without limit; the
// oldest entry is evicted when the cap is reached.
//
// The mapping is persisted to disk because both sides of a thread arrive in
// separate deliveries: a member posts, then replies under that post minutes
// later. Keeping it purely in memory meant any restart silently broke threading
// for every thread that spanned the restart — the parent id was simply unknown
// and the reply degraded to an unthreaded message.
type ReplyMap struct {
	mu     sync.Mutex
	order  []string
	values map[string]string
	max    int

	// path is the persistence file; empty disables persistence (tests).
	path  string
	dirty bool

	// appID scopes the mapping to the Feishu app that produced the message ids.
	// A message id is only meaningful to the app that issued it: after the bot is
	// rebound to a different app (or a different tenant), every stored id turns
	// stale and Feishu answers 230002 "Bot/User can NOT be out of the chat". Entries
	// written by another app are therefore dropped on load, which is cheaper and
	// far more reliable than discovering the staleness one failed reply at a time.
	appID string
}

// replyMapEntry is one persisted source-id -> message-id pair.
type replyMapEntry struct {
	SourceID  string `json:"sourceId"`
	MessageID string `json:"messageId"`
	// AppID records which app issued MessageID (see ReplyMap.appID).
	AppID string `json:"appId,omitempty"`
}

// NewReplyMap returns a mapping with at most max entries.
func NewReplyMap(max int) *ReplyMap {
	if max <= 0 {
		max = 1000
	}
	return &ReplyMap{values: make(map[string]string), max: max}
}

// NewPersistentReplyMap returns a mapping backed by path. It loads any existing
// entries so threads survive a restart; a missing or unreadable file is treated
// as an empty map rather than an error, since losing history only degrades
// threading and must never stop the bot from starting.
func NewPersistentReplyMap(max int, path, appID string) *ReplyMap {
	m := NewReplyMap(max)
	m.appID = appID
	if path == "" {
		return m
	}
	m.path = path
	if err := m.load(); err != nil {
		return m
	}
	return m
}

func (r *ReplyMap) load() error {
	file, err := os.Open(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var entry replyMapEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			// A truncated final line (killed mid-write) must not discard the rest.
			continue
		}
		if entry.SourceID == "" || entry.MessageID == "" {
			continue
		}
		// Written by a different app (or before app scoping existed): its message
		// ids are unusable now, so keep it out of the map entirely.
		if r.appID != "" && entry.AppID != r.appID {
			continue
		}
		if _, ok := r.values[entry.SourceID]; !ok {
			r.order = append(r.order, entry.SourceID)
		}
		r.values[entry.SourceID] = entry.MessageID
	}
	r.trimLocked()
	return scanner.Err()
}

// trimLocked drops the oldest entries beyond the cap. Caller holds the lock.
func (r *ReplyMap) trimLocked() {
	for len(r.order) > r.max {
		oldest := r.order[0]
		r.order = r.order[1:]
		delete(r.values, oldest)
	}
}

// rewriteLocked writes the whole map out atomically. Caller holds the lock.
func (r *ReplyMap) rewriteLocked() {
	if r.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return
	}
	temp := r.path + ".tmp"
	file, err := os.Create(temp)
	if err != nil {
		return
	}
	writer := bufio.NewWriter(file)
	for _, sourceID := range r.order {
		messageID, ok := r.values[sourceID]
		if !ok {
			continue
		}
		line, err := json.Marshal(replyMapEntry{SourceID: sourceID, MessageID: messageID, AppID: r.appID})
		if err != nil {
			continue
		}
		writer.Write(line)
		writer.WriteByte('\n')
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		os.Remove(temp)
		return
	}
	if err := file.Close(); err != nil {
		os.Remove(temp)
		return
	}
	// Rename last so a crash mid-write leaves the previous file intact.
	os.Rename(temp, r.path)
}

// Record stores the platform message id for a source message id.
func (r *ReplyMap) Record(sourceID, messageID string) {
	if r == nil || sourceID == "" || messageID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.values[sourceID]; !ok {
		r.order = append(r.order, sourceID)
	}
	r.values[sourceID] = messageID
	r.trimLocked()
	r.rewriteLocked()
}

// Lookup returns the platform message id recorded for sourceID.
func (r *ReplyMap) Lookup(sourceID string) (string, bool) {
	if r == nil || sourceID == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.values[sourceID]
	return id, ok
}

// DeleteMessage drops every mapping that points at messageID and reports how
// many were removed. Feishu hands back 230002 for anchors the bot can no longer
// reach (stale app, removed message, chat the bot left); keeping such an anchor
// means every later reply to the same thread fails the same way, so the caller
// prunes it once instead of retrying forever.
func (r *ReplyMap) DeleteMessage(messageID string) int {
	if r == nil || messageID == "" {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := make([]string, 0, len(r.order))
	removed := 0
	for _, sourceID := range r.order {
		if r.values[sourceID] == messageID {
			delete(r.values, sourceID)
			removed++
			continue
		}
		kept = append(kept, sourceID)
	}
	if removed == 0 {
		return 0
	}
	r.order = kept
	r.rewriteLocked()
	return removed
}

// Len returns the number of tracked mappings, for startup diagnostics.
func (r *ReplyMap) Len() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.values)
}
