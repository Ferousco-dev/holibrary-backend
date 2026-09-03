package domain_test

import (
	"testing"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

var serviceRolesCreatableBy = service.RolesCreatableBy

// The copy state machine is what stops a librarian corrupting real records with
// a well-meaning status edit (DEF-009).
func TestCopyStatusTransitions(t *testing.T) {
	cases := []struct {
		name    string
		from    domain.CopyStatus
		to      domain.CopyStatus
		allowed bool
	}{
		// The dangerous one: a borrowed copy pushed back to available would
		// abandon an open loan, so the library believes a book is on the shelf
		// while a student still holds it.
		{"borrowed copy cannot be quietly shelved", domain.CopyOnLoan, domain.CopyAvailable, false},

		// A book lost or damaged while out is a real event and must be
		// recordable, or a librarian will fake a return to close the loan.
		{"borrowed copy can be reported lost", domain.CopyOnLoan, domain.CopyLost, true},
		{"borrowed copy can be reported damaged", domain.CopyOnLoan, domain.CopyDamaged, true},

		{"available copy can be lost", domain.CopyAvailable, domain.CopyLost, true},
		{"available copy can be withdrawn", domain.CopyAvailable, domain.CopyWithdrawn, true},

		// Lending is never a status edit. It goes through the circulation
		// service so the loan and the copy change together.
		{"lending is not a status edit", domain.CopyAvailable, domain.CopyOnLoan, false},
		{"a lost copy cannot be lent", domain.CopyLost, domain.CopyOnLoan, false},

		{"a found copy returns to the shelf", domain.CopyLost, domain.CopyAvailable, true},
		{"a repaired copy returns to the shelf", domain.CopyDamaged, domain.CopyAvailable, true},

		// Withdrawn is terminal: a replacement is a new copy with its own
		// accession number, not a resurrection of this one.
		{"withdrawn cannot become available", domain.CopyWithdrawn, domain.CopyAvailable, false},
		{"withdrawn cannot be borrowed", domain.CopyWithdrawn, domain.CopyOnLoan, false},

		{"no change is always fine", domain.CopyAvailable, domain.CopyAvailable, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.from.CanTransitionTo(c.to); got != c.allowed {
				t.Errorf("%s -> %s: allowed = %v, want %v", c.from, c.to, got, c.allowed)
			}
		})
	}
}

// Lost and damaged close the open loan; nothing else does.
func TestClosesAnOpenLoan(t *testing.T) {
	for _, s := range []domain.CopyStatus{domain.CopyLost, domain.CopyDamaged} {
		if !s.ClosesAnOpenLoan() {
			t.Errorf("%s must close the open loan on that copy", s)
		}
	}
	for _, s := range []domain.CopyStatus{domain.CopyAvailable, domain.CopyWithdrawn, domain.CopyOnLoan} {
		if s.ClosesAnOpenLoan() {
			t.Errorf("%s must not close a loan by itself", s)
		}
	}
}

// A librarian registers members. Only an administrator creates staff, or the
// role field on a create-member request is a privilege-escalation vector
// (DEF-005).
func TestRolesCreatableBy(t *testing.T) {
	cases := []struct {
		actor   domain.Role
		target  domain.Role
		allowed bool
	}{
		{domain.RoleLibrarian, domain.RoleMember, true},
		{domain.RoleLibrarian, domain.RoleLibrarian, false},
		{domain.RoleLibrarian, domain.RoleAdmin, false},
		{domain.RoleAdmin, domain.RoleMember, true},
		{domain.RoleAdmin, domain.RoleLibrarian, true},
		{domain.RoleAdmin, domain.RoleAdmin, true},
		{domain.RoleMember, domain.RoleMember, false},
		{domain.RoleMember, domain.RoleAdmin, false},
	}
	for _, c := range cases {
		if got := serviceRolesCreatableBy(c.actor)[c.target]; got != c.allowed {
			t.Errorf("%s creating %s: allowed = %v, want %v", c.actor, c.target, got, c.allowed)
		}
	}
}

// Last-copy retention (DEC-018).
//
// A title held in two or more circulating copies always keeps one on the shelf,
// so a reader who walks in can still consult it. A single-copy title circulates
// normally — retaining it would mean nobody could ever read it, which at HOL
// would strand most of the Africana and OAU Publications holdings.
func TestBorrowableCount(t *testing.T) {
	cases := []struct {
		name      string
		stock     int
		available int
		want      int
	}{
		{"a single copy circulates, or nobody could read it", 1, 1, 1},
		{"a single copy already out", 1, 0, 0},
		{"five copies, three free: one stays on the shelf", 5, 3, 2},
		{"five copies, one free: that one stays", 5, 1, 0},
		{"five copies, none free", 5, 0, 0},
		{"two copies, both free: one may go", 2, 2, 1},
		{"two copies, one free: it stays", 2, 1, 0},
		{"a title with no circulating stock", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := domain.BorrowableCount(c.stock, c.available); got != c.want {
				t.Errorf("BorrowableCount(stock=%d, available=%d) = %d, want %d",
					c.stock, c.available, got, c.want)
			}
		})
	}
}

// A title whose only free copy is retained is present in the library but cannot
// be taken away. Reporting it as "available" would send a reader on a wasted
// journey; reporting it as absent would hide a book they could consult.
func TestAvailabilityDistinguishesShelfFromBorrowable(t *testing.T) {
	retained := domain.Availability{TotalCopies: 5, Available: 1, OnLoan: 4, Stock: 5}
	if retained.IsAvailable() {
		t.Error("a retained shelf copy must not read as available to borrow")
	}
	if !retained.OnShelf() {
		t.Error("but it is on the shelf and can be consulted")
	}
	if got := retained.Borrowable(); got != 0 {
		t.Errorf("Borrowable() = %d, want 0", got)
	}

	lone := domain.Availability{TotalCopies: 1, Available: 1, Stock: 1}
	if !lone.IsAvailable() {
		t.Error("a single-copy title circulates normally")
	}

	plenty := domain.Availability{TotalCopies: 5, Available: 3, OnLoan: 2, Stock: 5}
	if got := plenty.Borrowable(); got != 2 {
		t.Errorf("Borrowable() = %d, want 2", got)
	}
}

// Losing a copy must relax the rule rather than tighten it: stock counts only
// volumes that are on the shelf or out, so a lost copy leaves the collection.
func TestRetentionIgnoresLostAndWithdrawnCopies(t *testing.T) {
	// Two copies acquired, one lost: the remaining one is now a single-copy
	// title and circulates.
	afterLoss := domain.Availability{TotalCopies: 2, Available: 1, Stock: 1}
	if !afterLoss.IsAvailable() {
		t.Error("once the collection is down to one copy, it circulates again")
	}
}
