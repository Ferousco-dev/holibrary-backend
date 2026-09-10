package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// UpdateBook edits only the bibliographic record. The router requires staff.
func (h *CatalogueHandler) UpdateBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	p, err := decodeBookPatch(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		response.ValidationError(w, "The request body could not be read: "+err.Error(), nil)
		return
	}
	p.StaffID, _ = middleware.UserID(r.Context())
	book, err := h.catalogue.UpdateBook(r.Context(), id, p)
	if errors.Is(err, domain.ErrDuplicateISBN) {
		response.ValidationError(w, "Another catalogue title already uses that ISBN.", nil)
		return
	}
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, service.NewBookView(book), nil)
}

// Token-level parsing rejects duplicate and case-misspelled keys, which the
// standard struct decoder would otherwise accept. Null is never a clear operation:
// callers clear optional strings with "" and lists with [].
func decodeBookPatch(body io.Reader) (postgres.UpdateBookParams, error) {
	var p postgres.UpdateBookParams
	fields := map[string]any{
		"title": &p.Title, "subtitle": &p.Subtitle, "authors": &p.Authors,
		"subjects": &p.Subjects, "isbn13": &p.ISBN13, "isbn10": &p.ISBN10,
		"publisher": &p.Publisher, "published_year": &p.PublishedYear,
		"place_of_publication": &p.PlaceOfPublication, "call_number": &p.CallNumber,
		"edition": &p.Edition, "language": &p.Language, "description": &p.Description,
		"faculty": &p.Faculty, "department": &p.Department,
	}
	dec := json.NewDecoder(body)
	token, err := dec.Token()
	if err != nil {
		return p, err
	}
	if token != json.Delim('{') {
		return p, errors.New("expected a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return p, err
		}
		key, ok := token.(string)
		if !ok {
			return p, errors.New("expected a field name")
		}
		target, known := fields[key]
		if !known {
			return p, fmt.Errorf("unknown field %q", key)
		}
		if seen[key] {
			return p, fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return p, err
		}
		if !utf8.Valid(raw) {
			return p, fmt.Errorf("%s must contain valid UTF-8", key)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return p, fmt.Errorf("%s cannot be null", key)
		}
		// encoding/json accepts null as the zero string inside []string, so check
		// elements explicitly before decoding list fields.
		if key == "authors" || key == "subjects" {
			var values []json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				return p, err
			}
			for _, value := range values {
				if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return p, fmt.Errorf("%s cannot contain null", key)
				}
			}
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return p, fmt.Errorf("%s: %w", key, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		return p, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return p, errors.New("expected exactly one JSON object")
	}
	if len(seen) == 0 {
		return p, errors.New("supply at least one metadata field")
	}
	return p, nil
}
