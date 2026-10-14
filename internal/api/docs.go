package api

import (
	_ "embed"

	"github.com/gofiber/fiber/v3"
)

//go:embed openapi.json
var openAPISpec []byte

func registerDocs(app *fiber.App) {
	app.Get("/openapi.json", func(c fiber.Ctx) error {
		c.Set("Content-Type", "application/json")
		return c.Send(openAPISpec)
	})
	app.Get("/docs", func(c fiber.Ctx) error {
		c.Set("Content-Type", "text/html; charset=utf-8")
		return c.SendString(swaggerUI)
	})
}

const swaggerUI = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Mini Banking API — Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({url: '/openapi.json', dom_id: '#swagger-ui', deepLinking: true, validatorUrl: null});
  </script>
</body>
</html>`
