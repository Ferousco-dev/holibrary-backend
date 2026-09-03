// Package handler translates HTTP into service calls and back.
//
// Handlers decode, validate shape, call one service method, and render. They
// hold no business rules: if a rule appears here it is in the wrong layer and
// cannot be tested without a web server (docs/design.md DES-001).
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

// timeFormat is RFC 3339 in UTC, the only timestamp format this API emits.
const timeFormat = "2006-01-02T15:04:05Z"

// maxBodyBytes caps a request body. Without it, a single large upload could
// exhaust the memory of a small container.
const maxBodyBytes = 1 << 20 // 1 MiB

// decode reads and validates the shape of a JSON body (NFR-007).
//
// Unknown fields are rejected rather than ignored, so a client that misspells
// "category" is told, instead of silently getting a member with no borrowing
// entitlement.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		response.ValidationError(w, "The request body could not be read: "+err.Error(), nil)
		return false
	}
	return true
}

// pathUUID reads a UUID path parameter.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		response.ValidationError(w, "The "+name+" in the URL is not a valid identifier.", nil)
		return uuid.Nil, false
	}
	return id, true
}

// pagination reads page and per_page, with bounds.
//
// per_page is capped so that one request cannot ask for the entire catalogue.
func pagination(r *http.Request) (limit, offset, page int) {
	page = atoiOr(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	limit = atoiOr(r.URL.Query().Get("per_page"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	return limit, (page - 1) * limit, page
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func boolParam(r *http.Request, name string) bool {
	v := r.URL.Query().Get(name)
	return v == "true" || v == "1"
}
