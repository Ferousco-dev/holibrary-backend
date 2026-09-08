package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/Ferousco-dev/holibrary-backend/internal/domain"
	"github.com/Ferousco-dev/holibrary-backend/internal/service"
	"github.com/Ferousco-dev/holibrary-backend/internal/transport/http/middleware"
	"github.com/google/uuid"
)

type roleStore struct {
	service.MemberStore
	err      error
	category *domain.MemberCategory
	calls    int
}

func (s *roleStore) UpdateRole(_ context.Context, _ uuid.UUID, _ domain.Role, category *domain.MemberCategory, _ uuid.UUID) error {
	s.calls++
	s.category = category
	return s.err
}

func TestMemberRoleHTTPCategoryAndAuthorisation(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("r", 32), time.Hour, time.Hour)
	for _, test := range []struct {
		name, body, actor string
		storeErr          error
		status            int
		code              string
		calls             int
	}{
		{"missing category", `{"role":"member"}`, "admin", domain.ErrNoCategory, 422, "NO_CATEGORY", 1},
		{"explicit category", `{"role":"member","category":"undergraduate"}`, "admin", nil, 200, "", 1},
		{"existing category", `{"role":"member"}`, "admin", nil, 200, "", 1},
		{"invalid category", `{"role":"member","category":"student"}`, "admin", nil, 400, "INVALID_MEMBER_CATEGORY", 0},
		{"empty category", `{"role":"member","category":""}`, "admin", nil, 400, "INVALID_MEMBER_CATEGORY", 0},
		{"wrong category type", `{"role":"member","category":12}`, "admin", nil, 400, "VALIDATION_FAILED", 0},
		{"category on librarian", `{"role":"librarian","category":"staff"}`, "admin", nil, 400, "INVALID_MEMBER_CATEGORY", 0},
		{"invalid role", `{"role":"user"}`, "admin", nil, 400, "VALIDATION_FAILED", 0},
		{"last administrator", `{"role":"member","category":"staff"}`, "admin", domain.ErrConflict, 409, "CONFLICT", 1},
		{"unknown account", `{"role":"member","category":"staff"}`, "admin", domain.ErrNotFound, 404, "NOT_FOUND", 1},
		{"anonymous", `{"role":"member","category":"staff"}`, "", nil, 401, "UNAUTHENTICATED", 0},
		{"member", `{"role":"member","category":"staff"}`, "member", nil, 403, "FORBIDDEN", 0},
		{"librarian", `{"role":"member","category":"staff"}`, "librarian", nil, 403, "FORBIDDEN", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &roleStore{err: test.storeErr}
			h := NewMemberHandler(service.NewMemberService(store, nil), nil)
			protected := middleware.Authenticate(issuer, nil)(middleware.RequireAdmin(http.HandlerFunc(h.SetRole)))
			r := httptest.NewRequest("PATCH", "/members/"+uuid.NewString()+"/role", strings.NewReader(test.body))
			r.SetPathValue("id", uuid.NewString())
			if test.actor != "" {
				token, err := issuer.IssueAccessToken(uuid.New(), test.actor, false)
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			protected.ServeHTTP(w, r)
			if w.Code != test.status || test.code != "" && !strings.Contains(w.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("response %d %s", w.Code, w.Body.String())
			}
			if store.calls != test.calls {
				t.Fatalf("repository calls %d expected %d", store.calls, test.calls)
			}
			if test.name == "explicit category" && (store.category == nil || *store.category != domain.CategoryUndergraduate) {
				t.Fatal("request category lost")
			}
			if errors.Is(test.storeErr, domain.ErrNoCategory) && !strings.Contains(w.Body.String(), "undergraduate") {
				t.Fatal("missing-category error should explain allowed choices")
			}
		})
	}
}
