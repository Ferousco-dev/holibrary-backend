// Package response renders every HTTP reply the API sends.
//
// Success and failure each have exactly one shape, defined here and nowhere
// else, so the frontend can rely on them and no handler can invent a third
// (NFR-017).
package response

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
)

// Envelope wraps a successful payload. Data is always under "data" so a
// response can grow a "meta" block without breaking a client.
type Envelope struct {
	Data any   `json:"data"`
	Meta *Meta `json:"meta,omitempty"`
}

// Meta carries pagination.
type Meta struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

// ErrorBody is the only error shape the API emits.
//
// Code is stable and machine-readable, so the frontend switches on it; Message
// is written for a human and is safe to display. Nothing internal appears in
// either.
type ErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Details any    `json:"details,omitempty"`
	} `json:"error"`
}

// JSON writes a success response.
func JSON(w http.ResponseWriter, status int, data any, meta *Meta) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(Envelope{Data: data, Meta: meta}); err != nil {
		// The status line is already sent, so this can only be logged.
		slog.Error("writing response body", "error", err)
	}
}

// Error writes a failure response with an explicit code and message.
func Error(w http.ResponseWriter, status int, code, message string, details any) {
	var body ErrorBody
	body.Error.Code = code
	body.Error.Message = message
	body.Error.Details = details

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// errorMap is the single place a domain error becomes an HTTP status.
//
// Keeping it here means a handler never has to decide what a rule violation
// looks like on the wire, and a new rule cannot accidentally return 500.
var errorMap = []struct {
	err     error
	status  int
	code    string
	message string
}{
	{domain.ErrInvalidCredentials, http.StatusUnauthorized, "INVALID_CREDENTIALS",
		"Incorrect login details."},
	{domain.ErrUnauthenticated, http.StatusUnauthorized, "UNAUTHENTICATED",
		"Please sign in to continue."},
	{domain.ErrTokenInvalid, http.StatusUnauthorized, "TOKEN_INVALID",
		"That link or session has expired. Please request a new one."},
	{domain.ErrForbidden, http.StatusForbidden, "FORBIDDEN",
		"You do not have permission to do that."},
	{domain.ErrMemberNotActive, http.StatusForbidden, "MEMBER_NOT_ACTIVE",
		"This membership is not active. Please speak to the library desk."},
	{domain.ErrMustChangePassword, http.StatusForbidden, "MUST_CHANGE_PASSWORD",
		"You must change your password before continuing."},
	{domain.ErrNotFound, http.StatusNotFound, "NOT_FOUND",
		"We could not find that."},
	{domain.ErrDuplicateAccession, http.StatusConflict, "DUPLICATE_ACCESSION",
		"That accession number already belongs to another copy."},
	{domain.ErrConflict, http.StatusConflict, "CONFLICT",
		"That conflicts with a record we already hold."},
	{domain.ErrCopyNotAvailable, http.StatusConflict, "COPY_NOT_AVAILABLE",
		"That copy is already on loan."},
	{domain.ErrCopyNotBorrowable, http.StatusConflict, "COPY_NOT_BORROWABLE",
		"That copy does not circulate and must be used in the library."},
	{domain.ErrLoanAlreadyClosed, http.StatusConflict, "LOAN_ALREADY_CLOSED",
		"That loan has already been returned."},
	{domain.ErrLastCopyRetained, http.StatusConflict, "LAST_COPY_RETAINED",
		"This is the last copy on the shelf and is kept in the library for reference. It can be consulted here, but not borrowed."},
	{domain.ErrAlreadyReserved, http.StatusConflict, "ALREADY_RESERVED",
		"You already have a reservation for this title."},
	{domain.ErrCopiesAvailable, http.StatusConflict, "COPIES_AVAILABLE",
		"Copies are on the shelf, so no reservation is needed."},
	{domain.ErrLoanLimitReached, http.StatusUnprocessableEntity, "LOAN_LIMIT_REACHED",
		"This member has reached the borrowing limit for their category."},
	{domain.ErrNoCategory, http.StatusUnprocessableEntity, "NO_CATEGORY",
		"This member has no borrowing category set."},
	{domain.ErrPasswordTooWeak, http.StatusUnprocessableEntity, "PASSWORD_TOO_WEAK",
		"That password does not meet the minimum requirements."},
	{domain.ErrInvalidCallNumber, http.StatusUnprocessableEntity, "INVALID_CALL_NUMBER",
		"That is not a valid Library of Congress call number."},
	{domain.ErrNotReservable, http.StatusUnprocessableEntity, "NOT_RESERVABLE",
		"That title cannot be reserved."},
	{domain.ErrInvalidTransition, http.StatusUnprocessableEntity, "INVALID_STATUS_CHANGE",
		"That copy cannot move to that status from its current one."},
	{domain.ErrCopyOnLoan, http.StatusConflict, "COPY_ON_LOAN",
		"That copy is currently on loan and cannot be changed."},
}

// FromError maps a domain error to its HTTP response.
//
// Anything unrecognised becomes a 500 with a generic message, and the real error
// is logged rather than returned: a driver error on the wire tells an attacker
// about the schema (NFR-017).
func FromError(w http.ResponseWriter, err error) {
	for _, m := range errorMap {
		if errors.Is(err, m.err) {
			Error(w, m.status, m.code, m.message, nil)
			return
		}
	}
	slog.Error("unhandled error", "error", err)
	Error(w, http.StatusInternalServerError, "INTERNAL",
		"Something went wrong on our side. Please try again.", nil)
}

// ValidationError reports malformed input (NFR-007).
func ValidationError(w http.ResponseWriter, message string, details any) {
	Error(w, http.StatusBadRequest, "VALIDATION_FAILED", message, details)
}
