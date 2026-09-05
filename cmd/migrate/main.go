// Command migrate applies the schema by hand, through the same code path the
// server uses at startup.
//
// It exists because the alternative people reach for is wrong. Looping over
// internal/migrate/sql/*.sql with psql applies every file every time: it does
// not consult schema_migrations, so running it twice runs each migration
// twice, and it applies the demonstration seed unconditionally, whose
// passwords are published in a public repository.
//
// Usage:
//
//	DATABASE_URL=... go run ./cmd/migrate           apply what is pending
//	DATABASE_URL=... go run ./cmd/migrate -pending  say what would run, run nothing
//	DATABASE_URL=... go run ./cmd/migrate -seed     include the demonstration data
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/migrate"
)

func main() {
	var (
		dryRun = flag.Bool("pending", false, "list what would be applied, and stop")
		seed   = flag.Bool("seed", false, "include the demonstration seed (never in production)")
	)
	flag.Parse()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is not set")
		os.Exit(2)
	}

	if err := run(context.Background(), url, *dryRun, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url string, dryRun, seed bool) error {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		return err
	}

	if dryRun {
		pending, err := migrate.Pending(ctx, db, seed)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("the schema is up to date")
			return nil
		}
		fmt.Printf("%d migration(s) would be applied:\n", len(pending))
		for _, name := range pending {
			fmt.Println("  " + name)
		}
		return nil
	}

	ran, err := migrate.Apply(ctx, db, seed)
	if err != nil {
		return err
	}
	if len(ran) == 0 {
		fmt.Println("the schema is up to date, nothing to apply")
		return nil
	}
	for _, name := range ran {
		fmt.Println("applied " + name)
	}
	return nil
}
