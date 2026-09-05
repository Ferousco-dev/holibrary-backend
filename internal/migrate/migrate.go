// Package migrate applies the numbered SQL migrations.
//
// Locally, docker compose mounts migrations/ into the Postgres image's
// initdb directory, which runs them once on an empty volume. That mechanism
// does not exist on a hosted database, so production needs something that can
// be run repeatedly, knows what it has already applied, and refuses to guess.
package migrate

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var files embed.FS

// Migration is one numbered file.
type Migration struct {
	Name     string
	SQL      string
	Checksum string
	// Seed marks a file that inserts demonstration data. Seeds are opt-in,
	// because the seeded accounts and their passwords are published in a public
	// repository: applying them to a real deployment would hand anyone who
	// reads the repo an administrator account.
	Seed bool
}

// Load reads the embedded migrations in filename order.
func Load() ([]Migration, error) {
	entries, err := fs.ReadDir(files, "sql")
	if err != nil {
		return nil, err
	}

	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := files.ReadFile("sql/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		out = append(out, Migration{
			Name:     e.Name(),
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
			Seed:     strings.Contains(e.Name(), "seed"),
		})
	}

	// Filenames are zero-padded and numbered, so lexical order is apply order.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

const ledger = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    name        text PRIMARY KEY,
    checksum    text NOT NULL,
    applied_at  timestamptz NOT NULL DEFAULT now()
)`

// Apply brings the database up to date and reports what it did.
//
// Each migration runs inside its own transaction, so a file that fails leaves
// nothing behind and the next attempt starts from a known point. Postgres
// supports transactional DDL, which is what makes that possible.
func Apply(ctx context.Context, db *pgxpool.Pool, includeSeeds bool) ([]string, error) {
	if _, err := db.Exec(ctx, ledger); err != nil {
		return nil, fmt.Errorf("creating the migration ledger: %w", err)
	}

	applied := map[string]string{}
	rows, err := db.Query(ctx, `SELECT name, checksum FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			rows.Close()
			return nil, err
		}
		applied[name] = sum
	}
	rows.Close()

	migrations, err := Load()
	if err != nil {
		return nil, err
	}

	var ran []string
	for _, m := range migrations {
		if sum, done := applied[m.Name]; done {
			// A file that changed after being applied means the database and
			// the repository disagree about history. Editing an applied
			// migration is how two environments silently diverge, so this
			// refuses rather than guessing which is right.
			if sum != m.Checksum {
				return ran, fmt.Errorf(
					"%s was already applied but its contents have changed; "+
						"add a new migration instead of editing an applied one", m.Name)
			}
			continue
		}

		if m.Seed && !includeSeeds {
			slog.Info("skipping demonstration data", "migration", m.Name,
				"reason", "seeds are opt-in; their credentials are public")
			continue
		}

		tx, err := db.Begin(ctx)
		if err != nil {
			return ran, err
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return ran, fmt.Errorf("applying %s: %w", m.Name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (name, checksum) VALUES ($1,$2)`,
			m.Name, m.Checksum); err != nil {
			_ = tx.Rollback(ctx)
			return ran, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ran, err
		}

		ran = append(ran, m.Name)
		slog.Info("migration applied", "migration", m.Name)
	}
	return ran, nil
}
