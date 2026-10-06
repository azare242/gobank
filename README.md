# Users API

A small Go project using Fiber v3, GORM, and SQLite. The pure-Go SQLite driver needs no C compiler. The `users` table is created automatically on startup, with an auto-incrementing `id`, a unique `username`, and a `password` column containing a bcrypt hash. Passwords are omitted from API responses.

## Run

Install Go 1.26 or later, then run from this directory:

```powershell
go mod tidy
$key = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($key)
$rng.Dispose()
$env:JWT_SECRET = [Convert]::ToBase64String($key)
$env:JWT_ISSUER = 'gobank'
$env:JWT_AUDIENCE = 'gobank-api'
go run ./cmd/server
```

The server listens at `http://127.0.0.1:3000` and stores data in `users.db`. Override these defaults with the `LISTEN_ADDR` and `DB_PATH` environment variables. The database starts empty; create users through the API.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | Check that the server is running |
| POST | `/users` | Create a user |
| POST | `/login` | Exchange username/password for a 15-minute access token |
| GET | `/me` | Get the authenticated user's profile (Bearer token required) |
| GET | `/users` | List users (Bearer token required) |
| GET | `/users/:id` | Get a user by ID (Bearer token required) |

Create and retrieve a user in PowerShell:

```powershell
$body = @{ username = 'alice'; password = 'example-password' } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/users -ContentType 'application/json' -Body $body
$login = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/login -ContentType 'application/json' -Body $body
$headers = @{ Authorization = "Bearer $($login.access_token)" }
Invoke-RestMethod -Uri http://127.0.0.1:3000/me -Headers $headers
Invoke-RestMethod -Uri http://127.0.0.1:3000/users -Headers $headers
```

User responses contain only `id` and `username`. Usernames are trimmed, case-sensitive, and must be 1–64 characters. Passwords must be 8–72 bytes. Duplicate usernames return HTTP 409; invalid input returns 400; missing users return 404.

## Authentication

Startup requires `JWT_SECRET` (standard base64 containing at least 32 cryptographically random bytes), `JWT_ISSUER`, and `JWT_AUDIENCE`. Keep the same key in a secret manager across restarts and instances; the example above generates a new development key each time, invalidating previous tokens. Never commit secrets.

Login returns `access_token`, `token_type: "Bearer"`, and `expires_in: 900`, with `Cache-Control: no-store`. Unknown usernames and wrong passwords receive the same 401 response, with a dummy bcrypt comparison for unknown users. Login is limited to 10 attempts per IP per minute. The limiter uses process-local storage; multiple instances need shared limiter storage. Configure trusted proxies explicitly before deploying behind a proxy.

Tokens contain only the numeric user ID and standard claims. Validation pins HS256 and checks the signature, issuer, audience, expiry, issued-at, and not-before claims. Credentials and tokens must travel over HTTPS outside local development. Avoid storing bearer tokens in browser local storage. Access tokens expire after 15 minutes; there is no refresh endpoint or immediate revocation. Key rotation invalidates all existing tokens. Add server-side sessions or a revocation mechanism if immediate logout is required.

Attach the middleware to future routes or groups, then read the validated identity:

```go
app.Get("/private", tokens.Middleware(), func(c fiber.Ctx) error {
    userID, _ := auth.UserID(c)
    return c.JSON(fiber.Map{"user_id": userID})
})
// Or: private := app.Group("/private", tokens.Middleware())
```

Authentication establishes identity; implement ownership or role checks for sensitive resources. Currently all authenticated users can read the user directory; `/me` also verifies the account still exists. Registration remains public.

References: [golang-jwt validation options](https://pkg.go.dev/github.com/golang-jwt/jwt/v5) and [OWASP JWT guidance](https://cheatsheetseries.owasp.org/cheatsheets/JSON_Web_Token_Cheat_Sheet.html).

## Verify

```powershell
go mod verify
go test ./...
go vet ./...
go build ./cmd/server
```

Tests use temporary SQLite databases and check user persistence, bcrypt hashing, login, credential errors, rate limiting, middleware, deleted accounts, and rejection of invalid JWT claims and signatures.
