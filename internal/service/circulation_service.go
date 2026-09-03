package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// CirculationStore is the persistence the circulation desk needs.
type CirculationStore interface {
	Borrow(ctx context.Context, p postgres.BorrowParams) (domain.Loan, error)
	Return(ctx context.Context, loanID, staffID uuid.UUID) (domain.Loan, error)
	LoansForUser(ctx context.Context, userID uuid.UUID, openOnly bool) ([]domain.Loan, error)
	ListLoans(ctx context.Context, overdueOnly, openOnly bool, limit, offset int) ([]domain.Loan, int, error)
	Stats(ctx context.Context) (postgres.Stats, error)
}

// MemberLookup reads the member being lent to.
type MemberLookup interface {
	FindByID(ctx context.Context, id uuid.UUID) (domain.User, error)
}

// ReturnHook is notified when a copy comes back, so the reservation queue can
// advance. It is an interface rather than a direct dependency to keep
// circulation from importing reservations and vice versa.
type ReturnHook interface {
	OnCopyReturned(ctx context.Context, copyID uuid.UUID) (bool, error)
}

type CirculationService struct {
	loans      CirculationStore
	members    MemberLookup
	notifier   Notifier
	returnHook ReturnHook
	// now is injectable so the overdue rules can be tested at a fixed instant.
	// It returns UTC: every timestamp this service stores or compares is UTC,
	// and Africa/Lagos exists only at the point of display.
	now func() time.Time
}

func NewCirculationService(l CirculationStore, m MemberLookup, n Notifier) *CirculationService {
	return &CirculationService{
		loans: l, members: m, notifier: n,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// Borrow records that a member has walked away with a physical copy.
//
// The librarian performs this at the desk; a member can never record their own
// borrowing, which is both how the library works and what stops a member
// issuing themselves an unlimited number of books (REQ-041).
func (s *CirculationService) Borrow(ctx context.Context, copyID, memberID, librarianID uuid.UUID) (domain.Loan, error) {
	member, err := s.members.FindByID(ctx, memberID)
	if err != nil {
		return domain.Loan{}, err
	}

	// A suspended or inactive member does not borrow (REQ-045).
	if !member.CanBorrow() {
		return domain.Loan{}, domain.ErrMemberNotActive
	}
	if member.Category == nil {
		return domain.Loan{}, domain.ErrNoCategory
	}

	// Entitlement comes from the member's category: an undergraduate and a
	// member of staff do not get the same terms (DOM-005, DEC-005).
	terms, ok := domain.TermsFor(*member.Category)
	if !ok {
		return domain.Loan{}, domain.ErrNoCategory
	}

	// Both timestamps come from one clock, so the recorded loan period is
	// exactly the category's entitlement (DEF-003).
	borrowedAt := s.now()

	loan, err := s.loans.Borrow(ctx, postgres.BorrowParams{
		CopyID:     copyID,
		UserID:     memberID,
		IssuedBy:   librarianID,
		BorrowedAt: borrowedAt,
		DueAt:      borrowedAt.Add(terms.LoanPeriod), // REQ-042
		MaxLoans:   terms.MaxConcurrentLoans,         // REQ-043
	})
	if err != nil {
		return domain.Loan{}, err
	}

	// Queued, not sent: the desk should not wait on an email provider.
	_ = s.notifier.Queue(ctx, memberID, "email", "loan_receipt", map[string]any{
		"loan_id": loan.ID.String(),
		"title":   loan.BookTitle,
		"due_at":  loan.DueAt.UTC().Format(time.RFC3339),
	})
	return loan, nil
}

// SetReturnHook attaches the reservation queue. Optional: circulation works
// without it, which is what keeps the two services independently testable.
func (s *CirculationService) SetReturnHook(h ReturnHook) { s.returnHook = h }

// Return records a copy coming back and frees it for the next reader.
//
// If somebody is queued for the title they are promoted and notified. That
// happens after the return is committed and its failure is not propagated: the
// book has physically come back and the record must reflect that, whatever the
// queue does (REQ-058).
func (s *CirculationService) Return(ctx context.Context, loanID, librarianID uuid.UUID) (domain.Loan, error) {
	loan, err := s.loans.Return(ctx, loanID, librarianID)
	if err != nil {
		return domain.Loan{}, err
	}

	if s.returnHook != nil {
		if _, err := s.returnHook.OnCopyReturned(ctx, loan.CopyID); err != nil {
			slog.Warn("could not advance the reservation queue",
				"copy_id", loan.CopyID, "error", err)
		}
	}
	return loan, nil
}

// MyLoans lists what a member currently holds, with due dates (REQ-060).
func (s *CirculationService) MyLoans(ctx context.Context, memberID uuid.UUID) ([]domain.Loan, error) {
	return s.loans.LoansForUser(ctx, memberID, true)
}

// MyHistory lists everything a member has ever borrowed.
//
// The history survives the return, which is the point: a returned book is a
// closed record, not a deleted one (DOM-008, REQ-061).
func (s *CirculationService) MyHistory(ctx context.Context, memberID uuid.UUID) ([]domain.Loan, error) {
	return s.loans.LoansForUser(ctx, memberID, false)
}

// MemberHistory is the librarian's view of any member's record.
//
// The caller's role is checked by middleware before this is reached; a member
// asking for another member's history never gets here (REQ-062, REQ-063).
func (s *CirculationService) MemberHistory(ctx context.Context, memberID uuid.UUID) ([]domain.Loan, error) {
	return s.loans.LoansForUser(ctx, memberID, false)
}

// ListLoans serves the circulation view, optionally narrowed to overdue items.
func (s *CirculationService) ListLoans(ctx context.Context, overdueOnly, openOnly bool, limit, offset int) ([]domain.Loan, int, error) {
	return s.loans.ListLoans(ctx, overdueOnly, openOnly, limit, offset)
}

func (s *CirculationService) Stats(ctx context.Context) (postgres.Stats, error) {
	return s.loans.Stats(ctx)
}

// NotifyDueSoon queues reminders for loans falling due within the window.
//
// Run on a schedule. Overdue is recomputed from the clock every time rather than
// read from a stored flag, so a reminder is never sent against a stale value
// (REQ-053, REQ-069, REQ-070).
func (s *CirculationService) NotifyDueSoon(ctx context.Context, window time.Duration) (int, error) {
	open, _, err := s.loans.ListLoans(ctx, false, true, 500, 0)
	if err != nil {
		return 0, err
	}

	now := s.now()
	queued := 0
	for _, l := range open {
		// A returned loan is never chased, however far past its due date it
		// sits. The store is asked for open loans only, but this does not lean
		// on that: a reminder sent for a book already back on the shelf is the
		// kind of error a reader remembers. DEF-001.
		if l.IsReturned() {
			continue
		}

		switch {
		case l.IsOverdueAt(now):
			err = s.notifier.Queue(ctx, l.UserID, "email", "loan_overdue", map[string]any{
				// loan_id lets the worker re-check that the book is still out
				// before chasing a member who returned it (DEF-011).
				"loan_id":      l.ID.String(),
				"title":        l.BookTitle,
				"due_at":       l.DueAt.UTC().Format(time.RFC3339),
				"days_overdue": l.DaysOverdueAt(now),
			})
		case l.DueAt.Sub(now) <= window:
			err = s.notifier.Queue(ctx, l.UserID, "email", "loan_due_soon", map[string]any{
				"loan_id": l.ID.String(),
				"title":   l.BookTitle,
				"due_at":  l.DueAt.UTC().Format(time.RFC3339),
			})
		default:
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return queued, err
		}
		queued++
	}
	return queued, nil
}
