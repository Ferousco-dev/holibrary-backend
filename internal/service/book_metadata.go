package service

import (
	"context"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/google/uuid"
)

// BookMetadataStore keeps editing separate from copy/circulation operations.
type BookMetadataStore interface {
	UpdateBook(context.Context, uuid.UUID, postgres.UpdateBookParams) (domain.Book, error)
}

func (s *CatalogueService) UpdateBook(ctx context.Context, id uuid.UUID, p postgres.UpdateBookParams) (domain.Book, error) {
	if p.StaffID == uuid.Nil {
		return domain.Book{}, domain.ErrUnauthenticated
	}
	p, err := validateBookMetadata(p)
	if err != nil {
		return domain.Book{}, err
	}
	store, ok := s.books.(BookMetadataStore)
	if !ok {
		return domain.Book{}, domain.ErrNotFound
	}
	return store.UpdateBook(ctx, id, p)
}

// validateBookMetadata is shared by create and edit. It validates only supplied
// values, so editing a title does not reinterpret untouched historical metadata.
func validateBookMetadata(p postgres.UpdateBookParams) (postgres.UpdateBookParams, error) {
	supplied := 0
	fields := []struct {
		value               **string
		max                 int
		required, multiline bool
	}{
		{&p.Title, 500, true, false}, {&p.Subtitle, 500, false, false},
		{&p.ISBN13, 40, false, false}, {&p.ISBN10, 40, false, false},
		{&p.Publisher, 500, false, false}, {&p.PlaceOfPublication, 500, false, false},
		{&p.CallNumber, 200, true, false}, {&p.Edition, 200, false, false},
		{&p.Language, 200, false, false}, {&p.Faculty, 200, false, false},
		{&p.Department, 200, false, false}, {&p.Description, 20000, false, true},
	}
	for _, field := range fields {
		if *field.value == nil {
			continue
		}
		supplied++
		value := strings.TrimSpace(**field.value)
		if !validMetadataText(value, field.max, field.multiline) || (field.required && value == "") {
			return p, domain.ErrInvalidBookMetadata
		}
		*field.value = &value
	}
	if p.CallNumber != nil {
		v := strings.ToUpper(*p.CallNumber)
		if !callNumberPattern.MatchString(v) {
			return p, domain.ErrInvalidBookMetadata
		}
	}
	for _, field := range []struct {
		value  **string
		length int
	}{{&p.ISBN13, 13}, {&p.ISBN10, 10}} {
		if *field.value == nil {
			continue
		}
		v := strings.ToUpper(normaliseISBN(**field.value))
		if v != "" && !validISBN(v, field.length) {
			return p, domain.ErrInvalidBookMetadata
		}
		*field.value = &v
	}
	if p.PublishedYear != nil {
		supplied++
		if *p.PublishedYear < 1000 || *p.PublishedYear > time.Now().UTC().Year()+1 || *p.PublishedYear > 2100 {
			return p, domain.ErrInvalidBookMetadata
		}
	}
	for _, field := range []**[]string{&p.Authors, &p.Subjects} {
		if *field == nil {
			continue
		}
		supplied++
		if len(**field) > 100 {
			return p, domain.ErrInvalidBookMetadata
		}
		values := make([]string, 0, len(**field))
		seen := map[string]bool{}
		for _, value := range **field {
			v := strings.TrimSpace(value)
			if v == "" || !validMetadataText(v, 200, false) || seen[v] {
				return p, domain.ErrInvalidBookMetadata
			}
			seen[v] = true
			values = append(values, v)
		}
		*field = &values
	}
	if p.Subjects != nil {
		sort.Strings(*p.Subjects)
	}
	if supplied == 0 {
		return p, domain.ErrInvalidBookMetadata
	}
	return p, nil
}

func validMetadataText(value string, max int, multiline bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}

// ISBN checksums reject mistyped identifiers before they enter the catalogue.
// Normalisation matches the existing creation path (spaces and hyphens).
func validISBN(value string, length int) bool {
	if len(value) != length {
		return false
	}
	sum := 0
	if length == 10 {
		for i, r := range value {
			digit := int(r - '0')
			if i == 9 && r == 'X' {
				digit = 10
			} else if r < '0' || r > '9' {
				return false
			}
			sum += (10 - i) * digit
		}
		return sum%11 == 0
	}
	if !strings.HasPrefix(value, "978") && !strings.HasPrefix(value, "979") {
		return false
	}
	for i, r := range value {
		if r < '0' || r > '9' {
			return false
		}
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += weight * int(r-'0')
	}
	return sum%10 == 0
}
