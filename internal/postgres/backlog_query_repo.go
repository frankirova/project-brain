package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Package postgres — backlog_query_repo: implements app.BacklogQuery.
// Moved from repositories.go in change-18 PR3. The backlog is a
// pool-backed read surface (not transactional) — it does not
// participate in any UoW, and exposing it via a top-level
// NewBacklogQuery(pool) factory keeps the service wiring symmetric
// with the other pool-backed read accessors (NewSddDocumentRepo,
// NewFTSRetriever, …).
//
// backlogQuery is the read-side port implementation for the human
// backlog. It is a sibling of relationRepository — same pool-backed,
// not transactional — and a sibling of knowledgeObjectRepository
// (the read+write UoW-bounded surface) only in the sense that they
// read the same table. The backlog query is intentionally NOT
// added to a repositories{} bundle; it is exposed via
// newBacklogQuery(db.Pool()) so the service can be wired with just
// the read path and skip the UoW entirely on the GET.

type backlogQuery struct {
	conn *pgxpool.Pool
}

// newBacklogQuery returns a BacklogQuery backed by the given pool.
// The pool is the same one the write-path UoW uses; the read path
// does not need a dedicated pool because the query is short and
// the partial index idx_knowledge_objects_debating (migration
// 0012) keeps the debating subset O(workspace debating count).
func newBacklogQuery(db *pgxpool.Pool) *backlogQuery {
	return &backlogQuery{conn: db}
}

// NewBacklogQuery is the public composition-root factory. The
// lowercase newBacklogQuery is package-private so the implementation
// stays an internal detail; main.go (and any future caller) gets
// the app.BacklogQuery interface back. The name is singular
// ("BacklogQuery", not "NewBacklogQueryRepository") to match the
// app-layer port name and signal that this is a read-only,
// non-transactional surface.
func NewBacklogQuery(db *pgxpool.Pool) app.BacklogQuery {
	return newBacklogQuery(db)
}

// List runs the backlog query. The implementation:
//
//  1. Trims and lowercases the workspace ID (mirrors the service
//     layer's normalization; the service has already done this
//     but re-applying here keeps the repository safe to call
//     directly from a future HTTP handler without a service
//     round-trip).
//  2. Clamps the effective page size: 0 → BacklogDefaultPageSize,
//     >BacklogMaxPageSize → BacklogMaxPageSize. A value of 0 is
//     "use the default", NOT "return zero rows".
//  3. Decodes the cursor. A malformed cursor returns
//     ErrInvalidCursor and never reaches the SQL planner (this is
//     the same contract the service layer enforces; the
//     repository re-applies it for defense in depth so direct
//     callers of newBacklogQuery get the same guarantee).
//  4. Runs the SQL with LIMIT (pageSize + 1). The "fetch one
//     extra" pattern lets the service emit NextCursor exactly
//     when more rows exist without a second COUNT query.
//  5. Trims the trailing row so the service sees at most
//     pageSize items, with the (updated_at, id) of the last
//     row ready to encode as the next cursor.
//
// The query is served by the partial index
// idx_knowledge_objects_debating for the 'debating' subset; the
// 'proposed' and recent-'deprecated' portions fall back to a
// seqscan that is cheap in practice (per-workspace count is small
// for those subsets).
func (q *backlogQuery) List(ctx context.Context, filter app.BacklogFilter) (app.BacklogPage, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(filter.WorkspaceID))
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = app.BacklogDefaultPageSize
	}
	if pageSize > app.BacklogMaxPageSize {
		pageSize = app.BacklogMaxPageSize
	}

	cursor := strings.TrimSpace(filter.Cursor)
	var (
		cursorUpdatedAt time.Time
		cursorID        uuid.UUID
		hasCursor       bool
	)
	if cursor != "" {
		t, id, err := app.DecodeBacklogCursor(cursor)
		if err != nil {
			return app.BacklogPage{}, err
		}
		cursorUpdatedAt = t
		cursorID = id
		hasCursor = true
	}

	// SQL projection per the spec: filter by workspace + the
	// status mix (proposed OR debating OR recent-deprecated),
	// project the derived is_stale / stale_for_days columns,
	// order by (updated_at DESC, id DESC), and apply the keyset
	// cursor via a row-constructor comparison when present.
	// Fetching LIMIT (pageSize + 1) lets the service emit
	// NextCursor iff more rows exist.
	const backlogListQuery = `
SELECT
  id, workspace_id, type,
  COALESCE(title, '') AS title,
  COALESCE(summary, '') AS summary,
  status, updated_at,
  (status = 'debating' AND updated_at < now() - ($6::int * interval '1 day')) AS is_stale,
  GREATEST(EXTRACT(DAY FROM (now() - updated_at))::int, 0)         AS stale_for_days
FROM knowledge_objects
WHERE workspace_id = $1
  AND (
    status = 'proposed'
    OR status = 'debating'
    OR (status = 'deprecated' AND updated_at >= now() - ($7::int * interval '1 day'))
  )
  AND (
    $2::boolean = false
    OR (updated_at, id) < ($3::timestamptz, $4::uuid)
  )
ORDER BY updated_at DESC, id DESC
LIMIT $5`

	rows, err := q.conn.Query(ctx, backlogListQuery,
		workspaceID,
		hasCursor,
		cursorUpdatedAt,
		cursorID,
		pageSize+1,
		domain.DebateStaleDays,
		domain.BacklogRecentDeprecatedDays,
	)
	if err != nil {
		return app.BacklogPage{}, err
	}
	defer rows.Close()

	items := make([]app.BacklogItem, 0, pageSize+1)
	for rows.Next() {
		var item app.BacklogItem
		if err := rows.Scan(
			&item.ID,
			&item.WorkspaceID,
			&item.Type,
			&item.Title,
			&item.Summary,
			&item.Status,
			&item.UpdatedAt,
			&item.IsStale,
			&item.StaleForDays,
		); err != nil {
			return app.BacklogPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return app.BacklogPage{}, err
	}
	return app.BacklogPage{Items: items, NextCursor: ""}, nil
}

var _ app.BacklogQuery = (*backlogQuery)(nil)
