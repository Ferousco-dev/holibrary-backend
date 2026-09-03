// Command simulator exercises a running HOLibrary instance.
//
// It stocks the catalogue from an external bibliographic source, registers
// members, and drives borrowing and returns according to a probabilistic
// behaviour model, then asserts that the library is still internally consistent
// and prints a report.
//
// It is not an AI model. There is no training and no inference; the model is a
// few kilobytes of hand-chosen probabilities. The pattern has a proper name --
// SYNTHETIC MONITORING -- and describing it accurately is worth more than
// describing it impressively.
//
// Two things it is genuinely good for:
//
//   - The catalogue is populated, so the system can be demonstrated with a real
//     collection rather than four seeded books.
//   - Every pass re-exercises authentication, authorisation, validation, the
//     borrowing rules and the concurrency guards through the public API. If any
//     of them break overnight, the morning's report says so.
//
// Usage:
//
//	simulator -url http://localhost:8080 -login librarian@oauife.edu.ng
//
// Environment: SIMULATOR_PASSWORD supplies the librarian's password, so it is
// never a command-line argument visible in a process list.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Ferousco-dev/holibrary-backend/internal/simulator"
)

func main() {
	var (
		baseURL      = flag.String("url", envOr("SIMULATOR_URL", "http://localhost:8080"), "base URL of the API")
		login        = flag.String("login", envOr("SIMULATOR_LOGIN", ""), "librarian account the simulator acts through")
		catalogueURL = flag.String("catalogue", envOr("OPENLIBRARY_BASE_URL", ""), "external catalogue base URL")
		every        = flag.Duration("every", 0, "run repeatedly at this interval; a single pass if unset")
		seed         = flag.Int64("seed", 0, "random seed; 0 means unpredictable")
		record       = flag.String("record", os.Getenv("DATABASE_URL"), "record each run in simulation_runs; empty to skip")
		asJSON       = flag.Bool("json", false, "emit the report as JSON")
		allowReal    = flag.Bool("allow-real-data", false,
			"run even though this instance holds real member records (destructive; do not use in production)")
	)
	flag.Parse()

	password := os.Getenv("SIMULATOR_PASSWORD")
	if *login == "" || password == "" {
		fmt.Fprintln(os.Stderr,
			"a librarian account is required: -login <email> and SIMULATOR_PASSWORD=<password>")
		os.Exit(2)
	}

	model, err := simulator.LoadModel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "loading the behaviour model:", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts := simulator.Options{
		BaseURL: *baseURL, LibrarianLogin: *login,
		CatalogueURL: *catalogueURL, Seed: *seed,
	}

	// Optional run history. A pass that cannot write its own log still ran.
	var db *pgxpool.Pool
	if *record != "" {
		pool, err := pgxpool.New(ctx, *record)
		if err != nil {
			fmt.Fprintln(os.Stderr, "could not open the run log; continuing without it:", err)
		} else {
			defer pool.Close()
			db = pool
		}
	}

	runOnce := func() int {
		report, err := runPass(ctx, model, opts, password, *allowReal)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pass failed:", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(report)
		} else {
			printReport(model, report)
		}
		if db != nil {
			if err := simulator.Record(ctx, db, report); err != nil {
				fmt.Fprintln(os.Stderr, "could not record the run:", err)
			}
		}
		if report.Outcome == "failed" {
			return 1
		}
		return 0
	}

	// A single pass by default, so it can be run from a cron job or a GitHub
	// Actions schedule and its exit code means something.
	if *every == 0 {
		os.Exit(runOnce())
	}

	// Or resident, for a demonstration where the library should visibly tick
	// along while somebody is watching.
	fmt.Printf("simulator running every %s; press Ctrl-C to stop\n\n", *every)
	runOnce()
	ticker := time.NewTicker(*every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("stopped")
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// token is reused between passes.
//
// Signing in on every pass made the simulator trip the library's own per-account
// login limit -- five attempts a minute, which a resident run at a short
// interval exceeds by itself. That is the limiter working correctly, and the
// simulator was the one behaving badly: a real librarian signs in once at the
// start of a shift and works from that session, so this does the same. The token
// is refreshed only when it stops being accepted.
var token string

func runPass(ctx context.Context, model *simulator.Model,
	opts simulator.Options, password string, allowRealData bool) (*simulator.Report, error) {

	agent, err := simulator.NewAgent(model, opts)
	if err != nil {
		return nil, err
	}

	if token != "" {
		agent.UseToken(token)
	} else if err := agent.SignIn(ctx, opts.LibrarianLogin, password); err != nil {
		return nil, err
	} else {
		token = agent.Token()
	}

	// A 15-minute access token will expire during a long resident run. One
	// retry after a fresh sign-in covers that without re-authenticating every
	// pass.
	if !agent.Authenticated(ctx) {
		if err := agent.SignIn(ctx, opts.LibrarianLogin, password); err != nil {
			return nil, err
		}
		token = agent.Token()
	}

	// Refuse to generate activity against real records unless told to.
	if safe, why := agent.SafeToRun(ctx); !safe && !allowRealData {
		return nil, fmt.Errorf("%s\n"+
			"  The simulator lends books, registers borrowers and closes loans. Against a\n"+
			"  real member roll that is not a demonstration.\n"+
			"  Pass -allow-real-data only if you are certain.", why)
	}

	agent.StockCatalogue(ctx, model.Pass.MaxNewTitles)
	members := agent.RegisterMembers(ctx, model.Pass.MaxNewMembers)
	agent.Circulate(ctx, members, model.Pass.MaxActions)
	agent.RunChecks(ctx)

	return agent.Finish(), nil
}

func printReport(model *simulator.Model, r *simulator.Report) {
	mark := map[string]string{"ok": "OK", "degraded": "DEGRADED", "failed": "FAILED"}[r.Outcome]

	fmt.Printf("HOLibrary activity simulator - %s v%s\n", model.Name, model.Version)
	fmt.Printf("%s  (%.1fs)\n\n", mark, r.FinishedAt.Sub(r.StartedAt).Seconds())

	fmt.Println("  what it did")
	fmt.Printf("    titles imported   %d\n", r.BooksImported)
	fmt.Printf("    copies shelved    %d\n", r.CopiesAdded)
	fmt.Printf("    members added     %d\n", r.MembersCreated)
	fmt.Printf("    books lent        %d\n", r.LoansCreated)
	fmt.Printf("    books returned    %d\n", r.ReturnsMade)

	if len(r.Refusals) > 0 {
		fmt.Println("\n  rules that fired (these are the library working, not failing)")
		for code, n := range r.Refusals {
			fmt.Printf("    %-24s %d\n", code, n)
		}
	}

	fmt.Println("\n  consistency checks")
	for _, c := range r.Checks {
		status := "pass"
		if !c.Passed {
			status = "FAIL"
		}
		fmt.Printf("    [%s] %-46s %s\n", status, c.Name, c.Detail)
	}

	if len(r.Failures) > 0 {
		fmt.Println("\n  failures")
		for _, f := range r.Failures {
			fmt.Printf("    %s\n", f)
		}
	}
	fmt.Println()
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
