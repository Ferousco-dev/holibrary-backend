package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the payload of an access token.
//
// It carries the role so that authorisation does not need a database round trip
// on every request. The cost of that is a 15-minute window in which a revoked
// role is still honoured, which is why access tokens are short-lived and
// refresh tokens are the revocable half of the pair (NFR-003).
type Claims struct {
	UserID uuid.UUID `json:"uid"`
	Role   string    `json:"role"`
	jwt.RegisteredClaims
}

// TokenIssuer mints and verifies access tokens.
type TokenIssuer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

func NewTokenIssuer(secret string, accessTTL, refreshTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{secret: []byte(secret), accessTTL: accessTTL, refreshTTL: refreshTTL}
}

func (t *TokenIssuer) RefreshTTL() time.Duration { return t.refreshTTL }
func (t *TokenIssuer) AccessTTL() time.Duration  { return t.accessTTL }

// IssueAccessToken returns a signed JWT for the given user.
func (t *TokenIssuer) IssueAccessToken(userID uuid.UUID, role string) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(t.accessTTL)),
			Issuer:    "holibrary",
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	if err != nil {
		return "", fmt.Errorf("signing access token: %w", err)
	}
	return signed, nil
}

// ParseAccessToken verifies a token's signature and expiry and returns its claims.
func (t *TokenIssuer) ParseAccessToken(raw string) (*Claims, error) {
	parsed, err := jwt.ParseWithClaims(raw, &Claims{}, func(tok *jwt.Token) (any, error) {
		// Pinning the algorithm is what stops the "alg: none" and
		// HMAC-with-the-public-key substitution attacks.
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", tok.Header["alg"])
		}
		return t.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return nil, err
	}

	claims, ok := parsed.Claims.(*Claims)
	if !ok || !parsed.Valid {
		return nil, fmt.Errorf("token claims are not valid")
	}
	return claims, nil
}

// NewOpaqueToken returns a random token and its SHA-256 hash.
//
// Refresh and password-reset tokens are opaque rather than JWTs because they
// must be revocable, and only the hash is stored: a leaked database then yields
// no usable session (NFR-003, REQ-005).
func NewOpaqueToken() (token string, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generating token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken returns the storage form of an opaque token.
//
// SHA-256 without a salt is correct here, unlike for passwords: the input is 32
// bytes of entropy from the system CSPRNG, so there is no dictionary to attack
// and the lookup must be by exact hash.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
