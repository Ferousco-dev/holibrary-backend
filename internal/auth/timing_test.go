package auth_test

import (
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
)

// The unknown-account path must cost about the same as a real verification, or
// membership can be read off the clock (DEF-017).
func TestBurnTimeCostsWhatAVerificationCosts(t *testing.T) {
	real, err := auth.HashPassword("a real members password 1")
	if err != nil {
		t.Fatal(err)
	}

	measure := func(f func()) time.Duration {
		start := time.Now()
		for i := 0; i < 5; i++ {
			f()
		}
		return time.Since(start) / 5
	}

	verify := measure(func() { _, _ = auth.VerifyPassword("wrong password 12", real) })
	burn := measure(func() { auth.BurnTimeLikeAVerification("wrong password 12") })

	// Argon2 dominates both; anything within a factor of two is
	// indistinguishable across a network.
	ratio := float64(verify) / float64(burn)
	if ratio < 0.5 || ratio > 2 {
		t.Errorf("timing differs too much: verify=%v burn=%v ratio=%.2f", verify, burn, ratio)
	}
	t.Logf("verify=%v burn=%v ratio=%.2f", verify, burn, ratio)
}
