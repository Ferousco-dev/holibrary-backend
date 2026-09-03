package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

type fakeReservations struct {
	createErr   error
	hold        time.Duration
	promoted    domain.Reservation
	promoteErr  error
	cancelledID uuid.UUID
	expired     int
	bookForCopy uuid.UUID
}

func (f *fakeReservations) Create(_ context.Context, bookID, userID uuid.UUID, hold time.Duration) (domain.Reservation, error) {
	f.hold = hold
	if f.createErr != nil {
		return domain.Reservation{}, f.createErr
	}
	return domain.Reservation{ID: uuid.New(), BookID: bookID, UserID: userID, Status: "pending"}, nil
}
func (f *fakeReservations) ListForUser(context.Context, uuid.UUID) ([]domain.Reservation, error) {
	return nil, nil
}
func (f *fakeReservations) Cancel(_ context.Context, id, _ uuid.UUID) error {
	f.cancelledID = id
	return nil
}
func (f *fakeReservations) PromoteNext(_ context.Context, _ uuid.UUID, hold time.Duration) (domain.Reservation, error) {
	f.hold = hold
	return f.promoted, f.promoteErr
}
func (f *fakeReservations) ExpireStale(context.Context) (int, error) { return f.expired, nil }
func (f *fakeReservations) BookIDForCopy(context.Context, uuid.UUID) (uuid.UUID, error) {
	return f.bookForCopy, nil
}

// Telling a member to queue for a book that is on the shelf would send them
// away from a library that has it.
func TestReserveRefusedWhenCopiesAreAvailable(t *testing.T) {
	store := &fakeReservations{createErr: domain.ErrCopiesAvailable}
	svc := service.NewReservationService(store, &fakeNotifier{})

	if _, err := svc.Reserve(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrCopiesAvailable) {
		t.Errorf("error = %v, want ErrCopiesAvailable", err)
	}
}

// Reference works and restricted collections never leave the building, so there
// is no queue to join (DOM-004).
func TestReserveRefusedForNonReservableTitles(t *testing.T) {
	store := &fakeReservations{createErr: domain.ErrNotReservable}
	svc := service.NewReservationService(store, &fakeNotifier{})

	if _, err := svc.Reserve(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrNotReservable) {
		t.Errorf("error = %v, want ErrNotReservable", err)
	}
}

// A double tap on the reserve button must not buy two places in one queue.
func TestReserveRefusesADuplicate(t *testing.T) {
	store := &fakeReservations{createErr: domain.ErrAlreadyReserved}
	svc := service.NewReservationService(store, &fakeNotifier{})

	if _, err := svc.Reserve(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, domain.ErrAlreadyReserved) {
		t.Errorf("error = %v, want ErrAlreadyReserved", err)
	}
}

func TestReserveAppliesTheHoldPeriod(t *testing.T) {
	store := &fakeReservations{}
	svc := service.NewReservationService(store, &fakeNotifier{})

	if _, err := svc.Reserve(context.Background(), uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	if store.hold != service.HoldPeriod {
		t.Errorf("hold = %v, want %v", store.hold, service.HoldPeriod)
	}
}

// A returned copy promotes whoever is next and notifies them on both channels:
// a reservation expires, so a missed notification costs a member their place.
func TestReturningACopyNotifiesTheNextInQueue(t *testing.T) {
	store := &fakeReservations{
		bookForCopy: uuid.New(),
		promoted:    domain.Reservation{ID: uuid.New(), UserID: uuid.New(), Status: "ready"},
	}
	notifier := &fakeNotifier{}
	svc := service.NewReservationService(store, notifier)

	promoted, err := svc.OnCopyReturned(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("OnCopyReturned: %v", err)
	}
	if !promoted {
		t.Error("someone was waiting, so a member should have been promoted")
	}
	if len(notifier.queued) != 2 {
		t.Fatalf("queued %d notifications, want 2 (push and email)", len(notifier.queued))
	}
	for _, tmpl := range notifier.queued {
		if tmpl != "reservation_ready" {
			t.Errorf("unexpected template %q", tmpl)
		}
	}
}

// An empty queue is the ordinary case, not an error. A return must not fail
// because nobody was waiting.
func TestReturningACopyWithAnEmptyQueueIsNotAnError(t *testing.T) {
	store := &fakeReservations{promoteErr: domain.ErrNotFound}
	notifier := &fakeNotifier{}
	svc := service.NewReservationService(store, notifier)

	promoted, err := svc.OnCopyReturned(context.Background(), uuid.New())
	if err != nil {
		t.Errorf("an empty queue must not be an error: %v", err)
	}
	if promoted {
		t.Error("nobody was waiting, so nobody should have been promoted")
	}
	if len(notifier.queued) != 0 {
		t.Error("no notification should be queued when nobody is waiting")
	}
}

// A member who never collects must not block everyone behind them (REQ-059).
func TestExpireStaleReleasesUncollectedHolds(t *testing.T) {
	store := &fakeReservations{expired: 4}
	svc := service.NewReservationService(store, &fakeNotifier{})

	n, err := svc.ExpireStale(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("released %d, want 4", n)
	}
}
