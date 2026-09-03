package domain_test

import (
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// The wing is derived from the LCC class letter because HOL shelves A-J in the
// South wing and K-Z in the North wing (DOM-003).
func TestWingFor(t *testing.T) {
	cases := []struct {
		name  string
		class byte
		want  domain.Wing
	}{
		{"A is the first South class", 'A', domain.WingSouth},
		{"J is the last South class", 'J', domain.WingSouth},
		{"K is the first North class", 'K', domain.WingNorth},
		{"Z is the last North class", 'Z', domain.WingNorth},
		{"DT, an Africana class, is South", 'D', domain.WingSouth},
		{"a lowercase letter is not a class mark", 'a', domain.WingUnknown},
		{"a digit is not a class mark", '4', domain.WingUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := domain.WingFor(c.class); got != c.want {
				t.Errorf("WingFor(%q) = %q, want %q", c.class, got, c.want)
			}
		})
	}
}

// Only circulating copies leave the building. Reference works are consulted in
// the Reference Room and display items are reservable but not borrowable
// (DOM-004).
func TestLoanPolicy(t *testing.T) {
	cases := []struct {
		policy     domain.LoanPolicy
		borrowable bool
		reservable bool
	}{
		{domain.PolicyCirculating, true, true},
		{domain.PolicyReferenceOnly, false, false},
		{domain.PolicyOnDisplay, false, true},
		{domain.PolicyRestricted, false, false},
	}
	for _, c := range cases {
		t.Run(string(c.policy), func(t *testing.T) {
			if got := c.policy.IsBorrowable(); got != c.borrowable {
				t.Errorf("IsBorrowable() = %v, want %v", got, c.borrowable)
			}
			if got := c.policy.IsReservable(); got != c.reservable {
				t.Errorf("IsReservable() = %v, want %v", got, c.reservable)
			}
		})
	}
}

// Overdue is computed from the clock, never stored, so it cannot go stale
// (REQ-053).
func TestLoanIsOverdueAt(t *testing.T) {
	due := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	returned := due.Add(-48 * time.Hour)

	open := domain.Loan{DueAt: due}
	closed := domain.Loan{DueAt: due, ReturnedAt: &returned}

	if open.IsOverdueAt(due.Add(-time.Hour)) {
		t.Error("a loan is not overdue before its due date")
	}
	if !open.IsOverdueAt(due.Add(time.Hour)) {
		t.Error("an unreturned loan past its due date is overdue")
	}
	if closed.IsOverdueAt(due.Add(30 * 24 * time.Hour)) {
		t.Error("a returned loan is never overdue, however late we look")
	}
	if got := open.DaysOverdueAt(due.Add(76 * time.Hour)); got != 3 {
		t.Errorf("DaysOverdueAt = %d, want 3", got)
	}
}

func TestTermsFor(t *testing.T) {
	undergrad, ok := domain.TermsFor(domain.CategoryUndergraduate)
	if !ok {
		t.Fatal("undergraduate category must have terms")
	}
	if undergrad.MaxConcurrentLoans != 2 {
		t.Errorf("undergraduate limit = %d, want 2", undergrad.MaxConcurrentLoans)
	}
	if undergrad.LoanPeriod != 14*24*time.Hour {
		t.Errorf("undergraduate period = %v, want 336h", undergrad.LoanPeriod)
	}
	if _, ok := domain.TermsFor("professor"); ok {
		t.Error("an unknown category must not resolve to terms")
	}
}

func TestAvailabilityIsAvailable(t *testing.T) {
	// Book A: 10 copies, 7 free. Book B: 2 copies, none free.
	a := domain.Availability{TotalCopies: 10, Available: 7, OnLoan: 3}
	b := domain.Availability{TotalCopies: 2, Available: 0, OnLoan: 2}

	if !a.IsAvailable() {
		t.Error("a title with free copies is available")
	}
	if b.IsAvailable() {
		t.Error("a title with every copy on loan is not available")
	}
}

func TestRoleCanManageLibrary(t *testing.T) {
	if domain.RoleMember.CanManageLibrary() {
		t.Error("a member must never manage the library")
	}
	if !domain.RoleLibrarian.CanManageLibrary() || !domain.RoleAdmin.CanManageLibrary() {
		t.Error("librarians and admins manage the library")
	}
}
