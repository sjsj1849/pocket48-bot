package admin

import (
	"net/http"
	"pocket48-bot/internal/weverse"
)

func (s *Server) handleWeversePostPasswords(w http.ResponseWriter, r *http.Request) {
	dir := weverse.Dir(s.opts.ConfigPath)
	fail := func(e error) { writeJSON(w, 400, apiError{Error: e.Error()}) }
	switch r.Method {
	case http.MethodGet:
	case http.MethodPut:
		var input struct {
			URL      string `json:"url"`
			Password string `json:"password"`
		}
		if e := decodeJSON(r, &input); e != nil {
			fail(e)
			return
		}
		c, e := s.weverseClient()
		if e != nil {
			fail(e)
			return
		}
		if _, e = c.SavePostPassword(r.Context(), input.URL, input.Password); e != nil {
			fail(e)
			return
		}
	case http.MethodDelete:
		if e := weverse.DeletePostPassword(dir, r.URL.Query().Get("postId")); e != nil {
			fail(e)
			return
		}
	default:
		methodNotAllowed(w)
		return
	}
	rows, e := weverse.LoadPostPasswords(dir)
	if e != nil {
		fail(e)
		return
	}
	for i := range rows {
		rows[i].Password = ""
	}
	writeJSON(w, 200, map[string]any{"posts": rows})
}
