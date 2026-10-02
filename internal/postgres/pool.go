package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

func OpenPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Omit the underlying error because it may contain credentials.
		return nil, errors.New("invalid database URL")
	}
	cfg.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("open database pool failed")
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		// Connection errors may include credentials; expose only the failed operation.
		return nil, errors.New("database unavailable")
	}
	return pool, nil
}
