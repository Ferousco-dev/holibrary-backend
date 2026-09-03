package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

type CirculationRepo struct{ db *pgxpool.Pool }

func NewCirculationRepo(db *pgxpool.Pool) *CirculationRepo { return &CirculationRepo{db: db} }

// BorrowParams is one custody event: this member, this physical copy, this desk.
type BorrowParams struct {
	CopyID     uuid.UUID
	UserID     uuid.UUID
	IssuedBy   uuid.UUID
	BorrowedAt time.Time
	DueAt      time.Time
	MaxLoans   int
}

// Borrow records that a physical copy has left the building with a member.
//
// This is the most safety-critical function in the system. Two librarians can
// issue the last copy of a title in the same instant, and a naive
// read-then-write would lend one physical book to two people.
//
// Three things prevent that, and they are deliberately layered so that a bug in
// any one of them is caught by the next:
//
//  1. The copy is claimed with a single atomic UPDATE whose WHERE clause carries
//     the availability check. There is no window between checking and writing
//     because there is no separate check. The loser of the race updates zero
//     rows and is told the copy is gone.
//
//  2. The borrowing limit is counted inside the same transaction after the copy
//     row is claimed, so a member firing concurrent requests cannot slip past
//     their category limit.
//
//  3. The last-copy retention policy is applied in the same transaction, so two
//     librarians cannot each believe they are taking the second-to-last copy.
//
//  4. A partial unique index on loans(copy_id) WHERE returned_at IS NULL means
//     Postgres physically refuses to store a second open loan for a copy, even
//     if every line of Go above were wrong.
//
// REQ-041..047, NFR-009.
func (r *CirculationRepo) Borrow(ctx context.Context, p BorrowParams) (domain.Loan, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Loan{}, translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	// Step 1: claim the copy. The status change and the availability test are
	// one statement, so no other transaction can act between them.
	var claimed uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE copies
		   SET status = 'on_loan', updated_at = now()
		 WHERE id = $1
		   AND status = 'available'
		   AND loan_policy = 'circulating'
		RETURNING id`, p.CopyID).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		// Zero rows means the copy was not available, or does not circulate, or
		// does not exist. Distinguish those so the librarian gets a useful message.
		return domain.Loan{}, r.explainUnclaimableCopy(ctx, p.CopyID)
	}
	if err != nil {
		return domain.Loan{}, translate(err)
	}

	// Step 2: the last-copy retention policy (DEC-018).
	//
	// Counted after the claim and inside the same transaction, so two librarians
	// lending the second-to-last and last copy at the same instant cannot both
	// pass the check. Rolling back returns the claimed copy to the shelf.
	var stock, availableAfter int
	err = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE loan_policy = 'circulating'
		                          AND status IN ('available','on_loan')),
		       count(*) FILTER (WHERE loan_policy = 'circulating'
		                          AND status = 'available')
		  FROM copies
		 WHERE book_id = (SELECT book_id FROM copies WHERE id = $1)`, p.CopyID).
		Scan(&stock, &availableAfter)
	if err != nil {
		return domain.Loan{}, translate(err)
	}
	// A title held in two or more copies keeps one on the shelf. A single-copy
	// title circulates: retaining it would mean nobody could ever read it.
	if stock >= 2 && availableAfter == 0 {
		return domain.Loan{}, domain.ErrLastCopyRetained
	}

	// Step 3: the member's limit, counted inside the transaction.
	active, err := CountActiveLoans(ctx, tx, p.UserID)
	if err != nil {
		return domain.Loan{}, err
	}
	if active >= p.MaxLoans {
		// Rolling back returns the copy to 'available' as though nothing happened.
		return domain.Loan{}, domain.ErrLoanLimitReached
	}

	// Step 4: write the loan. The partial unique index is the final guarantee.
	var l domain.Loan
	// borrowed_at is written explicitly rather than defaulted to the database's
	// now(). The due date was computed against the application's clock, and
	// letting the two differ made a 14-day loan report as 13 days. One clock,
	// one pair of timestamps. DEF-003.
	err = tx.QueryRow(ctx, `
		INSERT INTO loans (copy_id, user_id, borrowed_at, due_at, issued_by)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, copy_id, user_id, borrowed_at, due_at, returned_at, issued_by`,
		p.CopyID, p.UserID, p.BorrowedAt, p.DueAt, p.IssuedBy).Scan(
		&l.ID, &l.CopyID, &l.UserID, &l.BorrowedAt, &l.DueAt, &l.ReturnedAt, &l.IssuedBy)
	if err != nil {
		if isUniqueViolation(err, "one_active_loan_per_copy") {
			// Reaching here means the compare-and-swap was bypassed somehow. The
			// database refused the bad state, which is exactly its job.
			return domain.Loan{}, domain.ErrCopyNotAvailable
		}
		return domain.Loan{}, translate(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Loan{}, translate(err)
	}
	return l, nil
}

// explainUnclaimableCopy turns a failed claim into the specific reason, so the
// desk is told "that copy is already out" rather than a bare failure.
func (r *CirculationRepo) explainUnclaimableCopy(ctx context.Context, copyID uuid.UUID) error {
	var status domain.CopyStatus
	var policy domain.LoanPolicy
	err := r.db.QueryRow(ctx,
		`SELECT status, loan_policy FROM copies WHERE id = $1`, copyID).Scan(&status, &policy)
	if err != nil {
		return translate(err) // ErrNotFound when the copy does not exist
	}
	if !policy.IsBorrowable() {
		// Reference works and display items stay in the building (DOM-004).
		return domain.ErrCopyNotBorrowable
	}
	return domain.ErrCopyNotAvailable
}

// Return records a copy coming back to the shelf.
//
// The loan is closed rather than deleted, because the library must still be able
// to say who held this copy last session (DOM-008, REQ-064). The WHERE clause
// requires returned_at to still be NULL, so returning twice is impossible even
// under concurrent requests.
func (r *CirculationRepo) Return(ctx context.Context, loanID, staffID uuid.UUID) (domain.Loan, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Loan{}, translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var l domain.Loan
	err = tx.QueryRow(ctx, `
		UPDATE loans
		   SET returned_at = now(), returned_to = $2
		 WHERE id = $1 AND returned_at IS NULL
		RETURNING id, copy_id, user_id, borrowed_at, due_at, returned_at, issued_by`,
		loanID, staffID).Scan(&l.ID, &l.CopyID, &l.UserID, &l.BorrowedAt,
		&l.DueAt, &l.ReturnedAt, &l.IssuedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Loan{}, r.explainUnreturnableLoan(ctx, loanID)
	}
	if err != nil {
		return domain.Loan{}, translate(err)
	}

	// The physical copy becomes available again (REQ-050).
	if _, err := tx.Exec(ctx, `
		UPDATE copies SET status = 'available', updated_at = now()
		 WHERE id = $1 AND status = 'on_loan'`, l.CopyID); err != nil {
		return domain.Loan{}, translate(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Loan{}, translate(err)
	}
	return l, nil
}

func (r *CirculationRepo) explainUnreturnableLoan(ctx context.Context, loanID uuid.UUID) error {
	var exists bool
	if err := r.db.QueryRow(ctx,
		`SELECT true FROM loans WHERE id = $1`, loanID).Scan(&exists); err != nil {
		return translate(err)
	}
	return domain.ErrLoanAlreadyClosed
}

// Split for the same reason as bookFields/bookFrom: the pagination count has to
// join the select list, not follow the FROM clause. DEF-002.
const loanFields = `
	SELECT l.id, l.copy_id, l.user_id, l.borrowed_at, l.due_at, l.returned_at,
	       l.issued_by, b.title, c.accession_number, u.full_name`

const loanFrom = `
	  FROM loans l
	  JOIN copies c ON c.id = l.copy_id
	  JOIN books  b ON b.id = c.book_id
	  JOIN users  u ON u.id = l.user_id`

const loanSelect = loanFields + loanFrom

func scanLoans(rows pgx.Rows) ([]domain.Loan, error) {
	defer rows.Close()
	var loans []domain.Loan
	for rows.Next() {
		var l domain.Loan
		if err := rows.Scan(&l.ID, &l.CopyID, &l.UserID, &l.BorrowedAt, &l.DueAt,
			&l.ReturnedAt, &l.IssuedBy, &l.BookTitle, &l.AccessionNumber,
			&l.MemberName); err != nil {
			return nil, err
		}
		loans = append(loans, l)
	}
	return loans, rows.Err()
}

// LoansForUser returns a member's loans. openOnly limits the result to books
// they still hold (REQ-060); otherwise it is their full history (REQ-061).
func (r *CirculationRepo) LoansForUser(ctx context.Context, userID uuid.UUID, openOnly bool) ([]domain.Loan, error) {
	q := loanSelect + `
	 WHERE l.user_id = $1 AND (NOT $2 OR l.returned_at IS NULL)
	 ORDER BY l.borrowed_at DESC, l.id`

	rows, err := r.db.Query(ctx, q, userID, openOnly)
	if err != nil {
		return nil, translate(err)
	}
	loans, err := scanLoans(rows)
	return loans, translate(err)
}

// ListLoans serves the librarian's circulation view.
//
// Overdue is expressed as a predicate over due_at and returned_at rather than
// read from a column. There is no is_overdue field to fall out of date, which is
// the whole point (REQ-052, REQ-053).
func (r *CirculationRepo) ListLoans(ctx context.Context, overdueOnly, openOnly bool, limit, offset int) ([]domain.Loan, int, error) {
	q := loanFields + `, count(*) OVER() AS total` + loanFrom + `
	 WHERE (NOT $1 OR (l.returned_at IS NULL AND l.due_at < now()))
	   AND (NOT $2 OR l.returned_at IS NULL)
	 -- l.id breaks ties so paging through loans cannot repeat or skip a row
	 -- when several share a due date (DEF-008).
	 ORDER BY l.due_at, l.id
	 LIMIT $3 OFFSET $4`

	rows, err := r.db.Query(ctx, q, overdueOnly, openOnly, limit, offset)
	if err != nil {
		return nil, 0, translate(err)
	}
	defer rows.Close()

	var loans []domain.Loan
	total := 0
	for rows.Next() {
		var l domain.Loan
		if err := rows.Scan(&l.ID, &l.CopyID, &l.UserID, &l.BorrowedAt, &l.DueAt,
			&l.ReturnedAt, &l.IssuedBy, &l.BookTitle, &l.AccessionNumber,
			&l.MemberName, &total); err != nil {
			return nil, 0, translate(err)
		}
		loans = append(loans, l)
	}
	return loans, total, rows.Err()
}

// Stats backs the librarian dashboard (REQ-065).
type Stats struct {
	Books       int `json:"books"`
	Copies      int `json:"copies"`
	Members     int `json:"members"`
	ActiveLoans int `json:"active_loans"`
	Overdue     int `json:"overdue"`
}

func (r *CirculationRepo) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := r.db.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM books  WHERE status = 'active'),
		       (SELECT count(*) FROM copies WHERE status <> 'withdrawn'),
		       (SELECT count(*) FROM users  WHERE role = 'member'),
		       (SELECT count(*) FROM loans  WHERE returned_at IS NULL),
		       (SELECT count(*) FROM loans  WHERE returned_at IS NULL AND due_at < now())
	`).Scan(&s.Books, &s.Copies, &s.Members, &s.ActiveLoans, &s.Overdue)
	return s, translate(err)
}
