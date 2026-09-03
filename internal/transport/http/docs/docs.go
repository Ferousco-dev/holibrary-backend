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
    :root {
      --hol-green: #14532d;
      --hol-green-dark: #0b3a1e;
      --hol-green-light: #1b6b3a;
    }

    /* The page sits on green; the specification itself stays on a light
       surface so the JSON, the schemas and the code samples keep the
       contrast they need to be readable. */
    body {
      margin: 0;
      background: var(--hol-green);
      color: #f4faf6;
    }

    .topbar { display: none; }

    .hol-header {
      padding: 26px 24px 20px;
      background: var(--hol-green-dark);
      border-bottom: 3px solid var(--hol-green-light);
    }
    .hol-header h1 {
      margin: 0;
      font: 600 22px/1.25 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      color: #ffffff;
      letter-spacing: -0.01em;
    }
    .hol-header p {
      margin: 6px 0 0;
      font: 400 14px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      color: #b9dcc6;
    }

    /* Swagger UI renders on white cards by default. Keep them, but float them
       on the green rather than letting a white sheet fill the viewport. */
    #swagger { max-width: 1180px; margin: 0 auto; padding: 20px 16px 56px; }
    #swagger .swagger-ui .wrapper { padding: 0; }
    /* The overview sits on green with light text. Its content is prose, so it
       reads fine reversed out; the operation blocks and schema viewers below
       keep light surfaces because they carry JSON and code samples. */
    #swagger .swagger-ui .info { margin: 20px 0; padding: 22px 26px; background: var(--hol-green-dark); border: 1px solid var(--hol-green-light); border-radius: 8px; }
    #swagger .swagger-ui .info .title,
    #swagger .swagger-ui .info h1, #swagger .swagger-ui .info h2,
    #swagger .swagger-ui .info h3, #swagger .swagger-ui .info h4,
    #swagger .swagger-ui .info p, #swagger .swagger-ui .info li,
    #swagger .swagger-ui .info table, #swagger .swagger-ui .info td,
    #swagger .swagger-ui .info th, #swagger .swagger-ui .info .markdown p { color: #eaf5ed; }
    #swagger .swagger-ui .info a { color: #8fd4a8; }
    #swagger .swagger-ui .info code,
    #swagger .swagger-ui .info .markdown code { background: rgba(255,255,255,0.12); color: #d7f0e0; }
    #swagger .swagger-ui .info pre { background: rgba(0,0,0,0.30); color: #d7f0e0; }
    #swagger .swagger-ui .info table th { border-bottom-color: var(--hol-green-light); }
    #swagger .swagger-ui .info .base-url { color: #b9dcc6; }

    #swagger .swagger-ui .scheme-container { background: var(--hol-green-dark); border: 1px solid var(--hol-green-light); border-radius: 8px; box-shadow: none; margin: 0 0 20px; padding: 16px 24px; }
    #swagger .swagger-ui .scheme-container .schemes-title,
    #swagger .swagger-ui .scheme-container label { color: #eaf5ed; }
    #swagger .swagger-ui .opblock-tag { color: #eaf5ed; border-bottom-color: var(--hol-green-light); }
    #swagger .swagger-ui .opblock-tag small { color: #b9dcc6; }
    #swagger .swagger-ui .opblock-tag:hover { background: rgba(255,255,255,0.06); border-radius: 6px; }
    /* Swagger tints each operation block with a 10%-alpha wash, which assumes a
       white page underneath. Over green the dark default text lost its
       contrast, so each block gets a solid light surface; the coloured border
       and method badge still distinguish GET from POST. */
    #swagger .swagger-ui .opblock { background: #ffffff; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.28); }
    #swagger .swagger-ui .opblock .opblock-summary { background: #ffffff; border-radius: 8px 8px 0 0; }
    #swagger .swagger-ui .opblock.is-open .opblock-summary { border-radius: 8px 8px 0 0; }
    #swagger .swagger-ui .opblock .opblock-section-header { background: #f2f5f3; }
    #swagger .swagger-ui section.models { background: #ffffff; border-radius: 8px; border: none; }
    #swagger .swagger-ui section.models h4 { color: #1a1a1a; }
    #swagger .swagger-ui .model-box { background: #f6f8f7; }
  </style>
</head>
<body>
  <header class="hol-header">
    <h1>HOLibrary API</h1>
    <p>Hezekiah Oluwasanmi Library, Obafemi Awolowo University &middot; Group 4</p>
  </header>
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
