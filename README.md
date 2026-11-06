# GoRequest API

GoRequest API is an open-source HTTP API testing backend built with **Go**.

The goal is to provide a simple and efficient way to execute HTTP requests, inspect responses, and help developers test and debug APIs.

This project focuses on exploring HTTP, networking, concurrency, and backend development using Go.

## Features

Implemented features:

- Support for GET, POST, PUT, PATCH, DELETE, HEAD, and OPTIONS requests
- Custom request headers and body
- HTTP response status, headers, and body
- Request execution time
- Bearer token support for destination APIs
- User signup/login with persistent SQLite accounts and cookie sessions
- Request timeout handling
- Public destination validation and bounded request/response sizes

## Tech Stack

- **Language:** Go
- **HTTP Server:** `net/http`
- **HTTP Client:** `net/http`
- **Communication:** REST API with JSON

## Getting Started

Start the server on port 8080 for local HTTP development (PowerShell):

```powershell
$env:AUTH_INSECURE_COOKIES = "true"
go run ./cmd/server
```

For Bash, use `AUTH_INSECURE_COOKIES=true go run ./cmd/server`.
Secure cookies are enabled by default; leave `AUTH_INSECURE_COOKIES` unset when
serving the API over HTTPS in production.

Run the tests:

```sh
go test ./...
```

### Execute an HTTP request

Sign in first using the authentication endpoints below. Send
`POST http://localhost:8080/requests/execute` with the session cookie and
`Content-Type: application/json`:

```json
{
  "method": "POST",
  "url": "https://httpbin.org/post",
  "headers": {
    "Content-Type": "application/json",
    "Authorization": "Bearer your-token"
  },
  "body": "{\"message\":\"hello\"}",
  "timeout_ms": 10000
}
```

`method` and `url` are required. Headers and body are optional. `timeout_ms`
defaults to 30000 when omitted or zero, and otherwise accepts 1 through 60000.
Bearer tokens for the destination API are supplied through `headers.Authorization`
inside the JSON body. GoRequest's own authentication uses a separate session cookie;
that cookie is never forwarded to the destination API.

A completed request returns HTTP 200 with the upstream response, including when
the upstream returns a 4xx or 5xx status:

```json
{
  "status_code": 200,
  "status": "200 OK",
  "headers": {"Content-Type": ["application/json"]},
  "body": "{\"message\":\"hello\"}",
  "size_bytes": 19,
  "duration_ms": 123.45
}
```

`size_bytes` counts response body bytes before JSON encoding, excluding headers.
It measures the body after any automatic decompression by the HTTP client, rather
than the compressed size on the network. An empty body has a size of zero.

Response headers use arrays to preserve repeated values. Duration includes reading
the response body. Bodies are returned as text; arbitrary binary downloads are
not supported losslessly (invalid UTF-8 is replaced during JSON encoding).
Redirect responses are returned without following the `Location` header.

Errors use `{"error":"description"}` with these API status codes:

| Status | Meaning |
| --- | --- |
| 400 | Invalid JSON, request fields, URL, method, headers, or timeout |
| 401 | Missing, invalid, expired, or revoked GoRequest session |
| 403 | Browser origin rejected by CSRF protection |
| 413 | JSON request exceeds 1 MiB |
| 415 | Content-Type is not application/json |
| 502 | Connection/TLS/DNS failure, blocked destination, or response exceeds 5 MiB |
| 504 | Execution timeout, including time spent reading the response |

Only absolute HTTP/HTTPS URLs resolving exclusively to public IP addresses are
allowed. Localhost, private, link-local, and reserved destinations are blocked at
connection time, and the client dials the validated IP to prevent DNS rebinding.
Proxy environment variables are ignored. URL credentials and fragments are
rejected. Host, framing, proxy, and connection headers are managed by the client.
TLS certificate verification uses Go's defaults.

`GET /health` returns `{"status":"ok"}` without authentication.

### User authentication

| Method | Endpoint | Behavior |
| --- | --- | --- |
| POST | `/auth/signup` | Create an account; returns 201 with `{"user":{"id":1,"email":"you@example.com"}}` |
| POST | `/auth/login` | Check credentials, set a session cookie, and return the user |
| GET | `/auth/me` | Return the signed-in user, or 401 |
| POST | `/auth/logout` | Revoke the current session and clear its cookie; returns 204 |

Signup and login require `Content-Type: application/json` and this body:

```json
{"email":"you@example.com","password":"a long unique password"}
```

Emails are trimmed and lowercased. Signup requires a password of at least 12
characters and at most 1024 bytes. Passwords are salted and hashed with Argon2id
(19 MiB memory, two iterations, one lane); plaintext passwords are never stored.
Duplicate emails return 409. Invalid login credentials return a generic 401.
Malformed input returns 400, oversized credentials (over 8 KiB) return 413, and an
unsupported Content-Type returns 415.

Signup creates the account; login is a separate step. For example, in PowerShell:

```powershell
$credentials = @{ email = "you@example.com"; password = "a long unique password" } | ConvertTo-Json
Invoke-RestMethod http://localhost:8080/auth/signup -Method Post -ContentType "application/json" -Body $credentials
Invoke-RestMethod http://localhost:8080/auth/login -Method Post -ContentType "application/json" -Body $credentials -SessionVariable session
Invoke-RestMethod http://localhost:8080/auth/me -WebSession $session
$request = @{ method = "GET"; url = "https://httpbin.org/get" } | ConvertTo-Json
Invoke-RestMethod http://localhost:8080/requests/execute -Method Post -ContentType "application/json" -Body $request -WebSession $session
Invoke-RestMethod http://localhost:8080/auth/logout -Method Post -WebSession $session
```

Sessions expire after seven days. Login rotates any session presented by the browser;
logout immediately revokes it on the server. Only SHA-256 hashes of random 256-bit
session tokens are stored in SQLite. Cookies use HttpOnly, SameSite=Strict, and, by
default, Secure with the `__Host-` prefix. Authentication responses are not cached.
Expired sessions are purged when a new session is created.

Signup and login share a limit of 10 attempts per minute per direct client IP, with
at most two concurrent password operations. Excess requests return 429 with
`Retry-After`. Forwarded IP headers are deliberately ignored; behind a reverse
proxy, callers share its IP limit. Configure per-client limiting at the proxy for
that deployment. Request execution does not yet have a rate limit.

Configuration:

| Variable | Default | Purpose |
| --- | --- | --- |
| `AUTH_DB_PATH` | `gorequest.db` | SQLite database file; parent directory must exist |
| `AUTH_INSECURE_COOKIES` | unset | Set exactly `true` only for local HTTP development |
| `FRONTEND_ORIGIN` | unset | Optional exact frontend origin, e.g. `http://localhost:3000`, for credentialed CORS and CSRF trust |

For the separate React frontend, set `FRONTEND_ORIGIN` to its origin and use
`credentials: "include"` on **all** authentication and protected API calls:

```typescript
const response = await fetch("http://localhost:8080/auth/login", {
  method: "POST",
  credentials: "include",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ email, password }),
});
```

No session token needs to be stored in localStorage. CSRF protection rejects unsafe
browser requests from untrusted origins. SameSite=Strict cookies require the
frontend and API to be on the same site: localhost with different ports works in
development; use HTTPS on the same domain or sibling subdomains in production.
An unrelated frontend domain will not receive cookies even if CORS permits it.

Persist and back up the SQLite file, restrict access to it, and exclude it from
version control. This storage and in-memory throttling target a single backend
instance. Email verification, password recovery, and frontend login screens are
not included yet.

### Prerequisites

- Go 1.26.5
- Git

## Project Structure

```text
gorequest-api/
├── cmd/
│   └── server/
│       └── main.go
├── internal/
│   ├── handler/
│   ├── service/
│   └── httpclient/
├── go.mod
├── README.md
└── LICENSE
```

## Roadmap

- [x] Set up the HTTP server
- [x] Implement HTTP request execution
- [x] Support different HTTP methods
- [x] Handle custom headers and request bodies
- [x] Return HTTP response details
- [x] Implement request timeout handling
- [x] Add request validation and destination restrictions
- [x] Write unit and integration tests
- [x] Document API endpoints
- [x] Add user authentication and authentication endpoint rate limiting
- [ ] Add request execution rate limiting

## Frontend

The frontend is maintained in a separate repository and built with React and TypeScript.

GoRequest API can be used independently by any HTTP client.

## Contributing

Contributions, suggestions, and bug reports are welcome!

To contribute:

1. Fork this repository.
2. Create a branch for your changes.
3. Implement and test your changes.
4. Open a Pull Request with a clear description.

Feel free to open an Issue to report bugs or suggest new features.

## License

This project is licensed under the MIT License. See the `LICENSE` file for details.
