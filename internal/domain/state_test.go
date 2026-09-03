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
