package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/google/uuid"
)

type discoveryStore struct {
	service.CatalogueStore
	query postgres.SearchParams
	calls int
}

func (s *discoveryStore) Search(_ context.Context, p postgres.SearchParams) ([]domain.Book, int, error) {
	s.query = p
	s.calls++
	return []domain.Book{}, 42, nil
}
func (s *discoveryStore) NewArrivals(_ context.Context, limit, offset int) ([]domain.Book, int, error) {
	s.query.Limit = limit
	s.query.Offset = offset
	s.calls++
	return []domain.Book{}, 42, nil
}
func (s *discoveryStore) Related(_ context.Context, _ uuid.UUID, limit int) ([]domain.Book, error) {
	s.query.Limit = limit
	s.calls++
	return []domain.Book{}, nil
}
func (s *discoveryStore) Facets(context.Context) (map[string][]domain.FacetValue, error) {
	s.calls++
	return map[string][]domain.FacetValue{"subjects": {}}, nil
}

func TestCatalogueHTTPPaginationAndValidation(t *testing.T) {
	store := &discoveryStore{}
	h := NewCatalogueHandler(service.NewCatalogueService(store))
	for _, test := range []struct {
		name    string
		handler http.HandlerFunc
		url     string
		status  int
	}{
		{"search page", h.Search, "/books?page=5&per_page=10", 200},
		{"arrivals page", h.NewArrivals, "/books/new-arrivals?page=5&per_page=10", 200},
		{"bad encoding", h.Search, "/books?q=%ZZ", 400},
		{"bad arrival encoding", h.NewArrivals, "/books/new-arrivals?page=%ZZ", 400},
		{"bad facets encoding", h.Facets, "/catalogue/facets?x=%ZZ", 400},
		{"bad page", h.Search, "/books?page=banana", 400},
		{"repeat", h.Search, "/books?sort=title&sort=newest", 400},
		{"arrival extra", h.NewArrivals, "/books/new-arrivals?q=x", 400},
		{"invalid bool", h.Search, "/books?available=maybe", 400},
		{"invalid sort", h.Search, "/books?sort=desc", 400},
		{"facets extra", h.Facets, "/catalogue/facets?user_id=x", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := store.calls
			w := httptest.NewRecorder()
			test.handler(w, httptest.NewRequest("GET", test.url, nil))
			if w.Code != test.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if test.status == 400 {
				if store.calls != before {
					t.Fatal("invalid request reached repository")
				}
				return
			}
			var body struct {
				Data []any                              `json:"data"`
				Meta struct{ Page, PerPage, Total int } `json:"meta"`
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(raw["data"], &body.Data)
			var meta map[string]int
			_ = json.Unmarshal(raw["meta"], &meta)
			if body.Data == nil || meta["page"] != 5 || meta["per_page"] != 10 || meta["total"] != 42 || store.query.Offset != 40 {
				t.Fatalf("pagination %s params %+v", w.Body.String(), store.query)
			}
		})
	}
}
func TestRelatedHTTPBoundsAndDefault(t *testing.T) {
	store := &discoveryStore{}
	h := NewCatalogueHandler(service.NewCatalogueService(store))
	for _, test := range []struct {
		query         string
		status, limit int
	}{{"", 200, 6}, {"?per_page=8", 200, 8}, {"?per_page=%ZZ", 400, 0}, {"?per_page=101", 400, 0}, {"?page=2", 400, 0}} {
		r := httptest.NewRequest("GET", "/books/id/related"+test.query, nil)
		r.SetPathValue("id", uuid.NewString())
		w := httptest.NewRecorder()
		h.Related(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: %d %s", test.query, w.Code, w.Body.String())
		}
		if test.status == 200 && store.query.Limit != test.limit {
			t.Fatalf("limit %d", store.query.Limit)
		}
	}
}
