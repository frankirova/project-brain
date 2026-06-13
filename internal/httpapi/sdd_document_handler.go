package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
)

type sddDocumentGetter interface {
	GetDocument(ctx context.Context, workspaceID string) (domain.SddDocument, error)
}

// SddDocumentHandler handles GET /v1/sdd-document. It returns the workspace
// SDD document as a Markdown string. The workspace is identified by the
// workspace_id query parameter.
//
// Responses:
//
//	200 OK              — body is the Markdown document, Content-Type: text/markdown; charset=utf-8
//	400 VALIDATION_ERROR — workspace_id query parameter is missing or blank
//	404 NOT_FOUND        — no SDD document exists for the given workspace
type SddDocumentHandler struct {
	service sddDocumentGetter
}

// NewSddDocumentHandler returns a handler backed by the given service.
func NewSddDocumentHandler(service sddDocumentGetter) *SddDocumentHandler {
	return &SddDocumentHandler{service: service}
}

// ServeHTTP reads the workspace_id query parameter, retrieves the document
// via the service, renders it as Markdown, and writes the response.
func (h *SddDocumentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger := slog.Default()
	logger.Debug("sdd-document request received",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr))

	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace_id query parameter is required")
		return
	}

	doc, err := h.service.GetDocument(r.Context(), workspaceID)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "no SDD document found for workspace "+workspaceID)
			return
		}
		logger.Error("sdd-document get failed",
			slog.String("workspace_id", workspaceID),
			slog.String("error", err.Error()),
			slog.Duration("elapsed", time.Since(start)))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to retrieve SDD document")
		return
	}

	markdown := app.RenderSddDocumentMarkdown(doc)

	logger.Debug("sdd-document response sent",
		slog.String("workspace_id", workspaceID),
		slog.Duration("elapsed", time.Since(start)))

	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(markdown))
}
