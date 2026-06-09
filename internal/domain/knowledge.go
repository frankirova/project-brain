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
	Type           string    `json:"type"`
	URI            string    `json:"uri"`
	ExternalID     string    `json:"external_id"`
	Title          string    `json:"title"`
	IdempotencyKey string    `json:"idempotency_key"`
	Metadata       Metadata  `json:"metadata"`
	CapturedAt     time.Time `json:"captured_at"`
}

type ObjectInput struct {
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Status    string   `json:"status"`
	Metadata  Metadata `json:"metadata"`
	CreatedBy string   `json:"created_by"`
}

type IngestTextRequest struct {
	WorkspaceID string
	Content     string
	Source      SourceInput
	Object      ObjectInput
}

type IngestTextResult struct {
	SourceID        uuid.UUID `json:"source_id"`
	ObjectID        uuid.UUID `json:"object_id"`
	AuditEventID    uuid.UUID `json:"audit_event_id"`
	ContentChecksum string    `json:"content_checksum"`
	IdentityKey     string    `json:"identity_key"`
	Duplicate       bool      `json:"duplicate"`
}
