package domain

import "errors"

// Business errors. The service layer returns these; the HTTP layer maps each to
// a status code and a stable machine-readable code in transport/http/response.
//
// They are values rather than strings so callers can test with errors.Is and the
// mapping lives in exactly one place.
var (
	// Authentication and accounts.
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("authentication required")
	ErrForbidden          = errors.New("not permitted")
	ErrPasswordTooWeak    = errors.New("password does not meet the minimum policy")
	ErrTokenInvalid       = errors.New("token is invalid or has expired")
	ErrMustChangePassword = errors.New("password change required before continuing")

	// Lookup.
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflicts with existing data")

	// Catalogue and inventory.
	ErrDuplicateAccession = errors.New("accession number already exists")
	ErrDuplicateISBN      = errors.New("a book with this ISBN already exists")
	ErrInvalidCallNumber  = errors.New("call number is not a valid LCC class mark")

	// Circulation. These are the rules that make the system a library rather
	// than a database with a web page in front of it.
	ErrCopyNotAvailable  = errors.New("that copy is not available")
	ErrCopyNotBorrowable = errors.New("that copy does not circulate")
	ErrLoanLimitReached  = errors.New("borrowing limit reached for this member category")
	ErrMemberNotActive   = errors.New("member account is not active")
	ErrLoanAlreadyClosed = errors.New("that loan has already been returned")
	ErrNoCategory        = errors.New("member has no borrowing category")

	// Reservations.
	ErrAlreadyReserved = errors.New("you already have an open reservation for this title")
	ErrNotReservable   = errors.New("that title cannot be reserved")
	ErrCopiesAvailable = errors.New("copies are available; reservation is unnecessary")
)

// Copy lifecycle.
var (
	ErrInvalidTransition = errors.New("that is not a valid change of status for this copy")
	ErrCopyOnLoan        = errors.New("that copy is currently on loan")
)
