package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	return OpenWithLogger(ctx, dsn, slog.Default())
}

// OpenWithLogger is like Open but lets the caller inject a structured
// logger for connection lifecycle and commit-failure events.
func OpenWithLogger(ctx context.Context, dsn string, logger *slog.Logger) (*DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool, logger: logger}, nil
}

func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool, logger: slog.Default()}
}

func (db *DB) Close() {
	db.pool.Close()
}

// Pool returns the underlying pgx connection pool. The FTS retriever
// (and future vector/structured retrievers) read from a different
// surface than the ingestion UoW, so they need direct pool access
// rather than going through WithinIngestionTx.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}

func (db *DB) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		db.logger.Error("begin tx failed", slog.String("error", err.Error()))
		return err
	}

	repos := newRepositories(tx)
	if err := fn(ctx, repos); err != nil {
		rollbackErr := tx.Rollback(ctx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			db.logger.Error("rollback failed",
				slog.String("tx_error", err.Error()),
				slog.String("rollback_error", rollbackErr.Error()))
			return errors.Join(err, rollbackErr)
		}
		return err
	}

	// Commit errors are part of the transaction outcome. Log them
	// loudly because Fase 3's "Approve" + audit must be atomic — a
	// silent commit failure corrupts the validation state and we'd
	// rather alert than pretend success.
	if commitErr := tx.Commit(ctx); commitErr != nil {
		db.logger.Error("commit failed",
			slog.String("error", commitErr.Error()))
		return commitErr
	}
	return nil
}

func (db *DB) WithinObjectValidationTx(ctx context.Context, fn func(context.Context, app.ObjectValidationRepositories) error) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		db.logger.Error("begin object validation tx failed", slog.String("error", err.Error()))
		return err
	}

	repos := newObjectValidationRepositories(tx)
	if err := fn(ctx, repos); err != nil {
		rollbackErr := tx.Rollback(ctx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			db.logger.Error("object validation rollback failed",
				slog.String("tx_error", err.Error()),
				slog.String("rollback_error", rollbackErr.Error()))
			return errors.Join(err, rollbackErr)
		}
		return err
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		db.logger.Error("object validation commit failed",
			slog.String("error", commitErr.Error()))
		return commitErr
	}
	return nil
}
