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
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/ratelimit"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// ctxKey is unexported so no other package can write to our context slots.
type ctxKey string

const (
	ctxKeyUserID    ctxKey = "user_id"
	ctxKeyRole      ctxKey = "role"
	ctxKeyRequestID ctxKey = "request_id"
	ctxKeyLogFields ctxKey = "log_fields"
)

// logFields carries what the access log learns about a request *after* the
// logger has already been entered.
//
// Middleware nests, so Logger wraps Authenticate and runs first. Authenticate
// puts the caller into a derived context, and a derived context is invisible to
// the outer frame. A pointer placed in the context before the chain descends is
// how the identity gets back out, so one log line can carry both the timing and
// who made the request.
type logFields struct {
	userID string
	role   string
}

func fieldsFrom(ctx context.Context) *logFields {
	f, _ := ctx.Value(ctxKeyLogFields).(*logFields)
	return f
}

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

// SessionValidator reports whether a token issued at the given instant is still
// good for this account.
//
// It exists because a JWT cannot be recalled. Refresh tokens are revocable, but
// an access token already in an attacker's hands stays valid until it expires --
// which meant a member who changed their password because they suspected a
// compromise still had a live attacker session for up to fifteen minutes
// afterwards. That is precisely the window during which it matters most.
//
// The cost is one primary-key lookup per authenticated request, which is the
// price of being able to end a session immediately. It is paid deliberately:
// the alternative is telling a student their password change takes effect in a
// quarter of an hour (DEF-021).
type SessionValidator func(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error)

// Authenticate verifies the bearer token and puts the caller in the context.
//
// It does not decide what the caller may do; that is RequireRole's job. Keeping
// the two apart means a route cannot accidentally be authenticated but
// unauthorised.
func Authenticate(issuer *auth.TokenIssuer, valid SessionValidator) func(http.Handler) http.Handler {
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

			// A token minted before the account's last password change is dead,
			// however long it has left to run (DEF-021).
			if valid != nil && claims.IssuedAt != nil {
				ok, err := valid(r.Context(), claims.UserID, claims.IssuedAt.Time)
				if err != nil {
					// Failing closed here would take the library offline on a
					// database blip, and the request is about to touch the same
					// database anyway. Log loudly and continue.
					slog.Error("could not check session validity", "error", err)
				} else if !ok {
					response.FromError(w, domain.ErrTokenInvalid)
					return
				}
			}

			// An account still on its librarian-issued temporary password may do
			// exactly one thing: replace it. Without this the temporary password
			// handed over on paper was a fully working credential for as long as
			// the member ignored the prompt. DEF-007.
			if claims.Pending && !isPasswordChangeRoute(r) {
				response.FromError(w, domain.ErrMustChangePassword)
				return
			}

			// Tell the access log who this is (see logFields).
			if f := fieldsFrom(r.Context()); f != nil {
				f.userID = claims.UserID.String()
				f.role = claims.Role
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserID, claims.UserID)
			ctx = context.WithValue(ctx, ctxKeyRole, domain.Role(claims.Role))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isPasswordChangeRoute reports whether the request is one an account with a
// pending password change is still permitted to make.
func isPasswordChangeRoute(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/v1/auth/change-password", "/api/v1/auth/logout":
		return true
	}
	return false
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

// RateLimit throttles a route by client address.
//
// This is the COARSE half of the control and is set generously on purpose. A
// whole faculty shares a small number of NAT addresses, so a tight per-IP limit
// punishes honest students while barely inconveniencing an attacker who can
// vary their apparent address. It catches a flood, not a guesser.
//
// The precise half lives in the authentication service, keyed on the ACCOUNT
// being attacked -- the one thing an attacker cannot change. See
// internal/ratelimit (NFR-005, DEF-019).
func RateLimit(limiter ratelimit.Limiter, p ratelimit.Policy, trustProxyHeaders bool) func(http.Handler) http.Handler {
	// Degrades to an in-process counter rather than switching off, so an
	// attacker cannot disable the control by disabling Redis (DEF-022).
	fallback := ratelimit.NewMemory()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "rl:ip:" + clientIP(r, trustProxyHeaders)

			ok, err := limiter.Allow(r.Context(), key, p.Limit, p.Window)
			if err != nil {
				slog.Error("rate limiter unavailable; falling back to the in-process counter",
					"error", err)
				ok, _ = fallback.Allow(r.Context(), key, p.Limit, p.Window)
			}
			if !ok {
				w.Header().Set("Retry-After", "60")
				response.Error(w, http.StatusTooManyRequests, "RATE_LIMITED",
					"Too many requests from this network. Please wait a minute.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP resolves the caller's address.
//
// CF-Connecting-IP and X-Forwarded-For are ordinary request headers: anyone
// talking to this service directly can set them to anything. They are only
// meaningful if every request provably passes through a proxy that overwrites
// them, and this application cannot verify that from the inside.
//
// So the decision is made in configuration and defaults to NOT trusting them.
// The previous version trusted them unconditionally, which meant that on any
// path reaching the origin directly -- a preview environment, a misconfigured
// load balancer, a direct host route -- an attacker could send a fresh
// CF-Connecting-IP with every request and never be limited at all, or forge a
// victim's address to lock them out. DEF-019.
func clientIP(r *http.Request, trustProxyHeaders bool) string {
	if trustProxyHeaders {
		if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
			return ip
		}
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			if first, _, ok := strings.Cut(forwarded, ","); ok {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(forwarded)
		}
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

		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		ctx = context.WithValue(ctx, ctxKeyLogFields, &logFields{})
		next.ServeHTTP(w, r.WithContext(ctx))
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
// Recorded: method, route, status, duration, request id, and — once the request
// has been authenticated — the caller's id and role. That is enough to answer
// "what happened to this member's request at 18:31" from the logs alone, and
// enough to correlate a log line with an audit entry.
//
// Deliberately NOT recorded: request bodies and query strings. Those carry
// passwords, reset tokens, search terms and member names, and logs are the
// easiest place in a system for personal data to escape (NFR-010, DOM-009).
// A borrowing history is a record of what a named student reads; it does not
// belong in a log aggregator.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestID(r.Context()),
		}
		// Anonymous requests simply have no actor, rather than an empty one.
		if f := fieldsFrom(r.Context()); f != nil && f.userID != "" {
			attrs = append(attrs, "actor_id", f.userID, "actor_role", f.role)
		}

		// A failed request is not routine and should not need a log level
		// filter to find.
		if rec.status >= 500 {
			slog.Error("request", attrs...)
			return
		}
		slog.Info("request", attrs...)
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
