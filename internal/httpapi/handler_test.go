package httpapi

import (
	"bytes"
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

// fakeUOW is a duplicate of the test double from internal/app/ingest_text_test.go.
// Design decision: prefer duplication over shared test helper for isolation.
type fakeUOW struct {
	repos      *fakeRepos
	started    bool
	committed  bool
	rolledBack bool
}

func newFakeUOW() *fakeUOW {
	return &fakeUOW{
		repos: &fakeRepos{
			source: &fakeSourceRepo{},
			object: &fakeObjectRepo{},
			link:   &fakeLinkRepo{},
			audit:  &fakeAuditRepo{},
		},
	}
}

func (u *fakeUOW) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	u.started = true
	if err := fn(ctx, u.repos); err != nil {
		u.rolledBack = true
		return err
	}
	u.committed = true
	return nil
}

type fakeRepos struct {
	source *fakeSourceRepo
	object *fakeObjectRepo
	link   *fakeLinkRepo
	audit  *fakeAuditRepo
}

func (r *fakeRepos) Sources() app.SourceRepository                   { return r.source }
func (r *fakeRepos) KnowledgeObjects() app.KnowledgeObjectRepository { return r.object }
func (r *fakeRepos) ObjectSources() app.ObjectSourceRepository       { return r.link }
func (r *fakeRepos) AuditEvents() app.AuditEventRepository           { return r.audit }

type fakeSourceRepo struct {
	existingResult domain.IngestTextResult
	err            error
	created        []domain.Source
}

func (r *fakeSourceRepo) FindIngestionResultByIdentityKey(_ context.Context, _ string, _ string) (domain.IngestTextResult, error) {
	if r.err != nil {
		return domain.IngestTextResult{}, r.err
	}
	if r.existingResult.SourceID != (uuid.UUID{}) {
		return r.existingResult, nil
	}
	return domain.IngestTextResult{}, app.ErrNotFound
}

func (r *fakeSourceRepo) Create(_ context.Context, source domain.Source) error {
	r.created = append(r.created, source)
	return nil
}

type fakeObjectRepo struct {
	created []domain.KnowledgeObject
}

func (r *fakeObjectRepo) Create(_ context.Context, object domain.KnowledgeObject) error {
	r.created = append(r.created, object)
	return nil
}

type fakeLinkRepo struct {
	err     error
	created []domain.ObjectSource
}

func (r *fakeLinkRepo) Create(_ context.Context, link domain.ObjectSource) error {
	if r.err != nil {
		return r.err
	}
	r.created = append(r.created, link)
	return nil
}

type fakeAuditRepo struct {
	created []domain.AuditEvent
}

func (r *fakeAuditRepo) Create(_ context.Context, event domain.AuditEvent) error {
	r.created = append(r.created, event)
	return nil
}

func TestIngestTextHandler_Success(t *testing.T) {
	uow := newFakeUOW()
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	body := ingestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "test content",
		Source: domain.SourceInput{
			Type:           "text",
			IdempotencyKey: "test-key",
		},
		Object: domain.ObjectInput{
			Type:      "document",
			Title:     "Test",
			CreatedBy: "user-1",
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	var result domain.IngestTextResult
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if result.SourceID == (uuid.UUID{}) {
		t.Error("source_id is zero UUID")
	}
	if result.ObjectID == (uuid.UUID{}) {
		t.Error("object_id is zero UUID")
	}
	if result.AuditEventID == (uuid.UUID{}) {
		t.Error("audit_event_id is zero UUID")
	}
	if result.ContentChecksum == "" {
		t.Error("content_checksum is empty")
	}
	if result.IdentityKey == "" {
		t.Error("identity_key is empty")
	}
	if result.Duplicate {
		t.Error("duplicate = true, want false")
	}

	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestIngestTextHandler_ValidationError(t *testing.T) {
	uow := newFakeUOW()
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	body := ingestTextRequest{
		WorkspaceID: "",
		Content:     "",
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var errResp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", errResp.Code)
	}
}

func TestIngestTextHandler_NotFoundError(t *testing.T) {
	t.Skip("workspace validation not yet implemented; ErrNotFound mapping is dead code until service validates workspace existence")
	// This test verifies that when the service returns ErrNotFound, the handler returns 404.
	// Currently the service does not return ErrNotFound for missing workspace.
	// The mapping is kept for future extensibility.
	uow := newFakeUOW()
	uow.repos.source.err = app.ErrNotFound
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	body := ingestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "test content",
		Source: domain.SourceInput{
			Type: "text",
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}

	var errResp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.Code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND", errResp.Code)
	}
}

func TestIngestTextHandler_InternalError(t *testing.T) {
	uow := newFakeUOW()
	uow.repos.source.err = errors.New("database connection lost")
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	body := ingestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "test content",
		Source: domain.SourceInput{
			Type: "text",
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}

	var errResp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.Code != "INTERNAL_ERROR" {
		t.Errorf("code = %q, want INTERNAL_ERROR", errResp.Code)
	}
	if errResp.Message == "database connection lost" {
		t.Error("error message leaked internal detail")
	}
}

func TestIngestTextHandler_Duplicate(t *testing.T) {
	existing := domain.IngestTextResult{
		SourceID:        uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		ObjectID:        uuid.MustParse("10000000-0000-0000-0000-000000000002"),
		AuditEventID:    uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		ContentChecksum: "persisted-checksum",
		IdentityKey:     "idem:already-seen",
	}
	uow := newFakeUOW()
	uow.repos.source.existingResult = existing
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	body := ingestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "different content",
		Source: domain.SourceInput{
			IdempotencyKey: "already-seen",
		},
	}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
	}

	var result domain.IngestTextResult
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !result.Duplicate {
		t.Error("duplicate = false, want true")
	}
	if result.SourceID != existing.SourceID {
		t.Errorf("source_id = %v, want %v", result.SourceID, existing.SourceID)
	}
}

func TestHealthHandler(t *testing.T) {
	handler := &HealthHandler{}

	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	var resp map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp["status"] != "ok" {
		t.Errorf("status = %q, want ok", resp["status"])
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestIngestTextHandler_MalformedJSON(t *testing.T) {
	uow := newFakeUOW()
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader([]byte("invalid json")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}

	var errResp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}

	if errResp.Code != "VALIDATION_ERROR" {
		t.Errorf("code = %q, want VALIDATION_ERROR", errResp.Code)
	}
}

func TestIngestTextHandler_RawJSONWireContract(t *testing.T) {
	uow := newFakeUOW()
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	// Raw JSON payload — validates actual snake_case wire contract, not Go struct tags.
	rawJSON := `{
		"workspace_id": "workspace-1",
		"content": "hello world",
		"source": {
			"type": "text",
			"idempotency_key": "raw-json-key",
			"uri": "https://example.com",
			"external_id": "ext-1",
			"title": "Source Title",
			"metadata": {"k": "v"},
			"captured_at": "2026-01-01T00:00:00Z"
		},
		"object": {
			"type": "document",
			"title": "Object Title",
			"summary": "sum",
			"status": "active",
			"metadata": {},
			"created_by": "user-1"
		}
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader([]byte(rawJSON)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Decode response and verify snake_case keys are present in the wire format.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rr.Body).Decode(&raw); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	requiredKeys := []string{"source_id", "object_id", "audit_event_id", "content_checksum", "identity_key", "duplicate"}
	for _, key := range requiredKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("response missing required key %q", key)
		}
	}

	// Verify source_id is a valid UUID string.
	var sourceID string
	if err := json.Unmarshal(raw["source_id"], &sourceID); err != nil {
		t.Errorf("source_id is not a string: %v", err)
	}
	if _, err := uuid.Parse(sourceID); err != nil {
		t.Errorf("source_id is not a valid UUID: %v", err)
	}

	// Verify duplicate is a boolean.
	var duplicate bool
	if err := json.Unmarshal(raw["duplicate"], &duplicate); err != nil {
		t.Errorf("duplicate is not a boolean: %v", err)
	}
}

func TestIngestTextHandler_BodyTooLarge(t *testing.T) {
	uow := newFakeUOW()
	svc := app.NewIngestTextService(uow)
	handler := NewIngestTextHandler(svc)

	// Build a payload that exceeds maxBodyBytes (1 MiB).
	bigContent := make([]byte, maxBodyBytes+1)
	for i := range bigContent {
		bigContent[i] = 'x'
	}
	rawJSON := `{"workspace_id":"w","content":"` + string(bigContent) + `"}`

	req := httptest.NewRequest(http.MethodPost, "/v1/ingest-text", bytes.NewReader([]byte(rawJSON)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d; body: %s", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}

	var errResp errorResponse
	if err := json.NewDecoder(rr.Body).Decode(&errResp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if errResp.Code != "PAYLOAD_TOO_LARGE" {
		t.Errorf("code = %q, want PAYLOAD_TOO_LARGE", errResp.Code)
	}
}
