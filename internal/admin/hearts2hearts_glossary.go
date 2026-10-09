package admin

import (
	"net/http"
	"pocket48-bot/internal/hearts2hearts"
)

func (s *Server) handleHearts2HeartsGlossary(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		glossary, err := hearts2hearts.Load(s.opts.ConfigPath)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "无法读取 Hearts2Hearts 术语表"})
			return
		}
		writeJSON(w, http.StatusOK, glossary)
	case http.MethodPut:
		var glossary hearts2hearts.Glossary
		if err := decodeJSON(r, &glossary); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		if err := hearts2hearts.Save(s.opts.ConfigPath, glossary); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, glossary)
	default:
		methodNotAllowed(w)
	}
}
