package httpapi

import "net/http"

func (s *Server) handleEHBotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	writeData(w, s.app.EHBotStatus())
}

func (s *Server) handleEHBotJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	items := s.app.EHBotJobs()
	writeData(w, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleEHBotRunOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	job, err := s.app.StartEHBotPull(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "EHBOT_PULL_FAILED", err.Error())
		return
	}
	writeData(w, job)
}
