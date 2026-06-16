package migrations

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Run is the API entry point for the schema-lifecycle step at
// process startup. It inspects the database, then dispatches to
// one of three branches:
//
//   - sentinel present + version table empty → baselining
//   - sentinel absent + version table empty → fresh install
//   - version table non-empty (any sentinel) → delta only
//
// It returns an error on any failure; it does NOT call os.Exit
// (the composition root decides). On failure it emits
// slog.Error("migrations failed", ...) with the failing version
// from a *goose.PartialError. fsys is the embedded migrations
// filesystem (see FS) or a synthetic tree in tests.
func Run(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	if pool == nil {
		return errors.New("migrations.Run: pool is nil")
	}
	if fsys == nil {
		return errors.New("migrations.Run: fsys is nil")
	}

	sentinelExists, versionTableEmpty, err := probe(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrations.Run probe: %w", err)
	}

	// Branch 1: baselining. See openspec/specs/db-migrations/spec.md §Baselining.
	if sentinelExists && versionTableEmpty {
		versions, err := collectOnDiskVersions(fsys)
		if err != nil {
			return fmt.Errorf("migrations.Run collect versions: %w", err)
		}
		if err := baseline(ctx, pool, versions); err != nil {
			return fmt.Errorf("migrations.Run baseline: %w", err)
		}
		logBaseline(slog.Default(), len(versions))
		return nil
	}

	if err := applyWithGoose(ctx, pool, fsys); err != nil {
		return err
	}
	return nil
}

// applyWithGoose bridges the *pgxpool.Pool the rest of the project
// uses to the *sql.DB goose requires. The pgx stdlib adapter wraps
// the pool without acquiring connections (MaxIdleConns=0), so
// closing the wrapper only releases database/sql bookkeeping —
// the pool is owned by the caller.
//
// goose.Provider is the v3 API that accepts an fs.FS, so the
// embedded FS passes through unchanged. Dialect is postgres; the
// table name is schema_migrations so the probe and provider agree.
func applyWithGoose(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		sqlDB,
		fsys,
		goose.WithTableName(schemaMigrations),
	)
	if err != nil {
		return fmt.Errorf("migrations.Run new goose provider: %w", err)
	}
	defer func() {
		// Close is a no-op for the underlying pool connection; kept for
		// the Provider's documented lifecycle. Cannot supersede Up error.
		_ = provider.Close()
	}()

	if _, err := provider.Up(ctx); err != nil {
		logMigrationFailure(err)
		return fmt.Errorf("migrations.Run goose up: %w", err)
	}
	return nil
}

// logMigrationFailure emits the structured "migrations failed"
// log the spec mandates. When goose returns a *goose.PartialError,
// the failing version is on Failed.Source; other errors have no
// version but still emit a structured log line.
func logMigrationFailure(err error) {
	logger := slog.Default()
	var pe *goose.PartialError
	if errors.As(err, &pe) && pe.Failed != nil && pe.Failed.Source != nil {
		logger.Error("migrations failed",
			slog.Int64("version", pe.Failed.Source.Version),
			slog.String("err", err.Error()),
		)
		return
	}
	logger.Error("migrations failed",
		slog.String("err", err.Error()),
	)
}

// versionPrefixRE matches the leading integer in a goose-style
// filename like "0001_anything.sql". Goose parses this more
// permissively; the simpler regex keeps baselining independent
// of goose's internal parser.
var versionPrefixRE = regexp.MustCompile(`^(\d+)_`)

// collectOnDiskVersions lists the SQL files in fsys and returns
// their version numbers, sorted ascending. The version is the
// integer prefix of the filename (the part before the first
// underscore) — the same prefix goose uses, so the baselining
// count matches what goose would apply on a fresh run.
//
// Files that do not match NNNN_*.sql are skipped (no error) so
// the FS can carry support files. An FS with zero SQL files is
// an error (embed directive misconfigured; a silent no-op would
// mask the bug).
func collectOnDiskVersions(fsys fs.FS) ([]int64, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	versions := make([]int64, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		match := versionPrefixRE.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		v, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse version from %q: %w", name, err)
		}
		if v <= 0 {
			return nil, fmt.Errorf("invalid version in %q: must be > 0", name)
		}
		versions = append(versions, v)
	}
	if len(versions) == 0 {
		return nil, errors.New("no SQL migrations found in fsys (embed directive misconfigured?)")
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i] < versions[j] })
	return versions, nil
}
