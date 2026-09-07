package handler

import (
	"encoding/json"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
	"net/http"
)

type SavedSearchHandler struct{ searches *service.SavedSearchService }

func NewSavedSearchHandler(s *service.SavedSearchService) *SavedSearchHandler {
	return &SavedSearchHandler{searches: s}
}

type savedSearchResponse struct {
	ID        string                     `json:"id"`
	Name      string                     `json:"name"`
	Query     map[string]json.RawMessage `json:"query"`
	CreatedAt string                     `json:"created_at"`
}

func toSavedSearchResponse(s domain.SavedSearch) savedSearchResponse {
	return savedSearchResponse{ID: s.ID.String(), Name: s.Name, Query: s.Query, CreatedAt: s.CreatedAt.UTC().Format(timeFormat)}
}
func (h *SavedSearchHandler) Create(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}
	var req struct {
		Name  string                     `json:"name"`
		Query map[string]json.RawMessage `json:"query"`
	}
	if !decode(w, r, &req) {
		return
	}
	out, err := h.searches.Create(r.Context(), memberID, req.Name, req.Query)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, toSavedSearchResponse(out), nil)
}
func (h *SavedSearchHandler) List(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}
	list, err := h.searches.List(r.Context(), memberID)
	if err != nil {
		response.FromError(w, err)
		return
	}
	out := make([]savedSearchResponse, 0, len(list))
	for _, s := range list {
		out = append(out, toSavedSearchResponse(s))
	}
	response.JSON(w, http.StatusOK, out, nil)
}
func (h *SavedSearchHandler) Delete(w http.ResponseWriter, r *http.Request) {
	memberID, ok := middleware.UserID(r.Context())
	if !ok {
		response.FromError(w, domain.ErrUnauthenticated)
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.searches.Delete(r.Context(), id, memberID); err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]string{"status": "deleted"}, nil)
}
