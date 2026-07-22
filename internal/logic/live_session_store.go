package logic

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const liveSessionStoreDir = "storage/live-sessions"

func liveSessionPath(roomID int64) string {
	return filepath.Join(liveSessionStoreDir, fmt.Sprintf("%d.json", roomID))
}

func (b *Bot) loadLiveSessionsFromDisk() {
	entries, err := os.ReadDir(liveSessionStoreDir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[NIM-live] load sessions dir: %v", err)
		}
		return
	}
	loaded := 0
	finished := 0
	b.liveSessionsMu.Lock()
	defer b.liveSessionsMu.Unlock()
	if b.finishedLives == nil {
		b.finishedLives = make(map[string]LiveGiftSession)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(liveSessionStoreDir, e.Name()))
		if err != nil {
			continue
		}
		var session LiveGiftSession
		if err := json.Unmarshal(raw, &session); err != nil || session.LiveID == "" {
			continue
		}
		// Drop very old sessions (>24h) to avoid stale summaries.
		if session.StartedAt > 0 && time.Since(time.UnixMilli(session.StartedAt)) > 24*time.Hour {
			_ = os.Remove(filepath.Join(liveSessionStoreDir, e.Name()))
			continue
		}
		roomID, err := strconv.ParseInt(e.Name()[:len(e.Name())-5], 10, 64)
		if err != nil || roomID == 0 {
			continue
		}
		cp := session
		if session.Ended {
			// Recently finished: keep in finishedLives so discovery cannot re-open zero-score.
			b.finishedLives[session.LiveID] = cp
			finished++
			// Drop ended files older than 6h to avoid disk pile-up.
			if session.StartedAt > 0 && time.Since(time.UnixMilli(session.StartedAt)) > 6*time.Hour {
				_ = os.Remove(filepath.Join(liveSessionStoreDir, e.Name()))
			}
			continue
		}
		b.liveSessions[roomID] = &cp
		loaded++
		log.Printf("[NIM-live] restored session room=%d liveId=%s legs=%d score=%s peak=%d",
			roomID, session.LiveID, session.ChickenLegs, formatScoreValue(session.AnnualScore), session.PeakOnline)
	}
	if loaded > 0 {
		log.Printf("[NIM-live] restored %d live session(s) from disk", loaded)
	}
	if finished > 0 {
		log.Printf("[NIM-live] remembered %d finished live(s) from disk (block re-open)", finished)
	}
}

func (b *Bot) persistLiveSession(roomID int64, session *LiveGiftSession) {
	if roomID == 0 || session == nil {
		return
	}
	if err := os.MkdirAll(liveSessionStoreDir, 0o755); err != nil {
		log.Printf("[NIM-live] mkdir session store: %v", err)
		return
	}
	raw, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return
	}
	tmp := liveSessionPath(roomID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("[NIM-live] write session tmp: %v", err)
		return
	}
	if err := os.Rename(tmp, liveSessionPath(roomID)); err != nil {
		log.Printf("[NIM-live] rename session: %v", err)
	}
}

func (b *Bot) removeLiveSessionFile(roomID int64) {
	// Ended sessions are left on disk (Ended=true) so restart can block re-open.
	// Only remove non-ended leftovers or when explicitly cleaning after long TTL in load.
	_ = roomID
}
