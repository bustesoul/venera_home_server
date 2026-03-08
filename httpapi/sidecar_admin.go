package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	backendpkg "venera_home_server/backend"
	configpkg "venera_home_server/config"
	metadatapkg "venera_home_server/metadata"
	"venera_home_server/shared"
)

func (s *Server) handleMetadataSidecar(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		locator, err := locatorFromQuery(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_LOCATOR", err.Error())
			return
		}
		data, err := s.metadataSidecarInfo(r.Context(), locator)
		if err != nil {
			writeError(w, http.StatusBadRequest, "METADATA_SIDECAR_FAILED", err.Error())
			return
		}
		writeData(w, data)
	case http.MethodPut:
		var payload struct {
			Locator locatorPayload `json:"locator"`
			Content string         `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
			return
		}
		locator, err := payload.Locator.toLocator()
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_LOCATOR", err.Error())
			return
		}
		if err := s.writeMetadataSidecar(r.Context(), locator, payload.Content); err != nil {
			writeError(w, http.StatusBadRequest, "METADATA_SIDECAR_WRITE_FAILED", err.Error())
			return
		}
		data, err := s.metadataSidecarInfo(r.Context(), locator)
		if err != nil {
			writeError(w, http.StatusBadRequest, "METADATA_SIDECAR_FAILED", err.Error())
			return
		}
		writeData(w, data)
	case http.MethodDelete:
		var payload struct {
			Locator locatorPayload `json:"locator"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid request body")
			return
		}
		locator, err := payload.Locator.toLocator()
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_LOCATOR", err.Error())
			return
		}
		if err := s.deleteMetadataSidecar(r.Context(), locator); err != nil {
			writeError(w, http.StatusBadRequest, "METADATA_SIDECAR_DELETE_FAILED", err.Error())
			return
		}
		data, err := s.metadataSidecarInfo(r.Context(), locator)
		if err != nil {
			writeError(w, http.StatusBadRequest, "METADATA_SIDECAR_FAILED", err.Error())
			return
		}
		writeData(w, data)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

type locatorPayload struct {
	LibraryID string `json:"library_id"`
	RootType  string `json:"root_type"`
	RootRef   string `json:"root_ref"`
}

func (p locatorPayload) toLocator() (metadatapkg.Locator, error) {
	locator := metadatapkg.Locator{
		LibraryID: strings.TrimSpace(p.LibraryID),
		RootType:  strings.TrimSpace(p.RootType),
		RootRef:   shared.CleanRel(strings.TrimSpace(p.RootRef)),
	}
	if !locator.Valid() {
		return metadatapkg.Locator{}, errors.New("library_id, root_type and root_ref are required")
	}
	return locator, nil
}

func locatorFromQuery(r *http.Request) (metadatapkg.Locator, error) {
	return locatorPayload{
		LibraryID: r.URL.Query().Get("library_id"),
		RootType:  r.URL.Query().Get("root_type"),
		RootRef:   r.URL.Query().Get("root_ref"),
	}.toLocator()
}

func (s *Server) metadataSidecarInfo(ctx context.Context, locator metadatapkg.Locator) (map[string]any, error) {
	lib, backend, rel, err := s.sidecarTarget(locator)
	if err != nil {
		return nil, err
	}
	content, exists, err := readSidecar(ctx, backend, rel)
	if err != nil {
		return nil, err
	}
	_, writable := backend.(backendpkg.WritableBackend)
	return map[string]any{
		"locator": map[string]any{
			"library_id": locator.LibraryID,
			"root_type":  locator.RootType,
			"root_ref":   locator.RootRef,
		},
		"backend_kind": backend.Kind(),
		"writable":     writable,
		"exists":       exists,
		"sidecar_ref":  rel,
		"display_path": sidecarDisplayPath(lib, rel),
		"content":      content,
	}, nil
}

func (s *Server) writeMetadataSidecar(ctx context.Context, locator metadatapkg.Locator, raw string) error {
	_, backend, rel, err := s.sidecarTarget(locator)
	if err != nil {
		return err
	}
	writable, ok := backend.(backendpkg.WritableBackend)
	if !ok {
		return errors.New("current backend is read-only for sidecar editing")
	}
	normalized, err := normalizeSidecarJSON(raw)
	if err != nil {
		return err
	}
	return writable.WriteSmallFile(ctx, rel, []byte(normalized))
}

func (s *Server) deleteMetadataSidecar(ctx context.Context, locator metadatapkg.Locator) error {
	_, backend, rel, err := s.sidecarTarget(locator)
	if err != nil {
		return err
	}
	writable, ok := backend.(backendpkg.WritableBackend)
	if !ok {
		return errors.New("current backend is read-only for sidecar editing")
	}
	return writable.DeleteFile(ctx, rel)
}

func (s *Server) sidecarTarget(locator metadatapkg.Locator) (configpkg.LibraryConfig, backendpkg.Backend, string, error) {
	lib, ok := serverLibraryConfig(s.app.Config(), locator.LibraryID)
	if !ok {
		return configpkg.LibraryConfig{}, nil, "", errors.New("library not found")
	}
	backend := s.app.Backend(locator.LibraryID)
	if backend == nil {
		return configpkg.LibraryConfig{}, nil, "", errors.New("backend not found")
	}
	rel, err := sidecarRelPath(locator)
	if err != nil {
		return configpkg.LibraryConfig{}, nil, "", err
	}
	return lib, backend, rel, nil
}

func serverLibraryConfig(cfg *configpkg.Config, id string) (configpkg.LibraryConfig, bool) {
	for _, lib := range cfg.Libraries {
		if lib.ID == id {
			return lib, true
		}
	}
	return configpkg.LibraryConfig{}, false
}

func sidecarRelPath(locator metadatapkg.Locator) (string, error) {
	rootRef := shared.CleanRel(locator.RootRef)
	switch strings.ToLower(strings.TrimSpace(locator.RootType)) {
	case "dir", "series":
		return shared.RelJoin(rootRef, ".venera.json"), nil
	case "archive":
		if rootRef == "" {
			return "", errors.New("root_ref is required for archive sidecar")
		}
		return rootRef + ".venera.json", nil
	default:
		return "", errors.New("unsupported root_type for sidecar")
	}
}

func readSidecar(ctx context.Context, backend backendpkg.Backend, rel string) (string, bool, error) {
	raw, err := backend.ReadSmallFile(ctx, rel, 256*1024)
	if err != nil {
		if isSidecarNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(raw), true, nil
}

func isSidecarNotFound(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "404") || strings.Contains(lower, "not found") || strings.Contains(lower, "not exist")
}

func normalizeSidecarJSON(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("content is empty")
	}
	var payload any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	if _, ok := payload.(map[string]any); !ok {
		return "", errors.New("sidecar content must be a JSON object")
	}
	pretty, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	out.Write(pretty)
	out.WriteByte('\n')
	return out.String(), nil
}

func sidecarDisplayPath(lib configpkg.LibraryConfig, rel string) string {
	rel = shared.CleanRel(rel)
	switch strings.ToLower(strings.TrimSpace(lib.Kind)) {
	case "local":
		if rel == "" {
			return filepath.Clean(lib.Root)
		}
		return filepath.Clean(filepath.Join(lib.Root, filepath.FromSlash(rel)))
	case "smb":
		base := `\\` + strings.TrimSpace(lib.Host) + `\` + strings.Trim(strings.ReplaceAll(lib.Share, "/", `\`), `\`)
		trimmed := strings.Trim(strings.ReplaceAll(lib.Path, "/", `\`), `\`)
		if trimmed != "" {
			base = filepath.Join(base, trimmed)
		}
		if rel == "" {
			return base
		}
		return filepath.Join(base, filepath.FromSlash(strings.ReplaceAll(rel, "/", `\`)))
	case "webdav":
		u, err := url.Parse(strings.TrimSpace(lib.URL))
		if err != nil {
			return rel
		}
		joined := shared.CleanRel(lib.Path)
		if joined != "" {
			joined = shared.RelJoin(joined, rel)
		} else {
			joined = rel
		}
		u.Path = path.Join(u.Path, joined)
		return u.String()
	default:
		return rel
	}
}
