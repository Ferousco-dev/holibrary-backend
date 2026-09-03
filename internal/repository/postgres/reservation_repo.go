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

// ReservationRepo manages the queue for titles that cannot be borrowed now.
//
// HOL takes reservations at the Loans desk for items that are out or on
// display, so the system does too (DOM-004, DEC-003).
type ReservationRepo struct{ db *pgxpool.Pool }

func NewReservationRepo(db *pgxpool.Pool) *ReservationRepo { return &ReservationRepo{db: db} }

// Create places a member in the queue for a title.
//
// The whole operation is one transaction, because the decision depends on state
// that another request could change underneath it: a copy becoming available
// between the availability check and the insert would leave a member queued for
// a book already back on the shelf.
func (r *ReservationRepo) Create(ctx context.Context, bookID, userID uuid.UUID, _ time.Duration) (domain.Reservation, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Reservation{}, translate(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	// Does the library hold this title at all, and is any copy reservable?
	var reservable, stock, available int
	err = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE loan_policy IN ('circulating','on_display')
		                          AND status <> 'withdrawn'),
		       count(*) FILTER (WHERE loan_policy = 'circulating'
		                          AND status IN ('available','on_loan')),
		       count(*) FILTER (WHERE loan_policy = 'circulating'
		                          AND status = 'available')
		  FROM copies WHERE book_id = $1`, bookID).Scan(&reservable, &stock, &available)
	if err != nil {
		return domain.Reservation{}, translate(err)
	}

	if reservable == 0 {
		// Reference works and restricted collections are consulted in place;
		// there is no queue to join because they never leave the building.
		return domain.Reservation{}, domain.ErrNotReservable
	}
	// The test is whether a copy could actually be borrowed, not merely whether
	// one is on the shelf: a title whose only free copy is being retained is
	// present but cannot be taken away, so a queue for it is meaningful
	// (DEC-018).
	if domain.BorrowableCount(stock, available) > 0 {
		// Telling a member to wait for a book they could borrow now would send
		// them away from a library that has it.
		return domain.Reservation{}, domain.ErrCopiesAvailable
	}

	// The queue position is returned with the new row so the member is told
	// where they stand immediately, rather than having to fetch their list to
	// find out. It is computed, never stored: a stored position would be wrong
	// the moment anyone ahead of them cancelled.
	var res domain.Reservation
	err = tx.QueryRow(ctx, `
		WITH inserted AS (
		    -- No expiry while merely waiting in line. The hold period is a
		    -- deadline for collecting a book that is ready, not a limit on how
		    -- long a member may wait for a popular title. PromoteNext sets
		    -- expires_at when a copy is actually held. DEF-016.
		    INSERT INTO reservations (book_id, user_id)
		    VALUES ($1, $2)
		    RETURNING id, book_id, user_id, status, created_at, expires_at
		)
		SELECT i.id, i.book_id, i.user_id, i.status, i.created_at, i.expires_at,
		       (SELECT count(*) + 1 FROM reservations q
		         WHERE q.book_id = i.book_id AND q.status = 'pending'
		           AND q.created_at < i.created_at)
		  FROM inserted i`,
		bookID, userID).Scan(
		&res.ID, &res.BookID, &res.UserID, &res.Status, &res.CreatedAt,
		&res.ExpiresAt, &res.QueuePos)
	if err != nil {
		// The partial unique index allows one open reservation per member per
		// title, so a double tap cannot create two places in the queue.
		if isUniqueViolation(err, "one_open_reservation_per_user_book") {
			return domain.Reservation{}, domain.ErrAlreadyReserved
		}
		return domain.Reservation{}, translate(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Reservation{}, translate(err)
	}
	return res, nil
}

const reservationSelect = `
	SELECT r.id, r.book_id, r.user_id, r.status, r.created_at, r.expires_at,
	       b.title,
	       (SELECT count(*) + 1 FROM reservations q
	         WHERE q.book_id = r.book_id AND q.status = 'pending'
	           AND q.created_at < r.created_at) AS queue_position
	  FROM reservations r JOIN books b ON b.id = r.book_id`

// ListForUser returns a member's own reservations, with their place in each
// queue computed at read time rather than stored — a stored position would go
// stale the moment anyone ahead cancelled.
func (r *ReservationRepo) ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.Reservation, error) {
	rows, err := r.db.Query(ctx, reservationSelect+`
	 WHERE r.user_id = $1 AND r.status IN ('pending','ready')
	 ORDER BY r.created_at, r.id`, userID)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()

	var out []domain.Reservation
	for rows.Next() {
		var res domain.Reservation
		if err := rows.Scan(&res.ID, &res.BookID, &res.UserID, &res.Status,
			&res.CreatedAt, &res.ExpiresAt, &res.BookTitle, &res.QueuePos); err != nil {
			return nil, translate(err)
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// Cancel withdraws a member's own reservation.
//
// The user id is part of the WHERE clause rather than checked afterwards, so a
// member cannot cancel somebody else's place in a queue by guessing an id
// (I-11).
func (r *ReservationRepo) Cancel(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE reservations SET status = 'cancelled'
		 WHERE id = $1 AND user_id = $2 AND status IN ('pending','ready')`, id, userID)
	if err != nil {
		return translate(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// PromoteNext marks the member at the head of the queue as ready and returns
// them, so a notification can be queued.
//
// Called when a copy comes back. Returns ErrNotFound when nobody is waiting,
// which is the ordinary case and not an error worth failing a return over.
func (r *ReservationRepo) PromoteNext(ctx context.Context, bookID uuid.UUID, hold time.Duration) (domain.Reservation, error) {
	var res domain.Reservation
	err := r.db.QueryRow(ctx, `
		UPDATE reservations SET status = 'ready', notified_at = now(),
		                        expires_at = now() + $2::interval
		 WHERE id = (
		     SELECT id FROM reservations
		      WHERE book_id = $1 AND status = 'pending'
		      ORDER BY created_at, id
		      -- Two returns processed at once must not promote the same person
		      -- twice, nor skip anybody.
		      FOR UPDATE SKIP LOCKED
		      LIMIT 1)
		RETURNING id, book_id, user_id, status, created_at, expires_at`,
		bookID, hold.String()).Scan(&res.ID, &res.BookID, &res.UserID,
		&res.Status, &res.CreatedAt, &res.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Reservation{}, domain.ErrNotFound
	}
	return res, translate(err)
}

// ExpireStale releases reservations nobody came to collect, so one member who
// never turns up does not block the queue for everyone behind them (REQ-059).
func (r *ReservationRepo) ExpireStale(ctx context.Context) (int, error) {
	// Only a held copy can go uncollected. A pending reservation has no expiry
	// and must never age out of the queue: the member has not been offered
	// anything yet, so there is nothing for them to have failed to collect.
	// DEF-016.
	tag, err := r.db.Exec(ctx, `
		UPDATE reservations SET status = 'expired'
		 WHERE status = 'ready' AND expires_at < now()`)
	if err != nil {
		return 0, translate(err)
	}
	return int(tag.RowsAffected()), nil
}

// BookIDForCopy resolves which title a returned copy belongs to.
func (r *ReservationRepo) BookIDForCopy(ctx context.Context, copyID uuid.UUID) (uuid.UUID, error) {
	var bookID uuid.UUID
	err := r.db.QueryRow(ctx, `SELECT book_id FROM copies WHERE id = $1`, copyID).Scan(&bookID)
	return bookID, translate(err)
}
