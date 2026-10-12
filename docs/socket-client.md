# Interactive TCP client

Run `go run ./cmd/socket-server` with `DB_PATH=socket-users.db` in one terminal
and `go run ./cmd/socket-client` in another. The HTTP version remains runnable
with `go run ./cmd/server` and its documented JWT settings. The TCP executable
requires no JWT configuration; the independent executable startup tests remove
all JWT settings before starting it.

The client uses integer amounts, validates inputs locally, and attaches its
in-memory session token automatically. The server remains authoritative.
Terminal passwords use `golang.org/x/term.ReadPassword`; piped input supports
scripted walkthroughs without printing passwords. Never place a password or
session token in command-line flags. On exit or EOF the client tries logout for
at most two seconds. Interrupts cancel exchanges and unblock stdin, restoring
terminal settings. If logout fails, the client explains session persistence.

## Example walkthrough

This condensed session is exercised against a real TCP server by both the menu
integration test and the compiled-executable delivery test. `[hidden]` denotes
password entry with terminal echo disabled.

```text
1. Register
2. Login
3. Health check
4. Exit
Choice: 1
Username: alice
Password: [hidden]
Registered alice, account A1001. Log in to continue.
Choice: 2
Username: alice
Password: [hidden]
User: alice (ID 1), account: A1001

Signed in as alice, account A1001
1. My profile
2. Account balance
3. Deposit
4. Withdraw
5. Logout
6. Exit
Choice: 2
Account A1001 balance: 0
Choice: 3
Amount: 100
Confirm deposit of 100 on A1001? [y/n]: y
Change completed.
Account A1001 balance: 100
Choice: 4
Amount: 40
Confirm withdraw of 40 on A1001? [y/n]: y
Change completed.
Account A1001 balance: 60
Choice: 5
Logged out.
Choice: 4
Goodbye.
```

Registration also accepts Unicode usernames of 1-64 characters after trimming
and passwords of 8-72 bytes. Invalid choices/amounts are reprompted; choosing `n`
at confirmation cancels without sending a mutation. The profile/account ID is
read from the server after login, so users do not need to enter account IDs.

## TLS

Use a certificate valid for the server's hostname/IP and its corresponding PEM
private key. For local development, if OpenSSL is installed:

```powershell
openssl req -x509 -newkey rsa:2048 -sha256 -days 7 -nodes -keyout localhost-key.pem -out localhost-cert.pem -subj /CN=localhost -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
$env:SOCKET_TLS_CERT = "localhost-cert.pem"
$env:SOCKET_TLS_KEY = "localhost-key.pem"
$env:DB_PATH = "socket-users.db"
go run ./cmd/socket-server
```

In the client terminal:

```powershell
go run ./cmd/socket-client -tls -ca localhost-cert.pem -addr 127.0.0.1:4000
```

Production certificates should come from the appropriate trusted CA. Without
`-ca`, the client uses system roots. `-server-name` can select a certificate
hostname when connecting to an IP. There is no insecure verification flag.
Tests reject untrusted certificates, wrong hostnames and insecure TLS configs.
Non-loopback server addresses require TLS certificate/key configuration.

## Reconnects and uncertain outcomes

If an exchange fails, its connection is discarded. The next explicit operation
opens a new connection and can reuse the current session. Session validation is
performed again on every request. Logout and revocation take effect on subsequent
validation, while operations already admitted may finish. A protocol integration
test demonstrates reconnecting with a valid token and rejection after logout.

A failed response to a money change is ambiguous: the balance and success audit
may already be committed. The client sends the mutation once, explains the
uncertainty, and offers a balance check rather than a retry. Durable exactly-once
mutations require a separate transactional idempotency design. Concurrent
clients can change the balance, so the check itself is only an observation.
