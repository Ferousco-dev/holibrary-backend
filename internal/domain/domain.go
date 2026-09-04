// Package domain holds the entities and rules of the library itself.
//
// It deliberately imports nothing from the rest of the project and nothing that
// touches HTTP or SQL. Everything here would still be true if the system were a
// paper register, which is what makes it testable without a database.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Role decides what an account may do. Every account has exactly one (REQ-008).
type Role string

const (
	RoleMember    Role = "member"
	RoleLibrarian Role = "librarian"
	RoleAdmin     Role = "admin"
)

// CanManageLibrary reports whether the role may alter catalogue, copies,
// members or loans. Checked server-side on every protected route; a client-side
// check is not access control (NFR-004).
func (r Role) CanManageLibrary() bool {
	return r == RoleLibrarian || r == RoleAdmin
}

// MemberCategory determines borrowing entitlement. HOL does not grant every
// reader the same access, so neither do we (DOM-005).
type MemberCategory string

const (
	CategoryUndergraduate MemberCategory = "undergraduate"
	CategoryPostgraduate  MemberCategory = "postgraduate"
	CategoryStaff         MemberCategory = "staff"
)

// LoanPolicy records why a copy may or may not leave the building.
//
// Not everything circulates. The Reference Room is consulted in place, and a
// book on the Recent Accessions display "may not be borrowed while on display
// but may be reserved at the Loans desk" (DOM-004).
type LoanPolicy string

const (
	PolicyCirculating   LoanPolicy = "circulating"
	PolicyReferenceOnly LoanPolicy = "reference_only"
	PolicyOnDisplay     LoanPolicy = "on_display"
	PolicyRestricted    LoanPolicy = "restricted"
)

// IsBorrowable reports whether a copy under this policy may be lent out.
func (p LoanPolicy) IsBorrowable() bool { return p == PolicyCirculating }

// IsReservable reports whether a member may join a queue for this copy.
// Display items are reservable precisely because they cannot be borrowed yet.
func (p LoanPolicy) IsReservable() bool {
	return p == PolicyCirculating || p == PolicyOnDisplay
}

// CopyStatus is the physical state of one volume.
type CopyStatus string

const (
	CopyAvailable CopyStatus = "available"
	CopyOnLoan    CopyStatus = "on_loan"
	CopyLost      CopyStatus = "lost"
	CopyDamaged   CopyStatus = "damaged"
	CopyWithdrawn CopyStatus = "withdrawn"
)

// UserStatus controls whether an account may borrow at all (REQ-045).
type UserStatus string

const (
	UserActive    UserStatus = "active"
	UserSuspended UserStatus = "suspended"
	UserInactive  UserStatus = "inactive"
)

// LoanTerms are the borrowing entitlements of one member category.
type LoanTerms struct {
	MaxConcurrentLoans int
	LoanPeriod         time.Duration
}

const day = 24 * time.Hour

// loanTermsByCategory is the single place these numbers live, so changing a
// loan period is a one-line edit rather than a search through the codebase.
//
// These values are an ASSUMPTION (DEC-005), not confirmed library policy. No
// librarian has yet reviewed them. Confirm at the Circulation desk before the
// system is used for real lending.
var loanTermsByCategory = map[MemberCategory]LoanTerms{
	CategoryUndergraduate: {MaxConcurrentLoans: 2, LoanPeriod: 14 * day},
	CategoryPostgraduate:  {MaxConcurrentLoans: 4, LoanPeriod: 21 * day},
	CategoryStaff:         {MaxConcurrentLoans: 6, LoanPeriod: 28 * day},
}

// TermsFor returns the borrowing entitlement for a category.
func TermsFor(c MemberCategory) (LoanTerms, bool) {
	t, ok := loanTermsByCategory[c]
	return t, ok
}

// Wing is a physical half of the library building.
type Wing string

const (
	WingSouth   Wing = "South"
	WingNorth   Wing = "North"
	WingUnknown Wing = "Unknown"
)

// WingFor locates a class mark in the building.
//
// HOL shelves LCC classes A to J in the South wing and K to Z in the North wing,
// so a reader who knows the call number already knows which way to walk. This is
// derived rather than stored: the wing is a fact about the class mark, and a
// stored copy of it could only ever go stale (DOM-003, REQ-027).
func WingFor(lccClass byte) Wing {
	switch {
	case lccClass >= 'A' && lccClass <= 'J':
		return WingSouth
	case lccClass >= 'K' && lccClass <= 'Z':
		return WingNorth
	default:
		return WingUnknown
	}
}

// User is a member of the library or a member of its staff.
type User struct {
	ID                 uuid.UUID
	Identifier         string // matriculation or staff number
	Email              string
	FullName           string
	FirstName          string
	LastName           string
	Faculty            string
	Department         string
	Level              string
	Role               Role
	Category           *MemberCategory // nil for staff accounts
	Status             UserStatus
	MustChangePassword bool
	IsSynthetic        bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// CanBorrow reports whether this account is in a state to receive a book.
func (u User) CanBorrow() bool { return u.Status == UserActive }

// Book is a bibliographic record: one title, shared by all of its copies.
type Book struct {
	ID                 uuid.UUID
	Title              string
	Subtitle           string
	ISBN13             string
	ISBN10             string
	Publisher          string
	PlaceOfPublication string
	PublishedYear      *int
	CallNumber         string // LCC class mark, shared across copies (DOM-002)
	LCCClass           string // first letter, for wing derivation
	Description        string
	Status             string
	Authors            []string
	Subjects           []string
	Availability       Availability
}

// Wing reports which half of the building shelves this title.
func (b Book) Wing() Wing {
	if b.LCCClass == "" {
		return WingUnknown
	}
	return WingFor(b.LCCClass[0])
}

// Availability is a count over copy states, computed on read.
//
// There is deliberately no available_copies column anywhere in the schema. A
// stored counter is a second source of truth, and second sources of truth drift
// (REQ-039).
type Availability struct {
	TotalCopies int `json:"total_copies"`
	Available   int `json:"available"` // on the shelf
	OnLoan      int `json:"on_loan"`
	NotForLoan  int `json:"not_for_loan"` // reference, display, restricted

	// Stock is the lending collection for this title: circulating copies that
	// are either on the shelf or out. Lost, damaged and withdrawn volumes are
	// not stock, so losing a copy correctly relaxes the retention rule.
	Stock int `json:"-"`
}

// Borrowable is how many copies may actually leave the building now, after the
// last-copy retention policy (DEC-018).
func (a Availability) Borrowable() int { return BorrowableCount(a.Stock, a.Available) }

// IsAvailable reports whether a reader could walk in and borrow this title now.
//
// This is the borrowable count, not the shelf count: a title whose only free
// copy is being retained is present in the library but cannot be taken away,
// and telling a reader otherwise would send them on a wasted journey.
func (a Availability) IsAvailable() bool { return a.Borrowable() > 0 }

// OnShelf reports whether a copy can be consulted in the building, even if none
// may be borrowed.
func (a Availability) OnShelf() bool { return a.Available > 0 }

// Copy is one physical volume, identified by the accession number the library
// assigned when it arrived (DOM-002).
type Copy struct {
	ID              uuid.UUID
	BookID          uuid.UUID
	AccessionNumber string
	LoanPolicy      LoanPolicy
	Status          CopyStatus
	AcquiredAt      *time.Time
	Notes           string
}

// Loan records custody of one copy by one member for a period.
//
// Returning a book closes this record; it never deletes it. The library must
// still be able to answer who held a copy last session (DOM-008).
type Loan struct {
	ID         uuid.UUID
	CopyID     uuid.UUID
	UserID     uuid.UUID
	BorrowedAt time.Time
	DueAt      time.Time
	ReturnedAt *time.Time
	IssuedBy   uuid.UUID
	ReturnedTo *uuid.UUID

	// Joined for display.
	BookTitle       string
	AccessionNumber string
	MemberName      string
}

// IsReturned reports whether the copy is back on the shelf.
func (l Loan) IsReturned() bool { return l.ReturnedAt != nil }

// IsOverdueAt reports whether the loan is late as at the given instant.
//
// Overdue is computed, never stored. An is_overdue column would be correct only
// until the clock moved past the next due date (REQ-053).
func (l Loan) IsOverdueAt(now time.Time) bool {
	return l.ReturnedAt == nil && now.After(l.DueAt)
}

// DaysOverdueAt reports how many whole days late the loan is, or zero.
func (l Loan) DaysOverdueAt(now time.Time) int {
	if !l.IsOverdueAt(now) {
		return 0
	}
	return int(now.Sub(l.DueAt).Hours() / 24)
}

// Reservation is a member's place in the queue for a title.
type Reservation struct {
	ID        uuid.UUID
	BookID    uuid.UUID
	UserID    uuid.UUID
	Status    string
	CreatedAt time.Time
	ExpiresAt *time.Time
	QueuePos  int
	BookTitle string
}

// DisplayTimezone is the timezone the library's readers think in.
//
// It is a named zone, not a fixed +01:00 offset: a name continues to be correct
// if Nigeria ever changes its offset, and an offset does not. Nothing in the
// backend stores or compares time in this zone. It exists so that a rendered
// message ("due Thursday at 7:15 PM") reads the way a student in Ile-Ife
// expects, while the instant behind it stays UTC.
const DisplayTimezone = "Africa/Lagos"

// InDisplayTimezone renders an instant in the library's local time.
//
// If the zone cannot be loaded the instant is returned in UTC rather than
// guessed at, because a wrong time shown confidently is worse than a correct
// time shown in an unexpected zone.
func InDisplayTimezone(t time.Time) time.Time {
	loc, err := time.LoadLocation(DisplayTimezone)
	if err != nil {
		return t.UTC()
	}
	return t.In(loc)
}

// copyTransitions is the state machine for a physical volume.
//
// A copy's status is not a free-text field a librarian may set to anything. Some
// transitions are physically meaningless and some are dangerous: moving a copy
// from on_loan straight to available would silently abandon an open loan,
// leaving the library believing a book is on the shelf while a student still
// holds it. Others are simply impossible, like lending a withdrawn volume.
//
// Only the transitions listed here are permitted. DEF-009.
var copyTransitions = map[CopyStatus][]CopyStatus{
	// Borrowing is not done through this map: it goes through the circulation
	// service, which claims the copy atomically and writes a loan in the same
	// transaction (REQ-047).
	CopyAvailable: {CopyDamaged, CopyLost, CopyWithdrawn},

	// A borrowed copy leaves this state by being returned, or by being reported
	// lost or damaged while out. Returning is the circulation service's job;
	// lost and damaged close the loan too, so a librarian never has to fake a
	// return in order to record a real loss.
	CopyOnLoan: {CopyLost, CopyDamaged},

	// A copy found again, or repaired, may return to the shelf.
	CopyLost:    {CopyAvailable, CopyWithdrawn},
	CopyDamaged: {CopyAvailable, CopyWithdrawn},

	// Withdrawn is terminal. A volume removed from the collection does not
	// come back; a replacement is a new copy with its own accession number.
	CopyWithdrawn: {},
}

// CanTransitionTo reports whether a copy may move from its current status to
// the given one. A transition to the same status is a no-op and allowed.
func (c CopyStatus) CanTransitionTo(next CopyStatus) bool {
	if c == next {
		return true
	}
	for _, allowed := range copyTransitions[c] {
		if allowed == next {
			return true
		}
	}
	return false
}

// ClosesAnOpenLoan reports whether moving a copy into this status must also
// close the loan that copy is currently out on.
//
// A copy reported lost or damaged while borrowed still has an open loan against
// it. Leaving that loan open would show the book as forever overdue; closing it
// as a "return" would be a lie in the permanent record. It is closed and marked
// for what it was.
func (c CopyStatus) ClosesAnOpenLoan() bool {
	return c == CopyLost || c == CopyDamaged
}

// LastCopyRetention decides how many of a title's copies may leave the building.
//
// A title held in two or more circulating copies always keeps one on the shelf,
// so a reader who walks in can still consult it. This is a common academic
// library practice and it is **our stated policy**, not a rule taken from the
// LIB 001 material: that document restricts only the Reference, Recent
// Accessions, Serials, Africana and Conservation collections, and says nothing
// about last copies. Recorded as DEC-018 so the distinction survives.
//
// The threshold matters. A blanket "never lend the last copy" would make every
// single-copy title permanently unborrowable, which at HOL would strand most of
// the Africana and OAU Publications holdings. A title held in one copy therefore
// circulates normally: retaining it would mean nobody could ever read it.
func BorrowableCount(stock, available int) int {
	if stock < 2 {
		return available // a single-copy title circulates, or nobody could read it
	}
	if available <= 1 {
		return 0 // the one on the shelf stays there
	}
	return available - 1
}

// RetainsAShelfCopy reports whether this title is subject to the retention rule.
func RetainsAShelfCopy(stock int) bool { return stock >= 2 }

// Bookmark is a member saving a title to come back to.
//
// It is an interest in a TITLE, never a claim on a copy. It reserves nothing,
// joins no queue and changes no availability figure. That is the whole
// difference from a reservation, and it is why a bookmark can exist for a title
// whose every copy is on the shelf.
type Bookmark struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	BookID    uuid.UUID
	CreatedAt time.Time
}

// BookmarkedBook is one entry in a member's saved list: the title itself,
// together with when they saved it.
//
// The whole book travels rather than an id, because every screen that shows a
// bookmark shows the title, its authors and whether a copy can be borrowed.
type BookmarkedBook struct {
	Book    Book
	SavedAt time.Time
}
