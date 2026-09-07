package handler

import (
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/response"
	"net/http"
	"net/url"
)

func bookViews(books []domain.Book) []service.BookView {
	out := make([]service.BookView, 0, len(books))
	for _, b := range books {
		out = append(out, service.NewBookView(b))
	}
	return out
}

func (h *CatalogueHandler) NewArrivals(w http.ResponseWriter, r *http.Request) {
	q, err := catalogueQueryValues(r)
	if err != nil {
		response.FromError(w, err)
		return
	}
	for key := range q {
		if key != "page" && key != "per_page" {
			response.FromError(w, domain.ErrInvalidSearch)
			return
		}
	}
	p, page, err := service.ParseCatalogueQuery(q)
	if err != nil {
		response.FromError(w, err)
		return
	}
	books, total, err := h.catalogue.NewArrivals(r.Context(), p.Limit, p.Offset)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, bookViews(books), &response.Meta{Page: page, PerPage: p.Limit, Total: total})
}
func (h *CatalogueHandler) Related(w http.ResponseWriter, r *http.Request) {
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	q, err := catalogueQueryValues(r)
	if err != nil {
		response.FromError(w, err)
		return
	}
	for key := range q {
		if key != "per_page" {
			response.FromError(w, domain.ErrInvalidSearch)
			return
		}
	}
	if _, ok := q["per_page"]; !ok {
		q.Set("per_page", "6")
	}
	p, _, err := service.ParseCatalogueQuery(q)
	if err != nil {
		response.FromError(w, err)
		return
	}
	books, err := h.catalogue.Related(r.Context(), id, p.Limit)
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, bookViews(books), nil)
}
func (h *CatalogueHandler) Facets(w http.ResponseWriter, r *http.Request) {
	q, err := catalogueQueryValues(r)
	if err != nil {
		response.FromError(w, err)
		return
	}
	if len(q) > 0 {
		response.FromError(w, domain.ErrInvalidSearch)
		return
	}
	facets, err := h.catalogue.Facets(r.Context())
	if err != nil {
		response.FromError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, facets, nil)
}

// URL.Query silently drops malformed pairs. Discovery must reject them rather
// than executing a broader query than the reader supplied.
func catalogueQueryValues(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, domain.ErrInvalidSearch
	}
	return q, nil
}
