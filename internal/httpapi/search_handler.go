package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/frankirova/project-brain/internal/app"
)

// SearchHandler handles GET /v1/search requests. It is thin: the
// heavy lifting lives in the Retriever implementation (FTS today,
// hybrid in a later change).
type SearchHandler struct {
	retriever app.Retriever
}

// NewSearchHandler returns a handler backed by retriever. r must be
// non-nil — if you want a disabled search endpoint, don't register
// the route.
func NewSearchHandler(r app.Retriever) *SearchHandler {
	return &SearchHandler{retriever: r}
}

// searchResponse is the JSON wire shape for a search response.
type searchResponse struct {
	Results []app.SearchResult `json:"results"`
	Query   string             `json:"query"`
	Count   int                `json:"count"`
}

// ServeHTTP returns matching knowledge objects ranked by the
// retriever. Returns 400 if workspace_id is missing, 200 with
// {"results":[...], "query":"...", "count":N} otherwise.
//
// Query params:
//   - q (optional): the search text. Empty returns no results.
//   - workspace_id (required): tenant scope.
//   - limit (optional): max results. Default 10, capped at 100.
func (h *SearchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	workspaceID := r.URL.Query().Get("workspace_id")
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "workspace_id query parameter is required")
		return
	}

	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "limit must be a non-negative integer")
			return
		}
		limit = n
	}

	results, err := h.retriever.Search(r.Context(), app.SearchQuery{
		Text:        q,
		WorkspaceID: workspaceID,
		Limit:       limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "search failed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(searchResponse{
		Results: results,
		Query:   q,
		Count:   len(results),
	})
}
