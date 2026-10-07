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
| GET | `/accounts/:id/balance` | Read your account balance (Bearer token required) |
| POST | `/accounts/:id/deposit` | Deposit integer currency units (Bearer token required) |
| POST | `/accounts/:id/withdraw` | Withdraw integer currency units (Bearer token required) |

Signup through `POST /users` creates a zero-balance account in the same transaction and returns its ID in the `account` field (for example, `A1001`). Accounts belong to the registering user; requests for another user's account return 404. Existing users from earlier databases are not automatically assigned accounts.

Deposit and withdrawal bodies use `{"amount":500}`. Amounts must be positive integers fitting in `int64`. Successful account requests return `{"status":"success","account":"A1001","balance":500}`. Updates are atomic, withdrawals cannot make balances negative, and deposits cannot overflow `int64`.

All errors use `{"status":"error","error":"insufficient_funds","message":"Account does not have enough balance"}`. Account errors map to 400 for `invalid_amount`, `missing_field`, `malformed_request`, and `unsupported_action`; 404 for `unknown_account`; 409 for `insufficient_funds`; 429 for `server_busy`; and 500 for `internal_error`. Authentication errors return 401 with `unauthorized`. All routes share a configurable concurrency limit (default 100) and return 429 when full.

Create and retrieve a user in PowerShell:

```powershell
$body = @{ username = 'alice'; password = 'example-password' } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/users -ContentType 'application/json' -Body $body
$login = Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/login -ContentType 'application/json' -Body $body
$headers = @{ Authorization = "Bearer $($login.access_token)" }
Invoke-RestMethod -Uri http://127.0.0.1:3000/me -Headers $headers
Invoke-RestMethod -Uri http://127.0.0.1:3000/users -Headers $headers
```

User responses contain `id`, `username`, and `account`. Usernames are trimmed, case-sensitive, and must be 1â€“64 characters. Passwords must be 8â€“72 bytes. Duplicate usernames return HTTP 409; invalid input returns 400; missing users return 404.

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

Tests use temporary SQLite databases and check user persistence, bcrypt hashing, login, credential errors, rate limiting, middleware, deleted accounts, invalid JWT claims and signatures, concurrent balance updates, insufficient funds, committed balance persistence after reopening, concurrency rejection and slot release, and request IDs.

## Concurrency and request logs

Set `MAX_CONCURRENT_REQUESTS` to a positive integer before starting the server (default: 100). This limit applies to all HTTP routes, including signup, login, and health. Requests beyond the limit immediately receive HTTP 429 with `server_busy`; admitted requests release their slot even when a handler fails. Invalid configuration prevents startup.

Every request uses the supplied `X-Request-ID` or a generated UUID. The ID is stored in Fiber locals as `request_id` and returned in the response header, including error responses. Each request produces a JSON log on stdout with timestamp, request ID, client IP, method, path, action, account ID, result, HTTP status, and latency in milliseconds. Successful account responses also log the committed balance. Request bodies, credentials, and bearer tokens are not logged. Logging wraps recovery and concurrency admission so rejected requests and handler errors are included.

Deposits and withdrawals run inside GORM transactions. Withdrawals use an atomic conditional SQL decrement with `balance >= amount`; deposits use an atomic increment guarded against overflow. The updated balance is read inside the transaction and HTTP success is sent only after commit. Errors roll back the transaction. SQLite uses a single connection per process to serialize this application's writes, and the database enforces `CHECK(balance >= 0)`.


