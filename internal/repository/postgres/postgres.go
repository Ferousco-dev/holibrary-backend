// Package postgres implements the repository interfaces against PostgreSQL.
//
// Everything in this package is SQL and row scanning. Business rules live in
// the service layer, and HTTP concerns live in the transport layer; the point of
// keeping them apart is that the rules can be tested without a database
// (docs/design.md DES-003).
//
// Every query is parameterised. There is no string-built SQL anywhere in this
// package, which is what makes SQL injection structurally impossible rather
// than merely unlikely (NFR-008).
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// Postgres error codes we act on. Reacting to the code rather than the message
// keeps behaviour stable across driver and server versions.
const (
	codeUniqueViolation     = "23505"
	codeForeignKeyViolation = "23503"
)

// Connect opens a pooled connection and verifies it is actually reachable.
//
// Ping matters here: a pool constructor that never contacts the server would
// turn a bad DATABASE_URL into a runtime failure on the first request instead
// of a startup failure the operator can see.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parsing DATABASE_URL: %w", err)
	}

	// Free-tier Postgres allows few connections, so the pool is kept small on
	// purpose; exhausting the server's limit is worse than queueing here.
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	return pool, nil
}

// isUniqueViolation reports whether err is a duplicate-key error, optionally for
// one named constraint.
func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != codeUniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// translate converts driver errors into domain errors so that callers above
// this layer never need to know which database is underneath.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrNotFound
	case isUniqueViolation(err, ""):
		return domain.ErrConflict
	default:
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == codeForeignKeyViolation {
			return domain.ErrConflict
		}
		return err
	}
}
