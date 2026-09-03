package simulator

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Record writes a pass to simulation_runs.
//
// This is the one place the simulator touches the database directly, and the
// distinction matters: the SIMULATION goes entirely through the public API, so
// every pass re-tests authentication, authorisation and the borrowing rules as a
// real client would. Only the simulator's own telemetry is written here, because
// a run log is operational data about the tool rather than library data, and
// giving the API an endpoint for it would mean exposing a write path that exists
// solely for this program.
//
// Recording is optional. A pass that cannot write its own log still ran, and
// still printed its report.
func Record(ctx context.Context, db *pgxpool.Pool, r *Report) error {
	refusals, err := json.Marshal(r.Refusals)
	if err != nil {
		return err
	}
	failures, err := json.Marshal(r.Failures)
	if err != nil {
		return err
	}

	// The checks are the interesting part of a run log: they say whether the
	// library was still consistent, not merely whether the tool ran.
	notes, err := json.Marshal(r.Checks)
	if err != nil {
		return err
	}

	_, err = db.Exec(ctx, `
		INSERT INTO simulation_runs
		    (started_at, finished_at, outcome, books_imported, copies_added,
		     members_created, loans_created, returns_made, reservations,
		     refusals, failures, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		r.StartedAt, r.FinishedAt, r.Outcome,
		r.BooksImported, r.CopiesAdded, r.MembersCreated,
		r.LoansCreated, r.ReturnsMade, r.Reservations,
		refusals, failures, string(notes))
	return err
}
