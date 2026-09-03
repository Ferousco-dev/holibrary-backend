// Command api is the HOLibrary backend.
//
// It is the composition root: the only place where concrete implementations are
// chosen and wired together. Every other package depends on interfaces, which is
// what allows the business rules to be tested without a database or a network.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/config"
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

func run() error {
	// JSON logs so a hosting platform can index them. Structured fields also
	// make it harder to accidentally interpolate a member's details into a
	// message string (NFR-010).
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

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

	// Stores.
	users := postgres.NewUserRepo(db)
	tokens := postgres.NewTokenRepo(db)
	catalogue := postgres.NewCatalogueRepo(db)
	circulation := postgres.NewCirculationRepo(db)
	outbox := postgres.NewOutboxRepo(db)
	audit := postgres.NewAuditRepo(db)

	// Services.
	issuer := auth.NewTokenIssuer(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	authService := service.NewAuthService(users, tokens, outbox, issuer)
	catalogueService := service.NewCatalogueService(catalogue)
	circulationService := service.NewCirculationService(circulation, users, outbox)
	memberService := service.NewMemberService(users, outbox)

	router := transport.NewRouter(transport.Handlers{
		Auth:        handler.NewAuthHandler(authService),
		Catalogue:   handler.NewCatalogueHandler(catalogueService),
		Circulation: handler.NewCirculationHandler(circulationService),
		Members:     handler.NewMemberHandler(memberService, circulationService),
		Admin:       handler.NewAdminHandler(circulationService, audit),
		Ping:        func() error { return db.Ping(ctx) },
	}, transport.Options{
		Issuer:      issuer,
		CORSOrigins: cfg.CORSOrigins,
	})

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
