// Package simulator drives the library API the way a real library would.
//
// What this is: a scheduled agent that stocks the catalogue from an external
// bibliographic source, registers members, and makes them borrow, return and
// reserve books according to a probabilistic behaviour model. It exists so the
// system can be seen alive and populated rather than empty, and so that any
// morning somebody can look at last night's run and know the library still works.
//
// What this is NOT: an AI model. There is no training, no learned weights and no
// inference. The behaviour model in model.json is a set of hand-chosen
// probabilities -- closer to the loot tables in a game than to a neural network.
// Calling it AI would be a claim nobody could defend, so it is not called that
// anywhere in this codebase.
//
// The correct name for the pattern is SYNTHETIC MONITORING: exercising a live
// system with generated traffic to prove it works, rather than waiting for a
// real user to discover that it does not.
//
// It drives the PUBLIC HTTP API, not the database, deliberately. Writing rows
// directly would populate the catalogue while proving nothing; going through the
// API means every pass re-tests authentication, authorisation, validation, the
// borrowing rules and the concurrency guards, exactly as a librarian's browser
// would.
package simulator

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
)

//go:embed model.json
var modelBytes []byte

// Model is the behaviour profile. It is embedded in the binary so a scheduled
// run cannot fail for want of a config file.
type Model struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`

	Collection struct {
		Subjects []struct {
			Query  string  `json:"query"`
			LCC    string  `json:"lcc"`
			Weight float64 `json:"weight"`
		} `json:"subjects"`
		CopiesPerTitle struct {
			Distribution []struct {
				Copies int     `json:"copies"`
				Weight float64 `json:"weight"`
			} `json:"distribution"`
		} `json:"copies_per_title"`
		LoanPolicy struct {
			Distribution []struct {
				Policy string  `json:"policy"`
				Weight float64 `json:"weight"`
			} `json:"distribution"`
		} `json:"loan_policy"`
	} `json:"collection"`

	Members struct {
		Archetypes []Archetype `json:"archetypes"`
		Names      struct {
			Given  []string `json:"given"`
			Family []string `json:"family"`
		} `json:"names"`
		Departments []string `json:"departments"`
		Levels      []string `json:"levels"`
	} `json:"members"`

	Popularity struct {
		ZipfExponent float64 `json:"zipf_exponent"`
	} `json:"popularity"`

	Pass struct {
		MaxNewTitles  int `json:"max_new_titles"`
		MaxNewMembers int `json:"max_new_members"`
		MaxActions    int `json:"max_actions"`
	} `json:"pass"`
}

// Archetype is one kind of reader.
type Archetype struct {
	Name            string  `json:"name"`
	Weight          float64 `json:"weight"`
	Category        string  `json:"category"`
	BorrowRate      float64 `json:"borrow_rate"`
	ReturnRate      float64 `json:"return_rate"`
	ReserveRate     float64 `json:"reserve_rate"`
	OverdueTendency float64 `json:"overdue_tendency"`
}

// LoadModel reads the embedded behaviour profile.
func LoadModel() (*Model, error) {
	var m Model
	if err := json.Unmarshal(modelBytes, &m); err != nil {
		return nil, fmt.Errorf("parsing the behaviour model: %w", err)
	}
	if len(m.Members.Archetypes) == 0 || len(m.Collection.Subjects) == 0 {
		return nil, fmt.Errorf("the behaviour model is empty")
	}
	return &m, nil
}

// pick chooses one item from a weighted list.
//
// Weights need not sum to 1: they are normalised by their total, so a subject
// can be added to model.json without rebalancing every other line.
func pick[T any](rng *rand.Rand, items []T, weight func(T) float64) T {
	var total float64
	for _, it := range items {
		total += weight(it)
	}
	roll := rng.Float64() * total
	for _, it := range items {
		roll -= weight(it)
		if roll <= 0 {
			return it
		}
	}
	return items[len(items)-1]
}

// ZipfIndex returns an index into a list of n items, heavily favouring the
// front.
//
// Library borrowing is not uniform. A handful of core texts absorb most of the
// demand, which is precisely what makes queues form on the titles that matter
// and leaves the long tail sitting on the shelf. Sampling uniformly would
// produce activity that looks busy but never reproduces the one situation the
// system most needs to handle: everybody wanting the same book.
func ZipfIndex(rng *rand.Rand, n int, exponent float64) int {
	if n <= 1 {
		return 0
	}
	// Normalising constant for the truncated Zipf distribution.
	var norm float64
	for i := 1; i <= n; i++ {
		norm += 1 / math.Pow(float64(i), exponent)
	}
	roll := rng.Float64() * norm
	for i := 1; i <= n; i++ {
		roll -= 1 / math.Pow(float64(i), exponent)
		if roll <= 0 {
			return i - 1
		}
	}
	return n - 1
}

// PickArchetype chooses a reader profile.
func (m *Model) PickArchetype(rng *rand.Rand) Archetype {
	return pick(rng, m.Members.Archetypes, func(a Archetype) float64 { return a.Weight })
}

// PickSubject chooses what to stock next, and the LCC class it files under.
func (m *Model) PickSubject(rng *rand.Rand) (query, lcc string) {
	s := pick(rng, m.Collection.Subjects, func(s struct {
		Query  string  `json:"query"`
		LCC    string  `json:"lcc"`
		Weight float64 `json:"weight"`
	}) float64 {
		return s.Weight
	})
	return s.Query, s.LCC
}

// PickCopyCount chooses how many volumes of a title the library holds.
func (m *Model) PickCopyCount(rng *rand.Rand) int {
	d := pick(rng, m.Collection.CopiesPerTitle.Distribution, func(c struct {
		Copies int     `json:"copies"`
		Weight float64 `json:"weight"`
	}) float64 {
		return c.Weight
	})
	return d.Copies
}

// PickLoanPolicy chooses whether a copy circulates.
func (m *Model) PickLoanPolicy(rng *rand.Rand) string {
	d := pick(rng, m.Collection.LoanPolicy.Distribution, func(p struct {
		Policy string  `json:"policy"`
		Weight float64 `json:"weight"`
	}) float64 {
		return p.Weight
	})
	return d.Policy
}

// PickName returns a member's name, and the department and level they study in.
func (m *Model) PickName(rng *rand.Rand) (fullName, department, level string) {
	given := m.Members.Names.Given[rng.Intn(len(m.Members.Names.Given))]
	family := m.Members.Names.Family[rng.Intn(len(m.Members.Names.Family))]
	dept := m.Members.Departments[rng.Intn(len(m.Members.Departments))]
	lvl := m.Members.Levels[rng.Intn(len(m.Members.Levels))]
	return given + " " + family, dept, lvl
}
