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
	SourceTypeText              = "text"
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
	AuditActionKnowledgeIngested          = "knowledge.ingested"
	AuditActionKnowledgeDuplicateDetected = "knowledge.duplicate_detected"
	AuditActionKnowledgeStatusChanged     = "knowledge.status_changed"
	AuditActionRelationCreated            = "relation.created"
)

// Change 14 (human-loop-orchestrator) domain constants. The
// audit-action constants are siblings of AuditActionKnowledgeStatusChanged
// (defined above) and are emitted by ObjectDebateService (added in
// PR 2+). The remaining constants are debate-specific enums and
// time-window defaults consumed by the human-backlog query and the
// derived stale marker. They live in domain so the app and postgres
// layers share the same identifiers without circular imports.
const (
	// Audit actions emitted by ObjectDebateService.
	AuditActionKnowledgeDebateOpened   = "knowledge.debate_opened"
	AuditActionKnowledgeDebateResolved = "knowledge.debate_resolved"

	// DebateTrigger discriminates who initiated a MarkDebating call.
	// The transition itself is always a human decision — humans
	// close the debate loop — but the suggestion can originate
	// from the system (a bot detected a contradiction) or the
	// human (an explicit caller action). Only "system" requires
	// SuggestedBy to be set.
	DebateTriggerSystem = "system"
	DebateTriggerHuman  = "human"

	// DebateSuggestion is the closed set of well-known system
	// identifiers that may populate Metadata.suggested_by on a
	// debate_opened audit row. The initial and only value tracks
	// the contradiction detector. Presence is meaningful: the
	// field is omitted iff the trigger was human-explicit.
	DebateSuggestionContradictionDetector = "bot:contradiction-detector"

	// BacklogRecentDeprecatedDays bounds how long a deprecated
	// object stays visible in the human backlog after its last
	// update. Matches DebateStaleDays on purpose: one mental
	// number (14d) for both "stale debating" and "recently
	// deprecated" recency.
	BacklogRecentDeprecatedDays = 14

	// DebateStaleDays is the read-time staleness threshold for
	// objects in `debating` status. A debating object is marked
	// is_stale once its updated_at is older than this window.
	// Derived at read time; no auto-transition is performed.
	DebateStaleDays = 14
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
	WorkspaceID string      `json:"workspace_id"`
	Content     string      `json:"content"`
	Source      SourceInput `json:"source"`
	Object      ObjectInput `json:"object"`
	RequestID   *uuid.UUID
}

type IngestTextResult struct {
	SourceID        uuid.UUID `json:"source_id"`
	ObjectID        uuid.UUID `json:"object_id"`
	AuditEventID    uuid.UUID `json:"audit_event_id"`
	ContentChecksum string    `json:"content_checksum"`
	IdentityKey     string    `json:"identity_key"`
	Duplicate       bool      `json:"duplicate"`
}
