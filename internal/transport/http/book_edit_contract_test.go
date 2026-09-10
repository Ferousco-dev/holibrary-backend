package http_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/repository/postgres"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	transport "github.com/Ferousco-dev/holibrary-backend/internal/transport/http"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/handler"
	"github.com/google/uuid"
)

type bookEditStore struct {
	service.CatalogueStore
	called bool
	actor  uuid.UUID
	err    error
}

func (s *bookEditStore) UpdateBook(_ context.Context, id uuid.UUID, p postgres.UpdateBookParams) (domain.Book, error) {
	s.called = true
	s.actor = p.StaffID
	b := domain.Book{ID: id, Title: "Existing", CallNumber: "QA76", LCCClass: "Q"}
	if p.Title != nil {
		b.Title = *p.Title
	}
	return b, s.err
}

func TestBookMetadataRoute(t *testing.T) {
	for _, tc := range []struct {
		name, role, body string
		status           int
		code             string
		err              error
	}{
		{name: "librarian", role: "librarian", body: `{"title":"Updated title"}`, status: 200},
		{name: "admin", role: "admin", body: `{"title":"Updated title"}`, status: 200},
		{name: "member", role: "member", body: `{"title":"Updated title"}`, status: 403, code: "FORBIDDEN"},
		{name: "anonymous", body: `{"title":"Updated title"}`, status: 401, code: "UNAUTHENTICATED"},
		{name: "unknown", role: "librarian", body: `{"titel":"Updated title"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "case typo", role: "librarian", body: `{"Title":"Updated title"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "copy mutation", role: "librarian", body: `{"status":"lost"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "actor injection", role: "librarian", body: `{"staff_id":"other"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "oversized body", role: "librarian", body: `{"description":"` + strings.Repeat("x", 1<<20) + `"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "invalid UTF-8", role: "librarian", body: "{\"title\":\"" + string([]byte{0xff}) + "\"}", status: 400, code: "VALIDATION_FAILED"},
		{name: "empty", role: "librarian", body: `{}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "no body", role: "librarian", body: ``, status: 400, code: "VALIDATION_FAILED"},
		{name: "null", role: "librarian", body: `null`, status: 400, code: "VALIDATION_FAILED"},
		{name: "null field", role: "librarian", body: `{"title":null}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "null list item", role: "librarian", body: `{"authors":[null]}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "trailing body", role: "librarian", body: `{"title":"Valid"} {}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "duplicate", role: "librarian", body: `{"title":"Valid","title":"Other"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "wrong type", role: "librarian", body: `{"published_year":"2009"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "empty title", role: "librarian", body: `{"title":" "}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "Dewey", role: "librarian", body: `{"call_number":"005.1"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "invalid ISBN13", role: "librarian", body: `{"isbn13":"9780262033849"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "invalid ISBN10", role: "librarian", body: `{"isbn10":"0262033845"}`, status: 400, code: "VALIDATION_FAILED"},
		{name: "missing title", role: "librarian", body: `{"title":"Updated title"}`, status: 404, code: "NOT_FOUND", err: domain.ErrNotFound},
		{name: "duplicate ISBN", role: "librarian", body: `{"isbn13":"9780262033848"}`, status: 400, code: "VALIDATION_FAILED", err: domain.ErrDuplicateISBN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)
			store := &bookEditStore{err: tc.err}
			router := transport.NewRouter(transport.Handlers{Catalogue: handler.NewCatalogueHandler(service.NewCatalogueService(store))}, transport.Options{Issuer: issuer})
			id, actor := uuid.New(), uuid.New()
			req := httptest.NewRequest("PATCH", "/api/v1/books/"+id.String(), strings.NewReader(tc.body))
			if tc.role != "" {
				token, err := issuer.IssueAccessToken(actor, tc.role, false)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) {
				t.Fatalf("wrong envelope: %s", w.Body.String())
			}
			if tc.status == 200 {
				var result struct {
					Data service.BookView `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Data.ID != id || result.Data.Title != "Updated title" || store.actor != actor {
					t.Fatalf("incorrect result or actor: %+v actor=%s", result, store.actor)
				}
			} else if tc.err == nil && store.called {
				t.Fatal("invalid or unauthorised request reached repository")
			}
		})
	}
}

func TestBookEditOpenAPIContract(t *testing.T) {
	spec := catalogueSpec(t)
	op := spec["paths"].(map[string]any)["/books/{id}"].(map[string]any)["patch"].(map[string]any)
	for _, status := range []string{"200", "400", "401", "403", "404"} {
		if op["responses"].(map[string]any)[status] == nil {
			t.Errorf("missing response %s", status)
		}
	}
	if security, ok := op["security"].([]any); ok && len(security) == 0 {
		t.Error("metadata edit must require bearer authentication")
	}
	schema := spec["components"].(map[string]any)["schemas"].(map[string]any)["BookMetadataPatch"].(map[string]any)
	if schema["additionalProperties"] != false || schema["minProperties"] != 1 {
		t.Error("patch schema must reject unknown fields and empty objects")
	}
	props := schema["properties"].(map[string]any)
	for _, field := range strings.Fields("id copy_ids accession_number status loan_policy loans reservations staff_id actor_id") {
		if props[field] != nil {
			t.Errorf("forbidden patch field %s", field)
		}
	}
}

func TestCirculationMutationRoutesRemainStaffOnly(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)
	router := transport.NewRouter(transport.Handlers{}, transport.Options{Issuer: issuer})
	token, err := issuer.IssueAccessToken(uuid.New(), "member", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/loans"}, {"PATCH", "/api/v1/copies/" + uuid.NewString()}} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
			t.Fatalf("%s %s: %d %s", route.method, route.path, w.Code, w.Body.String())
		}
	}
}
