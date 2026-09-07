package service

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
)

// ParseCatalogueQuery validates the public search contract, including legacy
// access points. Repeated keys are rejected rather than silently choosing one.
func ParseCatalogueQuery(q url.Values) (postgres.SearchParams, int, error) {
	p := postgres.SearchParams{Limit: 20, Sort: "relevance"}
	page := 1
	invalid := func() (postgres.SearchParams, int, error) { return postgres.SearchParams{}, 0, domain.ErrInvalidSearch }
	for key, values := range q {
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return invalid()
		}
		v := strings.TrimSpace(values[0])
		switch key {
		case "q", "title", "author", "subject", "faculty", "department", "language", "isbn", "call_number", "class":
			max := 200
			if key == "q" {
				max = 500
			}
			if !utf8.ValidString(v) || utf8.RuneCountInString(v) > max || strings.IndexFunc(v, unicode.IsControl) >= 0 {
				return invalid()
			}
			switch key {
			case "q":
				p.Query = v
			case "title":
				p.Title = v
			case "author":
				p.Author = v
			case "subject":
				p.Subject = v
			case "faculty":
				p.Faculty = v
			case "department":
				p.Department = v
			case "language":
				p.Language = v
			case "isbn":
				v = normaliseISBN(v)
				if len(v) != 10 && len(v) != 13 {
					return invalid()
				}
				for i, c := range v {
					if (c < '0' || c > '9') && !(len(v) == 10 && i == 9 && (c == 'X' || c == 'x')) {
						return invalid()
					}
				}
				p.ISBN = strings.ToUpper(v)
			case "call_number":
				p.CallNumber = v
			case "class":
				v = strings.ToUpper(v)
				if len(v) != 1 || v[0] < 'A' || v[0] > 'Z' {
					return invalid()
				}
				p.LCCClass = v
			}
		case "wing":
			if v != "South" && v != "North" && v != "Unknown" {
				return invalid()
			}
			p.Wing = v
		case "available", "borrowable":
			var b bool
			switch v {
			case "true", "1":
				b = true
			case "false", "0":
				b = false
			default:
				return invalid()
			}
			if key == "available" {
				p.Available = &b
			} else {
				p.Borrowable = &b
			}
		case "year_from", "year_to":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 9999 {
				return invalid()
			}
			if key == "year_from" {
				p.YearFrom = &n
			} else {
				p.YearTo = &n
			}
		case "sort":
			switch v {
			case "relevance", "title", "newest", "oldest":
				p.Sort = v
			default:
				return invalid()
			}
		case "page", "per_page":
			n, err := strconv.Atoi(v)
			max := 100
			if key == "page" {
				max = 1000000
			}
			if err != nil || n < 1 || n > max {
				return invalid()
			}
			if key == "page" {
				page = n
			} else {
				p.Limit = n
			}
		default:
			return invalid()
		}
	}
	if p.YearFrom != nil && p.YearTo != nil && *p.YearFrom > *p.YearTo {
		return invalid()
	}
	p.Offset = (page - 1) * p.Limit
	return p, page, nil
}

// ValidateSavedQuery uses the same semantics as discovery, while requiring JSON
// booleans/numbers and refusing nulls, owner identifiers and unrecognised fields.
func ValidateSavedQuery(query map[string]json.RawMessage) error {
	if query == nil {
		return domain.ErrInvalidSearch
	}
	values := url.Values{}
	for key, raw := range query {
		if strings.TrimSpace(string(raw)) == "null" {
			return domain.ErrInvalidSearch
		}
		switch key {
		case "available", "borrowable":
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				return domain.ErrInvalidSearch
			}
			values.Set(key, strconv.FormatBool(v))
		case "year_from", "year_to", "page", "per_page":
			var v int
			if err := json.Unmarshal(raw, &v); err != nil {
				return domain.ErrInvalidSearch
			}
			values.Set(key, strconv.Itoa(v))
		default:
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				return domain.ErrInvalidSearch
			}
			values.Set(key, v)
		}
	}
	_, _, err := ParseCatalogueQuery(values)
	return err
}
