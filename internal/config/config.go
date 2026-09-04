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
	// RedisPrefix namespaces this application's keys, so a Redis shared with
	// another application cannot collide with it.
	RedisPrefix string

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration

	CORSOrigins []string

	// TrustProxyHeaders says whether CF-Connecting-IP and X-Forwarded-For may
	// be believed for rate limiting. Anyone talking to the service directly can
	// forge them, so this defaults to false and must be switched on explicitly
	// once the deployment guarantees a proxy rewrites them (DEF-019).
	FirebaseKey       string
	TrustProxyHeaders bool

	// SeedDemoData applies the demonstration migration, whose accounts have
	// passwords published in a public repository. Development only; a
	// production deployment that sets this hands out an administrator account.
	SeedDemoData bool

	ResendAPIKey string
	MailFrom     string

	OpenLibraryBaseURL string

	// FrontendURL is where a member lands when they follow a link from an
	// email. It is configuration rather than a constant because the address
	// differs between a laptop, a preview deployment and production, and an
	// email with the wrong link in it is worse than one with none.
	FrontendURL string
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
		RedisPrefix:        fallback("REDIS_PREFIX", "holibrary"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		ResendAPIKey:       os.Getenv("RESEND_API_KEY"),
		MailFrom:           fallback("MAIL_FROM", "library@example.edu.ng"),
		OpenLibraryBaseURL: fallback("OPENLIBRARY_BASE_URL", "https://openlibrary.org"),
		FrontendURL:        strings.TrimRight(fallback("FRONTEND_URL", "https://library.appmd.dev"), "/"),
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

	// Firebase service account key, as raw JSON or base64. Push is optional:
	// an unset value leaves the channel unconfigured rather than failing boot.
	c.FirebaseKey = os.Getenv("FIREBASE_SERVICE_ACCOUNT")

	c.TrustProxyHeaders = os.Getenv("TRUST_PROXY_HEADERS") == "true"
	c.SeedDemoData = os.Getenv("SEED_DEMO_DATA") == "true"

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
	// The value committed to docker-compose.yml for local development is public.
	// Refusing it in production is cheaper than discovering later that anyone
	// who read the repository can forge an administrator token.
	if c.IsProduction() && strings.Contains(c.JWTSecret, "development-secret") {
		return c, fmt.Errorf("JWT_SECRET is the published development value; generate a real one")
	}
	if c.IsProduction() && c.SeedDemoData {
		return c, fmt.Errorf(
			"SEED_DEMO_DATA must not be set in production: the seeded accounts have published passwords")
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
