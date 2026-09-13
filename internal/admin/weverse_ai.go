package admin

import (
	"encoding/json"
	"net/http"
	"pocket48-bot/internal/weverse"
)

func (s *Server) handleWeverseAI(w http.ResponseWriter, r *http.Request) {
	dir := weverse.Dir(s.opts.ConfigPath)
	fail := func(err error) { writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()}) }
	if r.Method == http.MethodPut {
		var cfg weverse.AISettings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&cfg); err != nil {
			fail(err)
			return
		}
		if err := weverse.SaveAISettings(dir, cfg); err != nil {
			fail(err)
			return
		}
	} else if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cfg, err := weverse.LoadAISettings(dir)
	if err != nil {
		fail(err)
		return
	}
	configured := cfg.APIKey != ""
	cfg.APIKey = ""
	active, pending, last, problem := weverse.AIStatus(dir)
	writeJSON(w, 200, map[string]any{"settings": cfg, "keyConfigured": configured, "active": active, "pending": pending, "lastSuccess": last, "error": problem})
}
