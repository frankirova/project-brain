package domain

import (
	"time"

	"github.com/google/uuid"
)

// SddSectionKey identifies one of the four canonical sections of an SDD
// document. The string value is the key used in the JSONB storage layer.
type SddSectionKey string

const (
	// SddSectionContext is the default section for documents, facts, and any
	// knowledge object whose type does not map to a more specific section.
	SddSectionContext SddSectionKey = "context"

	// SddSectionDecisions holds entries whose source KnowledgeObject has
	// Type == KnowledgeObjectTypeDecision.
	SddSectionDecisions SddSectionKey = "decisions"

	// SddSectionConstraints holds entries whose source KnowledgeObject has
	// Type == KnowledgeObjectTypeConstraint.
	SddSectionConstraints SddSectionKey = "constraints"

	// SddSectionOpenQuestions holds entries whose source KnowledgeObject has
	// Type == KnowledgeObjectTypeOpenQuestion.
	SddSectionOpenQuestions SddSectionKey = "open_questions"
)

// SddOrderedSections is the canonical iteration and render order for the four
// SDD sections. Consumers MUST use this slice rather than ranging over the
// Sections map directly so that the heading order is stable across Go versions.
var SddOrderedSections = []SddSectionKey{
	SddSectionContext,
	SddSectionDecisions,
	SddSectionConstraints,
	SddSectionOpenQuestions,
}

// SddEntry is a single entry in an SDD document section. It is a compact
// projection of a KnowledgeObject: only the fields needed for human-readable
// rendering are kept; content and raw metadata are intentionally excluded.
type SddEntry struct {
	// ObjectID is the UUID of the source KnowledgeObject.
	ObjectID uuid.UUID
	// Title is KnowledgeObject.Title at time of upsert.
	Title string
	// Summary is KnowledgeObject.Summary at time of upsert.
	Summary string
	// UpdatedAt is when this entry was last merged into the document.
	UpdatedAt time.Time
}

// SddDocument is the living SDD document for a single workspace. Each
// workspace has at most one SddDocument row in the database. The Sections map
// is keyed by SddSectionKey; all four canonical keys are always present (even
// when empty) so callers can range over SddOrderedSections safely.
type SddDocument struct {
	// WorkspaceID is the tenant key that owns this document.
	WorkspaceID string
	// Sections maps each section key to its ordered slice of entries. The
	// ordering within each slice is UpdatedAt DESC (maintained by the writer).
	Sections map[SddSectionKey][]SddEntry
	// UpdatedAt is the timestamp of the last write to this document.
	UpdatedAt time.Time
}
