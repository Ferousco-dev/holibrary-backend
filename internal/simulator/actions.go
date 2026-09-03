package simulator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/books"
)

// StockCatalogue imports titles from the external catalogue and shelves copies.
//
// The external source supplies bibliographic metadata ONLY. How many copies HOL
// holds, which shelf they sit on and whether any may be borrowed is decided
// here and stored in our own database. An external API saying a book exists is
// not the same as the library owning one (invariant I-10).
func (a *Agent) StockCatalogue(ctx context.Context, maxTitles int) {
	query, lcc := a.model.PickSubject(a.rng)

	found, err := a.catalogue.Search(ctx, query, maxTitles)
	if err != nil {
		// The external catalogue being down must not fail the pass. Our own
		// catalogue works without it, and that is the point of I-10.
		a.report.Failures = append(a.report.Failures,
			fmt.Sprintf("external catalogue unavailable for %q: %v", query, err))
		return
	}

	for _, meta := range found {
		callNumber := a.callNumberFor(lcc, meta)

		var created struct {
			ID string `json:"ID"`
		}
		status, _, err := a.call(ctx, http.MethodPost, "/api/v1/books", map[string]any{
			"title":          truncate(meta.Title, 200),
			"subtitle":       truncate(meta.Subtitle, 200),
			"isbn13":         meta.ISBN13,
			"isbn10":         meta.ISBN10,
			"publisher":      truncate(meta.Publisher, 120),
			"published_year": meta.PublishedYear,
			"call_number":    callNumber,
			"description":    "Imported from " + meta.Source + " by the activity simulator.",
			"authors":        meta.Authors,
			"subjects":       meta.Subjects,
		}, &created, a.staffToken)
		if err != nil {
			a.report.Failures = append(a.report.Failures, "creating a book: "+err.Error())
			continue
		}
		if status != http.StatusCreated {
			// CONFLICT is expected and healthy: the title is already stocked.
			continue
		}
		a.report.BooksImported++

		// The library decides its own holdings. Most titles are held in one or
		// two copies, a few core texts in many.
		for i := 0; i < a.model.PickCopyCount(a.rng); i++ {
			policy := a.model.PickLoanPolicy(a.rng)
			status, _, err := a.call(ctx, http.MethodPost,
				"/api/v1/books/"+created.ID+"/copies", map[string]any{
					"accession_number": a.accessionNumber(),
					"loan_policy":      policy,
				}, nil, a.staffToken)
			if err == nil && status == http.StatusCreated {
				a.report.CopiesAdded++
			}
		}
	}
}

// callNumberFor builds a plausible Library of Congress class mark.
//
// HOL classifies with LCC, so a generated call number must look like one or the
// API rejects it -- which is itself a useful check that the validation still
// works every pass. The subject decides the letter, so the shelf wing derives
// correctly: classes A-J land in the South wing and K-Z in the North (DOM-001,
// DOM-003).
func (a *Agent) callNumberFor(lcc string, meta books.Metadata) string {
	// A Cutter-style suffix from the first author's surname, as a real
	// catalogue record carries.
	cutter := "A1"
	if len(meta.Authors) > 0 {
		fields := strings.Fields(meta.Authors[0])
		surname := fields[len(fields)-1]
		if len(surname) >= 2 {
			cutter = strings.ToUpper(surname[:1]) + strings.ToLower(surname[1:2]) +
				fmt.Sprint(a.rng.Intn(90)+10)
		}
	}
	return fmt.Sprintf("%s%d.%d .%s", lcc, a.rng.Intn(900)+76, a.rng.Intn(99), cutter)
}

// RegisterMembers creates synthetic borrowers.
//
// Registration goes through the librarian endpoint, exactly as it would at the
// desk, because there is no self-registration in this system and the simulator
// does not get an exception (DOM-006).
func (a *Agent) RegisterMembers(ctx context.Context, count int) []SimMember {
	var created []SimMember

	for i := 0; i < count; i++ {
		archetype := a.model.PickArchetype(a.rng)
		name, department, level := a.model.PickName(a.rng)
		identifier := fmt.Sprintf("SIM/%d/%04d", time.Now().Year(), a.rng.Intn(10000))

		var result struct {
			Member struct {
				ID string `json:"id"`
			} `json:"member"`
			TemporaryPassword string `json:"temporary_password"`
		}
		status, _, err := a.call(ctx, http.MethodPost, "/api/v1/members", map[string]any{
			"identifier": identifier,
			// A clearly non-deliverable domain. Nothing the simulator creates
			// should ever be able to email a real person.
			"email":      strings.ToLower(strings.ReplaceAll(identifier, "/", ".")) + "@simulated.invalid",
			"full_name":  name,
			"category":   archetype.Category,
			"department": department,
			"level":      level,
			// Flagged, so a simulated borrower can never be mistaken for a real
			// student in a report or a conversation with a librarian, and so
			// every trace of the simulator is removable in one statement when
			// the library's real records arrive (DEC-021).
			"is_synthetic": true,
		}, &result, a.staffToken)
		if err != nil || status != http.StatusCreated {
			continue
		}

		a.report.MembersCreated++
		created = append(created, SimMember{
			ID:        result.Member.ID,
			Name:      name,
			Archetype: archetype,
		})
	}
	return created
}

// SimMember is a synthetic borrower and the behaviour profile driving them.
type SimMember struct {
	ID        string
	Name      string
	Archetype Archetype
}

// Circulate makes the members behave: borrowing, returning and reserving.
func (a *Agent) Circulate(ctx context.Context, members []SimMember, maxActions int) {
	if len(members) == 0 {
		return
	}

	// One page of titles, ordered as the catalogue returns them, sampled with a
	// Zipf bias so demand concentrates on a few books.
	var catalogue []struct {
		ID           string `json:"ID"`
		Title        string `json:"Title"`
		Borrowable   int    `json:"borrowable"`
		Availability struct {
			Available int `json:"available"`
		} `json:"Availability"`
	}
	if _, _, err := a.call(ctx, http.MethodGet,
		"/api/v1/books?per_page=100", nil, &catalogue, ""); err != nil || len(catalogue) == 0 {
		return
	}

	for action := 0; action < maxActions; action++ {
		member := members[a.rng.Intn(len(members))]

		switch {
		case a.rng.Float64() < member.Archetype.ReturnRate:
			a.returnSomething(ctx)
		case a.rng.Float64() < member.Archetype.BorrowRate:
			idx := ZipfIndex(a.rng, len(catalogue), a.model.Popularity.ZipfExponent)
			a.borrow(ctx, member, catalogue[idx].ID)
		case a.rng.Float64() < member.Archetype.ReserveRate:
			idx := ZipfIndex(a.rng, len(catalogue), a.model.Popularity.ZipfExponent)
			a.reserve(ctx, catalogue[idx].ID)
		}
	}
}

// borrow finds a free copy of a title and lends it.
func (a *Agent) borrow(ctx context.Context, member SimMember, bookID string) {
	var detail struct {
		Copies []struct {
			ID         string `json:"ID"`
			Status     string `json:"Status"`
			LoanPolicy string `json:"LoanPolicy"`
		} `json:"copies"`
	}
	if _, _, err := a.call(ctx, http.MethodGet, "/api/v1/books/"+bookID, nil, &detail, a.staffToken); err != nil {
		return
	}

	for _, c := range detail.Copies {
		if c.Status != "available" || c.LoanPolicy != "circulating" {
			continue
		}
		status, _, err := a.call(ctx, http.MethodPost, "/api/v1/loans", map[string]any{
			"copy_id": c.ID, "member_id": member.ID,
		}, nil, a.staffToken)
		if err != nil {
			return
		}
		if status == http.StatusCreated {
			a.report.LoansCreated++
		}
		// Whatever the answer, one attempt per action. A refusal has already
		// been counted, and it is a rule firing rather than a failure.
		return
	}
}

// returnSomething brings back a book a SIMULATED member is holding.
//
// The filter is not a nicety. Asking for all open loans and returning one at
// random meant the simulator could close a real student's loan and put their
// book back on the shelf while they still had it -- silent corruption of the
// exact record this system exists to keep. It now sees only its own members'
// loans (DEF-023).
func (a *Agent) returnSomething(ctx context.Context) {
	var loans []struct {
		ID string `json:"id"`
	}
	if _, _, err := a.call(ctx, http.MethodGet,
		"/api/v1/loans?open=true&synthetic=true&per_page=40", nil, &loans, a.staffToken); err != nil || len(loans) == 0 {
		return
	}

	loan := loans[a.rng.Intn(len(loans))]
	status, _, err := a.call(ctx, http.MethodPost,
		"/api/v1/loans/"+loan.ID+"/return", nil, nil, a.staffToken)
	if err == nil && status == http.StatusOK {
		a.report.ReturnsMade++
	}
}

// reserve joins the queue for a title. Members place their own reservations, so
// this would need the member's own token; the simulator records the intent and
// leaves the call to a future pass that signs members in individually.
func (a *Agent) reserve(ctx context.Context, bookID string) {
	// Deliberately unimplemented rather than faked: reservations belong to the
	// member, and the simulator holds only a staff token. Making this work
	// honestly means signing in as each synthetic member, which is a larger
	// change than this pass needs. Recorded so the gap is visible.
	_ = ctx
	_ = bookID
}

func (a *Agent) accessionNumber() string {
	return fmt.Sprintf("SIM-%06d", a.rng.Intn(1000000))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return strings.TrimSpace(s[:n])
	}
	return s
}
