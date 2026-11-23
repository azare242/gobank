# Go service settings for Assignment 3

The Gateway and shard binaries support headless Service discovery, database
startup retries, graceful shutdown, JSON logs, and an optional metrics listener.
These are the application changes needed before deploying the Kubernetes plan.

## Shard discovery

A Kubernetes shard map can use one headless Service URL per shard:

```json
{
  "shards": [
    {"id":"a","first":1,"last":1000,"discovery":"dns","replicas":["http://shard-a-headless:3000"]},
    {"id":"b","first":1001,"last":2000,"discovery":"dns","replicas":["http://shard-b-headless:3000"]}
  ]
}
```

Set `SHARD_MAP` to the mounted file. Static maps without `discovery` still
require two distinct URLs per shard. Account ownership is unchanged. Leave
`ROUTING_DSN` unset for the core Kubernetes assignment.

The Gateway resolves each DNS name to IPv4/IPv6 addresses, removes duplicates,
checks each target's `/ready`, and routes only to healthy targets. Failed or
empty DNS answers retain the last successful targets, which still have to pass
readiness. Targets removed by a successful lookup leave the pool and metrics.
The Gateway retries discovery during `/ready` if a shard has no known targets.
DNS caching can delay discovery beyond the refresh interval. Use readiness,
a preStop delay, and client retries during pod replacement.

## Environment settings

Durations use Go notation, such as `500ms`, `25s`, or `5m`. Invalid optional
settings log a warning and use their default. Connection counts are integers;
open connections are always limited to at least one.

| Setting | Default | Purpose |
| --- | --- | --- |
| `STARTUP_TIMEOUT` | `2m` | Total startup budget |
| `STARTUP_ATTEMPT_TIMEOUT` | `10s` | Limit for one database startup step |
| `SHUTDOWN_TIMEOUT` | `25s` | Wait for active HTTP handlers |
| `HTTP_READ_TIMEOUT` | `10s` | HTTP request read limit |
| `HTTP_WRITE_TIMEOUT` | `60s` | HTTP response write limit |
| `READINESS_TIMEOUT` | `1s` | Readiness and DNS lookup budget |
| `SHARD_HTTP_TIMEOUT` | `6s` | One Gateway-to-shard HTTP attempt |
| `COORDINATOR_TIMEOUT` | `30s` | Coordinator operation/recovery task budget |
| `DISCOVERY_REFRESH_INTERVAL` | `2s` | Gateway discovery and health refresh |
| `DB_MAX_OPEN_CONNS` | `30` | Maximum connections per service process |
| `DB_MAX_IDLE_CONNS` | `5` | Retained idle connections |
| `DB_CONN_MAX_LIFETIME` | `5m` | Connection replacement interval |
| `DB_CONN_MAX_IDLE_TIME` | `1m` | Idle connection expiry |
| `METRICS_ADDR` | unset | Separate metrics listener, e.g. `:9090` |
| `VERSION`, `COMMIT` | `dev`, `unknown` | Logs/build metric identity |

Database connection, migration, fixture seed, and participant readiness steps
retry with exponential delays from 250ms to 5s until startup expires. Each
failed connection attempt closes its pool. PostgreSQL connections default to a
5s connect timeout unless the DSN specifies one. Set pool limits to 10–15 per
pod when increasing replicas, and use the CNPG primary (`-rw`) Service in DSNs.

`/health` remains independent of the database. `/ready` checks dependencies
with a deadline. SIGTERM closes the HTTP listener and waits for active handlers;
Gateway background workers stop before database connections close. Configure
Kubernetes termination grace longer than shutdown plus the preStop delay
(the plan uses 45s with a 5s preStop delay).

## Observability

`METRICS_ADDR=:9090` exposes `/metrics` separately from the banking API. It is
disabled by default. The small registry uses Prometheus text format and adds no
module dependencies. Keep this listener accessible to monitoring only.

Metrics cover request counts, latency histogram buckets, requests in flight,
readiness, discovered/healthy shard targets, DB connectivity, prepared
transactions, coordinator status/decision counts, pending transactions, and build
identity. HTTP labels use route templates; unmatched requests share one route.
Database gauges refresh every five seconds with a timeout; scraping does not
query PostgreSQL. Database-derived counts retain their last successful sample
during an outage; check the DB-up gauge alongside them. Oldest pending age is
measured from the oldest pending row's `updated_at` (last durable progress).

JSON request logs include request/operation/transaction IDs, response status,
error code, pending/decision fields, and duration in milliseconds. Coordinator
and participant logs record durable state changes, prepare results,
acknowledgements, recovery attempts, and unknown/orphan transaction detection.
Passwords, authentication tokens, and DSNs are excluded from these new logs.

## Checks

```bash
go test ./...
go test -race ./internal/gateway ./internal/httpbank ./internal/runtimeconfig ./internal/shardmap ./internal/observability
go vet ./...
```

Set `TEST_POSTGRES_DSN` to a dedicated PostgreSQL instance with prepared
transactions enabled to run the database integration tests. Kubernetes scaling,
CNPG failover, and the seven deployment experiments need a running cluster.
