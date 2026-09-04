#!/usr/bin/env bash
# Removes the bootstrap administrator so it can be created again.
#
# The bootstrap password is shown once and stored only as an Argon2 hash, so a
# lost one cannot be recovered -- which is the point. This deletes the account
# and lets you bootstrap a fresh one.
#
# It refuses if the library has real data, because deleting an administrator
# from a system in use is a different and much worse operation than resetting a
# deployment nobody has used yet.
set -euo pipefail

cd "$(dirname "$0")/.."
export DATABASE_URL="${DATABASE_URL:-$(cat ~/Desktop/SCHOOL/secrets/holibrary-neon-url.txt)}"

cat > /tmp/reset_admin_main.go <<'GO'
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer db.Close()

	var members, loans int
	if err := db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users WHERE role = 'member'),
		       (SELECT count(*) FROM loans)`).Scan(&members, &loans); err != nil {
		fmt.Println("query:", err)
		os.Exit(1)
	}

	// A deployment with members and loans is one somebody is using. Resetting
	// the administrator there is a support matter, not a setup step.
	if members > 0 || loans > 0 {
		fmt.Printf("refusing: this deployment has %d member(s) and %d loan(s).\n", members, loans)
		fmt.Println("Reset a forgotten password through the API instead, or ask another admin.")
		os.Exit(1)
	}

	tag, err := db.Exec(ctx, `DELETE FROM users WHERE role = 'admin'`)
	if err != nil {
		fmt.Println("delete:", err)
		os.Exit(1)
	}
	fmt.Printf("removed %d administrator account(s); bootstrap can run again\n", tag.RowsAffected())
}
GO
mkdir -p cmd/resetadmin
cp /tmp/reset_admin_main.go cmd/resetadmin/main.go
go run ./cmd/resetadmin
rm -rf cmd/resetadmin
