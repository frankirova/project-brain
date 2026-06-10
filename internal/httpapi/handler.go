package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
)

const maxBodyBytes = 1 << 20 // 1 MiB

// IngestTextHandler handles POST /v1/ingest-text requests.
type IngestTextHandler struct {
	service     *app.IngestTextService
	maxBodySize int64
}

// NewIngestTextHandler creates a new IngestTextHandler.
// maxBodySize caps the request body in bytes; 0 means use the
// default (1 MiB).
func NewIngestTextHandler(svc *app.IngestTextService, maxBodySize int64) *IngestTextHandler {
	if maxBodySize <= 0 {
		maxBodySize = 1 << 20
	}
	return &IngestTextHandler{service: svc, maxBodySize: maxBodySize}
}

// ingestTextRequest is the JSON wire type for incoming requests.
type ingestTextRequest struct {
	WorkspaceID string             `json:"workspace_id"`
	Content     string             `json:"content"`
	Source      domain.SourceInput `json:"source"`
	Object      domain.ObjectInput `json:"object"`
}

// errorResponse is the JSON error wire type.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

// ServeHTTP decodes the request, calls the service, and writes the response.
func (h *IngestTextHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	logger := slog.Default()
	logger.Debug("ingest request received",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr))

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBodySize)

	var req ingestTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// MaxBytesReader returns *http.MaxBytesError when the limit is
		// exceeded (Go 1.19+). Type-assert instead of string-matching
		// the error message.
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds size limit")
			return
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, &json.SyntaxError{}) || errors.Is(err, &json.UnmarshalTypeError{}) {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
			return
		}
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Invalid request body")
		return
	}

	domainReq := domain.IngestTextRequest{
		WorkspaceID: req.WorkspaceID,
		Content:     req.Content,
		Source:      req.Source,
		Object:      req.Object,
	}

		result, err := h.service.Ingest(r.Context(), domainReq)
		if err != nil {
			logger.Debug("ingest rejected by service",
				slog.String("workspace_id", domainReq.WorkspaceID),
				slog.String("error", err.Error()),
				slog.Duration("elapsed", time.Since(start)))
			switch {
			case errors.Is(err, app.ErrValidation):
				writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
			case errors.Is(err, app.ErrNotFound):
				writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			default:
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
			}
			return
		}

		logger.Debug("ingest http response sent",
			slog.String("workspace_id", domainReq.WorkspaceID),
			slog.Bool("duplicate", result.Duplicate),
			slog.Duration("elapsed", time.Since(start)))

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		// Mid-write encode failure produces a truncated response
		// the client cannot parse. Log so an operator can spot it
		// instead of silently sending half a JSON document.
		slog.Default().Error("response encode failed",
			slog.String("handler", "ingest_text"),
			slog.String("error", err.Error()))
	}
}

// HealthHandler handles GET /v1/health requests.
type HealthHandler struct{}

// ServeHTTP writes a simple health status response.
func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Default().Error("response encode failed",
			slog.String("handler", "health"),
			slog.String("error", err.Error()))
	}
}

// writeError is a helper to write error responses.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorResponse{
		Error:   http.StatusText(status),
		Message: message,
		Code:    code,
	}); err != nil {
		slog.Default().Error("response encode failed",
			slog.String("handler", "error"),
			slog.Int("status", status),
			slog.String("error", err.Error()))
	}
}
