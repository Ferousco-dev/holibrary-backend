// Command bootstrap creates the first administrator on a fresh deployment.
//
// A production database is deliberately empty: the demonstration seed is
// skipped because its passwords are published in a public repository. That
// leaves nobody able to sign in, which is the correct starting point and also a
// problem this command exists to solve exactly once.
//
// It generates the password rather than accepting one, and prints it a single
// time. Nothing is stored in readable form, nothing is emailed, and nothing is
// written to a log: the operator copies it, signs in, and changes it. The
// account is created with the first-login password change already required, so
// even the printed value stops working the moment it is used.
//
// Usage:
//
//	DATABASE_URL=... go run ./cmd/bootstrap -email you@oauife.edu.ng -name "Your Name"
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
)

func main() {
	var (
		email      = flag.String("email", "", "the administrator's email address")
		name       = flag.String("name", "", "their full name")
		identifier = flag.String("id", "", "staff number (defaults to LIB/ADMIN/001)")
	)
	flag.Parse()

	if *email == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "usage: bootstrap -email <address> -name <full name> [-id <staff number>]")
		os.Exit(2)
	}
	if *identifier == "" {
		*identifier = "LIB/ADMIN/001"
	}

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is required")
		os.Exit(2)
	}

	if err := run(context.Background(), url, *email, *name, *identifier); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, url, email, name, identifier string) error {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Ping(ctx); err != nil {
		return fmt.Errorf("database unreachable: %w", err)
	}

	// This runs once. A second administrator is created through the API by the
	// first, which leaves an audit trail; this command does not, because there
	// is nobody to attribute it to yet.
	var existing int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&existing); err != nil {
		return fmt.Errorf("checking for an existing administrator: %w", err)
	}
	if existing > 0 {
		return errors.New(
			"an administrator already exists; create further staff through the API, " +
				"where the action is attributable and audited")
	}

	// Generated, not chosen. A password an operator picks under time pressure
	// is a password they will reuse.
	raw, _, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	password := raw[:16]

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	_, err = db.Exec(ctx, `
		INSERT INTO users (identifier, email, full_name, password_hash, role,
		                   status, must_change_password)
		VALUES ($1, $2, $3, $4, 'admin', 'active', true)`,
		strings.TrimSpace(identifier), strings.ToLower(strings.TrimSpace(email)),
		strings.TrimSpace(name), hash)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			return errors.New("that email or staff number is already registered")
		}
		return err
	}

	// Printed to stdout once. Not logged, not stored, not recoverable: if this
	// is lost the account must be bootstrapped again against an empty database.
	fmt.Println()
	fmt.Println("  Administrator created.")
	fmt.Println()
	fmt.Printf("    sign in with   %s\n", email)
	fmt.Printf("    password       %s\n", password)
	fmt.Println()
	fmt.Println("  This is shown once and is not stored anywhere in readable form.")
	fmt.Println("  You will be required to change it at first sign-in, and every")
	fmt.Println("  other route is refused until you do.")
	fmt.Println()
	return nil
}

var _ = pgx.ErrNoRows
