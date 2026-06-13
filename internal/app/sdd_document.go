package app

import (
	"context"
	"log/slog"
	"sort"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// classifySection maps a KnowledgeObject's Type to the SDD section it belongs
// in. Unknown or unmapped types fall back to SddSectionContext, which is the
// safe default per the spec.
func classifySection(obj domain.KnowledgeObject) domain.SddSectionKey {
	switch obj.Type {
	case domain.KnowledgeObjectTypeDecision:
		return domain.SddSectionDecisions
	case domain.KnowledgeObjectTypeConstraint:
		return domain.SddSectionConstraints
	case domain.KnowledgeObjectTypeOpenQuestion:
		return domain.SddSectionOpenQuestions
	default:
		return domain.SddSectionContext
	}
}

// SddDocumentService manages the living SDD document for each workspace. It
// wires the post-validation hook to the SddDocumentUnitOfWork: validated
// objects are merged into the document; deprecated objects are removed.
//
// The write path (AppendValidatedObject) runs inside
// WithinSddDocumentTx, which holds SELECT ... FOR UPDATE on the
// row keyed by workspace_id for the duration of the JSONB
// read-modify-write. Concurrent appends/deprecates on the same
// workspace therefore serialize on the row lock and never lose
// entries. The read path (GetDocument) is uncontended and uses the
// pool-backed SddDocuments() accessor to skip a per-read transaction.
type SddDocumentService struct {
	uow    SddDocumentUnitOfWork
	now    Clock
	logger *slog.Logger
}

// NewSddDocumentService returns a service backed by the given unit of work.
// now is the clock used for entry and document timestamps. logger is used for
// best-effort hook error logging; it defaults to slog.Default() when nil.
func NewSddDocumentService(uow SddDocumentUnitOfWork, now Clock, logger *slog.Logger) *SddDocumentService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SddDocumentService{uow: uow, now: now, logger: logger}
}

// AppendValidatedObject updates the workspace SDD document when a knowledge
// object transitions to validated or deprecated.
//
// The whole read-modify-write happens inside a single transaction
// (WithinSddDocumentTx) that holds SELECT ... FOR UPDATE on the
// row keyed by workspace_id; the lock is released on commit. The
// tx-scoped repository is the one passed to the callback.
//
//   - validated: the entry is removed from all sections (handles type changes),
//     then appended to the classified section. Each section is kept sorted by
//     UpdatedAt DESC. The document's UpdatedAt is bumped.
//   - deprecated: the entry is removed from whichever section holds it (no-op
//     if absent). The document's UpdatedAt is bumped when an entry was found
//     and removed.
func (s *SddDocumentService) AppendValidatedObject(ctx context.Context, obj domain.KnowledgeObject) error {
	return s.uow.WithinSddDocumentTx(ctx, func(ctx context.Context, repo SddDocumentRepository) error {
		doc, err := repo.FindByWorkspace(ctx, obj.WorkspaceID)
		if err != nil {
			if err != ErrNotFound {
				return err
			}
			doc = emptyDocument(obj.WorkspaceID)
		}

		switch obj.Status {
		case domain.KnowledgeObjectStatusValidated:
			removeByObjectID(&doc, obj.ID)
			section := classifySection(obj)
			entry := domain.SddEntry{
				ObjectID:  obj.ID,
				Title:     obj.Title,
				Summary:   obj.Summary,
				UpdatedAt: s.now().UTC(),
			}
			doc.Sections[section] = append(doc.Sections[section], entry)
			sortSectionDesc(doc.Sections[section])
			doc.UpdatedAt = s.now().UTC()
			return repo.Upsert(ctx, doc)

		case domain.KnowledgeObjectStatusDeprecated:
			removed := removeByObjectID(&doc, obj.ID)
			if !removed {
				// No entry to remove — treat as no-op. The service
				// intentionally does NOT upsert an empty doc here:
				// when FindByWorkspace returned ErrNotFound we
				// initialised an empty doc, but the upsert that
				// would follow adds no information and would race
				// with a concurrent first-validate. Returning nil
				// commits an empty tx, which is the cheapest
				// correct answer for "nothing changed".
				return nil
			}
			doc.UpdatedAt = s.now().UTC()
			return repo.Upsert(ctx, doc)

		default:
			// Neither validated nor deprecated — nothing to do.
			return nil
		}
	})
}

// GetDocument returns the SDD document for the given workspace. When no
// document exists yet, it propagates ErrNotFound so read surfaces can return
// their documented 404/not-found behavior. The read goes through the
// pool-backed SddDocuments() accessor — uncontended, no tx needed.
func (s *SddDocumentService) GetDocument(ctx context.Context, workspaceID string) (domain.SddDocument, error) {
	doc, err := s.uow.SddDocuments().FindByWorkspace(ctx, workspaceID)
	if err != nil {
		return domain.SddDocument{}, err
	}
	return doc, nil
}

// emptyDocument returns a SddDocument for workspaceID with all four section
// keys initialised to empty (non-nil) slices.
func emptyDocument(workspaceID string) domain.SddDocument {
	sections := make(map[domain.SddSectionKey][]domain.SddEntry, len(domain.SddOrderedSections))
	for _, k := range domain.SddOrderedSections {
		sections[k] = []domain.SddEntry{}
	}
	return domain.SddDocument{WorkspaceID: workspaceID, Sections: sections}
}

// removeByObjectID removes any entry with the given objectID from every
// section of doc. It returns true when at least one entry was removed.
func removeByObjectID(doc *domain.SddDocument, objectID uuid.UUID) bool {
	removed := false
	for key, entries := range doc.Sections {
		filtered := entries[:0]
		for _, e := range entries {
			if e.ObjectID == objectID {
				removed = true
				continue
			}
			filtered = append(filtered, e)
		}
		doc.Sections[key] = filtered
	}
	return removed
}

// sortSectionDesc sorts entries by UpdatedAt descending; ties are broken by
// ObjectID string for determinism.
func sortSectionDesc(entries []domain.SddEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].UpdatedAt.Equal(entries[j].UpdatedAt) {
			return entries[i].ObjectID.String() > entries[j].ObjectID.String()
		}
		return entries[i].UpdatedAt.After(entries[j].UpdatedAt)
	})
}
