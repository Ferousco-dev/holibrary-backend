// Package service holds the use cases: the rules that make this a library
// rather than a database with a web page in front of it.
//
// Nothing here imports net/http or database/sql. Services depend on repository
// interfaces, which is what lets the rules be tested with fakes and no database
// (docs/design.md DES-002).
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// UserStore is the slice of persistence the auth service needs.
type UserStore interface {
	FindByLogin(ctx context.Context, login string) (domain.User, string, error)
	FindByID(ctx context.Context, id uuid.UUID) (domain.User, error)
	FindByEmail(ctx context.Context, email string) (domain.User, error)
	PasswordHash(ctx context.Context, id uuid.UUID) (string, error)
	UpdatePassword(ctx context.Context, id uuid.UUID, hash string) error
}

// TokenStore persists the revocable half of a session.
type TokenStore interface {
	SaveRefreshToken(ctx context.Context, userID uuid.UUID, hash string, expires time.Time) error
	ConsumeRefreshToken(ctx context.Context, hash string) (uuid.UUID, error)
	RevokeRefreshToken(ctx context.Context, hash string) error
	RevokeAllRefreshTokens(ctx context.Context, userID uuid.UUID) error
	SavePasswordReset(ctx context.Context, userID uuid.UUID, hash string, expires time.Time) error
	ConsumePasswordReset(ctx context.Context, hash string) (uuid.UUID, error)
}

// Notifier queues a message. Delivery happens elsewhere, asynchronously, so a
// slow mail provider can never slow down an API response (REQ-072).
type Notifier interface {
	Queue(ctx context.Context, userID uuid.UUID, channel, template string, payload map[string]any) error
}

type AuthService struct {
	users    UserStore
	tokens   TokenStore
	notifier Notifier
	issuer   *auth.TokenIssuer
}

func NewAuthService(u UserStore, t TokenStore, n Notifier, i *auth.TokenIssuer) *AuthService {
	return &AuthService{users: u, tokens: t, notifier: n, issuer: i}
}

// Session is what a successful login hands back.
//
// MustChangePassword is not merely advisory. A member holding a temporary
// password issued at the desk is confined to changing it: the access token
// carries the flag and the middleware refuses every other route until the
// change is made. Otherwise a temporary password handed over on paper would be
// a fully working credential for as long as the member ignored the prompt.
// DEF-007.
type Session struct {
	AccessToken        string      `json:"access_token"`
	RefreshToken       string      `json:"refresh_token"`
	ExpiresIn          int         `json:"expires_in"`
	MustChangePassword bool        `json:"must_change_password"`
	User               domain.User `json:"-"`
}

// Login authenticates a member or staff account (REQ-001).
//
// There is no sign-up counterpart. Accounts are created at the library desk
// after the applicant presents an identity card, exactly as HOL does it today
// (DOM-006, DEC-006).
func (s *AuthService) Login(ctx context.Context, login, password string) (Session, error) {
	user, hash, err := s.users.FindByLogin(ctx, login)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// A distinct "no such user" reply would turn this endpoint into a
			// way to discover which matriculation numbers are registered.
			return Session{}, domain.ErrInvalidCredentials
		}
		return Session{}, err
	}

	ok, err := auth.VerifyPassword(password, hash)
	if err != nil || !ok {
		return Session{}, domain.ErrInvalidCredentials
	}

	// A suspended member may not sign in at all, so a lost card cannot be used
	// to browse someone's borrowing history (REQ-045, DOM-009).
	if user.Status != domain.UserActive {
		return Session{}, domain.ErrMemberNotActive
	}

	return s.issueSession(ctx, user)
}

func (s *AuthService) issueSession(ctx context.Context, user domain.User) (Session, error) {
	access, err := s.issuer.IssueAccessToken(user.ID, string(user.Role), user.MustChangePassword)
	if err != nil {
		return Session{}, err
	}

	refresh, refreshHash, err := auth.NewOpaqueToken()
	if err != nil {
		return Session{}, err
	}
	expires := time.Now().UTC().Add(s.issuer.RefreshTTL())
	if err := s.tokens.SaveRefreshToken(ctx, user.ID, refreshHash, expires); err != nil {
		return Session{}, err
	}

	return Session{
		AccessToken:        access,
		RefreshToken:       refresh,
		ExpiresIn:          int(s.issuer.AccessTTL().Seconds()),
		MustChangePassword: user.MustChangePassword,
		User:               user,
	}, nil
}

// Refresh exchanges a refresh token for a new session.
//
// The old token is consumed as part of the exchange. Rotating on every use means
// a stolen token is good for one call at most, and reuse of a spent token is
// detectable.
func (s *AuthService) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	userID, err := s.tokens.ConsumeRefreshToken(ctx, auth.HashToken(refreshToken))
	if err != nil {
		return Session{}, domain.ErrTokenInvalid
	}
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return Session{}, domain.ErrTokenInvalid
	}
	if user.Status != domain.UserActive {
		return Session{}, domain.ErrMemberNotActive
	}
	return s.issueSession(ctx, user)
}

// Logout revokes the refresh token (REQ-006). The access token is left to expire
// on its own, which is the trade-off that comes with stateless access tokens and
// is why they are short-lived (NFR-003).
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	return s.tokens.RevokeRefreshToken(ctx, auth.HashToken(refreshToken))
}

// ChangePassword updates a password for a signed-in user (REQ-003).
//
// The current password is required, so a borrowed unlocked laptop is not enough
// to take over an account.
func (s *AuthService) ChangePassword(ctx context.Context, userID uuid.UUID, current, next string) error {
	hash, err := s.users.PasswordHash(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := auth.VerifyPassword(current, hash)
	if err != nil || !ok {
		return domain.ErrInvalidCredentials
	}
	if err := auth.ValidatePassword(next); err != nil {
		return fmt.Errorf("%w: %s", domain.ErrPasswordTooWeak, err)
	}

	newHash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(ctx, userID, newHash); err != nil {
		return err
	}

	// Changing a password is how someone reacts to a suspected compromise, so
	// it must end every other session. Previously the old refresh token kept
	// working and the change achieved nothing against an attacker who already
	// held one. DEF-006.
	return s.tokens.RevokeAllRefreshTokens(ctx, userID)
}

// PasswordResetTTL is deliberately short: the token arrives by email, and an
// email inbox is not a vault (REQ-005).
const PasswordResetTTL = 30 * time.Minute

// RequestPasswordReset queues a reset link (REQ-004).
//
// It returns nil whether or not the address is registered. Reporting "no such
// account" would let anyone test addresses against the member roll, which is a
// real privacy leak given what a borrowing history reveals (DOM-009).
func (s *AuthService) RequestPasswordReset(ctx context.Context, email string) error {
	user, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}

	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return err
	}
	if err := s.tokens.SavePasswordReset(ctx, user.ID, hash, time.Now().UTC().Add(PasswordResetTTL)); err != nil {
		return err
	}

	return s.notifier.Queue(ctx, user.ID, "email", "password_reset", map[string]any{
		"full_name": user.FullName,
		"token":     token,
	})
}

// ResetPassword completes a reset. The token is single-use and expiring;
// consuming it is the store's job (REQ-005).
func (s *AuthService) ResetPassword(ctx context.Context, token, next string) error {
	if err := auth.ValidatePassword(next); err != nil {
		return fmt.Errorf("%w: %s", domain.ErrPasswordTooWeak, err)
	}
	userID, err := s.tokens.ConsumePasswordReset(ctx, auth.HashToken(token))
	if err != nil {
		return domain.ErrTokenInvalid
	}
	hash, err := auth.HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.users.UpdatePassword(ctx, userID, hash); err != nil {
		return err
	}

	// A reset is the stronger case: the person resetting may be locked out
	// precisely because someone else holds a session. Revoke them all. DEF-006.
	return s.tokens.RevokeAllRefreshTokens(ctx, userID)
}
