// Package http wires the route table.
package http

import (
	"net/http"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/docs"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/handler"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
)

// Handlers collects everything the router needs.
type Handlers struct {
	Auth         *handler.AuthHandler
	Catalogue    *handler.CatalogueHandler
	Circulation  *handler.CirculationHandler
	Members      *handler.MemberHandler
	Reservations *handler.ReservationHandler
	Admin        *handler.AdminHandler
	Ping         func() error
}

// Options carries router configuration.
type Options struct {
	Issuer      *auth.TokenIssuer
	CORSOrigins []string
}

// NewRouter builds the route table.
//
// Routes are grouped by who may call them, and the grouping is the
// authorisation. Reading this file should make it obvious which endpoints a
// student can reach, which need staff, and which need an administrator; that
// legibility is itself a security property (NFR-004).
func NewRouter(h Handlers, opts Options) http.Handler {
	mux := http.NewServeMux()

	authenticate := middleware.Authenticate(opts.Issuer)

	// Guessing passwords is only practical if guesses are cheap, so the
	// credential endpoints are throttled per client IP (NFR-005).
	throttle := middleware.RateLimit(5, time.Minute)

	// ---- public: no token required ----------------------------------------

	mux.Handle("GET /healthz", handler.Health(h.Ping))

	// The API documents itself. The frontend is a separate repository and builds
	// against this contract, so it is served from the running service rather
	// than kept in a file someone has to remember to share (REQ-073).
	mux.Handle("GET /docs", docs.UI())
	mux.Handle("GET /openapi.yaml", docs.Spec())

	// The catalogue is public information. A visitor may search it without an
	// account, exactly as they may walk in and browse the shelves (REQ-037).
	mux.HandleFunc("GET /api/v1/books", h.Catalogue.Search)
	mux.HandleFunc("GET /api/v1/books/{id}", h.Catalogue.Get)

	mux.Handle("POST /api/v1/auth/login", throttle(http.HandlerFunc(h.Auth.Login)))
	mux.Handle("POST /api/v1/auth/refresh", throttle(http.HandlerFunc(h.Auth.Refresh)))
	mux.Handle("POST /api/v1/auth/forgot-password", throttle(http.HandlerFunc(h.Auth.ForgotPassword)))
	mux.Handle("POST /api/v1/auth/reset-password", throttle(http.HandlerFunc(h.Auth.ResetPassword)))

	// There is deliberately no POST /api/v1/auth/register. Membership begins in
	// the library building, not on the internet (DOM-006, DEC-006).

	// ---- signed in: any authenticated account ------------------------------

	mux.Handle("POST /api/v1/auth/logout", authenticate(http.HandlerFunc(h.Auth.Logout)))
	mux.Handle("POST /api/v1/auth/change-password", authenticate(http.HandlerFunc(h.Auth.ChangePassword)))

	// A member's own record is reached through /me, never through an id in the
	// URL. There is no parameter to tamper with, so one member cannot read
	// another's borrowing history (REQ-062, DOM-009).
	mux.Handle("GET /api/v1/me/loans", authenticate(http.HandlerFunc(h.Circulation.MyLoans)))
	mux.Handle("GET /api/v1/me/history", authenticate(http.HandlerFunc(h.Circulation.MyHistory)))

	// Members place their own reservations. Unlike borrowing, joining a queue
	// commits nothing physical, so there is no reason to make someone walk to
	// the library to do it (REQ-055..057).
	mux.Handle("GET /api/v1/me/reservations", authenticate(http.HandlerFunc(h.Reservations.List)))
	mux.Handle("POST /api/v1/reservations", authenticate(http.HandlerFunc(h.Reservations.Create)))
	mux.Handle("DELETE /api/v1/reservations/{id}", authenticate(http.HandlerFunc(h.Reservations.Cancel)))

	// ---- librarian: manages the collection and the desk ---------------------

	staff := func(next http.HandlerFunc) http.Handler {
		return authenticate(middleware.RequireLibrarian(next))
	}

	mux.Handle("POST /api/v1/books", staff(h.Catalogue.Create))
	mux.Handle("POST /api/v1/books/{id}/archive", staff(h.Catalogue.Archive))
	mux.Handle("POST /api/v1/books/{id}/copies", staff(h.Catalogue.AddCopy))
	mux.Handle("PATCH /api/v1/copies/{id}", staff(h.Catalogue.UpdateCopy))

	// Borrowing is recorded by staff at the desk. A member cannot issue a book
	// to themselves here any more than they could in the building (REQ-041).
	mux.Handle("POST /api/v1/loans", staff(h.Circulation.Borrow))
	mux.Handle("POST /api/v1/loans/{id}/return", staff(h.Circulation.Return))
	mux.Handle("GET /api/v1/loans", staff(h.Circulation.List))

	mux.Handle("GET /api/v1/members", staff(h.Members.List))
	mux.Handle("POST /api/v1/members", staff(h.Members.Create))
	mux.Handle("POST /api/v1/members/import", staff(h.Members.ImportCSV))
	mux.Handle("GET /api/v1/members/{id}", staff(h.Members.Get))
	mux.Handle("PATCH /api/v1/members/{id}/status", staff(h.Members.SetStatus))

	mux.Handle("GET /api/v1/admin/dashboard", staff(h.Admin.Dashboard))

	// ---- administrator ------------------------------------------------------

	mux.Handle("GET /api/v1/admin/audit",
		authenticate(middleware.RequireAdmin(http.HandlerFunc(h.Admin.Audit))))

	// Middleware runs outermost first. Recover is outermost so it catches a
	// panic raised anywhere inside, including in the logger.
	var root http.Handler = mux
	root = middleware.SecurityHeaders(root)
	root = middleware.CORS(opts.CORSOrigins)(root)
	root = middleware.Logger(root)
	root = middleware.WithRequestID(root)
	root = middleware.Recover(root)
	return root
}
