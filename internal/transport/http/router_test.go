package http_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The documentation is only useful if it describes the API that actually exists.
// A spec that has drifted is worse than none, because the frontend team builds
// against it and discovers the difference at integration time.
//
// This test reads the route table and the specification as text and checks that
// they agree. It is deliberately crude: it compares the two sources of truth
// rather than trusting either (bug class 44, Swagger/OpenAPI drift).
func TestEveryRouteIsDocumented(t *testing.T) {
	router, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("reading router: %v", err)
	}
	spec, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("reading spec: %v", err)
	}

	// Matches the pattern strings in the mux route table, e.g.
	//     mux.Handle("POST /api/v1/loans/{id}/return", ...)
	routePattern := regexp.MustCompile(`mux\.Handle(?:Func)?\("([A-Z]+) (/[^"]*)"`)

	specText := string(spec)
	documented := 0

	for _, m := range routePattern.FindAllStringSubmatch(string(router), -1) {
		method, path := m[1], m[2]

		// /healthz, /docs and /openapi.yaml sit outside the versioned API and
		// are not part of the contract the frontend builds against.
		if !strings.HasPrefix(path, "/api/v1") {
			continue
		}
		specPath := strings.TrimPrefix(path, "/api/v1")

		// The spec's servers carry the /api/v1 prefix, so paths appear without
		// it, as a YAML key at two spaces of indentation.
		if !strings.Contains(specText, "\n  "+specPath+":") {
			t.Errorf("route %s %s is not documented in docs/openapi.yaml", method, path)
			continue
		}
		documented++
	}

	if documented == 0 {
		t.Fatal("no routes were checked; the route pattern probably stopped matching")
	}
	t.Logf("%d documented routes verified against the specification", documented)
}

// Every documented path must exist in the router, or the frontend will build
// against an endpoint that returns 404.
func TestSpecDocumentsNoPhantomRoutes(t *testing.T) {
	router, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatalf("reading router: %v", err)
	}
	spec, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("reading spec: %v", err)
	}

	// Path keys in the spec: two spaces, a leading slash, ending in a colon.
	pathKey := regexp.MustCompile(`(?m)^  (/[a-z0-9{}/_-]*):$`)
	routerText := string(router)

	for _, m := range pathKey.FindAllStringSubmatch(string(spec), -1) {
		// Route patterns carry a method prefix, e.g. "POST /api/v1/loans".
		if !strings.Contains(routerText, " /api/v1"+m[1]+`"`) {
			t.Errorf("the specification documents %s, but no route serves it", m[1])
		}
	}
}

// The specification must be valid YAML.
//
// The two tests above compare the spec and the router as text, which is what
// makes them cheap. It is also what let a malformed spec pass: a tidy-up script
// once mangled the inline flow mappings and both tests stayed green, because
// the strings they look for were still present in a file no parser would
// accept. Text matching cannot tell a document from its wreckage.
func TestSpecIsValidYAML(t *testing.T) {
	raw, err := os.ReadFile("docs/openapi.yaml")
	if err != nil {
		t.Fatalf("reading spec: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the OpenAPI specification is not valid YAML: %v", err)
	}

	paths, ok := doc["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatal("the specification declares no paths")
	}

	// A structural check as well as a syntactic one: every path must carry at
	// least one operation, or the document parses while describing nothing.
	for name, node := range paths {
		ops, ok := node.(map[string]any)
		if !ok || len(ops) == 0 {
			t.Errorf("path %s declares no operations", name)
		}
	}
	t.Logf("%d paths parsed", len(paths))
}
