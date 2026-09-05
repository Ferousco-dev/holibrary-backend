package simulator

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// SafeToRun reports whether this instance holds real member records.
//
// The simulator lends books, registers borrowers and closes loans. Against a
// database holding a real member roll that is not a demonstration, it is
// vandalism -- and the damage is quiet, because every action it takes is a
// perfectly valid library operation.
//
// So the default is to refuse. An instance with even one non-synthetic member
// must be opted into explicitly, and the operator has to mean it (DEF-023).
func (a *Agent) SafeToRun(ctx context.Context) (bool, string) {
	var members []struct {
		ID          string `json:"id"`
		FullName    string `json:"full_name"`
		Role        string `json:"role"`
		IsSynthetic bool   `json:"is_synthetic"`
	}
	if _, _, err := a.call(ctx, http.MethodGet,
		"/api/v1/members?per_page=100", nil, &members, a.staffToken); err != nil {
		return false, "could not read the member roll: " + err.Error()
	}

	// Staff accounts are expected and are not what this guard protects. The
	// simulator signs in as a librarian, so a librarian must exist; what must
	// not exist is a real BORROWER, whose loans the simulator could disturb.
	real := 0
	for _, m := range members {
		if m.Role == "member" && !m.IsSynthetic {
			real++
		}
	}
	if real > 0 {
		return false, fmt.Sprintf(
			"this instance holds %d real borrower record(s); refusing to generate activity against it", real)
	}
	return true, fmt.Sprintf("%d members, all synthetic", len(members))
}

// RunChecks asserts that the library is in a sane state after the pass.
//
// This is what separates a simulator from a seeder. Generating traffic proves
// the endpoints answer; these checks prove the answers are CONSISTENT. A system
// can return 200 to everything and still be quietly wrong about which books it
// holds.
//
// Each check is phrased as something a librarian would notice.
func (a *Agent) RunChecks(ctx context.Context) {
	a.check("the catalogue is reachable without signing in", func() (bool, string) {
		var catalogue []struct {
			ID string `json:"ID"`
		}
		status, _, err := a.call(ctx, http.MethodGet, "/api/v1/books?per_page=5", nil, &catalogue, "")
		if err != nil {
			return false, err.Error()
		}
		if status != http.StatusOK {
			return false, fmt.Sprintf("HTTP %d", status)
		}
		return len(catalogue) > 0, fmt.Sprintf("%d titles listed", len(catalogue))
	})

	a.check("availability never exceeds the number of copies held", func() (bool, string) {
		var catalogue []struct {
			Title        string `json:"Title"`
			Borrowable   int    `json:"borrowable"`
			Availability struct {
				TotalCopies int `json:"total_copies"`
				Available   int `json:"available"`
				OnLoan      int `json:"on_loan"`
			} `json:"Availability"`
		}
		if _, _, err := a.call(ctx, http.MethodGet,
			"/api/v1/books?per_page=100", nil, &catalogue, ""); err != nil {
			return false, err.Error()
		}
		for _, b := range catalogue {
			av := b.Availability
			// The states that must be impossible (invariant I-08).
			if av.Available < 0 || av.OnLoan < 0 {
				return false, fmt.Sprintf("%q has a negative count", b.Title)
			}
			if av.Available+av.OnLoan > av.TotalCopies {
				return false, fmt.Sprintf("%q: %d available + %d on loan exceeds %d held",
					b.Title, av.Available, av.OnLoan, av.TotalCopies)
			}
			if b.Borrowable > av.Available {
				return false, fmt.Sprintf("%q offers more copies than are on the shelf", b.Title)
			}
		}
		return true, fmt.Sprintf("%d titles consistent", len(catalogue))
	})

	a.check("no copy is out on more than one loan", func() (bool, string) {
		var loans []struct {
			CopyID string `json:"copy_id"`
			Title  string `json:"book_title"`
		}
		if _, _, err := a.call(ctx, http.MethodGet,
			"/api/v1/loans?open=true&per_page=100", nil, &loans, a.staffToken); err != nil {
			return false, err.Error()
		}
		seen := map[string]string{}
		for _, l := range loans {
			if prev, dup := seen[l.CopyID]; dup {
				// This is invariant I-01. The database should make it
				// unstorable; the check exists because a claim about a
				// constraint is worth less than an observation of one.
				return false, fmt.Sprintf("copy %s is out twice (%q and %q)", l.CopyID, prev, l.Title)
			}
			seen[l.CopyID] = l.Title
		}
		return true, fmt.Sprintf("%d open loans, all distinct copies", len(loans))
	})

	a.check("a member cannot reach staff routes", func() (bool, string) {
		// No token at all must be refused. If this ever returns 200 the member
		// roll is public.
		status, _, err := a.call(ctx, http.MethodGet, "/api/v1/members", nil, nil, "")
		if err != nil {
			return false, err.Error()
		}
		return status == http.StatusUnauthorized, fmt.Sprintf("unauthenticated GET /members returned %d", status)
	})

	a.check("the service reports its database reachable", func() (bool, string) {
		var health struct {
			Status   string `json:"status"`
			Database string `json:"database"`
		}
		if _, _, err := a.call(ctx, http.MethodGet, "/healthz", nil, &health, ""); err != nil {
			return false, err.Error()
		}
		return health.Database == "reachable", "database " + health.Database
	})
}

func (a *Agent) check(name string, run func() (bool, string)) {
	passed, detail := run()
	a.report.Checks = append(a.report.Checks, Check{Name: name, Passed: passed, Detail: detail})
}

// Finish grades the pass.
//
// Three outcomes rather than pass/fail, because they mean different things to
// whoever reads the log in the morning:
//
//	failed   - an invariant broke, or the API returned 5xx. Stop and look.
//	degraded - the pass ran but did nothing, or a check could not be evaluated.
//	           Often the external catalogue being down, which is survivable.
//	ok       - the library worked.
func (a *Agent) Finish() *Report {
	a.report.FinishedAt = time.Now().UTC()

	for _, c := range a.report.Checks {
		if !c.Passed {
			a.report.Outcome = "failed"
			return a.report
		}
	}
	if len(a.report.Failures) > 0 {
		a.report.Outcome = "failed"
		return a.report
	}
	if a.report.LoansCreated == 0 && a.report.BooksImported == 0 {
		a.report.Outcome = "degraded"
		return a.report
	}
	a.report.Outcome = "ok"
	return a.report
}
