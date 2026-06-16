package migrations

// Runner tests are end-to-end: each one opens a real Postgres
// pool via PROJECT_BRAIN_TEST_DATABASE_DSN, sets the schema into
// the pre-state required for a probe branch, calls migrations.Run,
// and asserts the post-state. The test mirrors the env-gate
// pattern from internal/postgres/ingestion_integration_test.go
// (Skip when DSN is unset, Skip in `go test -short`).
//
// Each sub-test owns its own setup because the three branches
// share a single DSN and a single schema_migrations table. A
// `reset` helper drops every migration table and the version
// table, then each sub-test builds the pre-state it needs.

import (
	"context"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// allMigrationTables is the set of tables created by the 15
// SQL files plus the version table. The reset helper drops them
// all in one shot. order does not matter; we just need them
// gone so the next sub-test can build its own pre-state.
//
// The vector extension is created by 0007_embeddings.sql and is
// not in this list: extensions survive DROP TABLE.
var allMigrationTables = []string{
	// version table
	"schema_migrations",
	// 0001 knowledge core
	"sources",
	"knowledge_objects",
	"object_sources",
	"audit_events",
	// 0002 relations
	"relations",
	// 0003 / 0006 knowledge_objects FTS (no new table)
	// 0004 / 0005 lifecycle/audit richness (no new table)
	// 0007 embeddings
	"embeddings",
	// 0008 hnsw index (no new table)
	// 0009 telegram pending validations
	"telegram_pending_validations",
	// 0010 embedding jobs
	"embedding_jobs",
	// 0011 raw inputs
	"raw_inputs",
	// 0012 backlog debating index (no new table)
	// 0013 telegram review actions
	"telegram_pending_review_actions",
	// 0014 sdd documents
	"sdd_documents",
	// 0015 confidence check + index (no new table)
}

// TestRunnerProbeDecisionTree is the parent test for the three
// spec branches. It opens one pool (cleaned up automatically) and
// runs each sub-test sequentially. The sub-tests are NOT
// t.Parallel() because they share the DSN and the version table.
func TestRunnerProbeDecisionTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping migrations runner integration test in short mode")
	}
	dsn := os.Getenv("PROJECT_BRAIN_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("PROJECT_BRAIN_TEST_DATABASE_DSN is unset; skipping migrations runner integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open test pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Sanity: the test DSN is reachable. If not, skip rather
	// than fail so a CI without a live DB stays green (matches
	// the existing boot_dsn_test pattern).
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("test DSN unreachable (likely no live DB in this CI): %v", err)
	}

	t.Run("Baseline_SentinelPresent_VersionTableEmpty", func(t *testing.T) {
		testProbeBaseline(ctx, t, pool)
	})
	t.Run("Fresh_SentinelAbsent_VersionTableEmpty", func(t *testing.T) {
		testProbeFresh(ctx, t, pool)
	})
	t.Run("Delta_VersionTableNonEmpty", func(t *testing.T) {
		testProbeDelta(ctx, t, pool)
	})
}

// reset drops every table listed in allMigrationTables. After
// this returns, the database is in the "fresh install" state
// (no sentinel, no version rows) and each sub-test can build
// its own pre-state on top.
func reset(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for _, table := range allMigrationTables {
		// CASCADE drops FK dependencies; IF EXISTS keeps the
		// helper idempotent across re-runs of the same test.
		stmt := "DROP TABLE IF EXISTS " + table + " CASCADE"
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("reset: drop %s: %v", table, err)
		}
	}
}

// countVersionRows returns the number of rows currently in
// schema_migrations. Treats a missing table as zero (the same
// semantic the probe uses). Used to assert that baselining
// recorded N rows and that delta did not add the 15 already-
// applied versions.
func countVersionRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count)
	if err != nil {
		// 42P01 == undefined_table. Treat as zero.
		if isUndefinedTable(err) {
			return 0
		}
		t.Fatalf("count schema_migrations: %v", err)
	}
	return count
}

// columnExists returns true if the named table has a column with
// the given name. Used to assert that the baselining branch did
// NOT re-run migration SQL (a column we added manually survives
// the call) and that the delta branch DID run the 16th migration
// (a column unique to the 16th file is present).
func columnExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, column string) bool {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)`
	var exists bool
	if err := pool.QueryRow(ctx, q, table, column).Scan(&exists); err != nil {
		t.Fatalf("columnExists(%s.%s): %v", table, column, err)
	}
	return exists
}

// tableExists returns true if the named table is present. Used
// to assert that the fresh branch created knowledge_objects and
// that the delta branch created the 16th file's table.
func tableExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&exists)
	if err != nil {
		t.Fatalf("tableExists(%s): %v", table, err)
	}
	return exists
}

// isUndefinedTable reports whether err is a Postgres
// "undefined table" error (SQLSTATE 42P01). The pgx driver
// surfaces these as *pgconn.PgError; we import the sentinel via
// the package's helper constant.
func isUndefinedTable(err error) bool {
	return err != nil && containsSQLState(err, pgxRelationDoesNotExist)
}

// containsSQLState is a tiny helper for the SQLSTATE check; it
// keeps the test file's pgconn import on this single line so the
// dependency surface stays small.
func containsSQLState(err error, sqlState string) bool {
	type sqlStater interface{ SQLState() string }
	for cur := err; cur != nil; {
		if s, ok := cur.(sqlStater); ok && s.SQLState() == sqlState {
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := cur.(unwrapper)
		if !ok {
			return false
		}
		cur = u.Unwrap()
	}
	return false
}

// testProbeBaseline covers the spec §Baselining Existing Databases
// "Sentinel present, no records — baselined" scenario: the
// schema-sentinel (knowledge_objects) is present, the version
// table exists and is empty. After Run:
//   - no migration SQL was executed (a manually-added column on
//     a real migration table is still there);
//   - the version table is populated with the 15 on-disk
//     versions (1..15).
func testProbeBaseline(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	reset(ctx, t, pool)

	// Build the pre-state: knowledge_objects exists (sentinel
	// is present), schema_migrations is empty. Adding a column
	// to sources is the spec's recommended trick to detect
	// re-execution: if the runner applies 0001, the
	// `sources.test_marker` column would be lost (the file
	// has only the original columns).
	if _, err := pool.Exec(ctx, `
		CREATE TABLE knowledge_objects (
			id UUID PRIMARY KEY,
			workspace_id TEXT NOT NULL
		)`); err != nil {
		t.Fatalf("create sentinel: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		CREATE TABLE sources (
			id UUID PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			test_marker TEXT
		)`); err != nil {
		t.Fatalf("create sources with marker: %v", err)
	}
	// The version table must exist (so probe's COUNT(*) returns
	// 0, not a 42P01) but contain zero rows. Pre-creating it
	// mirrors what a "sentinel + version table empty" database
	// looks like: the version table was either left over from a
	// prior gofmt experiment or created ad-hoc.
	if _, err := pool.Exec(ctx, `
		CREATE TABLE schema_migrations (
			id integer PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY,
			version_id bigint NOT NULL,
			is_applied boolean NOT NULL,
			tstamp timestamp NOT NULL DEFAULT now()
		)`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	if err := Run(ctx, pool, FS); err != nil {
		t.Fatalf("Run (baseline): %v", err)
	}

	// The pre-existing test_marker column on `sources` must
	// still be there. If it is gone, the runner applied
	// 0001_knowledge_core_ingestion.sql and overwrote the
	// table.
	if !columnExists(ctx, t, pool, "sources", "test_marker") {
		t.Errorf("sources.test_marker column was dropped: baselining re-ran migration SQL")
	}

	// The version table must be populated with the 15 on-disk
	// versions.
	if got := countVersionRows(ctx, t, pool); got != 15 {
		t.Errorf("schema_migrations row count = %d, want 15 (one per on-disk migration)", got)
	}
}

// testProbeFresh covers the spec §Baselining Existing Databases
// "Sentinel absent, no records — fresh install" and §Idempotent
// Startup scenarios. After Run:
//   - knowledge_objects (sentinel) exists;
//   - the version table has 15 rows;
//   - running Run a second time is a no-op (idempotent).
func testProbeFresh(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	reset(ctx, t, pool)

	// Pre-state: empty DB. No knowledge_objects, no
	// schema_migrations.
	if tableExists(ctx, t, pool, "knowledge_objects") {
		t.Fatal("precondition: knowledge_objects must not exist before Run (fresh install branch)")
	}
	if got := countVersionRows(ctx, t, pool); got != 0 {
		t.Fatalf("precondition: schema_migrations count = %d, want 0", got)
	}

	if err := Run(ctx, pool, FS); err != nil {
		t.Fatalf("Run (fresh): %v", err)
	}

	if !tableExists(ctx, t, pool, "knowledge_objects") {
		t.Errorf("knowledge_objects still missing after Run (fresh install did not execute SQL)")
	}
	if got := countVersionRows(ctx, t, pool); got != 15 {
		t.Errorf("schema_migrations row count = %d, want 15", got)
	}

	// Idempotent: a second Run on the same DB is a no-op
	// (version table is non-empty, no SQL runs).
	if err := Run(ctx, pool, FS); err != nil {
		t.Fatalf("Run (fresh, second call): %v", err)
	}
	if got := countVersionRows(ctx, t, pool); got != 15 {
		t.Errorf("second Run changed schema_migrations row count: got %d, want 15", got)
	}
}

// testProbeDelta covers the spec §Delta-Only Updates After
// Baseline "New migration applied on next start" and "Versions
// at or below max are skipped" scenarios. We synthesize a 16th
// file (test-only, not committed), point Run at a synthetic FS
// that includes it, and assert:
//   - the 16th table exists;
//   - the 15th-version tables are unchanged (no re-application);
//   - schema_migrations has 16 rows.
func testProbeDelta(ctx context.Context, t *testing.T, pool *pgxpool.Pool) {
	reset(ctx, t, pool)

	// Establish the pre-state: a database at version 15. We do
	// this by running the embedded FS once (the fresh branch
	// exercises it from zero). The post-state of that run is
	// the "delta starts from here" baseline.
	if err := Run(ctx, pool, FS); err != nil {
		t.Fatalf("Run (fresh setup): %v", err)
	}
	if got := countVersionRows(ctx, t, pool); got != 15 {
		t.Fatalf("setup precondition: schema_migrations = %d, want 15", got)
	}

	// Synthesize a 16th SQL file that creates a table unique
	// to this test. The table's name embeds a recognizable
	// token so a reviewer can see at a glance which migration
	// created it.
	const deltaTable = "delta_only_test_table_16"
	const deltaSQL = "0016_delta_test.sql"
	const deltaBody = `CREATE TABLE ` + deltaTable + ` (
		id UUID PRIMARY KEY,
		note TEXT NOT NULL
	);
	`
	synthetic := fstest.MapFS{
		deltaSQL: &fstest.MapFile{Data: []byte(deltaBody)},
	}
	// Merge the embedded FS with the synthetic 16th file. The
	// merger keeps the original 15 files and adds the 16th.
	merged := mergeFS(FS, synthetic)

	if err := Run(ctx, pool, merged); err != nil {
		t.Fatalf("Run (delta): %v", err)
	}

	// The 16th-version table must exist (proves the delta ran).
	if !tableExists(ctx, t, pool, deltaTable) {
		t.Errorf("%s missing after delta Run: 16th file did not execute", deltaTable)
	}
	// schema_migrations must now have 16 rows (versions 1..16).
	if got := countVersionRows(ctx, t, pool); got != 16 {
		t.Errorf("schema_migrations row count = %d, want 16", got)
	}

	// 0001's knowledge_objects table must still be present and
	// untouched (no re-application of the 15 already-applied
	// migrations). columnExists also returns true for the
	// table, so the assertion is "knowledge_objects exists AND
	// still has the original columns".
	if !tableExists(ctx, t, pool, "knowledge_objects") {
		t.Errorf("knowledge_objects missing after delta Run: existing tables were dropped")
	}
	if !columnExists(ctx, t, pool, "knowledge_objects", "content") {
		t.Errorf("knowledge_objects.content missing after delta Run: 0001 re-applied unexpectedly")
	}
}

// mergeFS returns an fs.FS that contains every file from base
// plus every file from overlay. overlay entries win on collision.
// Used by testProbeDelta to add a synthetic 16th file on top of
// the embedded FS without writing anything to disk.
func mergeFS(base, overlay fs.FS) fs.FS {
	merged := fstest.MapFS{}
	if base != nil {
		if err := fs.WalkDir(base, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(base, path)
			if err != nil {
				return err
			}
			merged[path] = &fstest.MapFile{Data: data}
			return nil
		}); err != nil {
			panic(err) // WalkDir over a real FS in tests; panic is the right signal.
		}
	}
	if overlay != nil {
		if err := fs.WalkDir(overlay, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(overlay, path)
			if err != nil {
				return err
			}
			merged[path] = &fstest.MapFile{Data: data}
			return nil
		}); err != nil {
			panic(err)
		}
	}
	return merged
}
