// Package docs serves the API's own specification.
//
// The spec is embedded in the binary rather than read from disk, so the
// documentation is part of the deployed artefact and cannot drift away from it
// or go missing on a container with no source tree.
package docs

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var spec []byte

// Spec serves the OpenAPI document itself, for code generators and for the
// frontend team's tooling.
func Spec() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(spec)
	}
}

// swaggerUI is a minimal page that renders the embedded spec.
//
// Swagger UI's assets come from a CDN rather than being vendored: they are
// several megabytes, and this page is a developer convenience, not part of the
// API. If the CDN is unreachable the spec itself is still served at
// /openapi.yaml, which is what actually matters.
const swaggerUI = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>HOLibrary API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
  <style>
    body { margin: 0; background: #fafafa; }
    .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/openapi.yaml",
        dom_id: "#swagger",
        deepLinking: true,
        persistAuthorization: true,
        docExpansion: "list",
        defaultModelsExpandDepth: 1,
        tryItOutEnabled: true
      });
    };
  </script>
</body>
</html>`

// UI serves the interactive documentation page (REQ-073).
func UI() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerUI))
	}
}
