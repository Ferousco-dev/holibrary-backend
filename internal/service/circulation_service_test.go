package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

// The fakes below let the circulation rules be exercised without a database,
// which is what keeping business rules out of the repository layer buys us.

type fakeLoans struct {
	borrowed           postgres.BorrowParams
	borrowErr          error
	open               []domain.Loan
	askedSyntheticOnly bool
}

func (f *fakeLoans) Borrow(_ context.Context, p postgres.BorrowParams) (domain.Loan, error) {
	f.borrowed = p
	if f.borrowErr != nil {
		return domain.Loan{}, f.borrowErr
	}
	return domain.Loan{ID: uuid.New(), CopyID: p.CopyID, UserID: p.UserID,
		BorrowedAt: p.BorrowedAt, DueAt: p.DueAt}, nil
}

func (f *fakeLoans) Return(_ context.Context, loanID, staffID uuid.UUID) (domain.Loan, error) {
	now := time.Now()
	return domain.Loan{ID: loanID, ReturnedAt: &now, ReturnedTo: &staffID}, nil
}

func (f *fakeLoans) LoansForUser(_ context.Context, _ uuid.UUID, _ bool) ([]domain.Loan, error) {
	return f.open, nil
}

func (f *fakeLoans) ListLoans(_ context.Context, _, _ bool, _, _ int, syntheticOnly bool) ([]domain.Loan, int, error) {
	f.askedSyntheticOnly = syntheticOnly
	return f.open, len(f.open), nil
}

func (f *fakeLoans) Stats(context.Context) (postgres.Stats, error) { return postgres.Stats{}, nil }

type fakeMembers struct {
	user domain.User
	err  error
}

func (f *fakeMembers) FindByID(context.Context, uuid.UUID) (domain.User, error) {
	return f.user, f.err
}

type fakeNotifier struct{ queued []string }

func (f *fakeNotifier) Queue(_ context.Context, _ uuid.UUID, _, template string, _ map[string]any) error {
	f.queued = append(f.queued, template)
	return nil
}

func member(category domain.MemberCategory, status domain.UserStatus) domain.User {
	return domain.User{
		ID: uuid.New(), Role: domain.RoleMember, Category: &category, Status: status,
	}
}

// The due date comes from the member's category, not from a constant, because
// HOL does not give an undergraduate and a lecturer the same terms (DEC-005).
func TestBorrowSetsDueDateFromMemberCategory(t *testing.T) {
	cases := []struct {
		category  domain.MemberCategory
		wantDays  int
		wantLimit int
	}{
		{domain.CategoryUndergraduate, 14, 2},
		{domain.CategoryPostgraduate, 21, 4},
		{domain.CategoryStaff, 28, 6},
	}

	for _, c := range cases {
		t.Run(string(c.category), func(t *testing.T) {
			loans := &fakeLoans{}
			m := member(c.category, domain.UserActive)
			svc := service.NewCirculationService(loans, &fakeMembers{user: m}, &fakeNotifier{})

			before := time.Now()
			loan, err := svc.Borrow(context.Background(), uuid.New(), m.ID, uuid.New())
			if err != nil {
				t.Fatalf("Borrow: %v", err)
			}

			_ = before
			gotDays := int(loan.DueAt.Sub(loan.BorrowedAt).Hours() / 24)
			if gotDays != c.wantDays {
				t.Errorf("loan period = %d days, want %d", gotDays, c.wantDays)
			}
			if loans.borrowed.MaxLoans != c.wantLimit {
				t.Errorf("limit passed to store = %d, want %d", loans.borrowed.MaxLoans, c.wantLimit)
			}
		})
	}
}

// A suspended member does not borrow, and the check happens before the store is
// touched at all (REQ-045).
func TestBorrowRejectsInactiveMember(t *testing.T) {
	for _, status := range []domain.UserStatus{domain.UserSuspended, domain.UserInactive} {
		t.Run(string(status), func(t *testing.T) {
			loans := &fakeLoans{}
			m := member(domain.CategoryUndergraduate, status)
			svc := service.NewCirculationService(loans, &fakeMembers{user: m}, &fakeNotifier{})

			_, err := svc.Borrow(context.Background(), uuid.New(), m.ID, uuid.New())
			if !errors.Is(err, domain.ErrMemberNotActive) {
				t.Errorf("error = %v, want ErrMemberNotActive", err)
			}
			if loans.borrowed.MaxLoans != 0 {
				t.Error("the store must not be reached for an inactive member")
			}
		})
	}
}

// A member with no category has no defined entitlement, so lending to them would
// mean inventing a limit.
func TestBorrowRejectsMemberWithoutCategory(t *testing.T) {
	m := domain.User{ID: uuid.New(), Role: domain.RoleMember, Status: domain.UserActive}
	svc := service.NewCirculationService(&fakeLoans{}, &fakeMembers{user: m}, &fakeNotifier{})

	if _, err := svc.Borrow(context.Background(), uuid.New(), m.ID, uuid.New()); !errors.Is(err, domain.ErrNoCategory) {
		t.Errorf("error = %v, want ErrNoCategory", err)
	}
}

// The store reports the race loss and the service passes it through unchanged,
// so the desk is told the copy has gone rather than being handed a generic error.
func TestBorrowPropagatesCopyUnavailable(t *testing.T) {
	loans := &fakeLoans{borrowErr: domain.ErrCopyNotAvailable}
	m := member(domain.CategoryUndergraduate, domain.UserActive)
	svc := service.NewCirculationService(loans, &fakeMembers{user: m}, &fakeNotifier{})

	if _, err := svc.Borrow(context.Background(), uuid.New(), m.ID, uuid.New()); !errors.Is(err, domain.ErrCopyNotAvailable) {
		t.Errorf("error = %v, want ErrCopyNotAvailable", err)
	}
}

// Reference works and books on display stay in the building (DOM-004).
func TestBorrowPropagatesNonCirculatingCopy(t *testing.T) {
	loans := &fakeLoans{borrowErr: domain.ErrCopyNotBorrowable}
	m := member(domain.CategoryStaff, domain.UserActive)
	svc := service.NewCirculationService(loans, &fakeMembers{user: m}, &fakeNotifier{})

	if _, err := svc.Borrow(context.Background(), uuid.New(), m.ID, uuid.New()); !errors.Is(err, domain.ErrCopyNotBorrowable) {
		t.Errorf("error = %v, want ErrCopyNotBorrowable", err)
	}
}

// Reminders are queued rather than sent inline, so the circulation desk never
// waits on a mail provider (REQ-072).
func TestNotifyDueSoonPicksTheRightTemplate(t *testing.T) {
	now := time.Now()
	returned := now.Add(-time.Hour)

	loans := &fakeLoans{open: []domain.Loan{
		{UserID: uuid.New(), DueAt: now.Add(-72 * time.Hour), BookTitle: "Overdue"},
		{UserID: uuid.New(), DueAt: now.Add(24 * time.Hour), BookTitle: "Due soon"},
		{UserID: uuid.New(), DueAt: now.Add(10 * 24 * time.Hour), BookTitle: "Not yet"},
		{UserID: uuid.New(), DueAt: now.Add(-99 * time.Hour), ReturnedAt: &returned, BookTitle: "Returned"},
	}}
	notifier := &fakeNotifier{}
	svc := service.NewCirculationService(loans, &fakeMembers{}, notifier)

	queued, err := svc.NotifyDueSoon(context.Background(), 48*time.Hour)
	if err != nil {
		t.Fatalf("NotifyDueSoon: %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued = %d, want 2 (one overdue, one due soon)", queued)
	}

	want := map[string]bool{"loan_overdue": true, "loan_due_soon": true}
	for _, tmpl := range notifier.queued {
		if !want[tmpl] {
			t.Errorf("unexpected template %q", tmpl)
		}
		delete(want, tmpl)
	}
	if len(want) != 0 {
		t.Errorf("templates never queued: %v", want)
	}
}
