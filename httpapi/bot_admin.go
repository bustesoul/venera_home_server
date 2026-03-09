package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	apppkg "venera_home_server/app"
)

func (s *Server) handleEHBotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	writeData(w, s.app.EHBotStatus())
}

func (s *Server) handleEHBotConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeData(w, s.app.EHBotConfigView())
	case http.MethodPut:
		var payload apppkg.EHBotConfigUpdate
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
			return
		}
		view, err := s.app.UpdateEHBotConfig(r.Context(), payload)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "EHBOT_CONFIG_SAVE_FAILED", err.Error())
			return
		}
		writeData(w, view)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
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
