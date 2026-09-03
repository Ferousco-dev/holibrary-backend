// Command api is the HOLibrary backend.
//
// It is the composition root: the only place where concrete implementations are
// chosen and wired together. Every other package depends on interfaces, which is
// what allows the business rules to be tested without a database or a network.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// The production image is FROM scratch and has no /usr/share/zoneinfo, so
	// time.LoadLocation("Africa/Lagos") would fail there and nowhere else --
	// the classic bug that passes every test on a developer laptop and only
	// appears once deployed. This embeds the timezone database in the binary
	// (~450 KB) so named zones resolve identically everywhere.
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/books"
	"github.com/Ferousco-dev/holibrary-backend/internal/config"
	"github.com/Ferousco-dev/holibrary-backend/internal/migrate"
	"github.com/Ferousco-dev/holibrary-backend/internal/notify"
	"github.com/Ferousco-dev/holibrary-backend/internal/queue"
	"github.com/Ferousco-dev/holibrary-backend/internal/ratelimit"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	transport "github.com/Ferousco-dev/holibrary-backend/internal/transport/http"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/handler"
)

func main() {
	if err := run(); err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

// runSchedule performs the periodic library chores.
//
// Hourly is frequent enough for reminders measured in days, and infrequent
// enough that a free-tier database is not woken constantly. The first pass runs
// immediately so a restart does not skip a day.
func runSchedule(ctx context.Context, circulation *service.CirculationService,
	reservations *service.ReservationService) {

	const every = time.Hour
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	work := func() {
		// Reminders three days out. Overdue notices are raised by the same
		// pass, because overdue is computed from the clock rather than stored.
		if n, err := circulation.NotifyDueSoon(ctx, 3*24*time.Hour); err != nil {
			slog.Error("could not queue due-date reminders", "error", err)
		} else if n > 0 {
			slog.Info("due-date reminders queued", "count", n)
		}

		// A member who never collects must not block the queue behind them.
		if n, err := reservations.ExpireStale(ctx); err != nil {
			slog.Error("could not release stale reservations", "error", err)
		} else if n > 0 {
			slog.Info("stale reservations released", "count", n)
		}
	}

	work()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			work()
		}
	}
}

// migrationLockID is an arbitrary constant that identifies this application's
// migration lock. Any two processes using the same number are serialised.
const migrationLockID = 8_1_9_2_2_6

// withAdvisoryLock runs fn while holding a Postgres advisory lock.
//
// The lock is held on a single connection for the duration and released when it
// returns, so a crashed process does not leave the schema locked: Postgres drops
// advisory locks when the session ends.
func withAdvisoryLock(ctx context.Context, db *pgxpool.Pool, fn func() error) error {
	conn, err := db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	return fn()
}

func run() error {
	// JSON logs so a hosting platform can index them. Structured fields also
	// make it harder to accidentally interpolate a member's details into a
	// message string (NFR-010).
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// The process runs in UTC regardless of what the host is set to, so a log
	// line, a stored timestamp and a due-date comparison can never disagree
	// because of the container's local timezone. Africa/Lagos is a display
	// concern and belongs in the frontend.
	time.Local = time.UTC

	cfg, err := config.Load()
	if err != nil {
		// Refusing to start beats starting misconfigured and failing on the
		// first member who tries to sign in.
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	slog.Info("database connected")

	// Bring the schema up to date before serving. An advisory lock means two
	// instances starting together do not both try; the second waits, finds
	// nothing pending, and continues.
	if err := withAdvisoryLock(ctx, db, func() error {
		ran, err := migrate.Apply(ctx, db, cfg.SeedDemoData)
		if err != nil {
			return err
		}
		if len(ran) > 0 {
			slog.Info("migrations applied", "count", len(ran), "files", ran)
		} else {
			slog.Info("database schema is up to date")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("migrating: %w", err)
	}

	// Stores.
	users := postgres.NewUserRepo(db)
	tokens := postgres.NewTokenRepo(db)
	catalogue := postgres.NewCatalogueRepo(db)
	circulation := postgres.NewCirculationRepo(db)
	outbox := postgres.NewOutboxRepo(db)
	reservations := postgres.NewReservationRepo(db)
	audit := postgres.NewAuditRepo(db)

	// Rate limiting. Redis keeps the counters outside the process, so a limit
	// survives a restart and holds across instances. An in-process fallback
	// keeps development working and degrades a Redis outage rather than the
	// service (DEF-019).
	var limiter ratelimit.Limiter
	if cfg.RedisURL != "" {
		if r, err := ratelimit.NewRedis(cfg.RedisURL); err != nil {
			slog.Error("could not parse REDIS_URL; falling back to an in-process limiter",
				"error", err)
			limiter = ratelimit.NewMemory()
		} else if err := r.Ping(ctx); err != nil {
			slog.Error("Redis unreachable; falling back to an in-process limiter",
				"error", err)
			limiter = ratelimit.NewMemory()
		} else {
			defer r.Close()
			limiter = r
			slog.Info("rate limiting backed by Redis")
		}
	} else {
		slog.Warn("no REDIS_URL; rate limits are in-process and reset on restart")
		limiter = ratelimit.NewMemory()
	}

	if cfg.TrustProxyHeaders {
		slog.Info("trusting proxy headers for client address; ensure the origin is not directly reachable")
	}

	// The external catalogue address is validated at startup rather than on
	// first use, so a misconfigured or hostile URL stops the service instead of
	// being discovered by a librarian mid-catalogue (DEF-024).
	externalCatalogue, err := books.NewOpenLibrary(cfg.OpenLibraryBaseURL)
	if err != nil {
		return fmt.Errorf("external catalogue: %w", err)
	}

	// Services.
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	authService := service.NewAuthService(users, tokens, outbox, issuer, limiter)
	catalogueService := service.NewCatalogueService(catalogue)
	circulationService := service.NewCirculationService(circulation, users, outbox)
	memberService := service.NewMemberService(users, outbox)
	reservationService := service.NewReservationService(reservations, outbox)

	// A returned copy advances the queue for its title. Wired here rather than
	// as a constructor argument so neither service has to import the other.
	circulationService.SetReturnHook(reservationService)

	router := transport.NewRouter(transport.Handlers{
		Auth:         handler.NewAuthHandler(authService),
		Catalogue:    handler.NewCatalogueHandler(catalogueService),
		Circulation:  handler.NewCirculationHandler(circulationService),
		Members:      handler.NewMemberHandler(memberService, circulationService),
		Reservations: handler.NewReservationHandler(reservationService),
		Devices:      handler.NewDeviceHandler(outbox),
		Lookup:       handler.NewLookupHandler(externalCatalogue),
		Admin:        handler.NewAdminHandler(circulationService, audit),
		Ping:         func() error { return db.Ping(ctx) },
	}, transport.Options{
		Issuer: issuer,
		// A token issued before the account's last password change is dead,
		// however long it has left to run. One primary-key lookup per
		// authenticated request is the price of being able to end a session
		// immediately rather than in fifteen minutes (DEF-021).
		SessionValid: func(ctx context.Context, userID uuid.UUID, issuedAt time.Time) (bool, error) {
			invalidBefore, err := users.TokensInvalidBefore(ctx, userID)
			if err != nil {
				return false, err
			}
			// Whole-second granularity: JWT `iat` is a Unix second, so a token
			// minted in the same second as a password change would otherwise
			// compare as older than it.
			return !issuedAt.Add(time.Second).Before(invalidBefore), nil
		},
		CORSOrigins:       cfg.CORSOrigins,
		Limiter:           limiter,
		TrustProxyHeaders: cfg.TrustProxyHeaders,
	})

	// Notification delivery runs beside the server, never on a request. A
	// channel with no provider configured is simply absent, and its messages
	// stay queued until one appears, so nothing is lost during setup.
	var senders []notify.Sender
	switch resend := notify.NewResend(cfg.ResendAPIKey, cfg.MailFrom); {
	case resend.Configured():
		senders = append(senders, resend)
	case !cfg.IsProduction():
		// Development and demonstration: the whole pipeline runs and only the
		// final hop changes, so the outbox, the state re-check and the retry
		// accounting can all be observed without a mail account.
		slog.Warn("no mail provider configured; notifications will be logged, not sent")
		senders = append(senders, notify.NewConsole("email"), notify.NewConsole("push"))
	default:
		// In production, silence beats pretending. The messages stay queued and
		// are delivered once a provider is configured; marking them sent would
		// lose them (RSK-002).
		slog.Error("no mail provider configured in production; notifications will stay queued",
			"remedy", "set RESEND_API_KEY and verify a sending domain")
	}
	worker := queue.NewWorker(outbox, 30*time.Second, senders...)
	go worker.Run(ctx)

	// Scheduled work: due-soon and overdue reminders, and releasing holds that
	// nobody collected. Both recompute from the clock every pass rather than
	// trusting anything stored, so neither can act on a stale value.
	go runSchedule(ctx, circulationService, reservationService)

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
		// Timeouts are not optional on a public server: without them a handful
		// of slow clients can hold every connection open and take the service
		// down without sending a single valid request.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Serve in the background so the main goroutine can wait for a signal.
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("listening", "port", cfg.Port, "env", cfg.Env)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		// Graceful shutdown: a librarian who pressed "record loan" as the
		// container was recycled should have that request finish, not fail.
		slog.Info("shutdown signal received, draining connections")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		slog.Info("shutdown complete")
		return nil
	}
}
