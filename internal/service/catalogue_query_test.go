package service_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
)

func TestCatalogueQueryValidation(t *testing.T) {
	invalid := []string{"page=0", "page=-1", "page=oops", "page=1000001", "page=9999999999999999999999999999999", "per_page=0", "per_page=101", "per_page=1.5", "sort=random", "available=yes", "borrowable=TRUE", "wing=East", "year_from=0", "year_to=10000", "year_from=2025&year_to=2020", "year_to=no", "language=", "subject=%00", "author=a&author=b", "member_id=1", "class=QA", "isbn=abc", "q=" + strings.Repeat("a", 501)}
	for _, q := range invalid {
		t.Run(q[:min(len(q), 80)], func(t *testing.T) {
			v, _ := url.ParseQuery(q)
			_, _, err := service.ParseCatalogueQuery(v)
			if !errors.Is(err, domain.ErrInvalidSearch) {
				t.Fatalf("expected invalid query, got %v", err)
			}
		})
	}
	q, _ := url.ParseQuery("q=computer+science&subject=Computing&author=Ada&faculty=Science&department=Computing&year_from=1990&year_to=2026&language=en&wing=North&available=false&borrowable=true&sort=newest&page=3&per_page=7")
	p, page, err := service.ParseCatalogueQuery(q)
	if err != nil || page != 3 || p.Limit != 7 || p.Offset != 14 || p.Query != "computer science" || p.Available == nil || *p.Available || p.Borrowable == nil || !*p.Borrowable || p.YearFrom == nil || *p.YearFrom != 1990 {
		t.Fatalf("query: %+v page=%d err=%v", p, page, err)
	}
	p, page, err = service.ParseCatalogueQuery(url.Values{})
	if err != nil || page != 1 || p.Limit != 20 || p.Sort != "relevance" {
		t.Fatalf("defaults: %+v %d %v", p, page, err)
	}
}

func TestSavedQueryTypesAndAllowedFields(t *testing.T) {
	for _, body := range []string{`{"available":"true"}`, `{"borrowable":null}`, `{"year_from":"2000"}`, `{"year_to":2000.5}`, `{"page":null}`, `{"q":false}`, `{"user_id":"x"}`, `{"notifications":true}`, `{"query":{"q":"x"}}`, `{"year_from":2020,"year_to":1900}`} {
		var q map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &q); err != nil {
			t.Fatal(err)
		}
		if err := service.ValidateSavedQuery(q); !errors.Is(err, domain.ErrInvalidSearch) {
			t.Errorf("accepted %s: %v", body, err)
		}
	}
	var q map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"q":"computer science","available":true,"borrowable":false,"year_from":2000}`), &q)
	if err := service.ValidateSavedQuery(q); err != nil {
		t.Fatal(err)
	}
}

func TestBookViewPublicMetadataAndRetention(t *testing.T) {
	year := 2020
	b := domain.Book{Title: "Algorithms", Edition: "2", PublishedYear: &year, Publisher: "Press", Language: "en", Description: "A text", Subjects: []string{"Computing"}, Faculty: "Private internal mapping", Department: "Internal", Status: "active", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Availability: domain.Availability{Stock: 2, Available: 1}}
	raw, err := json.Marshal(service.NewBookView(b))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	for _, key := range []string{"edition", "publication_year", "publisher", "language", "description", "subjects", "created_at"} {
		if _, ok := v[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	for _, key := range []string{"Stock", "stock", "Faculty", "Department", "Status"} {
		if _, ok := v[key]; ok {
			t.Errorf("internal field %s leaked", key)
		}
	}
	if v["borrowable"] != float64(0) || v["is_available"] != false || v["shelf_copy_retained"] != true {
		t.Fatalf("retention projection: %s", raw)
	}
	raw, _ = json.Marshal(service.NewBookView(domain.Book{}))
	v = map[string]any{}
	_ = json.Unmarshal(raw, &v)
	for _, key := range []string{"edition", "publication_year", "publisher", "language", "description", "subjects", "created_at"} {
		if _, ok := v[key]; ok {
			t.Errorf("invented metadata %s", key)
		}
	}
}
