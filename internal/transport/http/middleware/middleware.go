// Package middleware holds the cross-cutting HTTP concerns: who is calling,
// whether they may, and how often they may.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// ctxKey is unexported so no other package can write to our context slots.
type ctxKey string

const (
	ctxKeyUserID    ctxKey = "user_id"
	ctxKeyRole      ctxKey = "role"
	ctxKeyRequestID ctxKey = "request_id"
)

// UserID returns the authenticated caller, if there is one.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(ctxKeyUserID).(uuid.UUID)
	return id, ok
}

// Role returns the authenticated caller's role.
func Role(ctx context.Context) domain.Role {
	r, _ := ctx.Value(ctxKeyRole).(domain.Role)
	return r
}

// RequestID returns the correlation id assigned to this request.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// Authenticate verifies the bearer token and puts the caller in the context.
//
// It does not decide what the caller may do; that is RequireRole's job. Keeping
// the two apart means a route cannot accidentally be authenticated but
// unauthorised.
func Authenticate(issuer *auth.TokenIssuer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			token, found := strings.CutPrefix(header, "Bearer ")
			if !found || token == "" {
				response.FromError(w, domain.ErrUnauthenticated)
				return
			}

			claims, err := issuer.ParseAccessToken(token)
			if err != nil {
				response.FromError(w, domain.ErrTokenInvalid)
				return
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxKeyRole, domain.Role(claims.Role))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireLibrarian rejects anyone who is not staff.
//
// This runs on the server, on every protected route. A frontend that hides the
// admin menu is a convenience for honest users, not a security control: the API
// is what an attacker talks to (NFR-004).
func RequireLibrarian(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !Role(r.Context()).CanManageLibrary() {
			response.FromError(w, domain.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin restricts a route to administrators.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Role(r.Context()) != domain.RoleAdmin {
			response.FromError(w, domain.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimit throttles a route by client IP.
//
// Applied to the authentication endpoints, this is what turns password guessing
// from a background task into an impractical one (NFR-005). It is an in-process
// fixed window, which is sufficient for a single container; a multi-instance
// deployment would move the counter into Redis.
func RateLimit(limit int, window time.Duration) func(http.Handler) http.Handler {
	type counter struct {
		count int
		reset time.Time
	}
	var (
		mu      sync.Mutex
		clients = make(map[string]*counter)
	)

	// Old entries are swept periodically so the map cannot grow without bound;
	// an unbounded map keyed by remote IP is itself a denial-of-service vector.
	go func() {
		for range time.Tick(window) {
			mu.Lock()
			for ip, c := range clients {
				if time.Now().After(c.reset) {
					delete(clients, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)

			mu.Lock()
			c, ok := clients[ip]
			if !ok || time.Now().After(c.reset) {
				c = &counter{reset: time.Now().Add(window)}
				clients[ip] = c
			}
			c.count++
			exceeded := c.count > limit
			mu.Unlock()

			if exceeded {
				w.Header().Set("Retry-After", "60")
				response.Error(w, http.StatusTooManyRequests, "RATE_LIMITED",
					"Too many attempts. Please wait a minute and try again.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP resolves the caller's address.
//
// Cloudflare terminates TLS in front of this service, so the socket address is
// Cloudflare's. CF-Connecting-IP carries the real client. It is only trusted
// because the deployment guarantees Cloudflare is the only ingress; on a
// directly exposed server this header would be attacker-controlled.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// WithRequestID tags each request so a log line can be traced to a report.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Logger writes one structured line per request.
//
// It records the method, path, status and duration. It deliberately does not
// record request bodies or query strings, because those carry passwords, reset
// tokens and member names, and logs are the easiest place for personal data to
// leak (NFR-010, DOM-009).
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestID(r.Context()),
		)
	})
}

// Recover turns a panic into a 500 instead of a dropped connection.
//
// One bad request should not take the whole library offline, which on a
// single-container free-tier deployment is exactly what an unrecovered panic
// would do.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("panic recovered",
					"error", err,
					"path", r.URL.Path,
					"stack", string(debug.Stack()))
				response.Error(w, http.StatusInternalServerError, "INTERNAL",
					"Something went wrong on our side. Please try again.", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS answers preflight requests for the origins we know about.
//
// The allowlist is explicit. A wildcard on an API that serves member borrowing
// history would let any website on the internet read it (NFR-016).
func CORS(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[strings.TrimSpace(o)] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods",
					"GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers",
					"Authorization, Content-Type, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")
				// Caches must not serve one origin's response to another.
				w.Header().Add("Vary", "Origin")
			}

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets the defensive headers a browser will honour.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		// HSTS is safe to send unconditionally here because Cloudflare
		// terminates TLS and redirects plain HTTP in front of us (NFR-006).
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}
