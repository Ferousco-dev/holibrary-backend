//go:build ignore

// Package presentation holds the data model as it appears in the project
// report and the defence slides.
//
// It is excluded from the build with a tag, because its job is to be read
// rather than compiled. Every field and rule below is real and traceable to
// internal/domain and to the migrations; this file gathers one profile into a
// single page so it can be shown and explained.

package presentation

import "time"

// PlanTier has no equivalent here. A university library does not sell tiers,
// it recognises who you are: an undergraduate, a postgraduate, or staff. That
// distinction is what decides how many books you may hold and for how long.
type MemberCategory string

const (
	Undergraduate MemberCategory = "undergraduate" // 2 books, 14 days
	Postgraduate  MemberCategory = "postgraduate"  // 4 books, 21 days
	Staff         MemberCategory = "staff"         // 6 books, 28 days
)

// Role decides what an account may do. Exactly one per account.
type Role string

const (
	RoleMember    Role = "member"    // reads the catalogue and their own record
	RoleLibrarian Role = "librarian" // manages the collection and the Loans desk
	RoleAdmin     Role = "admin"     // manages staff, reads the audit log
)

// UserStatus controls whether an account may borrow at all.
type UserStatus string

const (
	Active    UserStatus = "active"
	Suspended UserStatus = "suspended" // may not sign in, may not borrow
	Inactive  UserStatus = "inactive"  // graduated, left, or never activated
)

// ---------------------------------------------------------------------------

// Member is the profile of one person known to Hezekiah Oluwasanmi Library.
//
// Membership begins in the building. There is no public sign-up: a librarian
// creates the account after the applicant presents an identity card and signs
// the register, exactly as HOL works today. That single decision removes every
// attack that starts with an attacker creating their own account.
type Member struct {

	// Identity ----------------------------------------------------------

	// ID is the system's own identifier, a UUID rather than a running number.
	// A sequential id in a URL tells an attacker how many members exist and
	// invites them to walk the range.
	ID string

	// Identifier is the matriculation or staff number printed on the card:
	// SWE/2025/001. UNIQUE, and one half of what makes a person one account.
	// Indexed for exact lookup and again for trigram search, because a
	// librarian at the desk searches by fragment, not by whole number.
	Identifier string

	// Email is the university address. UNIQUE and case-insensitive (citext),
	// so Ada@oauife and ada@oauife cannot become two people.
	Email string

	// FullName is the display name. It may be supplied whole, or assembled
	// from the parts below, because a departmental CSV export gives whichever
	// the department happened to keep.
	FullName  string
	FirstName string
	LastName  string

	// Where the member belongs. Captured at registration, since the librarian
	// already has the card in hand, and indexed together so the roll can be
	// searched the way a librarian thinks: the 200-level Software Engineering
	// students.
	Faculty    string
	Department string
	Level      string

	// Entitlement -------------------------------------------------------

	// Role is checked on the SERVER for every protected route. A frontend that
	// hides the admin menu is a convenience for honest users, not a control:
	// the API is what an attacker talks to.
	Role Role

	// Category decides borrowing limits and loan period. Required for members
	// and null for staff accounts, enforced by a CHECK constraint rather than
	// by hope: a member without a category has no defined entitlement, and
	// guessing one would quietly grant the wrong terms.
	Category *MemberCategory

	// Status. A suspended member cannot sign in at all, so a lost card cannot
	// be used to read somebody's borrowing history.
	Status UserStatus

	// Credentials -------------------------------------------------------

	// PasswordHash is Argon2id with a per-password salt, memory-hard so that
	// GPU cracking is expensive. It is NEVER serialised: no API response
	// carries it, and it is absent from every DTO the transport layer builds.
	PasswordHash string

	// MustChangePassword is set when a librarian issues a temporary password
	// at the desk. It is not advisory: the flag travels in the access token,
	// and the middleware refuses every route except password-change until it
	// is cleared. A temporary password handed over on paper is not a working
	// credential.
	MustChangePassword bool

	// TokensInvalidBefore voids every session issued before this instant. It
	// is stamped in the SAME statement as a new password hash, so a concurrent
	// refresh cannot slip through the gap and mint a surviving session.
	// Revoking tokens as a separate step left exactly that window.
	TokensInvalidBefore time.Time

	// Provenance --------------------------------------------------------

	// IsSynthetic marks a borrower created by the activity simulator, so a
	// generated member can never be mistaken for a real student in a report,
	// and so every trace of the simulator is removable in one statement.
	IsSynthetic bool

	// Timestamps are TIMESTAMPTZ, stored in UTC and displayed in Africa/Lagos.
	// Plain TIMESTAMP records a wall-clock reading with no zone attached, so a
	// due date written in Lagos and read from a UTC server is silently an hour
	// wrong and nothing raises an error.
	LastLoginAt       *time.Time
	PasswordChangedAt *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ---------------------------------------------------------------------------

// Borrowing is the behaviour a member profile takes part in.
//
// Go's equivalent of an abstract base class is an interface: the rules are
// declared here and implemented against the database, which is what lets them
// be tested with no database at all.
type Borrowing interface {

	// CanBorrow reports whether this account is in a state to receive a book.
	// Status only; the limit and the copy are separate questions.
	CanBorrow() bool

	// Terms returns the entitlement for this member's category: how many books
	// at once, and for how long.
	Terms() (maxConcurrent int, period time.Duration)

	// Borrow records a physical copy leaving the building.
	//
	// Performed by a LIBRARIAN, never by the member. Three guards run in one
	// transaction, and the order matters:
	//
	//   1. the copy is claimed with an atomic compare-and-swap, so there is no
	//      window between checking availability and taking it;
	//   2. the member's row is locked before their open loans are counted, or
	//      two librarians serving the same person each count before the other
	//      has inserted, and both let them past their limit;
	//   3. a partial unique index on open loans makes a second loan against
	//      one copy physically unstorable, whatever the application does.
	Borrow(copyID string, issuedBy string) (Loan, error)

	// Return closes a loan and puts the copy back on the shelf. The record is
	// closed, never deleted: the library must still be able to say who held
	// this volume last session.
	Return(loanID string, receivedBy string) (Loan, error)

	// History is every loan this member has ever had, returned ones included.
	// Reached through /me, never through an id in the URL, so there is no
	// parameter to tamper with and one member cannot read another's reading.
	History() ([]Loan, error)
}

// Loan is one custody event: this member, this physical copy, this desk.
//
// Note what is NOT here. There is no status column and no is_overdue column.
// Both are derived: a loan is open while ReturnedAt is nil, and overdue when
// the clock has passed DueAt while it is still open. A stored flag would be
// correct only until the clock moved.
type Loan struct {
	ID         string
	CopyID     string // the physical volume, identified by accession number
	UserID     string
	BorrowedAt time.Time
	DueAt      time.Time  // computed from the member's category
	ReturnedAt *time.Time // nil means the book is still out
	IssuedBy   string     // which librarian, for the audit trail
	ReturnedTo *string
}
