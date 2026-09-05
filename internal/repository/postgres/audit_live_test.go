//go:build livedb

// Exercises every path that is supposed to leave an audit line, against a real
// Postgres, and asserts that it did. Run with:
//
//	DATABASE_URL=... go test -tags livedb ./internal/repository/postgres -run TestAudit -v
package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

func TestAuditTrailCoversStaffActions(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var staffID uuid.UUID
	if err := db.QueryRow(ctx,
		`SELECT id FROM users WHERE role <> 'member' LIMIT 1`).Scan(&staffID); err != nil {
		t.Fatal("no staff account to act as:", err)
	}

	catalogue := NewCatalogueRepo(db)
	users := NewUserRepo(db)
	circulation := NewCirculationRepo(db)

	before := count(t, db)

	book, err := catalogue.CreateBook(ctx, CreateBookParams{
		StaffID: staffID, Title: "Audit Trail Test Title", CallNumber: "QA 999",
	})
	if err != nil {
		t.Fatal("CreateBook:", err)
	}
	copyA, err := catalogue.AddCopy(ctx, book.ID, "HOL-AUDIT-1", domain.PolicyCirculating, staffID)
	if err != nil {
		t.Fatal("AddCopy:", err)
	}
	// A second copy, so the retention rule does not refuse the loan below.
	if _, err := catalogue.AddCopy(ctx, book.ID, "HOL-AUDIT-2", domain.PolicyCirculating, staffID); err != nil {
		t.Fatal("AddCopy 2:", err)
	}

	member, err := users.Create(ctx, CreateUserParams{
		CreatedBy: staffID, Identifier: "AUD/" + uuid.NewString()[:8],
		Email: uuid.NewString()[:8] + "@audit.invalid", FullName: "Audit Test Member",
		PasswordHash: "x", Role: domain.RoleMember,
		Category: ptr(domain.CategoryUndergraduate),
	})
	if err != nil {
		t.Fatal("Create member:", err)
	}

	loan, err := circulation.Borrow(ctx, BorrowParams{
		CopyID: copyA.ID, UserID: member.ID, IssuedBy: staffID,
		BorrowedAt: time.Now().UTC(), DueAt: time.Now().UTC().AddDate(0, 0, 14), MaxLoans: 5,
	})
	if err != nil {
		t.Fatal("Borrow:", err)
	}
	if _, err := circulation.Return(ctx, loan.ID, staffID); err != nil {
		t.Fatal("Return:", err)
	}

	policy := domain.PolicyReferenceOnly
	if err := catalogue.UpdateCopy(ctx, copyA.ID, &policy, nil, staffID); err != nil {
		t.Fatal("UpdateCopy:", err)
	}
	if err := users.UpdateStatus(ctx, member.ID, domain.UserSuspended, staffID); err != nil {
		t.Fatal("UpdateStatus:", err)
	}
	if err := catalogue.ArchiveBook(ctx, book.ID, staffID); err != nil {
		t.Fatal("ArchiveBook:", err)
	}

	want := []string{
		"BOOK_CREATED", "COPY_ADDED", "MEMBER_CREATED", "LOAN_ISSUED",
		"LOAN_RETURNED", "COPY_UPDATED", "MEMBER_STATUS_CHANGED", "BOOK_ARCHIVED",
	}
	for _, action := range want {
		var n int
		if err := db.QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = $1 AND actor_id = $2`,
			action, staffID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("no audit line for %s", action)
		} else {
			t.Logf("%-22s %d line(s), attributed", action, n)
		}
	}

	t.Logf("audit_log grew from %d to %d rows", before, count(t, db))
}

// The reason recordAudit takes the transaction and not the pool: a change that
// does not happen must not be written down as though it did.
func TestAuditLineRollsBackWithItsChange(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var staffID, memberID, copyID uuid.UUID
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE role <> 'member' LIMIT 1`).Scan(&staffID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT id FROM users WHERE role = 'member' LIMIT 1`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx,
		`SELECT copy_id FROM loans WHERE returned_at IS NULL LIMIT 1`).Scan(&copyID); err != nil {
		t.Skip("no copy is currently out, so there is no doomed borrow to attempt")
	}

	before := count(t, db)

	// This copy is already on loan, so the conditional UPDATE claims no row and
	// the transaction rolls back after the audit line has been written to it.
	_, err = NewCirculationRepo(db).Borrow(ctx, BorrowParams{
		CopyID: copyID, UserID: memberID, IssuedBy: staffID,
		BorrowedAt: time.Now().UTC(), DueAt: time.Now().UTC().AddDate(0, 0, 14), MaxLoans: 5,
	})
	if err == nil {
		t.Fatal("expected the borrow to be refused, it succeeded")
	}
	t.Logf("borrow refused as expected: %v", err)

	if after := count(t, db); after != before {
		t.Errorf("a refused borrow wrote %d audit line(s); it must write none", after-before)
	} else {
		t.Logf("audit_log still %d rows: nothing was claimed that did not happen", after)
	}
}

func count(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func ptr[T any](v T) *T { return &v }
