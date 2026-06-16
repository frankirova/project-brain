package migrations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaMigrations is the version-tracking table name. Fixed at
// compile time so probe and goose.Provider agree. See design §Decision 4.
const schemaMigrations = "schema_migrations"

// sentinelTable is the schema-sentinel for baselining. Created
// first by 0001_*.sql, so its presence = bootstrapped DB.
const sentinelTable = "knowledge_objects"

// pgxRelationDoesNotExist is the SQLSTATE for "relation does not
// exist"; probe uses it to tell an empty version table from a missing one.
const pgxRelationDoesNotExist = "42P01"

// probe inspects the database to decide which of the three
// startup branches the runner should take:
//
//	sentinel present + version table empty  → baselining
//	sentinel absent  + version table empty  → fresh install
//	version table non-empty                 → delta only
//
// Both "table does not exist" and "table has zero rows" are
// reported as empty. Two booleans (not an enum) keep probe
// independent of runner branching.
func probe(ctx context.Context, pool *pgxpool.Pool) (sentinelExists, versionTableEmpty bool, err error) {
	const sentinelQuery = "SELECT to_regclass($1) IS NOT NULL"
	var sentinel bool
	if err := pool.QueryRow(ctx, sentinelQuery, sentinelTable).Scan(&sentinel); err != nil {
		return false, false, fmt.Errorf("probe sentinel %q: %w", sentinelTable, err)
	}

	const countQuery = "SELECT count(*) FROM " + schemaMigrations
	var count int
	err = pool.QueryRow(ctx, countQuery).Scan(&count)
	if err == nil {
		return sentinel, count == 0, nil
	}
	// 42P01 = version table not created yet (fresh-install path).
	// Return sentinelExists with versionTableEmpty=true; the runner
	// branches on sentinelExists.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgxRelationDoesNotExist {
		return sentinel, true, nil
	}
	return false, false, fmt.Errorf("probe version table %q: %w", schemaMigrations, err)
}

// baseline writes one row per supplied version into
// schema_migrations inside a single transaction. It is the
// "schema was created by the old initdb hook" branch: the runner
// detects the sentinel is present but the version table is empty
// and calls baseline to INSERT every on-disk version, skipping
// the SQL files.
//
// The transaction is mandatory: a half-baselined DB would leave
// goose.Up with a partial view (e.g. version 5 recorded, 15
// missing) and mis-apply 6–15. baseline does NOT create
// schema_migrations — the probe confirmed it exists; a missing
// table here is corruption and surfaces as an INSERT error.
func baseline(ctx context.Context, pool *pgxpool.Pool, versions []int64) error {
	if len(versions) == 0 {
		// Refuse the no-op: a misconfigured call should be visible, not silently logged.
		return errors.New("baseline called with zero versions")
	}
	const insertSQL = "INSERT INTO " + schemaMigrations + " (version_id, is_applied) VALUES ($1, true)"

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("baseline begin tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	for _, v := range versions {
		if _, err := tx.Exec(ctx, insertSQL, v); err != nil {
			return fmt.Errorf("baseline insert version %d: %w", v, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("baseline commit: %w", err)
	}
	return nil
}

// logBaseline records a structured "migrations baselined" log
// entry; the log shape lives here, not in the runner. See
// openspec/specs/db-migrations/spec.md §Baselining Existing.
func logBaseline(logger *slog.Logger, count int) {
	logger.Info("migrations baselined", slog.Int("count", count))
}
