# HTTP and TCP comparison

Measured on 2026-10-12, Windows/amd64, Intel Core i7-11800H, with eight Go execution
threads. Reproduce with:

```powershell
go test ./internal/transporttest -run '^$' -bench BenchmarkTransports -benchtime=200x -benchmem -cpu=8 -count=1
```

Each case uses a fresh SQLite file, the same seeded user/account and single
database connection, real `127.0.0.1:0` listeners, persistent network connections,
and structured request logging to stdout. Setup, password hashing and issuing
credentials are excluded. Serial cases use one connection; parallel cases use
eight workers/connections. Pools are warmed before measurement. Protected HTTP
requests validate JWTs; TCP requests perform persisted session and user lookups.
Deposits/withdrawals change one integer unit and commit a durable audit record.

Results below are a single short local run. p95 measures individual roundtrip
latency; Go's parallel ns/op is amortized throughput, not individual latency.
Memory is allocated bytes per exchange across the process. SQLite wait time is
`database/sql` connection-pool contention, not a direct SQLite lock-time metric.
The [recorded Go metrics](socket-benchmark-results.txt) include all sixteen cases,
allocation counts and connection waits per operation. Request logs were omitted
from that result file to keep it readable.

| Workload | Mode | HTTP req/s | TCP req/s | HTTP p95 ms | TCP p95 ms | HTTP B/op | TCP B/op |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| health | serial | 9,328 | 11,329 | 0.993 | 0.995 | 9,779 | 6,405 |
| health | 8 workers | 42,708 | 40,586 | 1.002 | 0.785 | 9,017 | 6,568 |
| balance | serial | 3,894 | 3,145 | 1.224 | 1.173 | 18,766 | 23,197 |
| balance | 8 workers | 11,724 | 4,517 | 1.802 | 6.000 | 18,962 | 23,892 |
| deposit | serial | 460 | 453 | 6.999 | 7.591 | 42,045 | 45,145 |
| deposit | 8 workers | 561 | 508 | 41.206 | 31.333 | 42,669 | 45,290 |
| withdraw | serial | 516 | 489 | 6.917 | 7.291 | 41,508 | 44,864 |
| withdraw | 8 workers | 554 | 518 | 38.902 | 29.015 | 41,997 | 45,349 |

Parallel database wait per exchange was about 0.431 ms HTTP versus 1.374 ms TCP
for balance reads, 12.067 versus 13.220 ms for deposits, and 12.125 versus
12.969 ms for withdrawals. The extra persisted session lookups increase TCP
connection acquisitions; both versions serialize durable writes through SQLite.
These measurements support retaining the single database connection and do not
establish that sockets are faster. Larger repeated runs, TLS, other storage,
multiple processes and production workloads can change the comparison.

`TestHTTPAndTCPBehaviorParity` compares public profiles, signup/duplicate handling,
credential errors, ownership, invalid/overflowing amounts, insufficient funds and
committed balances using isolated databases. Authentication payloads intentionally
differ: HTTP returns a JWT, TCP returns an opaque session token.
