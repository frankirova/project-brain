package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/google/uuid"
)

// collisionDetector is the port the handler depends on, implemented by
// *app.CollisionDetector.
type collisionDetector interface {
	Detect(ctx context.Context, workspaceID, text string, excludeID *uuid.UUID) ([]app.Collision, error)
}

// CollisionHandler handles POST /v1/check-collision: given candidate
// text, it returns existing knowledge that semantically collides with
// it, WITHOUT storing anything. This is the read-only "what would this
// clash with?" preview behind the human-in-the-loop review flow.
type CollisionHandler struct {
	detector collisionDetector
	maxBytes int64
}

// NewCollisionHandler returns a handler backed by detector. maxBytes<=0
// defaults to 1 MiB.
func NewCollisionHandler(d collisionDetector, maxBytes int64) *CollisionHandler {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	return &CollisionHandler{detector: d, maxBytes: maxBytes}
}

type collisionRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Content     string `json:"content"`
}

type collisionResponse struct {
	Collisions []app.Collision `json:"collisions"`
	Count      int             `json:"count"`
}

// ServeHTTP embeds the candidate content and returns colliding
// knowledge. 400 on a missing/invalid body, 500 on a detector error,
// 200 with {"collisions":[...], "count":N} otherwise.
func (h *CollisionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBytes)

	var req collisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace_id is required")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "content is required")
		return
	}

	collisions, err := h.detector.Detect(r.Context(), req.WorkspaceID, req.Content, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "collision check failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(collisionResponse{Collisions: collisions, Count: len(collisions)})
}
