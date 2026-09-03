package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// ReservationStore is the persistence the queue needs.
type ReservationStore interface {
	Create(ctx context.Context, bookID, userID uuid.UUID, hold time.Duration) (domain.Reservation, error)
	ListForUser(ctx context.Context, userID uuid.UUID) ([]domain.Reservation, error)
	Cancel(ctx context.Context, id, userID uuid.UUID) error
	PromoteNext(ctx context.Context, bookID uuid.UUID, hold time.Duration) (domain.Reservation, error)
	ExpireStale(ctx context.Context) (int, error)
	BookIDForCopy(ctx context.Context, copyID uuid.UUID) (uuid.UUID, error)
}

// HoldPeriod is how long a member has to collect a book once it is ready.
//
// It applies from promotion, not from joining the queue. An earlier version set
// the expiry when the reservation was created, so a member waiting for a popular
// title was dropped from the queue after three days without ever having been
// offered anything (DEF-016).
//
// Three days is a guess, not library policy, and is the sort of number that
// should be confirmed at the Circulation desk before this is used for real
// lending. It is short on purpose: a long hold keeps a book off the shelf and
// away from everyone else in the queue.
const HoldPeriod = 3 * 24 * time.Hour

type ReservationService struct {
	reservations ReservationStore
	notifier     Notifier
}

func NewReservationService(r ReservationStore, n Notifier) *ReservationService {
	return &ReservationService{reservations: r, notifier: n}
}

// Reserve puts a member in the queue for a title (REQ-055, REQ-056).
//
// Members place their own reservations -- unlike borrowing, which is recorded by
// staff at the desk. Joining a queue commits nothing physical, so there is no
// reason to make someone walk to the library to do it.
func (s *ReservationService) Reserve(ctx context.Context, bookID, memberID uuid.UUID) (domain.Reservation, error) {
	return s.reservations.Create(ctx, bookID, memberID, HoldPeriod)
}

// MyReservations lists a member's own queue positions (REQ-057).
func (s *ReservationService) MyReservations(ctx context.Context, memberID uuid.UUID) ([]domain.Reservation, error) {
	return s.reservations.ListForUser(ctx, memberID)
}

// Cancel withdraws a member's own reservation (REQ-057).
func (s *ReservationService) Cancel(ctx context.Context, id, memberID uuid.UUID) error {
	return s.reservations.Cancel(ctx, id, memberID)
}

// OnCopyReturned promotes whoever is next in the queue for the returned title
// and queues a notification (REQ-058).
//
// It returns no error to its caller when nobody is waiting, and swallows its own
// failures deliberately: the book has physically come back, the return has been
// recorded, and a queue that could not be advanced must not undo that. The
// failure is surfaced to the caller to log, not to fail the return on.
func (s *ReservationService) OnCopyReturned(ctx context.Context, copyID uuid.UUID) (bool, error) {
	bookID, err := s.reservations.BookIDForCopy(ctx, copyID)
	if err != nil {
		return false, err
	}

	res, err := s.reservations.PromoteNext(ctx, bookID, HoldPeriod)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil // nobody waiting: the ordinary case
	}
	if err != nil {
		return false, err
	}

	// Push and email both: a reservation expires, so a missed notification
	// costs the member their place.
	payload := map[string]any{
		// reservation_id lets the worker check the hold is still live before
		// telling a member to come and collect a book that has moved on.
		"reservation_id": res.ID.String(),
		"book_id":        res.BookID.String(),
	}
	if res.ExpiresAt != nil {
		payload["expires_at"] = res.ExpiresAt.UTC().Format(time.RFC3339)
	}
	_ = s.notifier.Queue(ctx, res.UserID, "push", "reservation_ready", payload)
	_ = s.notifier.Queue(ctx, res.UserID, "email", "reservation_ready", payload)
	return true, nil
}

// ExpireStale releases uncollected reservations (REQ-059). Run on a schedule.
func (s *ReservationService) ExpireStale(ctx context.Context) (int, error) {
	return s.reservations.ExpireStale(ctx)
}
