// Package books fetches bibliographic metadata from an external catalogue.
//
// The distinction this package exists to preserve:
//
//	an external API saying a book exists  !=  HOL owning a copy of it
//
// Nothing here creates a copy, sets availability, or answers "can I borrow
// this". It supplies title, author, ISBN and publication details to pre-fill a
// librarian's form; whether the library holds the book, in how many volumes, on
// which shelf, and who has one, is answered exclusively from our own database
// (REQ-017, DEC-007, invariant I-10).
package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Metadata is what an external catalogue can tell us about a title.
type Metadata struct {
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle,omitempty"`
	Authors       []string `json:"authors"`
	ISBN13        string   `json:"isbn13,omitempty"`
	ISBN10        string   `json:"isbn10,omitempty"`
	Publisher     string   `json:"publisher,omitempty"`
	PublishedYear *int     `json:"published_year,omitempty"`
	Subjects      []string `json:"subjects,omitempty"`
	// Source names where this came from, so a librarian reviewing the record
	// can see it was machine-supplied rather than catalogued by hand.
	Source string `json:"source"`
}

// ErrNotFound means the external catalogue has no record. It does not mean the
// library does not hold the book: Africana and OAU Publications are largely
// absent from public catalogues and are entered by hand (DEC-007).
var ErrNotFound = errors.New("no external record for that ISBN")

// OpenLibrary queries openlibrary.org.
//
// Chosen over Google Books because it needs no API key: no secret to leak, and
// nothing to expire the week of the defence.
type OpenLibrary struct {
	baseURL string
	client  *http.Client
}

// Allowed hosts for the external catalogue.
//
// The base URL is configuration rather than user input, but configuration is
// not the same as trustworthy: an environment variable set by a compromised
// deployment, a copied .env, or a typo pointing at an internal address turns
// this client into a way to make the server fetch URLs of somebody else's
// choosing. Server-side request forgery does not require the attacker to be a
// user; it requires the server to fetch what it is told (DEF-024).
var allowedCatalogueHosts = map[string]bool{
	"openlibrary.org":     true,
	"www.openlibrary.org": true,
	// Local addresses so the test suite and offline development can point at a
	// stub. These are unreachable from a deployed container anyway.
	"localhost": true,
	"127.0.0.1": true,
}

// safeBaseURL rejects a catalogue address that is not an allowed public host.
//
// It refuses rather than falls back silently: a system quietly using a different
// catalogue than its operator configured is worse than one that will not start.
func safeBaseURL(raw string) (string, error) {
	if raw == "" {
		return "https://openlibrary.org", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("the catalogue URL is not a valid URL: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("the catalogue URL must be http or https, not %q", u.Scheme)
	}
	host := u.Hostname()
	if !allowedCatalogueHosts[host] {
		return "", fmt.Errorf("%q is not an allowed catalogue host", host)
	}
	if u.Scheme == "http" && host != "localhost" && host != "127.0.0.1" {
		return "", fmt.Errorf("the catalogue must be reached over https")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host, "/"), nil
}

// NewOpenLibrary builds a client, refusing a catalogue address that is not an
// allowed host.
func NewOpenLibrary(baseURL string) (*OpenLibrary, error) {
	safe, err := safeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &OpenLibrary{
		baseURL: safe,
		// An external catalogue that hangs must not hang the librarian's form.
		// The point of I-10 is that our catalogue works without theirs.
		client: &http.Client{
			Timeout: 12 * time.Second,
			// Do not follow a redirect off the allowed host. A permitted
			// catalogue that redirects is a permitted catalogue handing this
			// client to somewhere else.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if !allowedCatalogueHosts[req.URL.Hostname()] {
					return fmt.Errorf("refusing a redirect to %q", req.URL.Hostname())
				}
				if len(via) >= 3 {
					return fmt.Errorf("too many redirects")
				}
				return nil
			},
		},
	}, nil
}

type searchResponse struct {
	Docs []struct {
		Title            string   `json:"title"`
		Subtitle         string   `json:"subtitle"`
		AuthorName       []string `json:"author_name"`
		ISBN             []string `json:"isbn"`
		Publisher        []string `json:"publisher"`
		FirstPublishYear int      `json:"first_publish_year"`
		Subject          []string `json:"subject"`
	} `json:"docs"`
}

// Search returns candidate records for a free-text query.
func (o *OpenLibrary) Search(ctx context.Context, query string, limit int) ([]Metadata, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	// Only the fields we use are requested; Open Library otherwise returns a
	// very large document per result.
	q := url.Values{
		"q":      {query},
		"limit":  {fmt.Sprint(limit)},
		"fields": {"title,subtitle,author_name,isbn,publisher,first_publish_year,subject"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		o.baseURL+"/search.json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent",
		"HOLibrary/1.0 (Obafemi Awolowo University; student project)")

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching the external catalogue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the external catalogue returned %d", resp.StatusCode)
	}

	var parsed searchResponse
	// Cap the read rather than trusting a third party with this process's memory.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("reading the external catalogue's reply: %w", err)
	}

	// The response is a third party's, so the number of records it returns is
	// bounded here rather than trusted. A reply with a hundred thousand entries
	// would otherwise become a hundred thousand allocations (DEF-024).
	if len(parsed.Docs) > limit {
		parsed.Docs = parsed.Docs[:limit]
	}

	out := make([]Metadata, 0, len(parsed.Docs))
	for _, d := range parsed.Docs {
		if strings.TrimSpace(d.Title) == "" {
			continue // a record with no title is not usable
		}
		m := Metadata{
			Title:    strings.TrimSpace(d.Title),
			Subtitle: strings.TrimSpace(d.Subtitle),
			Authors:  trimAll(d.AuthorName, 3),
			Subjects: trimAll(d.Subject, 4),
			Source:   "openlibrary.org",
		}
		if len(d.Publisher) > 0 {
			m.Publisher = strings.TrimSpace(d.Publisher[0])
		}
		if d.FirstPublishYear > 0 {
			year := d.FirstPublishYear
			m.PublishedYear = &year
		}
		m.ISBN13, m.ISBN10 = pickISBNs(d.ISBN)
		out = append(out, m)
	}
	return out, nil
}

// ByISBN looks up a single title, for the librarian holding the book.
func (o *OpenLibrary) ByISBN(ctx context.Context, isbn string) (Metadata, error) {
	isbn = strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(isbn))
	if isbn == "" {
		return Metadata{}, errors.New("an ISBN is required")
	}

	results, err := o.Search(ctx, "isbn:"+isbn, 1)
	if err != nil {
		return Metadata{}, err
	}
	if len(results) == 0 {
		return Metadata{}, ErrNotFound
	}
	return results[0], nil
}

func pickISBNs(all []string) (isbn13, isbn10 string) {
	for _, raw := range all {
		s := strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(raw))
		if len(s) == 13 && isbn13 == "" {
			isbn13 = s
		}
		if len(s) == 10 && isbn10 == "" {
			isbn10 = s
		}
		if isbn13 != "" && isbn10 != "" {
			break
		}
	}
	return isbn13, isbn10
}

func trimAll(in []string, max int) []string {
	out := make([]string, 0, max)
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
			if len(out) == max {
				break
			}
		}
	}
	return out
}
