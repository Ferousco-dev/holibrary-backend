package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/ratelimit"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

// --- fakes -----------------------------------------------------------------

type fakeUsers struct {
	user          domain.User
	hash          string
	findErr       error
	newHash       string
	updateErr     error
	invalidBefore time.Time
	// tokens is the paired token store, so a password change stamps both, as
	// one SQL statement and one join would.
	tokens *fakeTokens
}

func (f *fakeUsers) FindByLogin(_ context.Context, _ string) (domain.User, string, error) {
	if f.findErr != nil {
		return domain.User{}, "", f.findErr
	}
	return f.user, f.hash, nil
}
func (f *fakeUsers) FindByID(_ context.Context, _ uuid.UUID) (domain.User, error) {
	return f.user, f.findErr
}
func (f *fakeUsers) FindByEmail(_ context.Context, _ string) (domain.User, error) {
	return f.user, f.findErr
}
func (f *fakeUsers) PasswordHash(_ context.Context, _ uuid.UUID) (string, error) {
	return f.hash, nil
}
func (f *fakeUsers) UpdatePassword(_ context.Context, _ uuid.UUID, hash string) error {
	f.newHash = hash
	// The real statement stamps tokens_invalid_before alongside the new hash.
	stamp := time.Now().UTC().Add(time.Millisecond)
	f.invalidBefore = stamp
	if f.tokens != nil {
		f.tokens.invalidBefore = &stamp
	}
	return f.updateErr
}
func (f *fakeUsers) TokensInvalidBefore(context.Context, uuid.UUID) (time.Time, error) {
	return f.invalidBefore, nil
}

// fakeTokens mirrors what the real store does, including the rule that a
// refresh token issued before its owner's last password change is dead.
//
// That rule lives in SQL, inside the statement that consumes the token. A fake
// that omits it does not merely under-test: it actively asserts that the wrong
// thing is correct. An earlier version of this fake ignored the stamp, so the
// regression test for DEF-015 passed against an implementation that in fact
// rejected nothing at all (DEF-020).
type fakeTokens struct {
	savedRefresh  string
	refreshOwner  uuid.UUID
	refreshIssued time.Time
	invalidBefore *time.Time // shared with the user store, as the real join is
	consumeErr    error
	revoked       string
	revokedAllFor uuid.UUID
	savedReset    string
	resetOwner    uuid.UUID
	resetErr      error
}

func (f *fakeTokens) SaveRefreshToken(_ context.Context, u uuid.UUID, hash string, _ time.Time) error {
	f.savedRefresh, f.refreshOwner = hash, u
	f.refreshIssued = time.Now().UTC()
	return nil
}

func (f *fakeTokens) ConsumeRefreshToken(_ context.Context, _ string) (uuid.UUID, error) {
	if f.consumeErr != nil {
		return uuid.Nil, f.consumeErr
	}
	// The real query joins users and requires
	//   refresh_tokens.created_at >= users.tokens_invalid_before
	// so a token minted before the last password change matches no row.
	if f.invalidBefore != nil && f.refreshIssued.Before(*f.invalidBefore) {
		return uuid.Nil, domain.ErrNotFound
	}
	return f.refreshOwner, nil
}
func (f *fakeTokens) RevokeRefreshToken(_ context.Context, hash string) error {
	f.revoked = hash
	return nil
}
func (f *fakeTokens) RevokeAllRefreshTokens(_ context.Context, u uuid.UUID) error {
	f.revokedAllFor = u
	return nil
}
func (f *fakeTokens) SavePasswordReset(_ context.Context, u uuid.UUID, hash string, _ time.Time) error {
	f.savedReset, f.resetOwner = hash, u
	return nil
}
func (f *fakeTokens) ConsumePasswordReset(_ context.Context, _ string) (uuid.UUID, error) {
	return f.resetOwner, f.resetErr
}

const testSecret = "a-test-secret-at-least-32-characters"

func newAuth(t *testing.T, u *fakeUsers, tk *fakeTokens, n *fakeNotifier) *service.AuthService {
	t.Helper()
	return service.NewAuthService(u, tk, n,
		auth.NewTokenIssuer(testSecret, 15*time.Minute, 7*24*time.Hour),
		ratelimit.NewMemory())
}

func activeMember(t *testing.T, password string) (*fakeUsers, domain.User) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	cat := domain.CategoryUndergraduate
	u := domain.User{
		ID: uuid.New(), Identifier: "SWE/2025/001", Email: "f@oauife.edu.ng",
		Role: domain.RoleMember, Category: &cat, Status: domain.UserActive,
	}
	return &fakeUsers{user: u, hash: hash}, u
}

// --- login -----------------------------------------------------------------

func TestLoginIssuesASession(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	session, err := svc.Login(context.Background(), "SWE/2025/001", "library2026x")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		t.Fatal("both tokens must be issued")
	}
	if session.User.ID != user.ID {
		t.Error("the session must carry the authenticated user")
	}

	// Only the hash of the refresh token is stored, so a database leak yields
	// no usable session.
	if tokens.savedRefresh == session.RefreshToken {
		t.Error("the refresh token must be stored hashed, not in the clear")
	}
	if tokens.savedRefresh != auth.HashToken(session.RefreshToken) {
		t.Error("the stored value must be the hash of the issued token")
	}
}

func TestLoginRejectsAWrongPassword(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	svc := newAuth(t, users, &fakeTokens{}, &fakeNotifier{})

	if _, err := svc.Login(context.Background(), "SWE/2025/001", "not-the-password"); !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("error = %v, want ErrInvalidCredentials", err)
	}
}

// An unknown account and a wrong password must be indistinguishable. A distinct
// "no such user" reply would turn login into a way to discover which
// matriculation numbers are registered.
func TestLoginDoesNotRevealWhetherAnAccountExists(t *testing.T) {
	missing := &fakeUsers{findErr: domain.ErrNotFound}
	svc := newAuth(t, missing, &fakeTokens{}, &fakeNotifier{})

	_, errUnknown := svc.Login(context.Background(), "SWE/9999/999", "whatever12")

	users, _ := activeMember(t, "library2026x")
	svc = newAuth(t, users, &fakeTokens{}, &fakeNotifier{})
	_, errWrongPass := svc.Login(context.Background(), "SWE/2025/001", "wrongpass12")

	if !errors.Is(errUnknown, domain.ErrInvalidCredentials) {
		t.Errorf("unknown account gave %v, want ErrInvalidCredentials", errUnknown)
	}
	if errUnknown.Error() != errWrongPass.Error() {
		t.Errorf("the two cases must be indistinguishable: %q vs %q", errUnknown, errWrongPass)
	}
}

// A suspended member cannot sign in at all, so a lost library card cannot be
// used to read someone's borrowing history.
func TestLoginRejectsSuspendedAndInactiveMembers(t *testing.T) {
	for _, status := range []domain.UserStatus{domain.UserSuspended, domain.UserInactive} {
		users, _ := activeMember(t, "library2026x")
		users.user.Status = status
		svc := newAuth(t, users, &fakeTokens{}, &fakeNotifier{})

		if _, err := svc.Login(context.Background(), "SWE/2025/001", "library2026x"); !errors.Is(err, domain.ErrMemberNotActive) {
			t.Errorf("%s: error = %v, want ErrMemberNotActive", status, err)
		}
	}
}

// The flag must survive into the session, because it is what confines an
// account still holding a librarian-issued temporary password.
func TestLoginCarriesMustChangePassword(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	users.user.MustChangePassword = true
	svc := newAuth(t, users, &fakeTokens{}, &fakeNotifier{})

	session, err := svc.Login(context.Background(), "SWE/2025/001", "library2026x")
	if err != nil {
		t.Fatal(err)
	}
	if !session.MustChangePassword {
		t.Error("the session must report that a password change is required")
	}
}

// --- refresh and logout ----------------------------------------------------

func TestRefreshRotatesTheToken(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{refreshOwner: user.ID}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	first, err := svc.Refresh(context.Background(), "some-refresh-token")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if first.RefreshToken == "some-refresh-token" {
		t.Error("refresh must issue a new token, so a stolen one is good for one call only")
	}
}

func TestRefreshRejectsASpentOrUnknownToken(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	tokens := &fakeTokens{consumeErr: domain.ErrNotFound}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if _, err := svc.Refresh(context.Background(), "spent"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("error = %v, want ErrTokenInvalid", err)
	}
}

// Suspending a member must end their ability to renew a session, or the
// suspension only takes effect when the access token happens to expire.
func TestRefreshRejectsASuspendedMember(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	users.user.Status = domain.UserSuspended
	tokens := &fakeTokens{refreshOwner: user.ID}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if _, err := svc.Refresh(context.Background(), "valid"); !errors.Is(err, domain.ErrMemberNotActive) {
		t.Errorf("error = %v, want ErrMemberNotActive", err)
	}
}

func TestLogoutRevokesTheRefreshToken(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	tokens := &fakeTokens{}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if err := svc.Logout(context.Background(), "the-token"); err != nil {
		t.Fatal(err)
	}
	if tokens.revoked != auth.HashToken("the-token") {
		t.Error("logout must revoke the presented token, matched by its hash")
	}
}

// --- password change -------------------------------------------------------

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	err := svc.ChangePassword(context.Background(), user.ID, "the-wrong-one", "newpassword99")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("error = %v, want ErrInvalidCredentials", err)
	}
	if users.newHash != "" {
		t.Error("the password must not have changed")
	}
}

// Changing a password is how someone reacts to a suspected compromise. If old
// refresh tokens kept working it would achieve nothing.
func TestChangePasswordEndsEveryOtherSession(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if err := svc.ChangePassword(context.Background(), user.ID, "library2026x", "newpassword99"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if users.newHash == "" {
		t.Fatal("the new password must be stored")
	}
	if strings.Contains(users.newHash, "newpassword99") {
		t.Error("the stored value must be a hash, not the password")
	}
	if tokens.revokedAllFor != user.ID {
		t.Error("every refresh token for the account must be revoked")
	}
}

func TestChangePasswordEnforcesTheMinimumPolicy(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	svc := newAuth(t, users, &fakeTokens{}, &fakeNotifier{})

	if err := svc.ChangePassword(context.Background(), user.ID, "library2026x", "short1"); !errors.Is(err, domain.ErrPasswordTooWeak) {
		t.Errorf("error = %v, want ErrPasswordTooWeak", err)
	}
}

// --- password reset --------------------------------------------------------

// The reply is the same whether or not the address is registered, so the
// endpoint cannot be used to test addresses against the member roll.
func TestRequestPasswordResetIsSilentAboutUnknownAddresses(t *testing.T) {
	missing := &fakeUsers{findErr: domain.ErrNotFound}
	tokens := &fakeTokens{}
	notifier := &fakeNotifier{}
	svc := newAuth(t, missing, tokens, notifier)

	if err := svc.RequestPasswordReset(context.Background(), "nobody@oauife.edu.ng"); err != nil {
		t.Errorf("an unknown address must not produce an error: %v", err)
	}
	if tokens.savedReset != "" {
		t.Error("no reset token should be created for an unknown address")
	}
	if len(notifier.queued) != 0 {
		t.Error("no mail should be queued for an unknown address")
	}
}

func TestRequestPasswordResetQueuesALink(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{}
	notifier := &fakeNotifier{}
	svc := newAuth(t, users, tokens, notifier)

	if err := svc.RequestPasswordReset(context.Background(), user.Email); err != nil {
		t.Fatal(err)
	}
	if tokens.savedReset == "" {
		t.Fatal("a reset token must be stored")
	}
	if tokens.resetOwner != user.ID {
		t.Error("the token must belong to the requesting account")
	}
	if len(notifier.queued) != 1 || notifier.queued[0] != "password_reset" {
		t.Errorf("queued = %v, want one password_reset", notifier.queued)
	}
}

func TestResetPasswordRejectsAnInvalidToken(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	tokens := &fakeTokens{resetErr: domain.ErrTokenInvalid}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if err := svc.ResetPassword(context.Background(), "expired", "newpassword99"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("error = %v, want ErrTokenInvalid", err)
	}
}

// A reset is the stronger case for revocation: the person resetting may be
// locked out precisely because someone else holds a session.
func TestResetPasswordEndsEveryOtherSession(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{resetOwner: user.ID}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if err := svc.ResetPassword(context.Background(), "a-valid-token", "newpassword99"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if users.newHash == "" {
		t.Error("the new password must be stored")
	}
	if tokens.revokedAllFor != user.ID {
		t.Error("every refresh token for the account must be revoked")
	}
}

func TestResetPasswordEnforcesTheMinimumPolicy(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{resetOwner: user.ID}
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	if err := svc.ResetPassword(context.Background(), "a-valid-token", "weak"); !errors.Is(err, domain.ErrPasswordTooWeak) {
		t.Errorf("error = %v, want ErrPasswordTooWeak", err)
	}
	if users.newHash != "" {
		t.Error("a password that fails the policy must not be stored")
	}
}

// A refresh must not complete across a password change. The stolen token is
// consumed, but the account's stamp has moved past it, so no new session is
// issued (DEF-015).
func TestRefreshIsRejectedAfterAPasswordChange(t *testing.T) {
	users, user := activeMember(t, "library2026x")
	tokens := &fakeTokens{refreshOwner: user.ID, refreshIssued: time.Now().UTC()}
	users.tokens = tokens
	svc := newAuth(t, users, tokens, &fakeNotifier{})

	// Before the change, refresh works.
	if _, err := svc.Refresh(context.Background(), "stolen"); err != nil {
		t.Fatalf("refresh should work before the change: %v", err)
	}

	// The victim changes their password.
	if err := svc.ChangePassword(context.Background(), user.ID, "library2026x", "newpassword99"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// The attacker's refresh must now fail, even though the token store would
	// still hand back an owner.
	if _, err := svc.Refresh(context.Background(), "stolen"); !errors.Is(err, domain.ErrTokenInvalid) {
		t.Errorf("refresh after a password change: err = %v, want ErrTokenInvalid", err)
	}
}

// Five attempts a minute against one account, whoever is asking. This is the
// control an attacker cannot dodge by changing address (DEF-019).
func TestLoginIsRateLimitedPerAccount(t *testing.T) {
	users, _ := activeMember(t, "library2026x")
	svc := newAuth(t, users, &fakeTokens{}, &fakeNotifier{})

	var limited bool
	for i := 0; i < 8; i++ {
		_, err := svc.Login(context.Background(), "SWE/2025/001", "wrong password 12")
		if errors.Is(err, domain.ErrRateLimited) {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("repeated attempts against one account must be rate limited")
	}

	// A different account is unaffected: the limit is per account, so one
	// student cannot lock out another by guessing at them.
	other, _ := activeMember(t, "library2026x")
	other.user.Identifier = "SWE/2025/999"
	svcOther := newAuth(t, other, &fakeTokens{}, &fakeNotifier{})
	if _, err := svcOther.Login(context.Background(), "SWE/2025/999", "wrong password 12"); errors.Is(err, domain.ErrRateLimited) {
		t.Error("a different account must not inherit another account's limit")
	}
}
