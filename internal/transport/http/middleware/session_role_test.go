package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	"github.com/google/uuid"
)

func TestAuthenticateRejectsOutdatedRole(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("v", 32), time.Hour, time.Hour)
	id := uuid.New()
	token, err := issuer.IssueAccessToken(id, "librarian", false)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	valid := func(_ context.Context, userID uuid.UUID, _ time.Time, role string) (bool, error) {
		checked = true
		if userID != id || role != "librarian" {
			t.Fatalf("incorrect session identity: %s %s", userID, role)
		}
		return role == "member", nil
	}
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("outdated role reached handler") })
	req := httptest.NewRequest("GET", "/api/v1/members", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	Authenticate(issuer, valid)(next).ServeHTTP(w, req)
	if !checked || w.Code != 401 || !strings.Contains(w.Body.String(), "TOKEN_INVALID") {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
}
