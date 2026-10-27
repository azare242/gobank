# Mini Banking API

## Sharded deployment

This guide runs the Gateway/Coordinator, four application replicas, and three PostgreSQL databases on one Docker host. All commands below run in **Bash from the repository root**. Standalone and Raft instructions follow later in this README.

### 1. Prerequisites and service layout

Install Docker Engine with Compose v2, OpenSSL, curl, jq, and Python 3. Install Go 1.26 or later if you want the native CLI or Go tests. Docker builds do not require Go on the host.

```bash
docker info >/dev/null
docker compose version
```

Host port `127.0.0.1:3000` must be free. Stop an existing standalone/Raft server using that port before starting this stack.

| Service | Purpose | Persistent storage |
| --- | --- | --- |
| `gateway` | Public HTTP API, routing, Coordinator, and recovery worker | Uses `db-coordinator` |
| `shard-a1`, `shard-a2` | Replicas for `A0001`–`A1000` | Share `db-a` |
| `shard-b1`, `shard-b2` | Replicas for `A1001`–`A2000` | Share `db-b` |
| `db-a`, `db-b` | Authoritative shard databases | `a-data`, `b-data` volumes |
| `db-coordinator` | Request registry, decisions, acknowledgments | `coordinator-data` volume |

Only the Gateway publishes a host port. Shards and databases use the internal `bank` network; the Gateway also joins `edge`. All four replicas use the same `gobank-shard` image. The shard map is mounted read-only from [shards.json](deploy/sharded/shards.json).

### 2. Create and retain configuration

Create a dedicated environment file once. The following command refuses to overwrite an existing file:

```bash
if [ -e deploy/sharded/.env ]; then
  echo 'Existing deploy/sharded/.env retained.'
else
  (
    umask 077
    {
      printf 'COMPOSE_PROJECT_NAME=gobank-sharded\n'
      printf 'VERSION=sharded\n'
      printf 'POSTGRES_PASSWORD=%s\n' "$(openssl rand -hex 24)"
      printf 'INTERNAL_TOKEN=%s\n' "$(openssl rand -hex 32)"
      printf 'JWT_SECRET=%s\n' "$(openssl rand -base64 32)"
      printf 'FIXTURE_PASSWORD=%s\n' "$(openssl rand -hex 16)"
      printf 'CRASH_AT=\n'
    } > deploy/sharded/.env
  )
fi
```

The `.env` filename is ignored by Git. Keep this file protected and retain it with your deployment. Do not regenerate the database password when reusing existing volumes: PostgreSQL initialization variables do not change an existing database role's password.

Load the file and define a Compose shortcut in **each new shell**:

```bash
set -a
source deploy/sharded/.env
set +a
compose() {
  docker compose --env-file deploy/sharded/.env \
    -f deploy/sharded/compose.yml "$@"
}
export GATEWAY_URL=http://127.0.0.1:3000
```

Source only your own trusted file. Exporting these values also supplies the experiment script, which launches Compose subprocesses. Keep `COMPOSE_PROJECT_NAME` unchanged: it determines the names of your volumes and containers. If upgrading an existing deployment created under another project name, use that original name to retain its data.

| Setting | Meaning |
| --- | --- |
| `POSTGRES_PASSWORD` | Shared demo database-role password; use URL-safe characters because DSNs contain it |
| `INTERNAL_TOKEN` | At least 32 bytes; authenticates Gateway-to-shard commands |
| `JWT_SECRET` | Standard base64 encoding of at least 32 random bytes |
| `FIXTURE_PASSWORD` | 8–72 bytes; login password for all seeded demo identities |
| `VERSION` | Tag shared by the two application images; `sharded` in this guide |
| `COMPOSE_PROJECT_NAME` | Stable namespace for this deployment |
| `CRASH_AT` | Empty normally; opt-in Coordinator crash injection |

Compose supplies JWT issuer/audience, replica IDs, DSNs, and the mounted shard-map path. PostgreSQL starts with `max_prepared_transactions=100` and `max_connections=150`. Schema initialization and fixture insertion happen in the shard processes; no separate SQL import is needed.

### 3. Build and start

```bash
compose config --quiet
compose build gateway shard-a1
compose up -d --no-build
compose ps
```

Building `shard-a1` creates the shared shard image used by all four replicas. The default Dockerfile target is the legacy server image, so use the service builds above or the explicit `gateway`/`shard` targets.

Wait for dependency readiness; container startup alone is not an application-readiness check:

```bash
ready=0
for attempt in $(seq 1 60); do
  if curl --fail --silent "$GATEWAY_URL/ready" >/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo 'Gateway not ready; inspect compose logs.' >&2
else
  curl --fail-with-body -sS "$GATEWAY_URL/ready" | jq
fi
```

`/health` checks the process. `/ready` also requires the metadata database and at least one healthy replica in every shard. The API specification is at `$GATEWAY_URL/openapi.json`; this Gateway does not provide the legacy `/docs` Swagger UI.

#### Build through v2rayN on port 10808

Image pulls use the **Docker daemon's** HTTP/HTTPS proxy configuration. Set both daemon proxies to `http://127.0.0.1:10808` on a Linux host running v2rayN locally, and confirm the effective values with:

```bash
docker info --format '{{.HTTPProxy}} {{.HTTPSProxy}}'
```

Downloads inside Dockerfile `RUN` steps need separate build proxy arguments. On Linux, host networking lets those build steps reach the loopback proxy:

```bash
for role in gateway shard; do
  docker build --network host \
    --build-arg HTTP_PROXY=http://127.0.0.1:10808 \
    --build-arg HTTPS_PROXY=http://127.0.0.1:10808 \
    --build-arg http_proxy=http://127.0.0.1:10808 \
    --build-arg https_proxy=http://127.0.0.1:10808 \
    --target "$role" -t "gobank-$role:$VERSION" .
done
compose up -d --no-build
```

These proxy settings are for downloads, not runtime banking traffic. With Docker Desktop or a remote Docker daemon, use a proxy address reachable from that daemon/build environment rather than assuming its loopback is your host.

### 4. Login and perform transfers

Fixtures use the account ID as username and its numeric part as the global user ID. All accounts initially have 1000 units except `A1500`, which has 500. Reusing volumes retains subsequent balances. Public registration is unavailable in this mode.

Log in as the source owner; acquire a destination-owner token to inspect both balances:

```bash
login() {
  jq -nc --arg username "$1" --arg password "$FIXTURE_PASSWORD" \
    '{username:$username,password:$password}' |
    curl --fail-with-body -sS "$GATEWAY_URL/login" \
      -H 'Content-Type: application/json' --data-binary @- |
    jq -er '.access_token'
}
source_token=$(login A0500)
destination_token=$(login A1500)

curl --fail-with-body -sS "$GATEWAY_URL/accounts/A0500/balance" \
  -H "Authorization: Bearer $source_token" | jq
```

Every mutation needs an `Idempotency-Key`. Generate it once and retain it for retries:

```bash
transfer_key=$(openssl rand -hex 16)
transfer_reply=$(curl --fail-with-body -sS "$GATEWAY_URL/transfers" \
  -H "Authorization: Bearer $source_token" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $transfer_key" \
  -d '{"source":"A0500","destination":"A1500","amount":300}')
printf '%s\n' "$transfer_reply" | jq
transaction_id=$(printf '%s\n' "$transfer_reply" | jq -er '.transaction_id')
```

On a fresh database, a committed result leaves balances 700 and 800. Repeating the exact request/key has one financial effect; changing its payload with the same key conflicts. For a same-shard transfer, use `A0501` as destination with a **new** logical key. Deposits and withdrawals use `/accounts/A0500/deposit` or `/withdraw` with `{"amount":10}` and their own keys.

Inspect the durable status and destination balance:

```bash
curl --fail-with-body -sS "$GATEWAY_URL/transactions/$transaction_id" \
  -H "Authorization: Bearer $source_token" | jq
curl --fail-with-body -sS "$GATEWAY_URL/accounts/A1500/balance" \
  -H "Authorization: Bearer $destination_token" | jq
```

HTTP 202 means pending, not failed. Poll the transaction ID until `pending` is false and inspect `decision` and `outcome`. `COMPLETED` can mean either COMMIT or ABORT. If a response was lost before receiving an ID, retry the original payload/key. Never create a new key to resolve an uncertain transfer. Tokens expire after 15 minutes; log in again and keep the original operation identity.

#### Native CLI and journal reconciliation

```bash
mkdir -p bin data
go build -o bin/bank-client ./cmd/client
export BANK_PASSWORD="$FIXTURE_PASSWORD"
export BANK_TOKEN="$source_token"

bin/bank-client -gateway "$GATEWAY_URL" -action balance
bin/bank-client -gateway "$GATEWAY_URL" -action transfer \
  -destination A1500 -amount 1 -journal data/source-journal.jsonl
```

Create the local journal directory first (`mkdir -p data`); the CLI does not create parent directories. Use a different journal for each source owner. The previous transfer of 300 and this CLI transfer of 1 are separate operations.

```bash
bin/bank-client -gateway "$GATEWAY_URL" -action status \
  -transaction "$transaction_id"
bin/bank-client -gateway "$GATEWAY_URL" -action reconcile \
  -journal data/source-journal.jsonl
```

The CLI waits for terminal outcomes where possible; a deadline retains an uncertain operation in the journal. Reconcile that file after recovery. Journals with an incomplete final line are rejected; back up the file and remove only the incomplete line before replaying complete intents.

For concurrent transfer traffic, use a fresh journal:

```bash
mkdir -p data
workload_journal="data/transfer-$(date +%s)-journal.jsonl"
bin/bank-client -gateway "$GATEWAY_URL" -action workload \
  -destination A1500 -amount 1 -count 40 -workers 4 \
  -interval 100ms -journal "$workload_journal"
```

That workload verifies the source balance against its own successful operations. Use the experiment suite or multi-account verification for conservation across both accounts. Do not run unrelated traffic on the same source while relying on the CLI's exact expected-balance check.

For `-action verify`, provide a JSON file with `expected` (account-to-balance) and `tokens` (account-to-owner-JWT) maps. Store it under ignored `data/`, create it with permissions 0600, and supply expected values calculated from your reconciled workload rather than copying current balances. Then run:

For a fresh dataset with only the 300-unit HTTP transfer, the 1-unit CLI transfer,
and the 40 successful 1-unit workload transfers shown above, the expected pair
is 659 and 841. If you ran the optional same-shard transfer or other traffic,
calculate the corresponding expectations instead.

```bash
(
  umask 077
  jq -n --arg source "$source_token" --arg destination "$destination_token" \
    '{expected:{A0500:659,A1500:841},tokens:{A0500:$source,A1500:$destination}}' \
    > data/verification.json
)
bin/bank-client -gateway "$GATEWAY_URL" -action verify \
  -verify-file data/verification.json
```

### 5. Experiments and failure recovery

Use a dedicated demo stack: experiments change fixture balances and stop/recreate application containers. The exported environment, project name, image version, and `GATEWAY_URL` must refer to the same stack.

```bash
python3 scripts/sharded/experiments.py all
# Or run one scenario:
python3 scripts/sharded/experiments.py 6
```

| Scenario | Check |
| --- | --- |
| 1 | Account distribution, range boundaries, invalid IDs |
| 2 | Requests reaching both replicas of one shard |
| 3 | Concurrent local operations and duplicate keys |
| 4 | Replica stop, fallback, and readmission |
| 5 | Same-shard and cross-shard transfers, rollback |
| 6 | Coordinator exits at four durable boundaries and recovery |
| 7 | Opposing transfers, duplicate deliveries, replica interruption, conservation |

The scripts do not delete database volumes. Scenario 6 clears crash injection when recreating the Gateway for recovery. Allow the script to finish; if interrupted, inspect `compose ps -a`, clear `CRASH_AT`, and restore stopped services with `compose up -d --no-build`.

For manual replica interruption:

```bash
compose stop shard-a1
curl --fail-with-body -sS "$GATEWAY_URL/accounts/A0500/balance" \
  -H "Authorization: Bearer $source_token" -D -
compose start shard-a1
```

Readiness probing readmits the restored replica. `compose start` starts existing containers; after `compose down`, use `compose up` instead.

For a manual Coordinator crash after its durable decision:

```bash
export CRASH_AT=after_decision
compose up -d --no-deps --force-recreate gateway
# Submit a NEW cross-shard transfer with a retained operation key.
# The Gateway exits with code 86 when the configured boundary is reached.
compose ps -a gateway
export CRASH_AT=
compose up -d --no-deps --force-recreate gateway
# Retry the original request/key or query its recorded transaction ID.
```

Other Coordinator values are `after_prepare`, `all_prepared`, and `after_ack`. The Compose variable applies to the Gateway; shard processes also support `after_prepare` and `after_finalize` when explicitly configured in a Compose override. Clear injection before restarting recovery. For a deliberate crash, `curl` may report an empty reply even though durable work exists.

Recovery fences live processing with the same database session lock. Undecided distributed operations are durably aborted; recorded decisions are retried until both participants acknowledge. A recorded COMMIT never becomes ABORT because a shard is temporarily down. Prepared locks survive replica failure. Balance reads may see old committed values while prepares exist; reconcile pending work before checking totals. Do not manually finalize prepared transactions or delete metadata as a recovery shortcut.

### 6. Inspect state and logs

```bash
compose logs --tail 100 gateway shard-a1 shard-a2
compose logs -f gateway
compose exec -T db-a psql -U bank -d bank -c \
  "SELECT id,user_id,balance FROM bank_accounts WHERE id='A0500';"
compose exec -T db-b psql -U bank -d bank -c \
  "SELECT id,user_id,balance FROM bank_accounts WHERE id='A1500';"
compose exec -T db-coordinator psql -U bank -d bank -c \
  "SELECT id,status,decision,acks FROM bank_transactions ORDER BY updated_at DESC LIMIT 10;"
compose exec -T db-a psql -U bank -d bank -c \
  "SELECT gid,prepared FROM pg_prepared_xacts WHERE database=current_database();"
```

Repeat the prepared-inventory query on `db-b`. An unresolved prepared entry during an outage can be expected; after successful recovery there should be no unexpected orphan. Application logs include request, operation, transaction, shard, and replica identifiers. `compose config` without `--quiet` renders credentials, so avoid copying that output into reports or public logs.

### 7. Restart, update, and reset

```bash
# Stop without removing containers; restart with the same credentials and data.
compose stop
compose start

# Remove containers/networks, retaining all named database volumes.
compose down
compose up -d --no-build

# Rebuild application images after source changes and replace their containers.
compose build gateway shard-a1
compose up -d --no-build
```

Keep the project name, all three volumes, shard map, and credentials together. Application redundancy does not provide database failover or host-failure protection. The static map is not a live migration mechanism.

Only for an intentional clean demo reset, after stopping traffic and reconciling pending operations:

```bash
# DESTRUCTIVE: deletes BOTH shard databases AND Coordinator history.
compose down -v
compose up -d --no-build
```

Reset all three stores together. Reusing an old journal against reset databases does not recover its lost deduplication history; use fresh journals and operation keys for the new demo dataset.

### 8. PostgreSQL integration tests

These tests require a dedicated PostgreSQL URL, prepared transactions enabled, and a role allowed to create isolated test databases. Banking databases are not published to the host, so use a separate test container:

```bash
test_password=$(openssl rand -hex 24)
docker run -d --name gobank-sharded-tests \
  -e POSTGRES_PASSWORD="$test_password" \
  -p 127.0.0.1:55432:5432 \
  postgres:17-bookworm@sha256:858bb5e98eed02eb8cafbcdc007f9a65d1b613972028944a8ec2133ccaf80d27 \
  -c max_prepared_transactions=100
until docker exec gobank-sharded-tests pg_isready -U postgres >/dev/null 2>&1; do
  sleep 1
done
export TEST_POSTGRES_DSN="postgres://postgres:$test_password@127.0.0.1:55432/postgres?sslmode=disable"
go test ./...
go test -race ./internal/coordinator ./internal/gateway ./internal/bankclient
go vet ./...
go mod verify
```

Tests create and remove isolated databases. Without `TEST_POSTGRES_DSN`, the PostgreSQL-specific tests skip; SQLite/Raft tests do not establish 2PC correctness. After testing, `docker stop gobank-sharded-tests` retains the test container, or `docker rm -f -v gobank-sharded-tests` intentionally removes it and its anonymous data volume.

### Troubleshooting

| Symptom | Action |
| --- | --- |
| Compose reports a missing variable | Reload `deploy/sharded/.env` and use the `compose` helper |
| Database password rejected after restart | Restore the password matching the existing volumes; changing an init variable does not rotate stored credentials |
| Gateway port is occupied | Stop the other local banking mode or change the Gateway's published host port and `GATEWAY_URL` |
| `/health` works but `/ready` returns 503 | Check both shard groups, Coordinator connectivity, and `compose logs` |
| Replica exits at startup | Check its DSN, shard/replica IDs, readable map mount, and prepared-transaction setting |
| Pulls work but `go mod download` fails during image build | Apply build proxy arguments as well as the daemon proxy |
| HTTP 401 during journal reconciliation | Log in again, update `BANK_TOKEN`, and reuse the journal/key |
| Transfer remains pending | Restore the original participant databases and Gateway; inspect decision/acks and retry status |
| Unexpected repeated Gateway exit 86 | Clear the exported `CRASH_AT`, then recreate the Gateway |
| Requests behave differently from the root standalone examples | Use this Gateway contract: fixtures replace registration, mutations require keys, and `/docs` is absent |

## Standalone and Raft modes

The standalone API is documented below. The [bonus implementation and demo guide](#bonus-implementation-and-demo-guide) covers the Assignment 1 replication mode. The [sharded deployment guide](#sharded-deployment) above covers the Docker Compose deployment.

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

## Bonus implementation and demo guide

This implements the advanced bonus (automatic primary failure recovery) and extreme bonus (network partition and split-brain prevention) from pages 4–5 of `Assignment 1 Mini Banking Transaction Server.pdf`. Use the separate `cmd/cluster-server` binary for these features. `cmd/server` remains the original single-server API and does not participate in replication.

### What was added

| Location | Purpose |
| --- | --- |
| `internal/cluster/node.go` | Three-voter Raft cluster, persistent log/stable storage, leader discovery, safe bootstrap |
| `internal/cluster/fsm.go` | Ordered banking commands, local SQLite durability, operation deduplication, audit history, snapshot/restore |
| `internal/cluster/http.go` | Authenticated cluster banking API, committed reads, diagnostics and opt-in demo faults |
| `cmd/cluster-server` | Cluster server with JSON node configuration and graceful shutdown |
| `internal/bankclient`, `cmd/client` | Retrying client, concurrent workload, durable intent/outcome journal, reconciliation and balance verification |
| `deploy/cluster/{a,b,c}.json` | Node listener, peer, and storage configurations |
| `scripts/cluster-status.sh` | Display roles and state digests across all servers |
| `scripts/partition.sh` | Create/restore a real server-to-server TCP partition on Linux |
| `scripts/core-demo.sh` | Normal operations, required errors, and actual SQL rollback demonstration |

Raft dependencies are pinned in `go.mod`/`go.sum`: `github.com/hashicorp/raft v1.7.3` and `github.com/hashicorp/raft-boltdb/v2 v2.3.1`. Go 1.26 or later is required by this repository.

### How correctness works

The client sends requests to the current leader. Followers return HTTP 503 with a leader hint; the client tries the configured servers automatically. A role label or heartbeat timeout alone never authorizes a balance change: the proposed command must commit through Raft with **two of three voters**. A minority cannot confirm writes or serve authoritative balance reads. Balance and operation-status inquiries themselves go through the ordered log; login/profile reads first commit a read fence.

Each node maintains its own `data/<node>/raft.db`, snapshots, and `bank.db`. The SQLite application state is intentionally one serialized JSON row containing users, accounts, deduplication outcomes, business audits, and the last applied command index. Each update uses a SQLite transaction with WAL and `synchronous=FULL`. Rewriting this row is simple and suitable for the small assignment; it is not a scalable database design.

An operation ID is scoped to its authenticated user. Within the same transaction as its balance change, the state machine stores the operation's payload fingerprint and exact terminal outcome. Retrying the same ID/payload returns that outcome; changing its payload returns `idempotency_conflict`. Even insufficient-funds rejections are remembered. A successful retry returns the balance **at the original operation**, which may differ from today's balance. Query balance separately for current state.

Registration is also replicated and deduplicated, in a separate unauthenticated ID namespace. Bcrypt hashes are generated before proposal; applying commands never generates different random hashes on different replicas. Registration fingerprints use an HMAC of the normalized credentials, keyed with the shared JWT secret. Keep that key unchanged across the experiment and restarts; rotating it also changes registration retry fingerprints.

Committed deposits, withdrawals, registration outcomes, and completed credential-check results have replicated audit records. Every HTTP attempt, including malformed requests and rejected requests, is logged to stdout with timestamp, request ID, operation ID, client IP, action/account, status, latency, and a safe response. The start script persists stdout/stderr. Passwords and bearer tokens are excluded from logs; login response bodies are omitted. Validation/rate-limit failures occurring before consensus appear in the receiving node's request log, rather than the replicated business history.

SQLite stores the last applied index so replay does not apply a previously persisted command twice. Snapshots include all application state and deduplication outcomes. Restore replaces that state atomically and later entries replay in order. A committed-entry persistence error fail-stops the process: it does not quietly continue as a divergent replica. Back up or restore a node's **entire data directory** with its process stopped; never copy only its application database into another node.

The guarantees assume nonmalicious nodes, intact durable disks, one common cluster configuration, and eventual recovery of a majority. When no majority can communicate, service deliberately becomes unavailable. A single physical host remains a shared failure domain.

### 1. Build the servers and client

Run from the repository root with Go 1.26 or later:

```bash
go mod download
mkdir -p bin logs
go build -o bin/cluster-server ./cmd/cluster-server
go build -o bin/bank-client ./cmd/client
```

### 2. Configure and start servers

All three nodes must use the same `JWT_SECRET`, `JWT_ISSUER`, and `JWT_AUDIENCE`. Generate the key once using the standalone setup instructions and export the values before starting each node. Review `deploy/cluster/{a,b,c}.json`: bind addresses and peer addresses must be reachable in your deployment, and each node needs its own persistent data directory. If running multiple processes on one host, assign distinct HTTP and Raft ports and update every peer entry accordingly.

`bootstrap` is true only on A and initializes membership only when no Raft state exists. Preserve each node's data directory across restarts. Start each node with its corresponding configuration:

```bash
# Run each command in its own terminal with the shared JWT settings exported.
bin/cluster-server -config deploy/cluster/a.json > logs/a.log 2>&1 &
echo $! > logs/a.pid

bin/cluster-server -config deploy/cluster/b.json > logs/b.log 2>&1 &
echo $! > logs/b.pid

bin/cluster-server -config deploy/cluster/c.json > logs/c.log 2>&1 &
echo $! > logs/c.pid
```

The client requires the three configured HTTP URLs in `BANK_SERVERS`, separated by commas. `scripts/cluster-status.sh` uses that variable to show roles. Expect one leader and two followers; a single voter cannot commit. `/cluster/status` is diagnostic and can be stale, while `/ready` requires a committed fence.

### 3. Start and use the client

The client is a command-line program, not a background service. Use a terminal with `BANK_SERVERS` set to the configured node URLs. The client needs its user's credentials, not the servers' JWT signing secret.

#### Register, log in, and send requests

Run the following in the same client terminal. Register once; for an existing user, start with login instead.

```bash
export BANK_PASSWORD='demo-password-123'
./bin/bank-client -servers "$BANK_SERVERS" -action register -username demo -id signup-demo-1
login=$(./bin/bank-client -servers "$BANK_SERVERS" -action login -username demo)
export BANK_TOKEN=$(jq -er '.access_token' <<< "$login")
export BANK_ACCOUNT=$(jq -er '.account' <<< "$login")
./bin/bank-client -servers "$BANK_SERVERS" -action balance
./bin/bank-client -servers "$BANK_SERVERS" -action deposit -amount 1000 -journal setup-journal.jsonl
./bin/bank-client -servers "$BANK_SERVERS" -action withdraw -amount 100 -journal setup-journal.jsonl
```

For continuous activity during the failover demonstration, run:

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action workload \
  -count 400 -workers 4 -interval 200ms -amount 1 -journal client-demo-journal.jsonl
```

Use a fresh journal filename for each new workload. After opening a new terminal or restarting the client, export `BANK_SERVERS` and `BANK_PASSWORD` again, then repeat login and the token/account exports above. Keep existing journals for reconciliation; do not register the same username again just to restart the client.

The client discovers your account through `/me` unless `-account` is given. Tokens expire after 15 minutes: log in again and export the fresh token before another demonstration, then reconcile pending requests using the same user. The client prints JSON; the login result intentionally contains the token so it can be captured into an environment variable. Do not include that value in screenshots or a public report.

Registration prints its operation ID to stderr before submission. If interrupted, reuse `-id signup-demo-1` with the same credentials. Each normal balance-changing CLI call generates one UUID, journals the intent and fsyncs it before network transmission, retries with that same ID, then fsyncs the terminal outcome. `-id` can also specify a banking operation ID for a controlled retry.

The default retry timeout is 45 seconds per logical operation. A timeout is **pending/unknown**, not a confirmed rejection. HTTP 503, connection failures, 429, and generic server errors are retried with backoff; terminal business errors are returned. The deliberate `injected_failure` response is terminal too. A leader election can cause a short pause and failed HTTP attempts before service resumes automatically.

### 4. Prepare A as initial leader

Raft elections do not promise a particular winner. If A is already leader, continue. Otherwise inspect the status output and submit this to the current leader, replacing the URL:

```bash
curl -sS -X POST http://CURRENT-LEADER-IP:3000/cluster/transfer/a \
  -H "Authorization: Bearer $BANK_TOKEN" | jq .
bash scripts/cluster-status.sh
```

This is preparation before inducing failure, not manual failover. After A crashes, either B or C may be elected as the new primary; both are full backups. The diagnostic node IDs make the actual successor visible. The assignment describes the successor as Server B; this three-server extension has two eligible backups. Explain the third voter's role in the report rather than claiming that Raft always elects the node literally named `b`.

### 5. Core/error and atomicity demonstrations

With A the leader and no concurrent workload on this account:

```bash
bash scripts/core-demo.sh http://192.168.56.11:3000
```

This shows balance/deposit/withdraw, all required error categories (including zero, negative, and nonnumeric amounts), and a failure after a tentative SQLite write. The state machine uses an actual SQL savepoint and rolls back that tentative balance before recording the failed outcome. The two surrounding balance reads must match.

For a separate repeatable concurrency test:

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action workload \
  -count 100 -workers 10 -interval 0s -amount 1 -journal concurrency-journal.jsonl
```

The workload alternates logical deposits and withdrawals across workers. It prints each operation's actual serving node and a final `verified:true` result. Insufficient-funds outcomes are counted as terminal rejections, not successful withdrawals. Use a new journal filename for each new workload. Keep the workload account exclusive to that run so final accounting is meaningful.

For the assignment's many-withdrawals example, prepare a separate account with balance exactly 1000, then run:

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action workload \
  -pattern withdraw -count 20 -workers 20 -interval 0s -amount 100 -journal withdrawals-journal.jsonl
```

Expect 10 successes, 10 `insufficient_funds` outcomes, final balance 0, and `verified:true`. `-pattern deposit` is also supported; `mixed` is the default.

### 6. Advanced bonus: crash during active requests

Open terminals for the client, A, B, and C. Show node addresses, roles, and server logs (`tail -f logs/a.log`, etc.). Keep auto-restart services disabled for this experiment.

1. On the client, start a workload long enough to make failure visible:

   ```bash
   ./bin/bank-client -servers "$BANK_SERVERS" -action workload \
     -count 400 -workers 4 -interval 200ms -amount 1 -journal crash-journal.jsonl
   ```

2. While it is still printing operations, on A kill the actual server PID saved during startup:

   ```bash
   ps -p "$(cat logs/a.pid)" -o pid,args
   kill -9 "$(cat logs/a.pid)"
   ```

3. The client must resume through B or C automatically, without editing its server list or manually promoting a node. Display `bash scripts/cluster-status.sh` from another client terminal. The workload's final JSON must show `verified:true`.

4. If it ends with pending operations, resolve them after the majority is healthy:

   ```bash
   ./bin/bank-client -servers "$BANK_SERVERS" -action reconcile -journal crash-journal.jsonl
   ```

5. Restart node A:

   ```bash
   bin/cluster-server -config deploy/cluster/a.json >> logs/a.log 2>&1 &
   echo $! > logs/a.pid
   ```

   Wait for catch-up. All three diagnostic `state_digest` values should match after traffic stops; applied indexes may briefly differ because reads also append commands. A returns as a follower, rather than overwriting the current leader's balances.

The workload records its initial balance and checks:

```text
expected = initial + sum(unique successful deposits) - sum(unique successful withdrawals)
```

The verifier counts logical operation IDs, not HTTP responses. Do not run unrelated deposits/withdrawals on this account between baseline capture and final verification.

#### Confirmed data survives restart

Record the balance after a successful CLI deposit, kill the current leader, let a backup serve a balance inquiry, restart the killed node, and query again. The cluster tests additionally cover stopping and reopening every node with its original data directory.

#### Ambiguous response: commit, crash, retry once

Do this separately after the workload finishes, and prepare A as leader again. Save the baseline on the client:

```bash
before=$(./bin/bank-client -servers "$BANK_SERVERS" -action balance | jq -er '.balance')
op="ambiguous-$(cat /proc/sys/kernel/random/uuid)"
curl --max-time 8 -sS -X POST "http://192.168.56.11:3000/accounts/$BANK_ACCOUNT/deposit" \
  -H "Authorization: Bearer $BANK_TOKEN" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $op" -H 'X-Demo-Crash-After-Commit: true' \
  --data '{"amount":25}'
```

An empty reply/connection failure is expected: A commits and durably applies the deposit, then exits with code 86 before sending success. Retry via the CLI **without the crash header**, preserving ID and amount:

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action deposit -amount 25 -id "$op" -journal ambiguous-journal.jsonl
./bin/bank-client -servers "$BANK_SERVERS" -action deposit -amount 25 -id "$op" -journal ambiguous-journal.jsonl
after=$(./bin/bank-client -servers "$BANK_SERVERS" -action balance | jq -er '.balance')
test "$after" -eq "$((before + 25))" && echo 'PASS: one deposit despite ambiguous response and repeated retry'
bash scripts/cluster-status.sh
```

Restart A before the partition demo. Reusing this operation ID with a different amount must return HTTP 409 `idempotency_conflict`. Keep that deliberate conflicting request out of the journal used for invariant verification.

### 7. Extreme bonus: a real partition while both sides run

Make sure all three servers are running/caught up and A is current leader. Start a new client workload:

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action workload \
  -count 400 -workers 4 -interval 200ms -amount 1 -journal partition-journal.jsonl
```

While it is active, on **node A's host**, isolate A from both other voters:

```bash
sudo bash scripts/partition.sh on 192.168.56.12 192.168.56.13
ps -p "$(cat logs/a.pid)" -o pid,args
sudo iptables -L BANK_LAB_PARTITION -n -v
```

The script drops TCP 7000 to/from those peers, including packets in existing connections. It leaves HTTP/SSH and other firewall rules intact. The B–C link remains usable, so B/C form a majority. Cutting only A–B would not necessarily isolate A because C could still communicate with it.

On the client, show that A is still alive but cannot provide an authoritative banking response:

```bash
curl --max-time 5 -sS http://192.168.56.11:3000/health | jq .
curl --max-time 8 -sS "http://192.168.56.11:3000/accounts/$BANK_ACCOUNT/balance" \
  -H "Authorization: Bearer $BANK_TOKEN" | jq .
bash scripts/cluster-status.sh
```

The health endpoint succeeds; the balance endpoint must become unavailable (HTTP 503 or a timeout during transition). The retrying workload continues on the B/C side. A stale `Leader` role immediately after partition does not mean A can commit; quorum is checked by the actual command path.

To demonstrate a directly attempted minority write, use a **different account** from the workload or run this after that workload ends, so a request that commits after healing cannot invalidate its accounting:

```bash
curl --max-time 8 -sS -X POST "http://192.168.56.11:3000/accounts/$BANK_ACCOUNT/deposit" \
  -H "Authorization: Bearer $BANK_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: minority-probe-1' --data '{"amount":1}' | jq .
```

A must not report a new committed success without quorum. A timeout does not prove permanent failure: use the same ID to reconcile any such request after healing. The regular CLI workload already journals all its own uncertain attempts.

Restore the network on A while requests are still active:

```bash
sudo bash scripts/partition.sh off
```

Keep all server processes running throughout the partition and recovery. Show A catching up automatically, one leader, continued client progress, final `verified:true`, and matching state digests after traffic settles:

```bash
bash scripts/cluster-status.sh
./bin/bank-client -servers "$BANK_SERVERS" -action reconcile -journal partition-journal.jsonl
```

No manual balance merge, copying live databases, or promotion is involved. Uncommitted suffixes from the isolated old leader are reconciled by Raft; the majority's committed history is authoritative.

#### Optional: demonstrate total loss of quorum

While A remains isolated, stop C's process too. Now A is alone and B is alone: neither side can commit writes or authoritative reads. Restore C and/or the network, then reconcile the same pending operation IDs. Availability resumes when a majority can communicate. Never force a one-node bootstrap to make the test appear available.

### 8. Client journals and interrupted runs

The journal is append-only JSONL with intent and terminal-outcome records. Workload journals also contain a baseline. Keep them on the client machine, and keep one workload per journal/account. They contain no passwords or bearer tokens. Do not run two client processes against the same journal; the mutex only serializes workers within one process.

The CLI checks `/me` and refuses journals belonging to another user/account before replaying intents. Keep journals associated with their original cluster as well; account IDs can repeat in a different, freshly initialized experiment.

```bash
./bin/bank-client -servers "$BANK_SERVERS" -action reconcile -journal crash-journal.jsonl
```

Reconciliation resends only intents without a recorded terminal outcome, using their original account/action/amount/ID. A committed outcome query is also available:

```bash
curl -sS "http://CURRENT-LEADER-IP:3000/operations/OPERATION-ID" \
  -H "Authorization: Bearer $BANK_TOKEN" | jq .
```

`unknown_operation` means no committed outcome is known at the read's position; it is not a guarantee that an earlier in-flight attempt can never commit. Retry the original operation with its ID. Do not create a replacement ID for an uncertain deposit.

After abrupt client termination, an incomplete last JSONL line is possible. The client refuses to append/reconcile past it. Back up the journal, inspect it, and remove **only the incomplete final line** before reconciliation. An intent is fsynced before sending; if its line was incomplete and no send occurred, dropping it loses no submitted operation. If an outcome line was incomplete, the complete intent remains and can be retried safely. Keep already complete lines intact.

If no baseline exists (ordinary single-operation journal), reconciliation reports unique-operation counts and net change but does not claim a final-balance invariant. With a workload baseline, it verifies the expected and actual balances and exits nonzero if they differ or operations remain pending.

### 9. Tests and verification

```bash
go mod verify
go test ./...
go vet ./...
go build ./cmd/server ./cmd/cluster-server ./cmd/client
```

The Raft tests cover deduplication, payload conflicts, ownership, overflow, SQL rollback, snapshots/replay, leader crash/retry, minority refusal, recovery/convergence, and whole-cluster restart. Cluster integration tests simulate transport partitions between three nodes. Use the configured deployment and partition helper for a separate process/network-level demonstration.

The sharded mode has its own PostgreSQL integration tests and seven Compose experiments, documented in [Sharded deployment](#sharded-deployment).

Reference behavior: [HashiCorp Raft API](https://github.com/hashicorp/raft/blob/v1.7.3/api.go), [FSM/snapshot interface](https://github.com/hashicorp/raft/blob/v1.7.3/fsm.go), and [Raft algorithm paper](https://raft.github.io/raft.pdf).

## Docker

### Standalone with PostgreSQL

Build the `gobank:latest` image once using `docker build -t gobank .`. Compose reuses this image without rebuilding it. Set a valid `JWT_SECRET` and `POSTGRES_PASSWORD` in the root `.env` file (see `.env.example`), then run:

```sh
docker compose --env-file .env -f standalone-compose.yml up -d
```

The server reads `.env` and always connects to the Compose PostgreSQL service, overriding any SQLite settings. PostgreSQL data persists in a named volume. The API is available on port 3000 by default; set `SERVER_PORT` to change the published port. PostgreSQL is only exposed inside the Compose network.

```sh
docker compose -f standalone-compose.yml down
```

Stopping the stack preserves database data.

### Individual containers

Build from the repository root:

```sh
docker build -t gobank .
```

Run the standalone server with the authentication settings from `.env` and persistent SQLite storage:

```sh
docker run --rm --env-file .env -e LISTEN_ADDR=0.0.0.0:3000 -e DB_PATH=/app/data/users.db -p 3000:3000 -v gobank-data:/app/data gobank
```

All three project commands are available. Override the default command to run the client:

```sh
docker run --rm --env-file .env gobank client -servers http://host.docker.internal:3000 -action balance
```

`host.docker.internal` is available with Docker Desktop. On Linux, add `--add-host host.docker.internal:host-gateway` when connecting to a server on the host.

Run a cluster node with a mounted configuration:

```sh
docker run --rm --env-file .env -p 3000:3000 -p 7000:7000 -v gobank-node-a:/app/data -v "./deploy/cluster:/config:ro" gobank cluster-server -config /config/a.json
```

For this legacy mode, adapt each configuration's Raft bind address and peer addresses to the container network. Set `data_dir` under `/app/data` for persistence. See the [bonus implementation and demo guide](#bonus-implementation-and-demo-guide) for configuration details; use the [sharded deployment guide](#sharded-deployment) for the current deployment.

The `build` stage also includes Go and the source tree for development commands:

```sh
docker build --target build -t gobank-dev .
docker run --rm gobank-dev go test ./...
```

Secrets, local databases, and host binaries are excluded from the build context. The runtime image runs as UID 10001; bind-mounted data directories must be writable by that user.

### Live B-to-C re-sharding

The optional overlay enables persistent routing from the coordinator database.
Start the deployment with this overlay from the beginning (with the same exported
secrets described above). Shard C is initially dormant and has no account range;
its database and two replicas can be started later, while banking traffic runs.
The existing static two-shard deployment still works without the overlay.

```sh
docker compose -f deploy/sharded/compose.yml -f deploy/sharded/resharding.yml up -d --build
# Later, while clients continue issuing deposits and transfers:
docker compose -f deploy/sharded/compose.yml -f deploy/sharded/resharding.yml --profile reshard up -d db-c shard-c1 shard-c2
docker compose -f deploy/sharded/compose.yml -f deploy/sharded/resharding.yml --profile reshard build reshard
docker compose -f deploy/sharded/compose.yml -f deploy/sharded/resharding.yml --profile reshard run --rm reshard
```

The worker moves **A1501–A2000** from B to C. It is deliberately scoped to this
assignment's fixed range and an initially empty, dedicated C database. All six
application replicas use the same shard image. Set `ROUTING_DSN` on every Gateway
and shard when running outside Compose; the worker also needs `SOURCE_DSN` and
`TARGET_DSN`. All processes read authoritative routing from PostgreSQL and fail
closed if metadata is unavailable; no process restart activates the new map.

The durable phases are `NEW → SNAPSHOT → REPLAY → FENCING → ACTIVATING → CLEANUP → DONE`.
A repeatable-read snapshot copies the range and replay metadata while B remains
writable. PostgreSQL triggers capture committed account and metadata changes in
an append-only log. A transaction advisory lock serializes log allocation with
commit order, including prepared transactions. Replay uses durable positions and
idempotent upserts; its checkpoint advances only after C commits. A failed
snapshot restarts from a new consistent snapshot and replays from zero. Audit,
idempotency and participant records are copied so retries retain their identities;
C holds only its own account balances. Historical non-account metadata is retained
on B, and the durable change log is retained as migration evidence.

Before cutover, the worker recovers unresolved coordinator decisions, acquires an
exclusive admission fence for the migrating range, and verifies that no affected
transaction is pending. Operations on other ranges continue. Account-row triggers
hold a shared guard-row lock inside each mutation, including PostgreSQL prepared
transactions. Fencing B therefore waits for those writes to resolve; an old epoch
or a fenced owner cannot mutate the range, even through a deliberately stale
internal request. A persisted cutover intent prevents reopening B after a crash.
The worker drains changes, compares the copied account state, enables C, and
atomically changes routing from epoch 1 to epoch 2. Only then does it delete B's
old accounts and range audits and reopen admission. Range mutations briefly wait
or return `range_cutover` during this final phase; retry the same idempotency key.
Balance reads may need a retry while ownership changes. Epochs are passed only
between trusted internal services and checked inside the account mutation.

To inspect progress:

```sh
docker compose -f deploy/sharded/compose.yml -f deploy/sharded/resharding.yml exec db-coordinator psql -U bank -d bank -c 'TABLE bank_migration; TABLE bank_routing;'
```

For process-crash experiments, run the worker with
`-e MIGRATION_CRASH_AT=snapshot` (or `replay`, `fencing`, `activation`) on the
`docker compose ... run --rm` command. It exits with code 86 at that boundary.
Rerun without the variable to resume automatically from durable state. A stopped
worker can be restarted with the same command; no repair SQL or ownership reset
is required. Preserve **all four** database volumes across restarts, and keep the
overlay enabled after migration. Do not reseed C or reset the routing database
independently of the shard databases.

`TestPostgresLiveReshardRecovery` exercises every crash boundary with real
PostgreSQL, concurrent snapshot-time writes, local and cross-shard transfers,
pre-existing durable COMMIT recovery, idempotent retries, stale-owner/epoch
rejection, old-data cleanup, and absence of orphan prepares. Run it with the same
`TEST_POSTGRES_DSN` requirements as the other Assignment 2 tests. These automated
tests simulate interruptions by unwinding the worker; the Compose process-exit
hooks are provided for the separate manual demo.

The actual Compose process-crash demo has also passed all four boundaries. See
[recorded evidence](docs/resharding/crash-recovery-demo.md). To repeat it against a
fresh isolated project with prebuilt images:

```sh
python3 scripts/sharded/resharding_demo.py --env-file /path/to/demo.env --evidence /tmp/resharding-evidence
```

The env file must define the documented demo secrets, the image `VERSION`, and a
unique `COMPOSE_PROJECT_NAME`. Optional `--override` and `--url` arguments allow
an isolated Gateway port. The runner preserves demo containers and volumes for
inspection and refuses a project that already has containers.

## Bonus II: autonomous replica replacement

The Go controller in `cmd/healing-controller` runs as a Docker Compose service.
It checks replica container state and `/ready`; after the failure grace period,
it creates a replacement with the original image ID, database environment,
configuration mounts and internal network. Healthy replicas keep their identities.
It uses the Docker Engine API directly, with no Docker CLI inside the image,
additional Go dependencies, prebuilt healing tools or automatic restart policies.

Export an explicit `COMPOSE_PROJECT_NAME` and the ordinary sharded deployment
variables, then add the overlay from initial startup:

```bash
docker compose --env-file deploy/sharded/.env \
  -f deploy/sharded/compose.yml -f deploy/sharded/self-healing.yml \
  up -d --build
```

The image is `gobank-healing-controller:${VERSION:-assignment2}`; optionally set
`HEALING_VERSION` to choose its tag independently. The dedicated Dockerfile
`healing-controller` target builds this standard-library-only executable without
module downloads. The existing Gateway and shard image targets are unchanged.

The named `healing-state` volume stores `/data/state.json`, including desired
per-shard counts, replica slots and immutable launch templates. State updates
use a private temporary file, file sync, atomic rename and directory sync.
A file lock prevents two controllers from using the same state. Deterministic
replacement names and `io.gobank.healing.slot` labels support adoption after a
crash, including between container creation and startup. Restart the controller
with the same volume; do not remove its state while managed replicas exist.
The state contains database credentials and is written with owner-only access.

Replacements register each original service DNS alias on the bank network.
The Gateway's existing probes admit that URL only once `/ready` succeeds, so
banking and coordinator participant calls reach the new container without
changing shard ownership or epochs. `X-Bank-Replica` identifies the logical slot;
Docker container IDs identify individual replacements. Authoritative balances,
idempotency records and prepared transactions remain in PostgreSQL.

The service runs as root to access the mounted `/var/run/docker.sock`. Docker
socket access grants control over the host daemon; deploy it only in the trusted
assignment stack. Readiness probes run inside the bank network, so the controller
needs no host access to bridge IP addresses. The demo driver still needs Python
3 and the Docker CLI. Replicas must have one network and configuration bind
mounts; the controller replaces applications, not databases or the Gateway.
A database outage can cause repeated replacement of unready applications.
Errors are isolated per slot so one failed repair does not block other slots.

Default desired slots are A1, A2, B1 and B2. Bootstrap requires one existing
Compose container per selected service, but later restarts use persisted state
if the originals have disappeared. The configured slot set is fixed for a
state volume; changing it is rejected. To include Bonus I's C replicas, start
C first, then bootstrap fresh controller state with
`HEALING_SERVICES=shard-a1,shard-a2,shard-b1,shard-b2,shard-c1,shard-c2`.
Do not run two controllers over the same slots. The controller does not edit
migration state. Simultaneous migration and healing have not been demonstrated.

No restart policy restarts the controller itself. After a controller crash,
start its Compose service again with the same volume; it reconciles durable
state automatically. Stop it before intentionally stopping/recreating the stack:

```bash
docker compose --env-file deploy/sharded/.env \
  -f deploy/sharded/compose.yml -f deploy/sharded/self-healing.yml \
  stop healing-controller
```

Custom replacements carry healing labels rather than Compose service labels;
`compose down` does not remove them. Inspect them with
`docker ps -a --filter label=io.gobank.healing.slot`, then explicitly remove the
selected containers when retiring a demo stack. Database volumes are preserved.

Run the controller tests with `go test ./internal/healing`. The real-container
demo needs prebuilt images, a fresh project name beginning with
`gobank-healing-demo`, and an env file containing the deployment variables:

```bash
python3 scripts/sharded/self_healing_demo.py \
  --env-file /path/to/healing-demo.env \
  --evidence /tmp/healing-evidence
# Optional: --override /path/to/ports.yml --url http://127.0.0.1:3103
```

The demo runs the Go controller under Compose, kills A1 during concurrent
banking traffic, crashes the controller after replacement creation, recreates
its Compose container with the same state volume, and pauses the adopted
replica to exercise a running but unresponsive application. It checks adoption
without duplicates, both replacements entering Gateway rotation, healthy
container identities, exact balances, duplicate-request effects and zero orphan
prepared transactions. It stops the controller and retains the stack and state
volume for inspection. Evidence is written to `summary.json`, `controller.log`
and `crashed-controller.log`. See [recorded results](docs/self-healing/demo.md).
