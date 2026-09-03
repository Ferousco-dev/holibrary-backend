package domain_test

import (
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// lagos is the display timezone for OAU. It is loaded by name rather than
// written as a fixed +01:00 offset, because a name survives a change to the
// offset and a hardcoded number does not.
func lagos(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Lagos")
	if err != nil {
		t.Skipf("tzdata unavailable in this environment: %v", err)
	}
	return loc
}

// The overdue verdict must not depend on where the reader is sitting.
//
// A loan due at 17:00 UTC is due at 18:00 in Lagos and 12:00 in New York. All
// three describe the same instant, so all three must agree on whether the book
// is late.
func TestOverdueIsTimezoneIndependent(t *testing.T) {
	loc := lagos(t)
	due := time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)
	loan := domain.Loan{DueAt: due}

	// One instant, expressed four ways.
	instant := due.Add(time.Minute)
	for _, zone := range []*time.Location{time.UTC, loc, time.Local} {
		if !loan.IsOverdueAt(instant.In(zone)) {
			t.Errorf("a loan one minute past its due date is overdue when read in %v", zone)
		}
	}

	before := due.Add(-time.Minute)
	for _, zone := range []*time.Location{time.UTC, loc, time.Local} {
		if loan.IsOverdueAt(before.In(zone)) {
			t.Errorf("a loan one minute before its due date is not overdue when read in %v", zone)
		}
	}
}

// The boundary itself: a loan is overdue strictly after its due instant, not at
// it. Off-by-one here is the difference between a member being emailed a
// warning and being emailed an accusation.
func TestOverdueBoundaryIsExclusive(t *testing.T) {
	due := time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)
	loan := domain.Loan{DueAt: due}

	if loan.IsOverdueAt(due) {
		t.Error("a loan is not yet overdue at the exact instant it falls due")
	}
	if !loan.IsOverdueAt(due.Add(time.Nanosecond)) {
		t.Error("a loan is overdue immediately after its due instant")
	}
}

// A due date computed in Lagos local time must land on the same instant as one
// computed in UTC. This is the bug the policy exists to prevent: a 14-day loan
// that becomes 13 days 23 hours because two clocks disagreed.
func TestLoanPeriodIsAnInstantNotAWallClockReading(t *testing.T) {
	loc := lagos(t)
	terms, ok := domain.TermsFor(domain.CategoryUndergraduate)
	if !ok {
		t.Fatal("undergraduate terms must exist")
	}

	borrowedUTC := time.Date(2026, 9, 3, 18, 15, 0, 0, time.UTC)
	borrowedLagos := borrowedUTC.In(loc) // 19:15 WAT -- the same instant

	dueFromUTC := borrowedUTC.Add(terms.LoanPeriod)
	dueFromLagos := borrowedLagos.Add(terms.LoanPeriod)

	if !dueFromUTC.Equal(dueFromLagos) {
		t.Errorf("due dates diverged: %v vs %v", dueFromUTC, dueFromLagos)
	}
	if got := dueFromUTC.UTC().Format(time.RFC3339); got != "2026-09-17T18:15:00Z" {
		t.Errorf("due = %s, want 2026-09-17T18:15:00Z", got)
	}
	// And it reads as 19:15 to a student in Ile-Ife.
	if got := dueFromUTC.In(loc).Format("15:04"); got != "19:15" {
		t.Errorf("Lagos display = %s, want 19:15", got)
	}
}

// Timestamps leave this system as RFC 3339, never as a local format like
// "03/09/26 6:15", which is ambiguous about day, month and zone all at once.
func TestTimestampsSerialiseAsRFC3339(t *testing.T) {
	instant := time.Date(2026, 9, 3, 18, 15, 0, 0, time.UTC)
	if got := instant.Format(time.RFC3339); got != "2026-09-03T18:15:00Z" {
		t.Errorf("RFC3339 = %s, want 2026-09-03T18:15:00Z", got)
	}

	// Parsing it back yields the same instant, which is the property that makes
	// the format safe to exchange across a network.
	parsed, err := time.Parse(time.RFC3339, "2026-09-03T19:15:00+01:00")
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(instant) {
		t.Errorf("19:15+01:00 and 18:15Z must be the same instant")
	}
}

// Days overdue is counted in whole 24-hour periods from the due instant, so the
// figure a librarian sees does not jump because of a timezone conversion.
func TestDaysOverdueCountsFromTheDueInstant(t *testing.T) {
	loc := lagos(t)
	due := time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)
	loan := domain.Loan{DueAt: due}

	for _, c := range []struct {
		after time.Duration
		want  int
	}{
		{time.Hour, 0},
		{23 * time.Hour, 0},
		{25 * time.Hour, 1},
		{7 * 24 * time.Hour, 7},
	} {
		if got := loan.DaysOverdueAt(due.Add(c.after).In(loc)); got != c.want {
			t.Errorf("%v after due: DaysOverdue = %d, want %d", c.after, got, c.want)
		}
	}
}
