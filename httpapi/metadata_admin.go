package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	apppkg "venera_home_server/app"
	exdbdryrunpkg "venera_home_server/exdbdryrun"
	metadatapkg "venera_home_server/metadata"
)

func (s *Server) handleMetadataJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	items := s.app.MetadataJobs()
	writeData(w, map[string]any{"items": items, "count": len(items)})
}

func (s *Server) handleMetadataEnrich(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload apppkg.MetadataEnrichRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}
	job, err := s.app.StartMetadataEnrichment(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_ENRICH_FAILED", err.Error())
		return
	}
	writeData(w, job)
}

func (s *Server) handleMetadataSources(w http.ResponseWriter, r *http.Request) {
	basePath := "/api/v1/admin/metadata/sources"
	if r.URL.Path == basePath || r.URL.Path == basePath+"/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		items, err := s.app.MetadataSources(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "METADATA_SOURCES_FAILED", err.Error())
			return
		}
		writeData(w, map[string]any{"directory": s.app.Config().Server.DataDir + `/externaldb`, "items": items, "count": len(items)})
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	remainder := strings.TrimPrefix(r.URL.Path, basePath+"/")
	parts := strings.Split(strings.Trim(remainder, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "records" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "metadata source endpoint not found")
		return
	}
	result, err := s.app.MetadataSourceBrowse(r.Context(), parts[0], exdbdryrunpkg.BrowseQuery{
		Table: strings.TrimSpace(r.URL.Query().Get("table")),
		Q:     strings.TrimSpace(r.URL.Query().Get("q")),
		Page:  parseInt(strings.TrimSpace(r.URL.Query().Get("page")), 1),
		Limit: parseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 50),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_SOURCE_BROWSE_FAILED", err.Error())
		return
	}
	writeData(w, result)
}

func (s *Server) handleMetadataRecordAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	var payload apppkg.MetadataRecordActionRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
		return
	}
	result, err := s.app.MetadataRecordAction(r.Context(), payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "METADATA_RECORD_ACTION_FAILED", err.Error())
		return
	}
	writeData(w, result)
}

func metadataRecordMap(record metadatapkg.Record) map[string]any {
	item := map[string]any{
		"id":                  record.ID,
		"locator":             map[string]any{"library_id": record.LibraryID, "root_type": record.RootType, "root_ref": record.RootRef},
		"library_id":          record.LibraryID,
		"root_type":           record.RootType,
		"root_ref":            record.RootRef,
		"folder_path":         record.FolderPath,
		"content_fingerprint": emptyToNil(record.ContentFingerprint),
		"title":               emptyToNil(record.Title),
		"title_jpn":           emptyToNil(record.TitleJPN),
		"subtitle":            emptyToNil(record.Subtitle),
		"description":         emptyToNil(record.Description),
		"artists":             record.Artists,
		"tags":                record.Tags,
		"language":            emptyToNil(record.Language),
		"category":            emptyToNil(record.Category),
		"source":              emptyToNil(record.Source),
		"source_id":           emptyToNil(record.SourceID),
		"source_token":        emptyToNil(record.SourceToken),
		"source_url":          emptyToNil(record.SourceURL),
		"match_kind":          emptyToNil(record.MatchKind),
		"manual_locked":       record.ManualLocked,
		"cover_source_url":    emptyToNil(record.CoverSourceURL),
		"cover_blob_relpath":  emptyToNil(record.CoverBlobRelpath),
		"last_error":          emptyToNil(record.LastError),
		"fetched_at":          formatTimePtrRFC3339(record.FetchedAt),
		"stale_after":         formatTimePtrRFC3339(record.StaleAfter),
		"last_seen_at":        formatTimePtrRFC3339(record.LastSeenAt),
		"missing_since":       formatTimePtrRFC3339(record.MissingSince),
		"state":               metadataRecordState(record),
		"hint":                record.Hint,
	}
	if record.HasRating {
		item["rating"] = record.Rating
	} else {
		item["rating"] = nil
	}
	if record.HasConfidence {
		item["confidence"] = record.Confidence
	} else {
		item["confidence"] = nil
	}
	return item
}

func parsePage(value string) int {
	page, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || page <= 0 {
		return 1
	}
	return page
}
