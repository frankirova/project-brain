package postgres

import (
	"context"
	"errors"

	"github.com/frankirova/project-brain/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &DB{pool: pool}, nil
}

func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

func (db *DB) Close() {
	db.pool.Close()
}

func (db *DB) WithinIngestionTx(ctx context.Context, fn func(context.Context, app.IngestionRepositories) error) error {
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	repos := newRepositories(tx)
	if err := fn(ctx, repos); err != nil {
		rollbackErr := tx.Rollback(ctx)
		if rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}

	// Commit errors are part of the transaction outcome and are returned to callers.
	return tx.Commit(ctx)
}
