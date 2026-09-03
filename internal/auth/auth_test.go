package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
)

func TestHashAndVerifyPassword(t *testing.T) {
	const plain = "correct horse battery staple 7"

	hash, err := auth.HashPassword(plain)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(hash, plain) {
		t.Fatal("the hash must not contain the password")
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("hash should be PHC-formatted argon2id, got %q", hash)
	}

	ok, err := auth.VerifyPassword(plain, hash)
	if err != nil || !ok {
		t.Errorf("the correct password must verify: ok=%v err=%v", ok, err)
	}

	ok, err = auth.VerifyPassword("wrong password 12", hash)
	if err != nil {
		t.Fatalf("verifying a wrong password is not an error: %v", err)
	}
	if ok {
		t.Error("a wrong password must not verify")
	}
}

// Two members who pick the same password must not share a hash, or one cracked
// hash would expose both accounts.
func TestHashPasswordIsSaltedPerPassword(t *testing.T) {
	a, err := auth.HashPassword("shared password 1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.HashPassword("shared password 1")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("identical passwords must produce different hashes")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	for _, bad := range []string{"", "not-a-hash", "$argon2id$broken"} {
		if _, err := auth.VerifyPassword("anything", bad); err == nil {
			t.Errorf("a malformed hash %q must be reported, not silently accepted", bad)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		password string
		wantErr  bool
	}{
		{"library2026", false},
		{"short1", true},         // under the minimum length
		{"allletterspass", true}, // no digit
		{"1234567890", true},     // no letter
	}
	for _, c := range cases {
		err := auth.ValidatePassword(c.password)
		if (err != nil) != c.wantErr {
			t.Errorf("ValidatePassword(%q) error = %v, wantErr %v", c.password, err, c.wantErr)
		}
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), 15*time.Minute, time.Hour)
	id := uuid.New()

	token, err := issuer.IssueAccessToken(id, "librarian", false)
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}

	claims, err := issuer.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	if claims.UserID != id {
		t.Errorf("UserID = %v, want %v", claims.UserID, id)
	}
	if claims.Role != "librarian" {
		t.Errorf("Role = %q, want librarian", claims.Role)
	}
}

// The pending flag rides in the token so the middleware can confine an account
// that still holds a librarian-issued temporary password (DEF-007).
func TestAccessTokenCarriesPendingPasswordChange(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)

	pending, err := issuer.IssueAccessToken(uuid.New(), "member", true)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := issuer.ParseAccessToken(pending)
	if err != nil {
		t.Fatal(err)
	}
	if !claims.Pending {
		t.Error("a token issued for an account with a temporary password must be marked pending")
	}

	settled, err := issuer.IssueAccessToken(uuid.New(), "member", false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err = issuer.ParseAccessToken(settled)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Pending {
		t.Error("a settled account's token must not be marked pending")
	}
}

// A token signed with a different secret must be rejected, or anyone could mint
// an admin session.
func TestAccessTokenRejectsForeignSignature(t *testing.T) {
	real := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)
	forger := auth.NewTokenIssuer(strings.Repeat("x", 32), time.Minute, time.Hour)

	forged, err := forger.IssueAccessToken(uuid.New(), "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := real.ParseAccessToken(forged); err == nil {
		t.Error("a token signed with another secret must not verify")
	}
}

func TestAccessTokenRejectsExpired(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), -time.Minute, time.Hour)

	expired, err := issuer.IssueAccessToken(uuid.New(), "member", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issuer.ParseAccessToken(expired); err == nil {
		t.Error("an expired token must not verify")
	}
}

func TestOpaqueTokenIsRandomAndHashed(t *testing.T) {
	token, hash, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || hash == "" {
		t.Fatal("token and hash must both be produced")
	}
	if token == hash {
		t.Error("the stored hash must differ from the token handed to the user")
	}
	if auth.HashToken(token) != hash {
		t.Error("HashToken must reproduce the stored hash for lookup")
	}

	other, _, err := auth.NewOpaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if other == token {
		t.Error("two generated tokens must not collide")
	}
}
