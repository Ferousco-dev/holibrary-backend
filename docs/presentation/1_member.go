//go:build ignore

package presentation

import "time"

// Member is one person known to Hezekiah Oluwasanmi Library.
//
// Membership begins in the building: a librarian creates the account after the
// applicant presents an identity card. There is no public sign-up.
type Member struct {

	// --- identity -------------------------------------------------------

	ID string // UUID, not a running number: a sequential id invites walking

	Identifier string // matric or staff number, UNIQUE, e.g. SWE/2025/001
	Email      string // UNIQUE, citext: Ada@ and ada@ are one person

	FullName  string // supplied whole, or built from the two below
	FirstName string
	LastName  string

	Faculty           string // captured at the desk, card already in hand
	Department, Level string // indexed together: "200-level SEN students"

	// --- entitlement ----------------------------------------------------

	Role Role // member | librarian | admin, checked on the SERVER every time

	// Required for members, null for staff, enforced by a CHECK constraint.
	// A member without a category has no defined borrowing entitlement.
	Category *MemberCategory

	Status UserStatus // suspended cannot sign in, so a lost card is useless

	// --- credentials ----------------------------------------------------

	PasswordHash string // Argon2id, per-password salt. Never serialised.

	// Set when a librarian issues a temporary password. Not advisory: it
	// travels in the token, and every route except password-change is refused
	// until it clears. A password handed over on paper is not a credential.
	MustChangePassword bool

	// Voids every session issued before this instant. Stamped in the SAME
	// statement as a new password hash, so a concurrent refresh cannot slip
	// through the gap and mint a session that survives the change.
	TokensInvalidBefore time.Time

	// --- provenance -----------------------------------------------------

	IsSynthetic bool // simulator-created; never mistakable for a real student

	// TIMESTAMPTZ: stored UTC, shown Africa/Lagos. Plain TIMESTAMP has no
	// zone, so a due date crossing a server is silently an hour out.
	CreatedAt, UpdatedAt time.Time
}
