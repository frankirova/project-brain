package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

type fakeDetector struct {
	collisions []app.Collision
	err        error
	gotWS      string
	gotText    string
}

func (f *fakeDetector) Detect(_ context.Context, ws, text string, _ *uuid.UUID) ([]app.Collision, error) {
	f.gotWS, f.gotText = ws, text
	return f.collisions, f.err
}

func postCollision(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/check-collision", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCollisionHandlerReturnsCollisions(t *testing.T) {
	det := &fakeDetector{collisions: []app.Collision{
		{Object: domain.KnowledgeObject{ID: uuid.New(), Content: "usamos Postgres"}, Similarity: 0.9, Verdict: app.CollisionStrongOverlap},
	}}
	h := NewCollisionHandler(det, 0)

	rec := postCollision(t, h, `{"workspace_id":"ws-1","content":"propongo MongoDB"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if det.gotWS != "ws-1" || det.gotText != "propongo MongoDB" {
		t.Fatalf("detector got ws=%q text=%q", det.gotWS, det.gotText)
	}

	var resp collisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Count != 1 || len(resp.Collisions) != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	if resp.Collisions[0].Verdict != app.CollisionStrongOverlap {
		t.Fatalf("verdict = %q", resp.Collisions[0].Verdict)
	}
}

func TestCollisionHandlerRequiresWorkspaceID(t *testing.T) {
	h := NewCollisionHandler(&fakeDetector{}, 0)
	rec := postCollision(t, h, `{"content":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCollisionHandlerRequiresContent(t *testing.T) {
	h := NewCollisionHandler(&fakeDetector{}, 0)
	rec := postCollision(t, h, `{"workspace_id":"ws-1","content":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCollisionHandlerRejectsInvalidJSON(t *testing.T) {
	h := NewCollisionHandler(&fakeDetector{}, 0)
	rec := postCollision(t, h, `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCollisionHandlerSurfacesDetectorError(t *testing.T) {
	h := NewCollisionHandler(&fakeDetector{err: errors.New("embed failed")}, 0)
	rec := postCollision(t, h, `{"workspace_id":"ws-1","content":"x"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
