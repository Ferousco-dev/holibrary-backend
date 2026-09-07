package http_test

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Ferousco-dev/holibrary-backend/internal/auth"
	transport "github.com/Ferousco-dev/holibrary-backend/internal/transport/http"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func catalogueSpec(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// Compare operations, not only paths: a documented POST must not conceal a missing GET.
func TestOpenAPIMethodsMatchRouter(t *testing.T) {
	spec := catalogueSpec(t)
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	routes := map[string]bool{}
	pattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) (/api/v1[^"]*)"`)
	paths := spec["paths"].(map[string]any)
	for _, match := range pattern.FindAllStringSubmatch(string(raw), -1) {
		path, method := strings.TrimPrefix(match[2], "/api/v1"), strings.ToLower(match[1])
		routes[method+" "+path] = true
		operations, ok := paths[path].(map[string]any)
		if !ok || operations[method] == nil {
			t.Errorf("missing OpenAPI operation %s %s", method, path)
		}
	}
	for path, value := range paths {
		if path == "/healthz" {
			continue
		}
		for method := range value.(map[string]any) {
			if strings.Contains(" get put post delete options head patch trace ", " "+method+" ") && !routes[method+" "+path] {
				t.Errorf("documented operation has no route: %s %s", method, path)
			}
		}
	}
}

func TestCatalogueOpenAPIContract(t *testing.T) {
	spec := catalogueSpec(t)
	paths := spec["paths"].(map[string]any)
	for _, path := range []string{"/books", "/books/new-arrivals", "/books/{id}/related", "/catalogue/facets"} {
		node, ok := paths[path].(map[string]any)
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		operation := node["get"].(map[string]any)
		security, ok := operation["security"].([]any)
		if !ok || len(security) != 0 {
			t.Errorf("%s must explicitly allow public access", path)
		}
	}
	parameters := paths["/books"].(map[string]any)["get"].(map[string]any)["parameters"].([]any)
	components := spec["components"].(map[string]any)
	shared := components["parameters"].(map[string]any)
	found := map[string]bool{}
	for _, value := range parameters {
		parameter := value.(map[string]any)
		if ref, ok := parameter["$ref"].(string); ok {
			parameter = shared[strings.TrimPrefix(ref, "#/components/parameters/")].(map[string]any)
		}
		found[parameter["name"].(string)] = true
	}
	for _, name := range strings.Fields("q subject author faculty department year_from year_to language wing available borrowable sort page per_page title isbn call_number class") {
		if !found[name] {
			t.Errorf("undocumented search filter %s", name)
		}
	}
	schemas := components["schemas"].(map[string]any)
	for _, name := range []string{"NewSavedSearch", "SavedSearchQuery"} {
		if schemas[name].(map[string]any)["additionalProperties"] != false {
			t.Errorf("%s must reject unknown fields", name)
		}
	}
	for _, path := range []string{"/me/saved-searches", "/me/saved-searches/{id}"} {
		for method, value := range paths[path].(map[string]any) {
			operation := value.(map[string]any)
			security, explicit := operation["security"]
			if explicit && len(security.([]any)) == 0 {
				t.Errorf("%s %s cannot be public", method, path)
			}
			for _, status := range []string{"401", "403"} {
				if operation["responses"].(map[string]any)[status] == nil {
					t.Errorf("%s %s lacks %s", method, path, status)
				}
			}
		}
	}
}

// Nil services are intentional: rejected requests must never reach business logic.
func TestSavedSearchRoutesRejectUnauthorisedCallers(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)
	router := transport.NewRouter(transport.Handlers{}, transport.Options{Issuer: issuer})
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/me/saved-searches"}, {"POST", "/api/v1/me/saved-searches"}, {"DELETE", "/api/v1/me/saved-searches/" + uuid.NewString()}} {
		for _, caller := range []struct {
			name, role string
			pending    bool
			status     int
			code       string
		}{{"anonymous", "", false, 401, "UNAUTHENTICATED"}, {"librarian", "librarian", false, 403, "FORBIDDEN"}, {"admin", "admin", false, 403, "FORBIDDEN"}, {"pending member", "member", true, 403, "MUST_CHANGE_PASSWORD"}} {
			t.Run(route.method+"/"+caller.name, func(t *testing.T) {
				req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"name":"test","query":{}}`))
				if caller.role != "" {
					token, err := issuer.IssueAccessToken(uuid.New(), caller.role, caller.pending)
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != caller.status {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				var body struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Error.Code != caller.code {
					t.Errorf("code = %q, want %q", body.Error.Code, caller.code)
				}
			})
		}
	}
}

func TestDeskLoanRouteStillRejectsMember(t *testing.T) {
	issuer := auth.NewTokenIssuer(strings.Repeat("k", 32), time.Minute, time.Hour)
	router := transport.NewRouter(transport.Handlers{}, transport.Options{Issuer: issuer})
	token, err := issuer.IssueAccessToken(uuid.New(), "member", false)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/loans", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
		t.Fatalf("desk loan route accepted a member: %d %s", w.Code, w.Body.String())
	}
}
