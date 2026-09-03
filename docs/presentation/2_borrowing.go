//go:build ignore

package presentation

import "time"

// MemberCategory decides entitlement. A university library does not sell
// tiers; it recognises who you are.
type MemberCategory string

const (
	Undergraduate MemberCategory = "undergraduate" // 2 books, 14 days
	Postgraduate  MemberCategory = "postgraduate"  // 4 books, 21 days
	Staff         MemberCategory = "staff"         // 6 books, 28 days
)

// Borrowing is the behaviour a profile takes part in. Go's answer to an
// abstract base class is an interface: declared here, implemented against the
// database, and therefore testable without one.
type Borrowing interface {
	CanBorrow() bool // status only; limit and copy are separate questions

	Terms() (maxConcurrent int, period time.Duration)

	// Borrow records a copy leaving the building. Performed by a LIBRARIAN,
	// never by the member. Three guards, one transaction, in this order:
	//
	//   1. the copy is claimed by an atomic compare-and-swap, so no window
	//      exists between checking availability and taking it;
	//   2. the member's row is LOCKED before their open loans are counted, or
	//      two librarians each count before the other has inserted;
	//   3. a partial unique index makes a second open loan on one copy
	//      physically unstorable, whatever the application does.
	Borrow(copyID, issuedBy string) (Loan, error)

	// Return closes the loan. Closed, never deleted: the library must still
	// say who held this volume last session.
	Return(loanID, receivedBy string) (Loan, error)

	// History reaches through /me, never an id in the URL, so there is no
	// parameter to tamper with.
	History() ([]Loan, error)
}

// Loan is one custody event. Note what is ABSENT: no status column and no
// is_overdue column. A loan is open while ReturnedAt is nil, and overdue when
// the clock has passed DueAt while still open. A stored flag would be correct
// only until the clock moved.
type Loan struct {
	ID, CopyID, UserID string
	BorrowedAt, DueAt  time.Time
	ReturnedAt         *time.Time // nil means the book is still out
	IssuedBy           string     // which librarian, for the audit trail
}
