package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sddDocumentRepo is the PostgreSQL implementation of app.SddDocumentRepository.
// It is pool-backed (not transactional) because the SDD document write happens
// outside the validation UoW, in a best-effort post-commit hook.
type sddDocumentRepo struct {
	pool *pgxpool.Pool
}

// NewSddDocumentRepo returns an SddDocumentRepository backed by the given pool.
func NewSddDocumentRepo(pool *pgxpool.Pool) *sddDocumentRepo {
	return &sddDocumentRepo{pool: pool}
}

// sddEntryDTO is the JSON wire shape for a single SddEntry. UUID is stored as
// a plain string so the JSONB stays schema-agnostic and readable.
type sddEntryDTO struct {
	ObjectID  string `json:"object_id"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	UpdatedAt string `json:"updated_at"` // RFC 3339
}

// sddSectionsDTO is the full JSONB body: a map from section key string to
// ordered entry slice.
type sddSectionsDTO map[string][]sddEntryDTO

// marshalSections encodes the domain sections map to JSON bytes suitable for
// a JSONB column. A nil or empty map encodes as '{}' (never SQL NULL) to
// satisfy the NOT NULL constraint.
func marshalSections(sections map[domain.SddSectionKey][]domain.SddEntry) ([]byte, error) {
	if len(sections) == 0 {
		return []byte("{}"), nil
	}
	dto := make(sddSectionsDTO, len(sections))
	for k, entries := range sections {
		dtoEntries := make([]sddEntryDTO, 0, len(entries))
		for _, e := range entries {
			dtoEntries = append(dtoEntries, sddEntryDTO{
				ObjectID:  e.ObjectID.String(),
				Title:     e.Title,
				Summary:   e.Summary,
				UpdatedAt: e.UpdatedAt.UTC().Format(time.RFC3339),
			})
		}
		dto[string(k)] = dtoEntries
	}
	return json.Marshal(dto)
}

// unmarshalSections decodes raw JSONB bytes back to a domain sections map.
// Unknown section keys are preserved so a future schema change is forward-
// compatible; known keys keep their SddSectionKey type.
func unmarshalSections(raw []byte) (map[domain.SddSectionKey][]domain.SddEntry, error) {
	if len(raw) == 0 {
		return initSections(), nil
	}
	var dto sddSectionsDTO
	if err := json.Unmarshal(raw, &dto); err != nil {
		return nil, err
	}
	sections := initSections()
	for keyStr, dtoEntries := range dto {
		entries := make([]domain.SddEntry, 0, len(dtoEntries))
		for _, de := range dtoEntries {
			id, err := uuid.Parse(de.ObjectID)
			if err != nil {
				return nil, err
			}
			updatedAt, err := time.Parse(time.RFC3339, de.UpdatedAt)
			if err != nil {
				return nil, err
			}
			entries = append(entries, domain.SddEntry{
				ObjectID:  id,
				Title:     de.Title,
				Summary:   de.Summary,
				UpdatedAt: updatedAt,
			})
		}
		sections[domain.SddSectionKey(keyStr)] = entries
	}
	return sections, nil
}

// initSections returns a map with all four canonical section keys initialised
// to empty slices.
func initSections() map[domain.SddSectionKey][]domain.SddEntry {
	m := make(map[domain.SddSectionKey][]domain.SddEntry, len(domain.SddOrderedSections))
	for _, k := range domain.SddOrderedSections {
		m[k] = []domain.SddEntry{}
	}
	return m
}

// FindByWorkspace loads the SDD document for the given workspace. It returns
// app.ErrNotFound when no row exists.
func (r *sddDocumentRepo) FindByWorkspace(ctx context.Context, workspaceID string) (domain.SddDocument, error) {
	var raw []byte
	var updatedAt time.Time
	err := r.pool.QueryRow(ctx, `
SELECT sections, updated_at
FROM sdd_documents
WHERE workspace_id = $1`, workspaceID).Scan(&raw, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SddDocument{}, app.ErrNotFound
		}
		return domain.SddDocument{}, err
	}
	sections, err := unmarshalSections(raw)
	if err != nil {
		return domain.SddDocument{}, err
	}
	return domain.SddDocument{
		WorkspaceID: workspaceID,
		Sections:    sections,
		UpdatedAt:   updatedAt,
	}, nil
}

// Upsert inserts or replaces the SDD document for doc.WorkspaceID. The entire
// sections map is written as a single JSONB value (Go-side merge, per design
// D9). updated_at is always set to the value in doc (caller controls the
// timestamp).
func (r *sddDocumentRepo) Upsert(ctx context.Context, doc domain.SddDocument) error {
	raw, err := marshalSections(doc.Sections)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
INSERT INTO sdd_documents (workspace_id, sections, updated_at)
VALUES ($1, $2::jsonb, $3)
ON CONFLICT (workspace_id) DO UPDATE
  SET sections   = EXCLUDED.sections,
      updated_at = EXCLUDED.updated_at`,
		doc.WorkspaceID,
		raw,
		doc.UpdatedAt,
	)
	return err
}

// Compile-time interface check.
var _ app.SddDocumentRepository = (*sddDocumentRepo)(nil)
var _ app.SddDocumentRepository = (*txSddDocumentRepo)(nil)

// txSddDocumentRepo is the transactional implementation of
// app.SddDocumentRepository. It is bound to a single pgx.Tx and is
// handed to a WithinSddDocumentTx callback so the entire JSONB
// read-modify-write happens under a row-level lock. The lock is
// acquired by FindByWorkspace (SELECT ... FOR UPDATE) and held until
// the surrounding transaction commits or rolls back.
//
// ErrNotFound semantics are preserved: a missing row returns
// app.ErrNotFound from FindByWorkspace so the service can fall back
// to an empty document and proceed to Upsert (which inserts a fresh
// row via ON CONFLICT). The lock is a no-op on a non-existent row;
// the INSERT inside Upsert is the only statement that touches the
// table after a FOR UPDATE miss, so there is no SQL error against a
// non-existent row.
type txSddDocumentRepo struct {
	tx pgx.Tx
}

// newTxSddDocumentRepo returns a tx-scoped SddDocumentRepository.
// Used exclusively from WithinSddDocumentTx.
func newTxSddDocumentRepo(tx pgx.Tx) *txSddDocumentRepo {
	return &txSddDocumentRepo{tx: tx}
}

// FindByWorkspace locks the row for the given workspace with
// SELECT ... FOR UPDATE and returns the current document. Returns
// app.ErrNotFound when no row exists; the surrounding transaction
// can then Upsert to create the row. The lock is released when the
// transaction commits or rolls back.
func (r *txSddDocumentRepo) FindByWorkspace(ctx context.Context, workspaceID string) (domain.SddDocument, error) {
	var raw []byte
	var updatedAt time.Time
	err := r.tx.QueryRow(ctx, `
SELECT sections, updated_at
FROM sdd_documents
WHERE workspace_id = $1
FOR UPDATE`, workspaceID).Scan(&raw, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SddDocument{}, app.ErrNotFound
		}
		return domain.SddDocument{}, err
	}
	sections, err := unmarshalSections(raw)
	if err != nil {
		return domain.SddDocument{}, err
	}
	return domain.SddDocument{
		WorkspaceID: workspaceID,
		Sections:    sections,
		UpdatedAt:   updatedAt,
	}, nil
}

// Upsert inserts or replaces the SDD document for doc.WorkspaceID
// inside the surrounding transaction. Identical SQL to
// sddDocumentRepo.Upsert; lives here so the tx-bound repo is a
// complete SddDocumentRepository implementation.
func (r *txSddDocumentRepo) Upsert(ctx context.Context, doc domain.SddDocument) error {
	raw, err := marshalSections(doc.Sections)
	if err != nil {
		return err
	}
	_, err = r.tx.Exec(ctx, `
INSERT INTO sdd_documents (workspace_id, sections, updated_at)
VALUES ($1, $2::jsonb, $3)
ON CONFLICT (workspace_id) DO UPDATE
  SET sections   = EXCLUDED.sections,
      updated_at = EXCLUDED.updated_at`,
		doc.WorkspaceID,
		raw,
		doc.UpdatedAt,
	)
	return err
}
