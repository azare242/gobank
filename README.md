# Mini Banking API

For the implemented three-server fault-tolerance and network-partition bonuses,
including VirtualBox setup, retrying client, and demonstration commands, see the
[bonus implementation and demo guide](#bonus-implementation-and-demo-guide) below.
The original single-server mode is documented first.

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
| `deploy/cluster/{a,b,c}.json` | Ready-to-edit configurations for three separate VMs |
| `scripts/start-node.sh` | Start one server, save its PID, append logs to disk |
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

The guarantees assume nonmalicious nodes, intact durable disks, one common cluster configuration, and eventual recovery of a majority. When no majority can communicate, service deliberately becomes unavailable. Four VMs on one host cannot survive failure of that physical host.

### 1. Create the VirtualBox VMs

Use three minimal Linux server VMs and one Linux client VM. A starting allocation is 1–2 vCPUs and 1–2 GB RAM per server, 1 vCPU and 1 GB for the client, with 15–20 GB dynamically allocated disks. Adjust to the host's capacity.

| VM | Host-only address | Services |
| --- | --- | --- |
| A | `192.168.56.11/24` | HTTP 3000, Raft TCP 7000 |
| B | `192.168.56.12/24` | HTTP 3000, Raft TCP 7000 |
| C | `192.168.56.13/24` | HTTP 3000, Raft TCP 7000 |
| Client | `192.168.56.20/24` | CLI, curl, jq |

Give every VM a NAT adapter for downloads and a second adapter on the **same Host-only Network**. Configure static addresses outside its DHCP pool, or use reservations. The host-only interface needs no default gateway. Generate fresh MAC addresses and hostnames if cloning VMs, and clone before initializing banking data. If this subnet conflicts with your network, change it everywhere in the JSON configurations and commands below.

On Linux, install Git, curl, jq, OpenSSL, iptables, and a Go version compatible with `go.mod`. For Debian/Ubuntu, the non-Go packages can be installed with:

```bash
sudo apt-get update
sudo apt-get install -y git curl jq openssl iptables
```

Copy/clone the same project revision onto all VMs. On each VM, from the project directory:

```bash
go mod download
mkdir -p bin
go build -o bin/cluster-server ./cmd/cluster-server
go build -o bin/bank-client ./cmd/client
```

Allow HTTP 3000 and TCP 7000 on the host-only lab interfaces if guest firewall policy blocks them. Do not run the cluster over the unrelated NAT addresses. Check `ip -br addr` on each VM and ping the host-only addresses from the client.

### 2. Configure and start servers

Create `.env` on A using the existing example, then copy the same JWT settings securely to B and C:

```bash
cp .env.example .env
chmod 600 .env
sed -i "s|^JWT_SECRET=.*|JWT_SECRET=$(openssl rand -base64 32)|" .env
```

All three servers must use the **same** `JWT_SECRET`, `JWT_ISSUER`, and `JWT_AUDIENCE`. Generate the key once; do not independently regenerate it per server. The cluster binary uses JSON configuration for listener/data paths; the `.env` database settings apply only to the original single-server binary.

Review `deploy/cluster/a.json`, `b.json`, and `c.json`. Each VM runs only its own node configuration. `bootstrap` is true only on A. It initializes the three-voter membership only when A has no Raft state; subsequent restarts reuse persisted state. Never enable bootstrap independently on B or C, erase a live node's directory, or create independent clusters to recover from a partition.

The example configurations enable demo faults. This permits the explicit crash/rollback controls described below and authenticated leadership transfer/snapshot controls. Set `enable_demo_faults` to false outside the disposable experiment. The transport is plaintext and has no peer authentication: use only the isolated lab network, synthetic data, and lab credentials.

Start from each VM's project directory:

The start script uses NetworkManager (`nmcli`) and `sudo` to create or update the persistent `bank-lab` profile on `enp0s8`, then activates it before starting the server. It assigns `.11`, `.12`, or `.13` for A, B, or C respectively, with a `/24` prefix and no default gateway. These values assume the host-only adapter and subnet shown above; update the script too if your lab uses different settings.

```bash
## On A:
bash scripts/start-node.sh a
## On B:
bash scripts/start-node.sh b
## On C:
bash scripts/start-node.sh c
```

Then, on the client VM:

```bash
export BANK_SERVERS=http://192.168.56.11:3000,http://192.168.56.12:3000,http://192.168.56.13:3000
bash scripts/cluster-status.sh
```

Expect one `Leader` and two `Follower` nodes after a few seconds. A alone cannot commit because the configured cluster needs two voters. `/health` reports process liveness; `/ready` succeeds only after committing a fence, so use the leader URL when checking readiness. `/cluster/status` is deliberately a possibly stale diagnostic view; it is not a banking read or proof that a node can commit.

### 3. Start and use the client VM

The client is a command-line program: each invocation performs an action and exits, except a workload runs until its requested operations finish. There is no client server or background service to start. Do not run `scripts/start-node.sh` on the client VM; that script is only for servers A, B, and C.

#### Configure the client network

In VirtualBox, keep Adapter 1 on NAT and connect Adapter 2 to the same host-only network (`lab`) as the servers. On the client VM, check `ip -br addr`. For Mint with NetworkManager and the host-only interface named `enp0s8`, configure the client address once:

```bash
sudo nmcli connection add type ethernet \
  con-name bank-lab ifname enp0s8 \
  ipv4.method manual ipv4.addresses 192.168.56.20/24 \
  ipv4.never-default yes connection.autoconnect yes
sudo nmcli connection up bank-lab
ip -br addr
```

If `bank-lab` already exists, update it instead of adding another profile:

```bash
sudo nmcli connection modify bank-lab connection.interface-name enp0s8 \
  ipv4.method manual ipv4.addresses 192.168.56.20/24 \
  ipv4.gateway '' ipv4.never-default yes connection.autoconnect yes
sudo nmcli connection up bank-lab
```

Use your actual host-only interface name if it differs. `lab` is the VirtualBox network name; `bank-lab` is a connection profile inside the guest. The names do not need to match.

#### Build and check connectivity

On the client VM, from the project directory (for example `cd ~/gobank`):

```bash
go mod download -x
mkdir -p bin
go build -o bin/bank-client ./cmd/client
./bin/bank-client -help
export BANK_SERVERS=http://192.168.56.11:3000,http://192.168.56.12:3000,http://192.168.56.13:3000
curl --connect-timeout 3 --max-time 5 http://192.168.56.11:3000/health
curl --connect-timeout 3 --max-time 5 http://192.168.56.12:3000/health
curl --connect-timeout 3 --max-time 5 http://192.168.56.13:3000/health
bash scripts/cluster-status.sh
```

Start the servers on their own VMs first. Health responses should contain `status: "ok"` and the corresponding node ID; cluster status should eventually show one leader and two followers. On the client, `localhost` refers to the client VM, so use the servers' host-only addresses. The client does not need the servers' `.env` or JWT signing secret.

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

Use a fresh journal filename for each new workload. After opening a new terminal or restarting the client VM, export `BANK_SERVERS` and `BANK_PASSWORD` again, then repeat login and the token/account exports above. Keep existing journals for reconciliation; do not register the same username again just to restart the client.

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

Open terminals for the client, A, B, and C. Show VM IPs, roles, and server logs (`tail -f logs/a.log`, etc.). Keep auto-restart services disabled for this experiment.

1. On the client, start a workload long enough to make failure visible:

   ```bash
   ./bin/bank-client -servers "$BANK_SERVERS" -action workload \
     -count 400 -workers 4 -interval 200ms -amount 1 -journal crash-journal.jsonl
   ```

2. While it is still printing operations, on A kill the actual server PID saved by the start script:

   ```bash
   ps -p "$(cat logs/a.pid)" -o pid,args
   kill -9 "$(cat logs/a.pid)"
   ```

3. The client must resume through B or C automatically, without editing its server list or manually promoting a node. Display `bash scripts/cluster-status.sh` from another client terminal. The workload's final JSON must show `verified:true`.

4. If it ends with pending operations, resolve them after the majority is healthy:

   ```bash
   ./bin/bank-client -servers "$BANK_SERVERS" -action reconcile -journal crash-journal.jsonl
   ```

5. Restart A on A's VM:

   ```bash
   bash scripts/start-node.sh a
   ```

   Wait for catch-up. All three diagnostic `state_digest` values should match after traffic stops; applied indexes may briefly differ because reads also append commands. A returns as a follower, rather than overwriting the current leader's balances.

The workload records its initial balance and checks:

```text
expected = initial + sum(unique successful deposits) - sum(unique successful withdrawals)
```

The verifier counts logical operation IDs, not HTTP responses. Do not run unrelated deposits/withdrawals on this account between baseline capture and final verification.

#### Confirmed data survives restart

Record the balance after a successful CLI deposit, kill the current leader, let a backup serve a balance inquiry, restart the killed node, and query again. The cluster tests additionally cover stopping and reopening every node with its original data directory. Do not use VM snapshot rollback as a restart test: that deliberately rewinds disks.

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

While it is active, on **A's VM**, isolate A from both other voters:

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

The journal is append-only JSONL with intent and terminal-outcome records. Workload journals also contain a baseline. Keep them on the client VM, and keep one workload per journal/account. They contain no passwords or bearer tokens. Do not run two client processes against the same journal; the mutex only serializes workers within one process.

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

### 9. Tests and recording checklist

Run:

```bash
go mod verify
go test ./...
go vet ./...
go build ./cmd/server ./cmd/cluster-server ./cmd/client
```

The new automated tests cover durable deduplication/rejections, changed-payload conflicts, ownership/overflow, actual SQL rollback, disk-write failure atomicity, snapshots/replay, concurrent proposals, leader crash/retry, minority refusal, recovery/convergence, whole-cluster restart, API/auth/error behavior, and client retry after a lost HTTP response. Cluster integration tests simulate transport partitions between three Raft nodes; the separate VirtualBox/firewall demo is still required to establish the assignment's real network-failure scenario.

The implementation can be checked locally, but the VM topology and real firewall interruption must be run on your VirtualBox guests. Do not claim they were demonstrated solely because the Go tests passed.

Validation performed during implementation: the full Go test suite, `go vet ./...`, and `go mod verify` passed; the cluster failure tests passed three consecutive runs; all Bash scripts passed syntax checks. Both bonus binaries also cross-compiled successfully for Linux/amd64. A separate local three-process TCP smoke run used the actual CLI while hard-killing a leader, checked final accounting, exercised commit-before-reply crash plus duplicate retry, and restarted all servers including snapshot restore; those checks passed. The real VirtualBox/firewall demonstration has not been run here. This workspace's Git ownership differs from the sandbox user's ownership, so local build/test verification used the session-only `GOFLAGS=-buildvcs=false`; this did not change repository or global Git settings.

For the submitted video (maximum five minutes), rehearse a prepared sequence: show VM IPs and roles; run normal/error/rollback commands; show concurrent operations and confirmed-state durability; kill the leader during an active workload and show automatic takeover; partition/reconnect the old leader while processes stay alive; finish with verified accounting, converged digests, and logs. Shorten workload counts/intervals to fit the recording after practicing. Include the ambiguous-response proof if time permits, and include its test evidence in the report.

Keep the report within five A4 pages. Explain why majority commitment prevents split brain, why stable operation IDs prevent duplicate effects, why no majority means unavailability, how snapshots/replay recover state, and the exact commands/topology used. State that the third server is a full replica/voter and that either backup can become primary.

Reference behavior: [HashiCorp Raft API](https://github.com/hashicorp/raft/blob/v1.7.3/api.go), [FSM/snapshot interface](https://github.com/hashicorp/raft/blob/v1.7.3/fsm.go), and [Raft algorithm paper](https://raft.github.io/raft.pdf).
