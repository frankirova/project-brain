package domain

import (
	"time"

	"github.com/google/uuid"
)

type Metadata map[string]any

const (
	SourceTypeText = "text"

	KnowledgeObjectTypeDocument = "document"
	KnowledgeObjectStatusActive = "active"

	AuditActionKnowledgeIngested = "knowledge.ingested"
	AuditTargetKnowledgeObject   = "knowledge_object"
)

type Source struct {
	ID          uuid.UUID
	WorkspaceID string
	Type        string
	URI         string
	ExternalID  string
	Title       string
	Checksum    string
	IdentityKey string
	Metadata    Metadata
	CapturedAt  time.Time
}

type KnowledgeObject struct {
	ID          uuid.UUID
	WorkspaceID string
	Type        string
	Title       string
	Summary     string
	Content     string
	Status      string
	Metadata    Metadata
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ObjectSource struct {
	ObjectID  uuid.UUID
	SourceID  uuid.UUID
	Relevance float64
}

type AuditEvent struct {
	ID          uuid.UUID
	WorkspaceID string
	ActorID     string
	Action      string
	TargetType  string
	TargetID    uuid.UUID
	After       Metadata
	CreatedAt   time.Time
}

type SourceInput struct {
	Type           string
	URI            string
	ExternalID     string
	Title          string
	IdempotencyKey string
	Metadata       Metadata
	CapturedAt     time.Time
}

type ObjectInput struct {
	Type      string
	Title     string
	Summary   string
	Status    string
	Metadata  Metadata
	CreatedBy string
}

type IngestTextRequest struct {
	WorkspaceID string
	Content     string
	Source      SourceInput
	Object      ObjectInput
}

type IngestTextResult struct {
	SourceID        uuid.UUID
	ObjectID        uuid.UUID
	AuditEventID    uuid.UUID
	ContentChecksum string
	IdentityKey     string
	Duplicate       bool
}
