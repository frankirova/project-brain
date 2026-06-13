package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

var ErrValidation = errors.New("validation error")

// FieldError carries the name and reason of a single validation
// failure. Callers (e.g. Fase 3's Telegram validation UI) can extract
// the field to highlight it in the response.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	return e.Field + ": " + e.Reason
}

// FieldErrorf returns a FieldError wrapped in ErrValidation. Callers
// can errors.As to recover the FieldError and extract the structured
// detail.
func FieldErrorf(field, reason string) error {
	return &validationError{Field: FieldError{Field: field, Reason: reason}}
}

type validationError struct {
	Field FieldError
}

func (e *validationError) Error() string {
	return e.Field.Error()
}

func (e *validationError) Unwrap() error { return ErrValidation }

type IDGenerator func() uuid.UUID

type Clock func() time.Time

// PostIngestHook runs after a successful, non-duplicate ingest, OUTSIDE
// the transaction. It is best-effort: IngestTextService logs and swallows
// any error it returns, so the ingest contract (and the sacred 4-write
// count) is unaffected even when the hook calls an external service like
// an embedding API. The canonical use is generating and storing the
// object's embedding.
type PostIngestHook func(ctx context.Context, obj domain.KnowledgeObject) error

type IngestTextService struct {
	uow        IngestionUnitOfWork
	ids        IDGenerator
	now        Clock
	logger     *slog.Logger
	postIngest PostIngestHook
}

func NewIngestTextService(uow IngestionUnitOfWork) *IngestTextService {
	return NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now, slog.Default())
}

func NewIngestTextServiceWithDependencies(uow IngestionUnitOfWork, ids IDGenerator, now Clock, logger *slog.Logger) *IngestTextService {
	if logger == nil {
		logger = slog.Default()
	}
	return &IngestTextService{uow: uow, ids: ids, now: now, logger: logger}
}

// SetPostIngestHook wires a best-effort hook to run after each successful
// non-duplicate ingest. Intended for composition-root (wire-time) use:
// the hook is invoked outside the ingest transaction, so it can call
// external services without affecting the 4-write contract.
func (s *IngestTextService) SetPostIngestHook(h PostIngestHook) {
	s.postIngest = h
}

func (s *IngestTextService) Ingest(ctx context.Context, req domain.IngestTextRequest) (domain.IngestTextResult, error) {
	start := s.now()
	prepared, err := prepareIngestText(req)
	if err != nil {
		s.logger.Debug("ingest validation failed",
			slog.String("workspace_id", strings.TrimSpace(req.WorkspaceID)),
			slog.String("error", err.Error()))
		return domain.IngestTextResult{}, err
	}

	var result domain.IngestTextResult
	// createdObject is captured from inside the transaction so the
	// post-ingest hook (e.g. embedding) can run on it after commit.
	var createdObject domain.KnowledgeObject
	err = s.uow.WithinIngestionTx(ctx, func(ctx context.Context, repos IngestionRepositories) error {
		existing, err := repos.Sources().FindIngestionResultByIdentityKey(ctx, prepared.workspaceID, prepared.identityKey)
		if err == nil {
			existing.Duplicate = true
			result = existing
			dupAudit := domain.AuditEvent{
				ID:          s.ids(),
				WorkspaceID: prepared.workspaceID,
				ActorID:     strings.TrimSpace(prepared.object.CreatedBy),
				Action:      domain.AuditActionKnowledgeDuplicateDetected,
				TargetType:  domain.AuditTargetKnowledgeObject,
				TargetID:    existing.ObjectID,
				After: domain.Metadata{
					"identity_key":      prepared.identityKey,
					"original_audit_id": existing.AuditEventID.String(),
				},
				CreatedAt: s.now().UTC(),
			}
			return repos.AuditEvents().Create(ctx, dupAudit)
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}

		createdAt := s.now().UTC()
		capturedAt := prepared.source.CapturedAt
		if capturedAt.IsZero() {
			capturedAt = createdAt
		}

		sourceID := s.ids()
		objectID := s.ids()
		auditEventID := s.ids()

		source := domain.Source{
			ID:          sourceID,
			WorkspaceID: prepared.workspaceID,
			Type:        prepared.sourceType,
			URI:         strings.TrimSpace(prepared.source.URI),
			ExternalID:  strings.TrimSpace(prepared.source.ExternalID),
			Title:       strings.TrimSpace(prepared.source.Title),
			Checksum:    prepared.contentChecksum,
			IdentityKey: prepared.identityKey,
			Metadata:    prepared.source.Metadata,
			CapturedAt:  capturedAt.UTC(),
		}
		object := domain.KnowledgeObject{
			ID:          objectID,
			WorkspaceID: prepared.workspaceID,
			Type:        prepared.objectType,
			Title:       strings.TrimSpace(prepared.object.Title),
			Summary:     strings.TrimSpace(prepared.object.Summary),
			Content:     prepared.content,
			Status:      prepared.objectStatus,
			Metadata:    prepared.object.Metadata,
			CreatedBy:   strings.TrimSpace(prepared.object.CreatedBy),
			CreatedAt:   createdAt,
			UpdatedAt:   createdAt,
			ProjectID:   prepared.object.ProjectID,
			Tags:        prepared.tags,
			Confidence:  prepared.object.Confidence,
			Importance:  prepared.object.Importance,
		}
		link := domain.ObjectSource{ObjectID: objectID, SourceID: sourceID, Relevance: 1}
		auditEvent := domain.AuditEvent{
			ID:          auditEventID,
			WorkspaceID: prepared.workspaceID,
			ActorID:     strings.TrimSpace(prepared.object.CreatedBy),
			Action:      domain.AuditActionKnowledgeIngested,
			TargetType:  domain.AuditTargetKnowledgeObject,
			TargetID:    objectID,
			After: domain.Metadata{
				"source_id":        sourceID.String(),
				"content_checksum": prepared.contentChecksum,
				"identity_key":     prepared.identityKey,
			},
			CreatedAt: createdAt,
		}

		if err := repos.Sources().Create(ctx, source); err != nil {
			return err
		}
		if err := repos.KnowledgeObjects().Create(ctx, object); err != nil {
			return err
		}
		if err := repos.ObjectSources().Create(ctx, link); err != nil {
			return err
		}
		if err := repos.AuditEvents().Create(ctx, auditEvent); err != nil {
			return err
		}

		createdObject = object
		result = domain.IngestTextResult{
			SourceID:        sourceID,
			ObjectID:        objectID,
			AuditEventID:    auditEventID,
			ContentChecksum: prepared.contentChecksum,
			IdentityKey:     prepared.identityKey,
		}
		return nil
	})
	if err != nil {
		s.logger.Error("ingest transaction failed",
			slog.String("workspace_id", prepared.workspaceID),
			slog.String("error", err.Error()),
			slog.Duration("elapsed", s.now().Sub(start)))
		return domain.IngestTextResult{}, err
	}

	s.logger.Info("ingest complete",
		slog.String("workspace_id", prepared.workspaceID),
		slog.Int("content_bytes", len(prepared.content)),
		slog.Bool("duplicate", result.Duplicate),
		slog.String("source_id", result.SourceID.String()),
		slog.String("object_id", result.ObjectID.String()),
		slog.Duration("elapsed", s.now().Sub(start)))

	// Best-effort post-ingest work (e.g. embedding). Runs outside the
	// transaction and never fails the ingest: a hook error degrades a
	// downstream capability (search recall) but the knowledge is already
	// durably committed. Duplicates are skipped — the object, and its
	// embedding, already exist.
	if !result.Duplicate && s.postIngest != nil {
		if hookErr := s.postIngest(ctx, createdObject); hookErr != nil {
			s.logger.Warn("post-ingest hook failed (best-effort, ingest unaffected)",
				slog.String("workspace_id", prepared.workspaceID),
				slog.String("object_id", result.ObjectID.String()),
				slog.String("error", hookErr.Error()))
		}
	}

	return result, nil
}

type preparedIngestText struct {
	workspaceID     string
	content         string
	contentChecksum string
	identityKey     string
	sourceType      string
	objectType      string
	objectStatus    string
	source          domain.SourceInput
	object          domain.ObjectInput
	// tags is the defaulted (non-nil) copy of req.Object.Tags.
	tags []string
}

func prepareIngestText(req domain.IngestTextRequest) (preparedIngestText, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(req.WorkspaceID))
	if workspaceID == "" {
		return preparedIngestText{}, FieldErrorf("workspace_id", "is required")
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		return preparedIngestText{}, FieldErrorf("content", "is required")
	}

	sourceType := strings.TrimSpace(req.Source.Type)
	if sourceType == "" {
		sourceType = domain.SourceTypeText
	}
	objectType := strings.TrimSpace(req.Object.Type)
	if objectType == "" {
		objectType = domain.KnowledgeObjectTypeDocument
	}
	objectStatus := strings.TrimSpace(req.Object.Status)
	if objectStatus == "" {
		objectStatus = domain.KnowledgeObjectStatusActive
	} else if !domain.ValidateKnowledgeObjectStatus(objectStatus) {
		return preparedIngestText{}, FieldErrorf("object.status", fmt.Sprintf("is not a valid lifecycle value: %q", objectStatus))
	}

	contentChecksum := checksum(content)
	identityKey := computeIdentityKey(workspaceID, sourceType, req.Source, contentChecksum)

	// Default tags to a non-nil empty slice so writes are predictable and
	// round-trips preserve a non-nil value.
	tags := req.Object.Tags
	if tags == nil {
		tags = []string{}
	}

	return preparedIngestText{
		workspaceID:     workspaceID,
		content:         content,
		contentChecksum: contentChecksum,
		identityKey:     identityKey,
		sourceType:      sourceType,
		objectType:      objectType,
		objectStatus:    objectStatus,
		source:          req.Source,
		object:          req.Object,
		tags:            tags,
	}, nil
}

func computeIdentityKey(workspaceID string, sourceType string, source domain.SourceInput, contentChecksum string) string {
	if idempotencyKey := strings.TrimSpace(source.IdempotencyKey); idempotencyKey != "" {
		return "idem:" + idempotencyKey
	}

	locator := strings.TrimSpace(source.URI)
	if locator == "" {
		locator = strings.TrimSpace(source.ExternalID)
	}

	return "sha256:" + checksum(strings.Join([]string{
		strings.ToLower(strings.TrimSpace(workspaceID)),
		strings.ToLower(strings.TrimSpace(sourceType)),
		strings.ToLower(locator),
		contentChecksum,
	}, "\x00"))
}

func checksum(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
