package app

import (
	_ "embed"
	"net/http"
)

// Documentación de la API, igual que en sphere-go. Solo se publica con SWAGGER_ENABLED=True.

//go:embed docs/openapi.yaml
var openAPISpec []byte

const swaggerUIVersion = "5.17.14"
const redocVersion = "2.1.5"

const swaggerHTML = `<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API Inscripciones UPAC - Swagger</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: "../swagger.yaml",
      dom_id: "#swagger-ui",
      deepLinking: true,
      persistAuthorization: true,
      displayRequestDuration: true,
      tryItOutEnabled: true
    });
  </script>
</body>
</html>
`

const redocHTML = `<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>API Inscripciones UPAC - ReDoc</title>
  <style>body { margin: 0; }</style>
</head>
<body>
  <redoc spec-url="../swagger.yaml"></redoc>
  <script src="https://cdn.jsdelivr.net/npm/redoc@` + redocVersion + `/bundles/redoc.standalone.js"></script>
</body>
</html>
`

// registerDocs publica /swagger/, /redoc/ y /swagger.yaml.
func (a *App) registerDocs() {
	static := func(body []byte, contentType string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body)
		}
	}
	a.mux.Handle("GET /swagger", http.RedirectHandler("/swagger/", http.StatusMovedPermanently))
	a.mux.Handle("GET /redoc", http.RedirectHandler("/redoc/", http.StatusMovedPermanently))
	a.mux.HandleFunc("GET /swagger/{$}", static([]byte(swaggerHTML), "text/html; charset=utf-8"))
	a.mux.HandleFunc("GET /redoc/{$}", static([]byte(redocHTML), "text/html; charset=utf-8"))
	a.mux.HandleFunc("GET /swagger.yaml", static(openAPISpec, "application/yaml; charset=utf-8"))
}
