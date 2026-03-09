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
	page := parsePage(strings.TrimSpace(r.URL.Query().Get("page")))
	limit := parseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 100)
	if limit <= 0 {
		limit = 100
	}
	result, err := s.app.JobHistoryPage(r.Context(), metadatapkg.JobQuery{
		Kind:      strings.TrimSpace(r.URL.Query().Get("kind")),
		Trigger:   strings.TrimSpace(r.URL.Query().Get("trigger")),
		Status:    strings.TrimSpace(r.URL.Query().Get("status")),
		LibraryID: strings.TrimSpace(r.URL.Query().Get("library_id")),
		Limit:     limit,
		Offset:    (page - 1) * limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "JOB_HISTORY_FAILED", err.Error())
		return
	}
	writeData(w, map[string]any{
		"items":     result.Items,
		"count":     len(result.Items),
		"total":     result.Total,
		"page":      page,
		"page_size": result.Limit,
		"offset":    result.Offset,
	})
}
