package logic

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const douyinLiveSessionStoreDir = "storage/douyin-live-sessions"
const douyinLivePersistInterval = 10 * time.Second

func safeDouyinLiveFilename(liveID string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(liveID) {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func (m *DouyinMonitor) liveStatePath(sessionID string) string {
	return filepath.Join(m.liveSessionDir, safeDouyinLiveFilename(sessionID)+".json")
}

func (m *DouyinMonitor) loadLiveStatesFromDisk() {
	entries, err := os.ReadDir(m.liveSessionDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[Douyin-live] load session store: %v", err)
		}
		return
	}
	now := time.Now()
	loaded := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.liveSessionDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state douyinLiveState
		if json.Unmarshal(raw, &state) != nil || strings.TrimSpace(state.LiveID) == "" {
			continue
		}
		if state.SessionID == "" {
			// Migration from c2110ac, where the filename/live ID represented
			// both the creator room and the session.
			state.SessionID = strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		}
		if state.ComboGiftCounts == nil {
			state.ComboGiftCounts = make(map[string]int64)
		}
		if state.StartNotificationQueued == nil {
			state.StartNotificationQueued = make(map[string]bool)
		}
		if state.EndNotificationQueued == nil {
			state.EndNotificationQueued = make(map[string]bool)
		}
		// Keep recent completed sessions for duplicate ROOM_ENDED suppression.
		if !state.Online && !state.LastUpdatedAt.IsZero() && now.Sub(state.LastUpdatedAt) > 14*24*time.Hour {
			_ = os.Remove(path)
			continue
		}
		cp := state
		current := m.liveStates[state.LiveID]
		if current == nil || state.LastUpdatedAt.After(current.LastUpdatedAt) {
			m.liveStates[state.LiveID] = &cp
		}
		m.livePersistedAt[state.SessionID] = now
		loaded++
	}
	if loaded > 0 {
		log.Printf("[Douyin-live] restored %d persisted session(s)", loaded)
	}
	m.pruneDouyinLiveHistory(50)
}

func (m *DouyinMonitor) persistLiveState(liveID string, force bool) {
	m.livePersistMu.Lock()
	defer m.livePersistMu.Unlock()
	now := time.Now()
	m.mu.Lock()
	state := m.liveStates[liveID]
	if state == nil || state.SessionID == "" || (!force && now.Sub(m.livePersistedAt[state.SessionID]) < douyinLivePersistInterval) {
		m.mu.Unlock()
		return
	}
	cp := *state
	cp.ProcessedGiftMessageIDs = append([]string(nil), state.ProcessedGiftMessageIDs...)
	cp.ComboGiftCounts = make(map[string]int64, len(state.ComboGiftCounts))
	for key, count := range state.ComboGiftCounts {
		cp.ComboGiftCounts[key] = count
	}
	cp.StartNotificationQueued = make(map[string]bool, len(state.StartNotificationQueued))
	for key, queued := range state.StartNotificationQueued {
		cp.StartNotificationQueued[key] = queued
	}
	cp.EndNotificationQueued = make(map[string]bool, len(state.EndNotificationQueued))
	for key, queued := range state.EndNotificationQueued {
		cp.EndNotificationQueued[key] = queued
	}
	m.livePersistedAt[state.SessionID] = now
	m.mu.Unlock()

	if err := os.MkdirAll(m.liveSessionDir, 0o755); err != nil {
		log.Printf("[Douyin-live] mkdir session store: %v", err)
		return
	}
	raw, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(m.liveSessionDir, ".douyin-session-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, m.liveStatePath(cp.SessionID))
	}
	if err != nil {
		log.Printf("[Douyin-live] persist session %s: %v", safeDouyinLiveFilename(liveID), err)
	}
}

func (m *DouyinMonitor) pruneDouyinLiveHistory(max int) {
	entries, err := os.ReadDir(m.liveSessionDir)
	if err != nil || len(entries) <= max {
		return
	}
	type fileInfo struct {
		path string
		mod  time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.liveSessionDir, entry.Name())
		raw, readErr := os.ReadFile(path)
		var state douyinLiveState
		if readErr == nil && json.Unmarshal(raw, &state) == nil && state.Online {
			continue
		}
		if info, err := entry.Info(); err == nil {
			files = append(files, fileInfo{path, info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for len(files) > max {
		_ = os.Remove(files[0].path)
		files = files[1:]
	}
}
