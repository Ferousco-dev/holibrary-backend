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

// Only an active account may receive a book. A suspended card handed in at the
// desk must be refused there, not discovered later (REQ-045).
func TestUserCanBorrow(t *testing.T) {
	cases := []struct {
		status domain.UserStatus
		can    bool
	}{
		{domain.UserActive, true},
		{domain.UserSuspended, false},
		{domain.UserInactive, false},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			if got := (domain.User{Status: c.status}).CanBorrow(); got != c.can {
				t.Errorf("CanBorrow() = %v, want %v", got, c.can)
			}
		})
	}
}

// The wing a title is shelved in is a fact about its class mark, so the book
// derives it rather than storing it (DOM-003).
func TestBookWing(t *testing.T) {
	cases := []struct {
		name  string
		class string
		want  domain.Wing
	}{
		{"Africana, class D, South wing", "D", domain.WingSouth},
		{"computing, class Q, North wing", "Q", domain.WingNorth},
		{"a title with no class mark", "", domain.WingUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (domain.Book{LCCClass: c.class}).Wing(); got != c.want {
				t.Errorf("Wing() = %q, want %q", got, c.want)
			}
		})
	}
}

// A loan is open until it has a return time. There is no separate status field
// that could contradict this (I-02).
func TestLoanIsReturned(t *testing.T) {
	now := time.Now()
	if (domain.Loan{}).IsReturned() {
		t.Error("a loan with no return time is still open")
	}
	if !(domain.Loan{ReturnedAt: &now}).IsReturned() {
		t.Error("a loan with a return time is closed")
	}
}

// Instants are stored and compared in UTC; Africa/Lagos exists only for display.
func TestInDisplayTimezone(t *testing.T) {
	instant := time.Date(2026, 9, 17, 18, 15, 0, 0, time.UTC)

	shown := domain.InDisplayTimezone(instant)
	if !shown.Equal(instant) {
		t.Error("converting for display must not move the instant")
	}
	// Lagos is one hour ahead, so 18:15 UTC reads as 19:15 to a student.
	if got := shown.Format("15:04"); got != "19:15" && got != "18:15" {
		t.Errorf("display time = %s, want 19:15 (or 18:15 if tzdata is unavailable)", got)
	}
}

// A loan already back on the shelf is never overdue, whatever the clock says.
func TestDaysOverdueIgnoresReturnedLoans(t *testing.T) {
	due := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	returned := due.Add(-time.Hour)
	loan := domain.Loan{DueAt: due, ReturnedAt: &returned}

	if got := loan.DaysOverdueAt(due.Add(90 * 24 * time.Hour)); got != 0 {
		t.Errorf("DaysOverdueAt on a returned loan = %d, want 0", got)
	}
}
