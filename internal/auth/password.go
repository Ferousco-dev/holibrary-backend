// Package auth handles password hashing and token issuing.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. Argon2id won the Password Hashing Competition and is
// memory-hard, which is what makes GPU cracking expensive; a plain SHA hash is
// not a password hash however many times it is applied (NFR-002).
//
// 64 MB and one pass is the low end of the OWASP guidance, chosen because the
// service runs on a small free-tier container. Raise memory before iterations
// if more budget becomes available.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 1
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

var ErrInvalidHashFormat = errors.New("stored password hash is malformed")

// HashPassword returns a PHC-formatted Argon2id hash.
//
// The salt is random per password, so two members who choose the same password
// do not share a hash and a stolen table cannot be attacked in bulk.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating salt: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// The parameters travel with the hash so they can be raised later without
	// invalidating every existing password.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the encoded hash.
func VerifyPassword(plain, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHashFormat
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, ErrInvalidHashFormat
	}

	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, ErrInvalidHashFormat
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHashFormat
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHashFormat
	}

	got := argon2.IDKey([]byte(plain), salt, iterations, memory, threads, uint32(len(want)))

	// Constant-time comparison: a byte-by-byte compare that returns early leaks
	// how much of the hash matched through its timing.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// MinPasswordLength is the shortest password accepted.
//
// Length does more for resistance than symbol classes do, so the policy leans
// on it rather than demanding punctuation the user will write on a sticky note.
const MinPasswordLength = 10

// ValidatePassword checks a new password against the minimum policy.
func ValidatePassword(p string) error {
	if len([]rune(p)) < MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	}
	var hasLetter, hasNumber bool
	for _, r := range p {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsNumber(r):
			hasNumber = true
		}
	}
	if !hasLetter || !hasNumber {
		return errors.New("password must contain at least one letter and one number")
	}
	return nil
}
