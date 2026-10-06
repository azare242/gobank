# Users API

A small Go project using Fiber v3, GORM, and SQLite. The pure-Go SQLite driver needs no C compiler. The `users` table is created automatically on startup, with an auto-incrementing `id`, a unique `username`, and a `password` column containing a bcrypt hash. Passwords are omitted from API responses.

## Run

Install Go 1.26 or later, then run from this directory:

```powershell
go mod tidy
go run ./cmd/server
```

The server listens at `http://127.0.0.1:3000` and stores data in `users.db`. Override these defaults with the `LISTEN_ADDR` and `DB_PATH` environment variables. The database starts empty; create users through the API.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/health` | Check that the server is running |
| POST | `/users` | Create a user |
| GET | `/users` | List users |
| GET | `/users/:id` | Get a user by ID |

Create and retrieve a user in PowerShell:

```powershell
$body = @{ username = 'alice'; password = 'example-password' } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri http://127.0.0.1:3000/users -ContentType 'application/json' -Body $body
Invoke-RestMethod -Uri http://127.0.0.1:3000/users
Invoke-RestMethod -Uri http://127.0.0.1:3000/users/1
```

User responses contain only `id` and `username`. Usernames are trimmed, case-sensitive, and must be 1–64 characters. Passwords must be 8–72 bytes. Duplicate usernames return HTTP 409; invalid input returns 400; missing users return 404.

This starter provides user storage and creation; authentication is not implemented.

## Verify

```powershell
go mod verify
go test ./...
go vet ./...
go build ./cmd/server
```

The integration test uses a temporary SQLite database and checks user creation, password hashing, duplicate usernames, validation, retrieval, and persistence.
