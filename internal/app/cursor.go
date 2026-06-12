package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidCursor is returned by DecodeBacklogCursor when the
// supplied token is not a valid opaque backlog cursor. Callers MUST
// treat this as a client error and surface it to the user; they MUST
// NOT silently fall back to the first page, because that would
// discard whatever forward progress the previous response encoded.
var ErrInvalidCursor = errors.New("invalid backlog cursor")

// cursorJSONTemplate is the on-the-wire shape of the opaque backlog
// cursor. UpdatedAt is rendered as RFC3339Nano (UTC) so round-tripping
// through the database's timestamptz column is lossless. The id is
// rendered as the canonical 36-char hyphenated UUID form.
const cursorJSONTemplate = `{"updated_at":"%s","id":"%s"}`

// EncodeBacklogCursor returns the opaque URL-safe base64 (RFC 4648
// §5, no padding) of JSON {"updated_at", "id"}. The result is the
// NextCursor value a caller passes back on the next page request.
// The encoded token is safe to embed in URL query strings.
func EncodeBacklogCursor(t time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf(cursorJSONTemplate, t.UTC().Format(time.RFC3339Nano), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeBacklogCursor parses an opaque backlog cursor previously
// produced by EncodeBacklogCursor. It returns ErrInvalidCursor on:
//
//   - non-base64 input (RFC 4648 §5 URL-safe, no padding)
//   - valid base64 that is not JSON
//   - JSON missing `updated_at` or `id`
//   - JSON with id equal to uuid.Nil (rejects hand-crafted zero-UUID
//     tokens that would otherwise map to a keyset row that does not
//     exist)
//
// The fields are unmarshalled as pointers so a missing JSON key is
// distinguishable from a present key whose value happens to be the
// zero value — the spec requires rejection on missing, not on zero.
//
// On error the returned time.Time is the zero value and the returned
// uuid.UUID is uuid.Nil; callers can safely use them in a `WHERE
// (updated_at, id) < (...)` expression only after checking the
// error first.
func DecodeBacklogCursor(token string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	var c struct {
		UpdatedAt *time.Time `json:"updated_at"`
		ID        *uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	if c.UpdatedAt == nil || c.ID == nil || *c.ID == uuid.Nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return *c.UpdatedAt, *c.ID, nil
}
