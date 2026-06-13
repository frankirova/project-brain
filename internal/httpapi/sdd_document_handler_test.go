package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// sddDocumentRepoStub is an in-memory stub for app.SddDocumentRepository used
// in SDD handler tests. FindByWorkspace returns the configured doc or error;
// Upsert is a no-op.
type sddDocumentRepoStub struct {
	doc domain.SddDocument
	err error
}

type sddDocumentGetterStub struct {
	doc domain.SddDocument
	err error
}

func (s *sddDocumentRepoStub) FindByWorkspace(_ context.Context, _ string) (domain.SddDocument, error) {
	return s.doc, s.err
}

func (s *sddDocumentRepoStub) Upsert(_ context.Context, _ domain.SddDocument) error {
	return nil
}

func (s *sddDocumentGetterStub) GetDocument(_ context.Context, _ string) (domain.SddDocument, error) {
	return s.doc, s.err
}

// newSddServiceWithStub builds a real SddDocumentService backed by the given stub.
func newSddServiceWithStub(doc domain.SddDocument, repoErr error) *app.SddDocumentService {
	repo := &sddDocumentRepoStub{doc: doc, err: repoErr}
	return app.NewSddDocumentService(repo, func() time.Time { return time.Now() }, nil)
}

func TestSddDocumentHandler_MissingWorkspaceID(t *testing.T) {
	svc := newSddServiceWithStub(domain.SddDocument{}, nil)
	h := NewSddDocumentHandler(svc)

	for _, target := range []string{"/v1/sdd-document", "/v1/sdd-document?workspace_id=%20%20%09"} {
		req := httptest.NewRequest("GET", target, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: code = %d, want 400", target, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "VALIDATION_ERROR") {
			t.Errorf("%s: body missing VALIDATION_ERROR; got: %s", target, rr.Body.String())
		}
	}
}

func TestSddDocumentHandler_200_MarkdownBody(t *testing.T) {
	doc := domain.SddDocument{
		WorkspaceID: "ws-test",
		Sections: map[domain.SddSectionKey][]domain.SddEntry{
			domain.SddSectionContext: {
				{
					ObjectID:  uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
					Title:     "A Decision",
					Summary:   "Summary here.",
					UpdatedAt: time.Now(),
				},
			},
			domain.SddSectionDecisions:     {},
			domain.SddSectionConstraints:   {},
			domain.SddSectionOpenQuestions: {},
		},
	}

	svc := newSddServiceWithStub(doc, nil)
	h := NewSddDocumentHandler(svc)

	req := httptest.NewRequest("GET", "/v1/sdd-document?workspace_id=ws-test", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/markdown; charset=utf-8", ct)
	}

	body := rr.Body.String()
	if !strings.Contains(body, "# SDD Document") {
		t.Errorf("body missing H1 heading; got: %s", body)
	}
}

func TestSddDocumentHandler_404_RealServiceRepoErrNotFound(t *testing.T) {
	// Production wiring passes the real SddDocumentService to the handler. The
	// service must propagate repo ErrNotFound so the handler returns the spec's
	// HTTP 404 instead of rendering an empty initialized document.
	svc := newSddServiceWithStub(domain.SddDocument{}, app.ErrNotFound)
	h := NewSddDocumentHandler(svc)

	req := httptest.NewRequest("GET", "/v1/sdd-document?workspace_id=unknown-ws", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no SDD document found for workspace unknown-ws") {
		t.Errorf("body missing not-found message; got: %s", rr.Body.String())
	}
}

func TestSddDocumentHandler_404_WhenServiceReturnsErrNotFound(t *testing.T) {
	svc := &sddDocumentGetterStub{err: app.ErrNotFound}
	h := NewSddDocumentHandler(svc)

	req := httptest.NewRequest("GET", "/v1/sdd-document?workspace_id=missing-ws", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "NOT_FOUND") {
		t.Errorf("body missing NOT_FOUND; got: %s", rr.Body.String())
	}
}
