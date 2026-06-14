package app

import (
	"context"
	"strings"
)

// ListHumanBacklog returns a workspace-scoped, cursor-paginated
// worklist of knowledge objects needing human attention. The set
// is {proposed, debating, recently-deprecated-within-14d}. Each
// row carries the derived is_stale / stale_for_days markers so
// the UI can flag long-running debates.
//
// The service is a thin orchestration layer over the BacklogQuery
// port: it normalizes the workspace ID, clamps the page size,
// decodes the cursor, and post-processes the page to emit
// NextCursor exactly when more rows exist. The SQL projection
// (and the partial index that backs it) live in the postgres
// implementation.
//
// NextCursor contract: emitted iff the underlying query returned
// at least one more row than the page size (the standard
// "fetch N+1, trim to N, encode the Nth" keyset idiom). On the
// last page NextCursor is the empty string and the caller MUST
// stop paginating.
//
// Error contract: ErrInvalidCursor is returned (and no database
// read is issued) when the supplied cursor does not decode. Other
// errors are returned as-is from the BacklogQuery implementation.
func (s *ObjectDebateService) ListHumanBacklog(ctx context.Context, filter BacklogFilter) (BacklogPage, error) {
	workspaceID := strings.ToLower(strings.TrimSpace(filter.WorkspaceID))
	pageSize := filter.PageSize
	if pageSize <= 0 {
		pageSize = BacklogDefaultPageSize
	}
	if pageSize > BacklogMaxPageSize {
		pageSize = BacklogMaxPageSize
	}

	cursor := strings.TrimSpace(filter.Cursor)
	if cursor != "" {
		// Decode eagerly so a malformed cursor never reaches the
		// SQL planner. DecodeBacklogCursor returns ErrInvalidCursor
		// on every malformed-input branch (non-base64, base64-non-
		// JSON, missing keys, zero UUID) — the codec contract
		// inherited from PR 1.
		if _, _, err := DecodeBacklogCursor(cursor); err != nil {
			return BacklogPage{}, ErrInvalidCursor
		}
	}

	page, err := s.query.List(ctx, BacklogFilter{
		WorkspaceID: workspaceID,
		PageSize:    pageSize,
		Cursor:      cursor,
	})
	if err != nil {
		return BacklogPage{}, err
	}

	// "More rows available" sentinel: if the implementation
	// returned exactly pageSize+1 rows, we have a next page.
	// The implementation in postgres fetches LIMIT pageSize+1 and
	// returns the trimmed slice; a fake in tests is expected to
	// mirror the same shape.
	if len(page.Items) > pageSize {
		page.Items = page.Items[:pageSize]
		last := page.Items[pageSize-1]
		page.NextCursor = EncodeBacklogCursor(last.UpdatedAt, last.ID)
	} else {
		// Ensure NextCursor is empty on the last page even if a
		// future implementation forgets to clear it. The postgres
		// implementation already returns an empty string, but the
		// guarantee belongs to the service, not the implementation.
		page.NextCursor = ""
	}
	return page, nil
}
