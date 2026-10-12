# Socket implementation record: steps 1-9

Steps 1-6 were completed first, followed by the interactive client, broader
verification and delivery in steps 7-9. The mandatory task testing requirement
was applied throughout implementation. The existing Fiber server remains
available with its original HTTP routes and JWT authentication.

| Step | Implemented behavior | Validation run after implementation |
| --- | --- | --- |
| 1 | HTTP characterization and banking/authentication/error contract in socket-protocol.md | Consistency review against API handlers/tests; baseline `go test ./...` passed |
| 2 | Strict JSON envelope, version/ID rules, bounded big-endian framing, complete writes, documented response limit | `go test ./internal/socketserver ./internal/api ./internal/service` passed; framing tests include fragmentation, multiple frames, partial writes, truncation, bad lengths and invalid envelopes |
| 3 | Shared banking services, Fiber adapters, atomic money/audit transactions, hashed persistent sessions, revoke/logout/expiry/bounded cleanup | `go test ./...` passed after extraction and again after adapter completion; service tests cover credential errors, duplicates, ownership, audit rollback, session hashes, expiry, logout, restart, revoke-all and deleted users |
| 4 | Connection cap, per-connection handlers, admission semaphore, absolute deadlines, contexts, panic recovery and tracked lifecycle | `go test ./...` passed; `go test -race ./internal/socketserver ./internal/service` passed; lifecycle tests cover slow frames, excess connections, panic slot release, idle shutdown and forced operation cancellation |
| 5 | Dispatcher, IP limiter, per-request sessions, integer amounts, centralized failed audits, transaction-owned success audits and sanitized structured logs | `go test ./...` passed after adding real TCP tests; persistent connection flow covers all operations, reconnect/logout, throttling, monetary validation and audit counts; oversized listing regression test passed |
| 6 | Separate TCP entrypoint, validated environment settings, loopback default/TLS, signal-driven bounded shutdown | `go test ./...` passed after configuration implementation and lifecycle tests; TLS roundtrip verifies a trusted certificate; invalid settings and plaintext non-loopback binding are rejected |

Additional verification: `go test -race ./...`, `go vet ./...`, and
`go build ./cmd/server ./cmd/socket-server` passed. `go list -deps
./cmd/socket-server` contains neither Fiber nor internal/auth nor JWT.
The later delivery checks also build the socket-client executable.

Environment notes: some sandboxed Go commands failed to access the shared
`D:\GoCache`. Those commands were rerun with approved access and passed. The
initial expiration test used a local-zone timestamp; it was corrected to UTC
to match persisted session timestamps, and rerun successfully. No required
validation remains blocked.

## Steps 7-9

| Task | Implementation and verification | Passing commands |
| --- | --- | --- |
| 7: reusable client | Bounded frame exchange, raw JSON result data preserving int64 amounts, request ID/version/status validation, deadlines/cancellation, TLS verification, next-request reconnect without replay | `go test ./internal/socketclient`; later `go test ./...` and `go test -race ./internal/socketclient ./internal/socketcli` |
| 7: interactive menus | Public/authenticated menus, input reprompting, hidden terminal passwords, cleared password buffers, automatic sessions, mutation confirmation/cancellation, EOF/interrupt cleanup, logout/unauthorized state clearing, uncertain mutation with optional balance check | `go test ./...` after menu implementation and after password/interrupt refinements; menu unit tests and `TestInteractiveWalkthrough` passed |
| 8: correctness/concurrency | Twenty-four simultaneous deposits/withdrawals, conditional insufficient-funds outcomes, overflow, reused-connection expiry, login/throttle audit counts, malformed/truncated frames over net.Pipe, retained boundaries after invalid JSON, committed response-write failure, active drain, operation deadline, audit/log redaction, resource release and signup rollback | `go test ./...` and `go test -race ./internal/socketserver` after concurrency tasks; full `go test -race ./...` passed |
| 8: transport comparison | Isolated databases with real HTTP/TCP listeners compare status/error codes and successful business payloads. Matched serial/eight-worker benchmark reports latency, throughput, allocated memory and SQLite pool waits | `go test ./internal/transporttest`; `go test ./internal/transporttest -run '^$' -bench BenchmarkTransports -benchtime=200x -benchmem -cpu=8 -count=1` passed; results in socket-benchmarks.md and socket-benchmark-results.txt |
| 9: executable delivery | Compiled Fiber server starts with JWT config; compiled TCP server starts with all JWT settings removed; compiled client completes signup/login/deposit/withdraw/logout on a real TCP connection | `go test ./internal/delivery` passed, including the full menu walkthrough |
| 9: final checks/docs | README, protocol schemas/examples, TLS instructions, menus, retries/session lifecycle and measurement report reviewed against code/tests; TCP dependency graph excludes Fiber, JWT and internal/auth | `go vet ./...`; `go build ./cmd/server ./cmd/socket-server ./cmd/socket-client`; `go list -deps ./cmd/socket-server` passed |

Failures corrected before completing their tasks: the initial ambiguous-mutation
menu test exposed transport errors carrying a partial response, so the menu now
discards that response and always shows the uncertain-outcome flow. Static
analysis found unkeyed shared-service test inputs; these were changed to named
fields, and `go vet ./...` passed on rerun. Installing the terminal dependency
initially hit a sandbox cache permission error; the approved retry succeeded.
`go mod tidy` completed successfully. No required checks are blocked.

Benchmarking shows database-backed sessions incur extra SQLite lookups, and
durable write throughput is similar under the measured workload. No assertion
that TCP is faster is made. SQLite remains configured with one connection.
