# Mini Banking API

A banking API built with Go, Fiber v3, and GORM, using SQLite or PostgreSQL. It supports registration, JWT authentication, account balances, deposits, and withdrawals, with ownership checks and audit logs for authentication attempts and money movements. Balance changes are atomic and prevent overdrafts and integer overflow.

The pure-Go SQLite driver needs no C compiler. The `users`, `accounts`, and `audit_logs` tables are created automatically on startup. Usernames are unique, and passwords are stored as bcrypt hashes and omitted from API responses.

## Run

On Linux, install Go 1.26 or later, OpenSSL, curl, and jq. Run these commands
from the project directory to create your local configuration:

```bash
go mod download
cp .env.example .env
chmod 600 .env
sed -i "s|^JWT_SECRET=.*|JWT_SECRET=$(openssl rand -base64 32)|" .env
```

Create `.env` once and keep it for subsequent runs. Edit it to change the listener,
database, or concurrency limit. It is ignored by Git; keep secrets out of commits.

Load the file and start the server in Bash:

```bash
set -a
source ./.env
set +a
go run ./cmd/server
```

The server reads environment variables; Bash exports the values from `.env` with
`set -a`. Repeat the loading commands after editing `.env` or opening a new shell.
Use only a trusted `.env` file because `source` executes shell syntax.

The default listener is `http://127.0.0.1:3000` and the SQLite database is
`users.db`. The database starts empty; create users through the API.

### Database selection and repositories

The HTTP server uses `DB_DRIVER`, which defaults to `sqlite`; `postgres` and `postgresql` select PostgreSQL.
`DB_DSN` is a SQLite filename/URI or a PostgreSQL connection string. For SQLite,
it takes precedence over `DB_PATH`. For PostgreSQL it is required; create the
database before starting the server:

Edit these entries in `.env` to use PostgreSQL:

```dotenv
DB_DRIVER=postgres
DB_DSN='host=localhost port=5432 user=gobank password=development-password dbname=gobank sslmode=disable'
```

Reload `.env` with `set -a; source ./.env; set +a`, then run
`go run ./cmd/server`.

Use your deployment's TLS settings in the PostgreSQL DSN. Switching drivers
connects to a separate database; it does not copy existing data. The same models
and repositories work with either dialect. SQLite retains its single connection;
PostgreSQL uses a pool of up to 20 connections.

`internal/repository` exposes `Users`, `Accounts`, `AuditLogs`, and `Store`
interfaces. `NewGORM` implements them, translating missing and duplicate records
into repository errors. HTTP handlers receive a `Store` through
`api.NewWithRepository`; `api.New(db, tokens)` remains a compatibility wrapper.
The banking service uses these repositories for HTTP operations.
Profile queries omit password hashes; login queries include them.

```go
db, err := database.OpenConfig(database.Config{Driver: "postgres", DSN: dsn})
if err != nil { return err }
store := repository.NewGORM(db).WithContext(ctx)
err = store.Transaction(func(tx repository.Store) error {
    if err := tx.Accounts().ChangeBalance(accountID, userID, 500, false); err != nil {
        return err
    }
    return tx.AuditLogs().Create(&entry)
})
```

Use the callback's `tx` repositories for all operations in a transaction. Returning
an error rolls them all back. Balance changes use atomic conditional updates to
enforce ownership, prevent overdrafts, and prevent integer overflow.

## Endpoints

### Swagger documentation

Open `http://127.0.0.1:3000/docs` for interactive Swagger UI. The OpenAPI 3.0
specification is served at `/openapi.json` and maintained in
`internal/api/openapi.json`; it is embedded in the server binary.

Create a user with `POST /users`, sign in with `POST /login`, then click
**Authorize** and enter the returned access token to try protected endpoints.
Swagger UI loads its assets from a CDN, so viewing it requires internet access.
The specification itself is available without internet access.

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

Create and retrieve a user from a second Linux terminal:

```bash
body='{"username":"alice","password":"example-password"}'
curl --fail-with-body -sS http://127.0.0.1:3000/users \
  -H 'Content-Type: application/json' -d "$body"
token=$(curl --fail-with-body -sS http://127.0.0.1:3000/login \
  -H 'Content-Type: application/json' -d "$body" | jq -er '.access_token')
curl --fail-with-body -sS http://127.0.0.1:3000/me \
  -H "Authorization: Bearer $token"
curl --fail-with-body -sS http://127.0.0.1:3000/users \
  -H "Authorization: Bearer $token"
```

User responses contain `id`, `username`, and `account`. Usernames are trimmed, case-sensitive, and must be 1â€“64 characters. Passwords must be 8â€“72 bytes. Duplicate usernames return HTTP 409; invalid input returns 400; missing users return 404.

## Authentication

Startup requires `JWT_SECRET` (standard base64 containing at least 32 cryptographically random bytes), `JWT_ISSUER`, and `JWT_AUDIENCE`. Keep the same key in a secret manager across restarts and instances; the setup above generates a development key when you create `.env`. Replacing that key invalidates previous tokens. Never commit secrets.

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

```bash
go mod verify
go test ./...
go vet ./...
go build -o /tmp/gobank-server ./cmd/server
```

Repository contract tests always run on SQLite. To run the same tests against a
dedicated PostgreSQL test database (tables are migrated; test rows are rolled back):

Add the test database connection to `.env`:

```dotenv
TEST_POSTGRES_DSN='host=localhost port=5432 user=gobank password=development-password dbname=gobank_test sslmode=disable'
```

```bash
set -a
source ./.env
set +a
go test ./internal/repository -v
```

Tests use temporary SQLite databases and check user persistence, bcrypt hashing, login, credential errors, rate limiting, middleware, deleted accounts, invalid JWT claims and signatures, concurrent balance updates, insufficient funds, committed balance persistence after reopening, concurrency rejection and slot release, and request IDs.

## Concurrency and request logs

### Database audit log

Startup automatically creates the `audit_logs` table. Every login attempt and deposit/withdrawal request records a UTC timestamp, request ID, client IP, action, HTTP method/path, result, status code, and error code when applicable. Records include the authenticated user ID, submitted login username, account ID, validated amount, and committed balance where available. Passwords, password hashes, request bodies, and access tokens are excluded. Login throttling, authentication failures, invalid requests, insufficient funds, and concurrency rejections are also audited.

Successful deposits and withdrawals insert their audit record in the same database transaction as the balance update. If the audit insert fails, the balance change rolls back and the API returns an error. Failed requests are recorded separately after rollback; login success is returned only after its audit insert succeeds. If the database cannot store an audit record, the request returns an internal error. Audit records persist across restarts and are not exposed through a public API. Existing request logs continue to be written to stdout.

Set `MAX_CONCURRENT_REQUESTS` in `.env` to a positive integer before starting the server (default: 100). This limit applies to all HTTP routes, including signup, login, and health. Requests beyond the limit immediately receive HTTP 429 with `server_busy`; admitted requests release their slot even when a handler fails. Invalid configuration prevents startup.

Every request uses the supplied `X-Request-ID` or a generated UUID. The ID is stored in Fiber locals as `request_id` and returned in the response header, including error responses. Each request produces a readable single-line log on stdout with aligned UTC timestamp, severity (INFO for success, WARN for 4xx, ERROR for 5xx), HTTP status, method/path, and latency in milliseconds, followed by labeled result, request ID, client IP, action, and account ID when applicable. String values are quoted and escaped to keep each request on one line. Successful account responses also log the committed balance. Request bodies, credentials, and bearer tokens are not logged. Logging wraps recovery and concurrency admission so rejected requests and handler errors are included.

Deposits and withdrawals run inside GORM transactions. Withdrawals use an atomic conditional SQL decrement with `balance >= amount`; deposits use an atomic increment guarded against overflow. The updated balance is read inside the transaction and HTTP success is sent only after commit. Errors roll back the transaction. SQLite uses a single connection per process to serialize this application's writes, and the database enforces `CHECK(balance >= 0)`.
