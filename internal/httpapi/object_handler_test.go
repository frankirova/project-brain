package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type fakeHydrator struct {
	obj *domain.KnowledgeObject
	err error
}

func (f *fakeHydrator) FindByID(_ context.Context, _ string, _ uuid.UUID) (*domain.KnowledgeObject, error) {
	return f.obj, f.err
}

func dispatch(t *testing.T, h *ObjectHandler, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("GET /v1/objects/{id}", h)
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestObjectHandler_MissingWorkspaceID(t *testing.T) {
	h := NewObjectHandler(&fakeHydrator{obj: &domain.KnowledgeObject{}})
	rr := dispatch(t, h, "/v1/objects/11111111-1111-1111-1111-111111111111")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestObjectHandler_BadUUID(t *testing.T) {
	h := NewObjectHandler(&fakeHydrator{obj: &domain.KnowledgeObject{}})
	rr := dispatch(t, h, "/v1/objects/not-a-uuid?workspace_id=ws-1")

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestObjectHandler_NotFound(t *testing.T) {
	h := NewObjectHandler(&fakeHydrator{err: app.ErrNotFound})
	rr := dispatch(t, h, "/v1/objects/11111111-1111-1111-1111-111111111111?workspace_id=ws-1")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rr.Code)
	}
}

func TestObjectHandler_Success(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	obj := &domain.KnowledgeObject{
		ID:      id,
		Title:   "Some title",
		Content: "some content",
		Status:  domain.KnowledgeObjectStatusValidated,
	}
	h := NewObjectHandler(&fakeHydrator{obj: obj})
	rr := dispatch(t, h, "/v1/objects/11111111-1111-1111-1111-111111111111?workspace_id=ws-1")

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
	var body objectResponse
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Object.ID != id {
		t.Fatalf("id = %v, want %v", body.Object.ID, id)
	}
	if body.Object.Title != "Some title" {
		t.Fatalf("title = %q, want %q", body.Object.Title, "Some title")
	}
}

func TestObjectHandler_HydratorError(t *testing.T) {
	h := NewObjectHandler(&fakeHydrator{err: errors.New("db down")})
	rr := dispatch(t, h, "/v1/objects/11111111-1111-1111-1111-111111111111?workspace_id=ws-1")

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rr.Code)
	}
}
