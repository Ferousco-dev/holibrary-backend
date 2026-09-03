package simulator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/books"
)

// Report is what one pass produces.
//
// It is the answer to "did the library still work last night", so it records
// refusals as well as successes. A simulator that counted only what succeeded
// would report a healthy system while the library had quietly stopped lending.
type Report struct {
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	Outcome        string         `json:"outcome"` // ok | degraded | failed
	BooksImported  int            `json:"books_imported"`
	CopiesAdded    int            `json:"copies_added"`
	MembersCreated int            `json:"members_created"`
	LoansCreated   int            `json:"loans_created"`
	ReturnsMade    int            `json:"returns_made"`
	Reservations   int            `json:"reservations"`
	Refusals       map[string]int `json:"refusals"`
	Failures       []string       `json:"failures"`
	Checks         []Check        `json:"checks"`
}

// Check is one assertion about the state of the library after the pass.
type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

// Agent drives the API as a client.
type Agent struct {
	baseURL   string
	http      *http.Client
	model     *Model
	catalogue *books.OpenLibrary
	rng       *rand.Rand

	staffToken string
	report     *Report
}

// Options configures a run.
type Options struct {
	BaseURL         string
	LibrarianLogin  string
	LibrarianSecret string
	CatalogueURL    string
	Seed            int64
}

func NewAgent(model *Model, opts Options) (*Agent, error) {
	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	catalogue, err := books.NewOpenLibrary(opts.CatalogueURL)
	if err != nil {
		return nil, err
	}

	// The simulator sends a librarian's bearer token to whatever -url names, so
	// that address decides who receives staff credentials. A hostile or
	// mistyped value hands them over. Refuse anything that is not plainly local
	// or https (DEF-025).
	base, err := safeTargetURL(opts.BaseURL)
	if err != nil {
		return nil, err
	}

	return &Agent{
		baseURL:   base,
		http:      &http.Client{Timeout: 20 * time.Second},
		model:     model,
		catalogue: catalogue,
		rng:       rand.New(rand.NewSource(seed)),
		report: &Report{
			StartedAt: time.Now().UTC(),
			Refusals:  map[string]int{},
		},
	}, nil
}

// safeTargetURL rejects a target that should not receive staff credentials.
//
// The simulator authenticates as a librarian and sends that bearer token with
// every request. Whatever -url points at therefore receives it. Plain http is
// allowed only for a local address, where the traffic does not leave the
// machine (DEF-025).
func safeTargetURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil {
		return "", fmt.Errorf("the target URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("the target URL must be http or https, not %q", u.Scheme)
	}

	host := u.Hostname()
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "api"
	if u.Scheme == "http" && !isLocal {
		return "", fmt.Errorf(
			"refusing to send librarian credentials to %q over plain http; use https", host)
	}
	return u.Scheme + "://" + u.Host, nil
}

// apiError is the API's error envelope.
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call makes one API request and decodes the reply.
//
// A 4xx is not treated as a failure. Most of them are the library correctly
// refusing something -- a member at their limit, a reference volume, a retained
// last copy -- and those refusals are the most interesting output of a pass:
// they are the business rules firing. Only transport errors and 5xx count as
// the system being broken.
func (a *Agent) call(ctx context.Context, method, path string, body, out any, token string) (int, string, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, reader)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := a.http.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()

	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode >= 400 {
		var e apiError
		_ = json.Unmarshal(payload, &e)
		code := e.Error.Code
		if code == "" {
			code = fmt.Sprintf("HTTP_%d", resp.StatusCode)
		}
		a.report.Refusals[code]++
		if resp.StatusCode >= 500 {
			// The library breaking is different from the library saying no.
			a.report.Failures = append(a.report.Failures,
				fmt.Sprintf("%s %s -> %d %s", method, path, resp.StatusCode, code))
		}
		return resp.StatusCode, code, nil
	}

	if out != nil && len(payload) > 0 {
		var envelope struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return resp.StatusCode, "", err
		}
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return resp.StatusCode, "", err
		}
	}
	return resp.StatusCode, "", nil
}

// Token returns the current staff token, so a resident run can carry it into
// the next pass.
func (a *Agent) Token() string { return a.staffToken }

// UseToken adopts a token obtained by an earlier pass.
func (a *Agent) UseToken(t string) { a.staffToken = t }

// SignIn authenticates as the librarian the simulator acts through.
//
// It uses a real staff account and the real login endpoint, so every pass also
// exercises authentication. There is no back door for the simulator; if login
// breaks, the simulator is the first thing to notice.
func (a *Agent) SignIn(ctx context.Context, login, password string) error {
	var session struct {
		AccessToken        string `json:"access_token"`
		MustChangePassword bool   `json:"must_change_password"`
	}
	status, code, err := a.call(ctx, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"login": login, "password": password}, &session, "")
	if err != nil {
		return fmt.Errorf("signing in: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("signing in: %s", code)
	}
	if session.MustChangePassword {
		return fmt.Errorf("the simulator's account still holds a temporary password; change it first")
	}
	if status == http.StatusTooManyRequests {
		return fmt.Errorf("signing in: rate limited")
	}
	a.staffToken = session.AccessToken
	return nil
}

// Authenticated reports whether the held token is still accepted.
//
// A cheap staff-only request. Checking costs one call; discovering the token
// expired halfway through a pass costs a confusing half-finished report.
func (a *Agent) Authenticated(ctx context.Context) bool {
	if a.staffToken == "" {
		return false
	}
	status, _, err := a.call(ctx, http.MethodGet, "/api/v1/admin/dashboard", nil, nil, a.staffToken)
	if err != nil {
		return false
	}
	// The dashboard is librarian-only, so a 200 proves both that the token is
	// valid and that it still carries staff authority.
	return status == http.StatusOK
}
