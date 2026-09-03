// Package config loads runtime configuration from the environment.
//
// Every secret arrives this way. Nothing sensitive is compiled in and nothing
// sensitive is committed; .env is git-ignored and .env.example carries only
// placeholders (NFR-015).
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port string
	Env  string

	DatabaseURL string
	RedisURL    string

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	CORSOrigins []string

	// TrustProxyHeaders says whether CF-Connecting-IP and X-Forwarded-For may
	// be believed for rate limiting. Anyone talking to the service directly can
	// forge them, so this defaults to false and must be switched on explicitly
	// once the deployment guarantees a proxy rewrites them (DEF-019).
	TrustProxyHeaders bool

	ResendAPIKey string
	MailFrom     string

	OpenLibraryBaseURL string
}

// IsProduction reports whether stricter defaults should apply.
func (c Config) IsProduction() bool { return c.Env == "production" }

// Load reads configuration and fails loudly if anything required is missing.
//
// Starting with a broken configuration and discovering it on the first request
// is worse than refusing to start, so this returns an error rather than
// substituting defaults for things that have no safe default.
func Load() (Config, error) {
	// A missing .env is normal in production, where real environment variables
	// are set by the platform. Only a malformed one is worth reporting.
	_ = godotenv.Load()

	c := Config{
		Port:               fallback("PORT", "8080"),
		Env:                fallback("ENV", "development"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		RedisURL:           os.Getenv("REDIS_URL"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		ResendAPIKey:       os.Getenv("RESEND_API_KEY"),
		MailFrom:           fallback("MAIL_FROM", "library@example.edu.ng"),
		OpenLibraryBaseURL: fallback("OPENLIBRARY_BASE_URL", "https://openlibrary.org"),
	}

	var err error
	if c.AccessTokenTTL, err = duration("ACCESS_TOKEN_TTL", 15*time.Minute); err != nil {
		return c, err
	}
	if c.RefreshTokenTTL, err = duration("REFRESH_TOKEN_TTL", 7*24*time.Hour); err != nil {
		return c, err
	}

	// CORS is an explicit allowlist. A wildcard origin on an API that carries
	// member borrowing history would let any site read it (NFR-016).
	for _, o := range strings.Split(os.Getenv("CORS_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORSOrigins = append(c.CORSOrigins, o)
		}
	}

	c.TrustProxyHeaders = os.Getenv("TRUST_PROXY_HEADERS") == "true"

	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if c.JWTSecret == "" {
		return c, fmt.Errorf("JWT_SECRET is required")
	}
	// A short secret makes token forgery feasible, so it is treated as a
	// configuration error rather than a warning.
	if len(c.JWTSecret) < 32 {
		return c, fmt.Errorf("JWT_SECRET must be at least 32 characters")
	}
	return c, nil
}

func fallback(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func duration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s is not a valid duration: %w", key, err)
	}
	return d, nil
}
