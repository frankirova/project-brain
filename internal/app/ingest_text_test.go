package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gentle-ai/hermes-agents/internal/domain"
	"github.com/google/uuid"
)

func TestIngestCreatesCompleteAuditableRecords(t *testing.T) {
	ids := fixedIDs{
		uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		uuid.MustParse("00000000-0000-0000-0000-000000000003"),
	}
	now := time.Date(2026, 6, 9, 10, 11, 12, 0, time.UTC)
	uow := newFakeUOW()
	service := NewIngestTextServiceWithDependencies(uow, ids.next, func() time.Time { return now })

	result, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: " workspace-1 ",
		Content:     " important knowledge ",
		Source: domain.SourceInput{
			Type:           "telegram",
			ExternalID:     "message-123",
			Title:          "Source title",
			IdempotencyKey: "retry-safe-key",
			Metadata:       domain.Metadata{"chat_id": "42"},
		},
		Object: domain.ObjectInput{
			Type:      "decision",
			Title:     "Decision title",
			Summary:   "Summary",
			Status:    "active",
			CreatedBy: "user-1",
			Metadata:  domain.Metadata{"importance": "high"},
		},
	})
	if err != nil {
		t.Fatalf("Ingest() returned error: %v", err)
	}

	if !uow.committed || uow.rolledBack {
		t.Fatalf("transaction committed=%v rolledBack=%v, want committed only", uow.committed, uow.rolledBack)
	}
	if result.Duplicate {
		t.Fatal("result.Duplicate = true, want false")
	}
	if result.SourceID != uow.repos.source.created[0].ID || result.ObjectID != uow.repos.object.created[0].ID || result.AuditEventID != uow.repos.audit.created[0].ID {
		t.Fatalf("result IDs do not match created records: %+v", result)
	}
	if result.IdentityKey != "idem:retry-safe-key" {
		t.Fatalf("IdentityKey = %q, want idempotency key identity", result.IdentityKey)
	}

	source := uow.repos.source.created[0]
	if source.WorkspaceID != "workspace-1" || source.Type != "telegram" || source.ExternalID != "message-123" || source.Checksum == "" {
		t.Fatalf("source not populated from request: %+v", source)
	}
	object := uow.repos.object.created[0]
	if object.Content != "important knowledge" || object.Type != "decision" || object.Title != "Decision title" || object.CreatedAt != now || object.UpdatedAt != now {
		t.Fatalf("object not populated from request: %+v", object)
	}
	link := uow.repos.link.created[0]
	if link.SourceID != source.ID || link.ObjectID != object.ID || link.Relevance != 1 {
		t.Fatalf("link = %+v, want source/object link with full relevance", link)
	}
	audit := uow.repos.audit.created[0]
	if audit.Action != domain.AuditActionKnowledgeIngested || audit.TargetID != object.ID || audit.TargetType != domain.AuditTargetKnowledgeObject {
		t.Fatalf("audit = %+v, want knowledge ingestion event", audit)
	}
}

func TestIngestRejectsWhitespaceWithoutWrites(t *testing.T) {
	uow := newFakeUOW()
	service := NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now)

	_, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     " \t\n ",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Ingest() error = %v, want validation error", err)
	}
	if uow.started || uow.repos.writeCount() != 0 {
		t.Fatalf("started=%v writes=%d, want no transaction or writes", uow.started, uow.repos.writeCount())
	}
}

func TestIngestReturnsDuplicateWithoutCreatingRecords(t *testing.T) {
	existing := domain.IngestTextResult{
		SourceID:        uuid.MustParse("10000000-0000-0000-0000-000000000001"),
		ObjectID:        uuid.MustParse("10000000-0000-0000-0000-000000000002"),
		AuditEventID:    uuid.MustParse("10000000-0000-0000-0000-000000000003"),
		ContentChecksum: "persisted-checksum",
		IdentityKey:     "idem:already-seen",
	}
	uow := newFakeUOW()
	uow.repos.source.existingResult = existing
	service := NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now)

	result, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "different content with reused key",
		Source: domain.SourceInput{
			IdempotencyKey: "already-seen",
		},
	})
	if err != nil {
		t.Fatalf("Ingest() returned error: %v", err)
	}

	if !result.Duplicate {
		t.Fatal("Duplicate = false, want true")
	}
	if result.SourceID != existing.SourceID || result.ObjectID != existing.ObjectID || result.AuditEventID != existing.AuditEventID {
		t.Fatalf("result = %+v, want existing IDs %+v", result, existing)
	}
	if result.ContentChecksum != existing.ContentChecksum || result.IdentityKey != existing.IdentityKey {
		t.Fatalf("result checksum/identity = %q/%q, want persisted %q/%q", result.ContentChecksum, result.IdentityKey, existing.ContentChecksum, existing.IdentityKey)
	}
	if uow.repos.writeCount() != 0 {
		t.Fatalf("writes=%d, want no duplicate writes", uow.repos.writeCount())
	}
}

func TestIngestDoesNotRequireDeferredExternalCapabilities(t *testing.T) {
	uow := newFakeUOW()
	ids := fixedIDs{
		uuid.MustParse("20000000-0000-0000-0000-000000000001"),
		uuid.MustParse("20000000-0000-0000-0000-000000000002"),
		uuid.MustParse("20000000-0000-0000-0000-000000000003"),
	}
	service := NewIngestTextServiceWithDependencies(uow, ids.next, func() time.Time { return time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC) })

	_, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "text-only knowledge",
	})
	if err != nil {
		t.Fatalf("Ingest() returned error without Telegram/RAG dependencies: %v", err)
	}
	if got, want := uow.repos.writeCount(), 4; got != want {
		t.Fatalf("writes=%d, want source/object/link/audit only", got)
	}
	source := uow.repos.source.created[0]
	object := uow.repos.object.created[0]
	if source.Type != domain.SourceTypeText || object.Type != domain.KnowledgeObjectTypeDocument || object.Status != domain.KnowledgeObjectStatusActive {
		t.Fatalf("defaults source=%+v object=%+v", source, object)
	}
}

func TestIngestRollsBackWhenARequiredRecordFails(t *testing.T) {
	failure := errors.New("link failed")
	uow := newFakeUOW()
	uow.repos.link.err = failure
	ids := fixedIDs{
		uuid.MustParse("30000000-0000-0000-0000-000000000001"),
		uuid.MustParse("30000000-0000-0000-0000-000000000002"),
		uuid.MustParse("30000000-0000-0000-0000-000000000003"),
	}
	service := NewIngestTextServiceWithDependencies(uow, ids.next, time.Now)

	_, err := service.Ingest(context.Background(), domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "valid content",
	})
	if !errors.Is(err, failure) {
		t.Fatalf("Ingest() error = %v, want link failure", err)
	}
	if !uow.rolledBack || uow.committed {
		t.Fatalf("transaction committed=%v rolledBack=%v, want rollback only", uow.committed, uow.rolledBack)
	}
}

func TestIdentityKeyAllowsSameContentFromDistinctSources(t *testing.T) {
	one, err := prepareIngestText(domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "same content",
		Source: domain.SourceInput{
			URI: "https://example.test/one",
		},
	})
	if err != nil {
		t.Fatalf("prepareIngestText() returned error: %v", err)
	}
	two, err := prepareIngestText(domain.IngestTextRequest{
		WorkspaceID: "workspace-1",
		Content:     "same content",
		Source: domain.SourceInput{
			URI: "https://example.test/two",
		},
	})
	if err != nil {
		t.Fatalf("prepareIngestText() returned error: %v", err)
	}

	if one.contentChecksum != two.contentChecksum {
		t.Fatalf("content checksum differs for same content: %q vs %q", one.contentChecksum, two.contentChecksum)
	}
	if one.identityKey == two.identityKey {
		t.Fatalf("identity keys equal for distinct source identities: %q", one.identityKey)
	}
}

type fakeUOW struct {
	repos      *fakeRepos
	started    bool
	committed  bool
	rolledBack bool
}

func newFakeUOW() *fakeUOW {
	return &fakeUOW{repos: &fakeRepos{source: &fakeSourceRepo{}, object: &fakeObjectRepo{}, link: &fakeLinkRepo{}, audit: &fakeAuditRepo{}}}
}

func (u *fakeUOW) WithinIngestionTx(ctx context.Context, fn func(context.Context, IngestionRepositories) error) error {
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

func (r *fakeRepos) Sources() SourceRepository                   { return r.source }
func (r *fakeRepos) KnowledgeObjects() KnowledgeObjectRepository { return r.object }
func (r *fakeRepos) ObjectSources() ObjectSourceRepository       { return r.link }
func (r *fakeRepos) AuditEvents() AuditEventRepository           { return r.audit }

func (r *fakeRepos) writeCount() int {
	return len(r.source.created) + len(r.object.created) + len(r.link.created) + len(r.audit.created)
}

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
	return domain.IngestTextResult{}, ErrNotFound
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

type fixedIDs []uuid.UUID

func (ids *fixedIDs) next() uuid.UUID {
	if len(*ids) == 0 {
		panic("no fixed IDs remaining")
	}
	next := (*ids)[0]
	*ids = (*ids)[1:]
	return next
}
