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

func NewOpenLibrary(baseURL string) *OpenLibrary {
	if baseURL == "" {
		baseURL = "https://openlibrary.org"
	}
	return &OpenLibrary{
		baseURL: strings.TrimRight(baseURL, "/"),
		// An external catalogue that hangs must not hang the librarian's form.
		// The point of I-10 is that our catalogue works without theirs.
		client: &http.Client{Timeout: 12 * time.Second},
	}
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
