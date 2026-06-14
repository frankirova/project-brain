package postgres

import (
	"database/sql"
	"encoding/json"

	"github.com/frankirova/project-brain/internal/domain"
	"github.com/google/uuid"
)

// Package postgres — repo_helpers: shared SQL encoding helpers for the
// per-table repositories. Moved from repositories.go in change-18 PR3
// because they are consumed by 2+ per-table files (source, knowledge_objects,
// audit_event, object_relations, raw_input_repo). Keeping them in one place
// avoids the per-file duplication of the same nil-to-default mapping rules.

// marshalMetadata encodes Metadata for a JSONB column. A nil OR empty
// map becomes '{}'. The sources and knowledge_objects metadata columns
// are NOT NULL DEFAULT '{}'; writing an explicit SQL NULL violates the
// constraint (the DEFAULT only applies when the column is omitted from
// the INSERT, not when NULL is passed). So nil must serialize to an
// empty JSON object, not NULL — otherwise a metadata-less ingest fails
// with a 23502 not-null violation.
func marshalMetadata(metadata domain.Metadata) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

// nullableString maps empty optional create fields to SQL NULL.
func nullableString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

// nullableUUID returns the uuid pointer as-is; pgx maps a nil *uuid.UUID to
// SQL NULL. A non-nil pointer is passed through unchanged.
func nullableUUID(value *uuid.UUID) *uuid.UUID {
	return value
}

// nullableFloat64 returns the pointer as-is; pgx maps a nil *float64 to SQL
// NULL. A non-nil pointer is passed through unchanged.
func nullableFloat64(value *float64) *float64 {
	return value
}

// nullableInt returns the pointer as-is; pgx maps a nil *int to SQL NULL.
// A non-nil pointer is passed through unchanged.
func nullableInt(value *int) *int {
	return value
}
