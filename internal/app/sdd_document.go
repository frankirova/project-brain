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
// wires the post-validation hook to the SddDocumentRepository: validated
// objects are merged into the document; deprecated objects are removed.
type SddDocumentService struct {
	repo   SddDocumentRepository
	now    Clock
	logger *slog.Logger
}

// NewSddDocumentService returns a service backed by the given repository.
// now is the clock used for entry and document timestamps. logger is used for
// best-effort hook error logging; it defaults to slog.Default() when nil.
func NewSddDocumentService(repo SddDocumentRepository, now Clock, logger *slog.Logger) *SddDocumentService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SddDocumentService{repo: repo, now: now, logger: logger}
}

// AppendValidatedObject updates the workspace SDD document when a knowledge
// object transitions to validated or deprecated.
//
//   - validated: the entry is removed from all sections (handles type changes),
//     then appended to the classified section. Each section is kept sorted by
//     UpdatedAt DESC. The document's UpdatedAt is bumped.
//   - deprecated: the entry is removed from whichever section holds it (no-op
//     if absent). The document's UpdatedAt is bumped when an entry was found
//     and removed.
func (s *SddDocumentService) AppendValidatedObject(ctx context.Context, obj domain.KnowledgeObject) error {
	doc, err := s.repo.FindByWorkspace(ctx, obj.WorkspaceID)
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
		return s.repo.Upsert(ctx, doc)

	case domain.KnowledgeObjectStatusDeprecated:
		removed := removeByObjectID(&doc, obj.ID)
		if !removed {
			// No entry to remove — treat as no-op but still persist so
			// the document's updated_at does not drift from reality.
			// If the doc was ErrNotFound we already initialised an empty doc;
			// upserting an empty doc is harmless.
			return nil
		}
		doc.UpdatedAt = s.now().UTC()
		return s.repo.Upsert(ctx, doc)

	default:
		// Neither validated nor deprecated — nothing to do.
		return nil
	}
}

// GetDocument returns the SDD document for the given workspace. When no
// document exists yet, it propagates ErrNotFound so read surfaces can return
// their documented 404/not-found behavior.
func (s *SddDocumentService) GetDocument(ctx context.Context, workspaceID string) (domain.SddDocument, error) {
	doc, err := s.repo.FindByWorkspace(ctx, workspaceID)
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
