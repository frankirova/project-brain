package domain

import (
	"time"

	"github.com/google/uuid"
)

// Metadata is an open-ended key/value bag persisted as JSONB. It is
// `map[string]any` to keep the wire format permissive, but the
// application layer should use one of the typed sub-structs below
// or a documented set of well-known keys for new code.
//
// Reserved keys:
//   - "chat_id"     — Telegram chat ID (string of int64)
//   - "user_id"     — Telegram user ID (string of int64)
//   - "importance"  — legacy short-form marker ("high"|"medium"|"low")
type Metadata map[string]any

const (
	SourceTypeText = "text"
	KnowledgeObjectTypeDocument = "document"

	// KnowledgeObjectStatus is the lifecycle of a knowledge object.
	// Mirrors the CHECK constraint on knowledge_objects.status
	// (migrations/0005_lifecycle_and_audit_richness.sql).
	KnowledgeObjectStatusActive     = "active"     // historical default; equivalent to "validated but not formally reviewed"
	KnowledgeObjectStatusProposed   = "proposed"   // recently ingested, awaiting human validation
	KnowledgeObjectStatusDebating   = "debating"   // a human is reviewing / questioning
	KnowledgeObjectStatusValidated  = "validated"  // a human explicitly approved
	KnowledgeObjectStatusDeprecated = "deprecated" // superseded or invalidated
)

var validKnowledgeObjectStatuses = map[string]bool{
	KnowledgeObjectStatusActive:     true,
	KnowledgeObjectStatusProposed:   true,
	KnowledgeObjectStatusDebating:   true,
	KnowledgeObjectStatusValidated:  true,
	KnowledgeObjectStatusDeprecated: true,
}

// ValidateKnowledgeObjectStatus returns true if status is one of the
// five allowed lifecycle values.
func ValidateKnowledgeObjectStatus(status string) bool {
	return validKnowledgeObjectStatuses[status]
}

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
	// ProjectID scopes the object to a project; nullable, no FK yet.
	ProjectID *uuid.UUID
	// Tags is always non-nil on read; defaults to an empty slice on ingest.
	Tags []string
	// Confidence is the source's self-reported confidence; nullable.
	Confidence *float64
	// Importance is a 0..100 score enforced by a CHECK constraint in the DB.
	Importance *int
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
	Before      Metadata
	After       Metadata
	Reason      string
	RequestID   *uuid.UUID
	Metadata    Metadata
	CreatedAt   time.Time
}

// AuditAction enumerates the recognized audit event actions. The DB
// does not enforce this list yet (forward-compatible), but the app
// layer should use these constants for consistency.
const (
	AuditActionKnowledgeIngested         = "knowledge.ingested"
	AuditActionKnowledgeDuplicateDetected = "knowledge.duplicate_detected"
	AuditActionKnowledgeStatusChanged    = "knowledge.status_changed"
	AuditActionRelationCreated           = "relation.created"
)

// AuditTargetType enumerates the recognized audit target types.
const (
	AuditTargetKnowledgeObject = "knowledge_object"
	AuditTargetRelation        = "relation"
	AuditTargetRawInput        = "raw_input"
)

// RelationType is a string enum for the 14 allowed relation types.
type RelationType string

const (
	RelationTypeRelatesTo    RelationType = "relates_to"
	RelationTypeDependsOn    RelationType = "depends_on"
	RelationTypeContradicts  RelationType = "contradicts"
	RelationTypeSupersedes   RelationType = "supersedes"
	RelationTypeSupports     RelationType = "supports"
	RelationTypeDerivedFrom  RelationType = "derived_from"
	RelationTypeMentions     RelationType = "mentions"
	RelationTypeDecides      RelationType = "decides"
	RelationTypeImplements   RelationType = "implements"
	RelationTypeComparesWith RelationType = "compares_with"
	RelationTypeReplaces     RelationType = "replaces"
	RelationTypeBlocks       RelationType = "blocks"
	RelationTypeReferences   RelationType = "references"
	RelationTypePartOf       RelationType = "part_of"
)

var validRelationTypes = map[RelationType]bool{
	RelationTypeRelatesTo:    true,
	RelationTypeDependsOn:    true,
	RelationTypeContradicts:  true,
	RelationTypeSupersedes:   true,
	RelationTypeSupports:     true,
	RelationTypeDerivedFrom:  true,
	RelationTypeMentions:     true,
	RelationTypeDecides:      true,
	RelationTypeImplements:   true,
	RelationTypeComparesWith: true,
	RelationTypeReplaces:     true,
	RelationTypeBlocks:       true,
	RelationTypeReferences:   true,
	RelationTypePartOf:       true,
}

// ValidateRelationType returns true if relType is one of the 14 allowed values.
func ValidateRelationType(relType RelationType) bool {
	return validRelationTypes[relType]
}

// Relation is a typed directed edge between two knowledge objects.
type Relation struct {
	ID             uuid.UUID
	WorkspaceID    string
	SourceObjectID uuid.UUID
	TargetObjectID uuid.UUID
	RelationType   RelationType
	Confidence     *float64
	Evidence       string
	Metadata       Metadata
	CreatedAt      time.Time
}

// RelationInput is the creation payload for a relation.
type RelationInput struct {
	SourceObjectID uuid.UUID    `json:"source_object_id"`
	TargetObjectID uuid.UUID    `json:"target_object_id"`
	RelationType   RelationType `json:"relation_type"`
	Confidence     *float64     `json:"confidence"`
	Evidence       string       `json:"evidence"`
	Metadata       Metadata     `json:"metadata"`
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
	// §10.1 metadata. All optional; pass-through to KnowledgeObject.
	ProjectID  *uuid.UUID `json:"project_id,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Confidence *float64   `json:"confidence,omitempty"`
	Importance *int       `json:"importance,omitempty"`
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
