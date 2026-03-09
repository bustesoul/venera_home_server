package httpapi

import (
	"net/http"
	"strings"

	metadatapkg "venera_home_server/metadata"
)

func (s *Server) handleAdminJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	items, err := s.app.JobHistory(r.Context(), metadatapkg.JobQuery{
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		Trigger:   strings.TrimSpace(r.URL.Query().Get("trigger")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		LibraryID: strings.TrimSpace(r.URL.Query().Get("library_id")),
		Limit:     parseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 100),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "JOB_HISTORY_FAILED", err.Error())
		return
	}
	writeData(w, map[string]any{"items": items, "count": len(items)})
}
