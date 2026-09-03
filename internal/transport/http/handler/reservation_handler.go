package handler

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type ReservationHandler struct{ reservations *service.ReservationService }

func NewReservationHandler(r *service.ReservationService) *ReservationHandler {
	return &ReservationHandler{reservations: r}
}

type reserveRequest struct {
	BookID string `json:"book_id"`
}

// Create places the signed-in member in the queue for a title (REQ-055).
//
// The member comes from the token, never from the body: nobody can queue
// somebody else, and nobody can queue on behalf of a member they are not.
func (h *ReservationHandler) Create(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req reserveRequest
	if !decode(w, r, &req) {
		return
	}
	bookID, err := uuid.Parse(req.BookID)
	if err != nil {
		response.ValidationError(w, "book_id must be a valid identifier.", nil)
		return
	}

	res, err := h.reservations.Reserve(r.Context(), bookID, memberID)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toReservationResponse(res), nil)
}

// List returns the signed-in member's own reservations (REQ-057).
func (h *ReservationHandler) List(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	list, err := h.reservations.MyReservations(r.Context(), memberID)
	if err != nil {
		response.FromError(w, err)
		return
	}

	out := make([]reservationResponse, 0, len(list))
	for _, res := range list {
		out = append(out, toReservationResponse(res))
	}
	response.JSON(w, http.StatusOK, out, nil)
}

// Cancel withdraws the member's own reservation (REQ-057).
//
// Ownership is part of the query, not a check afterwards, so guessing another
// member's reservation id achieves nothing.
func (h *ReservationHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := h.reservations.Cancel(r.Context(), id, memberID); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "cancelled"}, nil)
}

type reservationResponse struct {
	ID            string `json:"id"`
	BookID        string `json:"book_id"`
	BookTitle     string `json:"book_title,omitempty"`
	Status        string `json:"status"`
	QueuePosition int    `json:"queue_position"`
	CreatedAt     string `json:"created_at"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

func toReservationResponse(r domain.Reservation) reservationResponse {
	out := reservationResponse{
		ID: r.ID.String(), BookID: r.BookID.String(), BookTitle: r.BookTitle,
		Status: r.Status, QueuePosition: r.QueuePos,
		CreatedAt: r.CreatedAt.UTC().Format(timeFormat),
	}
	if r.ExpiresAt != nil {
		out.ExpiresAt = r.ExpiresAt.UTC().Format(timeFormat)
	}
	return out
}
