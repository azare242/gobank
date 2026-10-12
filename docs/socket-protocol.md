# TCP version 1

The existing Fiber routes and JWT configuration remain supported. HTTP behavior is
specified by `internal/api/*_test.go`: signup trims usernames (1–64 Unicode
characters), accepts passwords of 8–72 bytes, returns 201 and `{id,username,account}`,
and atomically creates account A(1000+id). Duplicates return 409/conflict.
Login uses generic 401/unauthorized credential errors, dummy bcrypt comparison for
unknown users, and ten attempts per peer IP per minute. User reads return public
profiles; missing users return 404/unsupported_action. Account responses contain
status, account and integer balance. Ownership failures return 404/unknown_account.
Missing amounts return 400/missing_field; nonpositive, fractional, overflowing or
noninteger amounts return 400/invalid_amount. Insufficient funds return
409/insufficient_funds. Admission/throttling returns 429/server_busy. Internal
failures return 500/internal_error. Successful mutations and their audits commit
together; login audits must persist before credentials are returned.

HTTP health returns `{status:"ok"}`. HTTP login returns `access_token`,
`token_type:"Bearer"` and `expires_in:900`; TCP login uses the session response
below. `/me` and `/users/:id` return `{id,username,account}`, and `/users` returns
an ordered array of those profiles (including `[]` when empty). HTTP `/me` rejects
a deleted user with 401. Protected routes require authentication before their
operation validation. Errors use `{status:"error",error,message}`. HTTP signup
also sets Location, while authenticated/profile/account responses set no-store.

Each frame is a four-byte unsigned big-endian length and one UTF-8 JSON object.
Requests are 1–16384 bytes; responses are at most 1 MiB. Invalid/truncated framing
closes the connection. Complete malformed JSON receives malformed_request.
Requests run sequentially on each connection, concurrently across connections.
No multiplexing is supported. An oversized user listing returns an explicit
response-too-large error, never truncated data.

Every request requires integer `version:1` and string `operation`. Optional
`request_id` is at most 128 bytes with no control characters; omitted IDs get UUIDs.
`token` holds the opaque session token for protected operations. Unknown fields,
invalid types, invalid versions and unknown operations are rejected. Inputs:

| Operation | Fields | Authentication |
| --- | --- | --- |
| health | none | public |
| create_user, login | username, password | public |
| logout, me, list_users | none | session |
| get_user | id (positive unsigned integer) | session |
| balance | account (nonempty string) | session + ownership |
| deposit, withdraw | account, amount (positive int64) | session + ownership |

Responses contain version, request_id, status, status_code, and data on success
or error and message on failure. status_code is compatibility metadata, not HTTP.
Login data is `{session_token,expires_in}`; sessions have an absolute 15-minute
default lifetime and survive reconnects/restarts. Logout revokes the presented
session. Every protected request looks up its session and user again.

Pipeline order: decode, identify audit intent, global admission, login IP limiter,
session validation, operation input validation, ownership/business transaction,
audit finalization, logging, response write. Audits use method TCP and paths
/login or /accounts/{account}/{deposit|withdraw}. Logs exclude credentials and
payloads. Failed audits use a separate bounded context after operation rollback.
A failed response write does not change a committed result. Request IDs provide
correlation only: never blindly retry money changes after an ambiguous disconnect.
Browsers, curl and Invoke-RestMethod cannot speak this protocol directly.

## Wire examples and results

These are JSON payloads; each must be prefixed with its four-byte length. The
interactive client handles framing and token attachment without requiring JSON.

```json
{"version":1,"request_id":"demo-1","operation":"login","username":"alice","password":"example-password"}
```

```json
{"version":1,"request_id":"demo-1","status":"success","status_code":200,"data":{"session_token":"<opaque-token>","expires_in":900}}
```

```json
{"version":1,"request_id":"demo-2","operation":"deposit","token":"<opaque-token>","account":"A1001","amount":500}
```

```json
{"version":1,"request_id":"demo-2","status":"success","status_code":200,"data":{"status":"success","account":"A1001","balance":500}}
```

`create_user` returns 201 with `{id,username,account}`; `me` and `get_user` return
the same public profile at 200. `list_users` returns an ordered array of profiles.
`health` returns `{status:"ok"}`. `balance`, `deposit` and `withdraw` return
`{status:"success",account,balance}` at 200. `logout` returns
`{logged_out:true}` at 200 and revokes the presented token. All other protected
requests with that token then return 401/unauthorized, including on the same
connection. Login creates a distinct session each time. The database stores
only SHA-256 token hashes, user IDs, creation and absolute expiration timestamps.

The 1 MiB listing limit produces 500/response_too_large with an explanatory
message; clients receive no partial listing. Unknown operations return
400/unsupported_action; unsupported versions and invalid envelopes return
400/malformed_request. Credential failures and invalid/missing/expired/revoked
sessions return 401/unauthorized. Framing failures close the connection, while
complete invalid JSON receives an error and permits the next valid frame.
