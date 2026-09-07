package handler

import (
	"context"
	"encoding/json"
	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type ownedSearchStore struct{ rows []domain.SavedSearch }

func (s *ownedSearchStore) Create(_ context.Context, u uuid.UUID, n string, q map[string]json.RawMessage, _ int) (domain.SavedSearch, error) {
	v := domain.SavedSearch{ID: uuid.New(), UserID: u, Name: n, Query: q, CreatedAt: time.Now()}
	s.rows = append(s.rows, v)
	return v, nil
}
func (s *ownedSearchStore) ListForUser(_ context.Context, u uuid.UUID) ([]domain.SavedSearch, error) {
	out := []domain.SavedSearch{}
	for _, v := range s.rows {
		if v.UserID == u {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *ownedSearchStore) Delete(_ context.Context, id, u uuid.UUID) error {
	for i, v := range s.rows {
		if v.ID == id && v.UserID == u {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			return nil
		}
	}
	return domain.ErrNotFound
}
func TestSavedSearchHTTPIdentityAndOwnership(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	store := &ownedSearchStore{}
	h := NewSavedSearchHandler(service.NewSavedSearchService(store))
	issuer := auth.NewTokenIssuer(strings.Repeat("x", 32), time.Hour, time.Hour)
	mux := http.NewServeMux()
	protect := func(f http.HandlerFunc) http.Handler {
		return middleware.Authenticate(issuer, nil)(middleware.RequireMember(f))
	}
	mux.Handle("POST /me/saved-searches", protect(h.Create))
	mux.Handle("GET /me/saved-searches", protect(h.List))
	mux.Handle("DELETE /me/saved-searches/{id}", protect(h.Delete))
	request := func(method, path, body string, id uuid.UUID, role string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if id != uuid.Nil {
			token, err := issuer.IssueAccessToken(id, role, false)
			if err != nil {
				t.Fatal(err)
			}
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/me/saved-searches"
		if method == "DELETE" {
			path += "/" + uuid.NewString()
		}
		if w := request(method, path, `{}`, uuid.Nil, ""); w.Code != 401 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := request("GET", "/me/saved-searches", "", owner, "librarian"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, field := range []string{"member_id", "user_id"} {
		if w := request("POST", "/me/saved-searches", `{"name":"Books","query":{},"`+field+`":"`+other.String()+`"}`, owner, "member"); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request("POST", "/me/saved-searches", `{"name":"Books","query":{"available":true}}`, owner, "member")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "user_id") || strings.Contains(w.Body.String(), owner.String()) {
		t.Fatal("owner leaked", w.Body.String())
	}
	id := store.rows[0].ID.String()
	w = request("GET", "/me/saved-searches", "", other, "member")
	if w.Code != 200 || strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("DELETE", "/me/saved-searches/"+id, "", other, "member")
	if w.Code != 404 || len(store.rows) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/me/saved-searches", "", owner, "member")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("DELETE", "/me/saved-searches/"+id, "", owner, "member")
	if w.Code != 200 || len(store.rows) != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
