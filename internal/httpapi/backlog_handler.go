package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/frankirova/project-brain/internal/app"
)

// BacklogHandler handles GET /v1/backlog. It is the read-side
// companion to the ObjectDebateService (change 14, PR 3 write
// path), exposing the workspace-scoped, cursor-paginated human
// backlog of objects needing human attention.
//
// Query parameters:
//
//	workspace_id (required): tenant scope. Lowercased and trimmed
//	                          before being passed to the service.
//	page_size     (optional): 1..100, default 25. Out-of-range
//	                          values are clamped by the service.
//	cursor        (optional): opaque token returned by the prior
//	                          page's NextCursor. Empty means
//	                          "first page". Malformed cursors
//	                          return 400 INVALID_CURSOR.
type BacklogHandler struct {
	service *app.ObjectDebateService
}

// NewBacklogHandler wires the handler to the debate service. The
// service is the only dependency because the read path does not
// need a UoW or audit repo; the SQL projection is pool-backed.
func NewBacklogHandler(service *app.ObjectDebateService) *BacklogHandler {
	return &BacklogHandler{service: service}
}

type backlogResponse struct {
	Items      []app.BacklogItem `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

// ServeHTTP validates the query string, calls the service, and
// writes the response. Error mapping:
//
//	missing workspace_id       -> 400 VALIDATION_ERROR
//	page_size parse failure    -> 400 VALIDATION_ERROR
//	ErrInvalidCursor           -> 400 INVALID_CURSOR
//	other service error        -> 500 INTERNAL_ERROR
func (h *BacklogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger := slog.Default()
	logger.Debug("backlog request received",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr))

	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace_id query parameter is required")
		return
	}

	pageSize := 0
	if raw := r.URL.Query().Get("page_size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "page_size must be an integer")
			return
		}
		pageSize = parsed
	}

	cursor := r.URL.Query().Get("cursor")

	page, err := h.service.ListHumanBacklog(r.Context(), app.BacklogFilter{
		WorkspaceID: workspaceID,
		PageSize:    pageSize,
		Cursor:      cursor,
	})
	if err != nil {
		if errors.Is(err, app.ErrInvalidCursor) {
			writeError(w, http.StatusBadRequest, "INVALID_CURSOR", "cursor is malformed")
			return
		}
		logger.Error("backlog list failed",
			slog.String("workspace_id", workspaceID),
			slog.String("error", err.Error()),
			slog.Duration("elapsed", time.Since(start)))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "backlog list failed")
		return
	}

	logger.Debug("backlog http response sent",
		slog.String("workspace_id", workspaceID),
		slog.Int("items", len(page.Items)),
		slog.Bool("has_next_cursor", page.NextCursor != ""),
		slog.Duration("elapsed", time.Since(start)))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(backlogResponse{
		Items:      page.Items,
		NextCursor: page.NextCursor,
	}); err != nil {
		logger.Error("response encode failed",
			slog.String("handler", "backlog"),
			slog.String("error", err.Error()))
	}
}
