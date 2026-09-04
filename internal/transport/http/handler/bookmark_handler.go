package handler

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
)

type BookmarkHandler struct{ bookmarks *service.BookmarkService }

func NewBookmarkHandler(b *service.BookmarkService) *BookmarkHandler {
	return &BookmarkHandler{bookmarks: b}
}

type bookmarkRequest struct {
	BookID string `json:"book_id"`
}

type bookmarkResponse struct {
	Book    service.BookView `json:"book"`
	SavedAt string           `json:"saved_at"`
}

// Create saves a title to the signed-in member's list.
//
// The member comes from the token and never from the body. There is no way to
// write into somebody else's reading list, because there is no parameter that
// would let you name one.
func (h *BookmarkHandler) Create(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	var req bookmarkRequest
	if !decode(w, r, &req) {
		return
	}
	bookID, err := uuid.Parse(req.BookID)
	if err != nil {
		response.ValidationError(w, "book_id must be a valid identifier.", nil)
		return
	}

	if _, err := h.bookmarks.Save(r.Context(), memberID, bookID); err != nil {
		response.FromError(w, err)
		return
	}

	// 204 rather than 201: saving a title twice is allowed and changes
	// nothing, so a status that promises "created" would sometimes be a lie.
	w.WriteHeader(http.StatusNoContent)
}

// Delete removes a title from the member's list.
//
// Addressed by book id, not by bookmark id: a reader looking at a title knows
// its id, and should not have to fetch their whole list to find the row that
// points at it.
func (h *BookmarkHandler) Delete(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	bookID, err := uuid.Parse(r.PathValue("bookID"))
	if err != nil {
		response.ValidationError(w, "The book identifier is not valid.", nil)
		return
	}

	if err := h.bookmarks.Remove(r.Context(), memberID, bookID); err != nil {
		response.FromError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// List returns the signed-in member's saved titles, newest first.
func (h *BookmarkHandler) List(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}

	limit, offset, page := pagination(r)
	saved, total, err := h.bookmarks.List(r.Context(), memberID, limit, offset)
	if err != nil {
		response.FromError(w, err)
		return
	}

	out := make([]bookmarkResponse, 0, len(saved))
	for _, s := range saved {
		out = append(out, bookmarkResponse{
			Book: service.NewBookView(s.Book),
			// RFC 3339 in UTC, rendered in Africa/Lagos by the reader.
			SavedAt: s.SavedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	response.JSON(w, http.StatusOK, out, &response.Meta{
		Page: page, PerPage: limit, Total: total,
	})
}
