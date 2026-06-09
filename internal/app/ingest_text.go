package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

var ErrValidation = errors.New("validation error")

type IDGenerator func() uuid.UUID

type Clock func() time.Time

type IngestTextService struct {
	uow IngestionUnitOfWork
	ids IDGenerator
	now Clock
}

func NewIngestTextService(uow IngestionUnitOfWork) *IngestTextService {
	return NewIngestTextServiceWithDependencies(uow, uuid.New, time.Now)
}

func NewIngestTextServiceWithDependencies(uow IngestionUnitOfWork, ids IDGenerator, now Clock) *IngestTextService {
	return &IngestTextService{uow: uow, ids: ids, now: now}
}

func (s *IngestTextService) Ingest(ctx context.Context, req domain.IngestTextRequest) (domain.IngestTextResult, error) {
	prepared, err := prepareIngestText(req)
	if err != nil {
		return domain.IngestTextResult{}, err
	}

	var result domain.IngestTextResult
	err = s.uow.WithinIngestionTx(ctx, func(ctx context.Context, repos IngestionRepositories) error {
		existing, err := repos.Sources().FindIngestionResultByIdentityKey(ctx, prepared.workspaceID, prepared.identityKey)
		if err == nil {
			existing.Duplicate = true
			result = existing
			return nil
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
		return domain.IngestTextResult{}, err
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
}

func prepareIngestText(req domain.IngestTextRequest) (preparedIngestText, error) {
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID == "" {
		return preparedIngestText{}, fmt.Errorf("%w: workspace_id is required", ErrValidation)
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		return preparedIngestText{}, fmt.Errorf("%w: content is required", ErrValidation)
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
	}

	contentChecksum := checksum(content)
	identityKey := computeIdentityKey(workspaceID, sourceType, req.Source, contentChecksum)

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
