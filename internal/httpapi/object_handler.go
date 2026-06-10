package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// ObjectHandler handles GET /v1/objects/{id}. It is the read-side
// companion to the ingest endpoint — useful for "open this
// knowledge object in detail" flows in the Telegram bot, the
// future web UI, or a downstream agent.
type ObjectHandler struct {
	hydrator app.ObjectHydrator
}

// NewObjectHandler returns a handler backed by h. h must be non-nil;
// don't register the route if you want a disabled endpoint.
func NewObjectHandler(h app.ObjectHydrator) *ObjectHandler {
	return &ObjectHandler{hydrator: h}
}

type objectResponse struct {
	Object domain.KnowledgeObject `json:"object"`
}

// ServeHTTP returns the object as JSON. 400 on bad UUID, 404 if
// the object does not exist (or is in a different workspace —
// we never reveal the existence of objects in other tenants).
func (h *ObjectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace_id query parameter is required")
		return
	}

	idStr := r.PathValue("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "id path segment must be a valid UUID")
		return
	}

	obj, err := h.hydrator.FindByID(r.Context(), workspaceID, id)
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "object not found")
			return
		}
		slog.Default().Error("object lookup failed",
			slog.String("object_id", idStr),
			slog.String("error", err.Error()))
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "object lookup failed")
		return
	}
	if obj == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "object not found")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(objectResponse{Object: *obj})
}
