package handler

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type CirculationHandler struct{ circulation *service.CirculationService }

func NewCirculationHandler(c *service.CirculationService) *CirculationHandler {
	return &CirculationHandler{circulation: c}
}

// loanResponse presents a loan with its overdue state computed at render time.
//
// There is no is_overdue column behind this. The value is derived from the
// clock on every read, so it cannot be stale (REQ-053).
//
// Every timestamp is emitted as RFC 3339 in UTC. Whether a reader opens this
// from Ile-Ife, London or Toronto, the server's answer to "is this overdue" is
// the same; converting to Africa/Lagos is the frontend's job and affects
// nothing but presentation.
type loanResponse struct {
	ID              string     `json:"id"`
	CopyID          string     `json:"copy_id"`
	UserID          string     `json:"user_id"`
	MemberName      string     `json:"member_name,omitempty"`
	BookTitle       string     `json:"book_title,omitempty"`
	AccessionNumber string     `json:"accession_number,omitempty"`
	BorrowedAt      time.Time  `json:"borrowed_at"`
	DueAt           time.Time  `json:"due_at"`
	ReturnedAt      *time.Time `json:"returned_at"`
	Status          string     `json:"status"`
	IsOverdue       bool       `json:"is_overdue"`
	DaysOverdue     int        `json:"days_overdue"`
}

func toLoanResponse(l domain.Loan, now time.Time) loanResponse {
	status := "borrowed"
	switch {
	case l.IsReturned():
		status = "returned"
	case l.IsOverdueAt(now):
		status = "overdue"
	}

	return loanResponse{
		ID: l.ID.String(), CopyID: l.CopyID.String(), UserID: l.UserID.String(),
		MemberName: l.MemberName, BookTitle: l.BookTitle,
		AccessionNumber: l.AccessionNumber,
		BorrowedAt:      l.BorrowedAt, DueAt: l.DueAt, ReturnedAt: l.ReturnedAt,
		Status: status, IsOverdue: l.IsOverdueAt(now), DaysOverdue: l.DaysOverdueAt(now),
	}
}

func toLoanResponses(loans []domain.Loan) []loanResponse {
	// One instant for the whole page, in UTC, so two rows in the same response
	// can never disagree about whether "now" is past a due date.
	now := time.Now().UTC()
	out := make([]loanResponse, 0, len(loans))
	for _, l := range loans {
		out = append(out, toLoanResponse(l, now))
	}
	return out
}

type borrowRequest struct {
	CopyID   string `json:"copy_id"`
	MemberID string `json:"member_id"`
}

// Borrow records a physical copy leaving the building (REQ-041).
//
// This route is librarian-only. The member ID comes from the request body and
// the librarian ID from the token, which is the shape of the real transaction:
// a member cannot issue a book to themselves any more than they could at the
// Loans desk.
func (h *CirculationHandler) Borrow(w http.ResponseWriter, r *http.Request) {
	librarianID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req borrowRequest
	if !decode(w, r, &req) {
		return
	}

	copyID, err := uuid.Parse(req.CopyID)
	if err != nil {
		response.ValidationError(w, "copy_id must be a valid identifier.", nil)
		return
	}
	memberID, err := uuid.Parse(req.MemberID)
	if err != nil {
		response.ValidationError(w, "member_id must be a valid identifier.", nil)
		return
	}

	loan, err := h.circulation.Borrow(r.Context(), copyID, memberID, librarianID)
	if err != nil {
		// A lost race returns 409 COPY_NOT_AVAILABLE, so the desk is told the
		// copy has just gone rather than being shown a generic failure.
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toLoanResponse(loan, time.Now().UTC()), nil)
}

// Return records a copy coming back to the shelf (REQ-048..051).
func (h *CirculationHandler) Return(w http.ResponseWriter, r *http.Request) {
	librarianID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}
	loanID, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	loan, err := h.circulation.Return(r.Context(), loanID, librarianID)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toLoanResponse(loan, time.Now().UTC()), nil)
}

// List serves the circulation desk, optionally narrowed to overdue items
// (REQ-052, REQ-054).
func (h *CirculationHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, offset, page := pagination(r)
	overdueOnly := boolParam(r, "overdue")

	// Asking for overdue items implies open loans; a returned book is never
	// overdue however late it came back.
	openOnly := overdueOnly || boolParam(r, "open")

	// synthetic=true restricts the result to simulated borrowers, so the
	// activity simulator can find the loans it created without touching a real
	// member's record.
	loans, total, err := h.circulation.ListLoans(r.Context(), overdueOnly, openOnly,
		limit, offset, boolParam(r, "synthetic"))
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toLoanResponses(loans), &response.Meta{
		Page: page, PerPage: limit, Total: total,
	})
}

// MyLoans lists the books the signed-in member currently holds (REQ-060).
func (h *CirculationHandler) MyLoans(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	loans, err := h.circulation.MyLoans(r.Context(), memberID)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toLoanResponses(loans), nil)
}

// MyHistory lists everything the signed-in member has ever borrowed (REQ-061).
//
// The member ID is taken from the token and never from the URL. That is what
// makes REQ-062 hold: there is no parameter to tamper with, so one member
// cannot read another's reading history (DOM-009).
func (h *CirculationHandler) MyHistory(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	loans, err := h.circulation.MyHistory(r.Context(), memberID)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, toLoanResponses(loans), nil)
}
