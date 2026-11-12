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
- Optional GitHub OAuth sign-in with persistent SQLite accounts and cookie sessions
- Guest access to HTTP request execution
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

### Run with Docker

Install Docker with the Compose plugin (on Windows, use Docker Desktop with
Linux containers), then run from the repository root:

```sh
docker compose up --build -d
docker compose ps
```

The API is available at `http://localhost:8080`. Check
`http://localhost:8080/health` for `{"status":"ok"}`. The image also checks this
endpoint automatically. View logs or stop the service with:

```sh
docker compose logs -f api
docker compose down
```

The multi-stage [Dockerfile](./Dockerfile) builds the Go binary and runs it as a
non-root user with HTTPS CA certificates. [compose.yaml](./compose.yaml) binds
port 8080 to localhost for development. After source changes, run
`docker compose up --build -d` again to rebuild the image.

SQLite runs inside the API process. Its database is stored at
`/data/gorequest.db` in the `api-data` named volume, which survives container
rebuilds and `docker compose down`. **`docker compose down --volumes` deletes
that database.** The container starts with a fresh database; the existing
repository-local `gorequest.db` is not copied or changed.

Compose reads the root `.env` for the settings listed in its `environment`
section and passes them to the running container. Secrets and local databases
are excluded from the image build. GitHub sign-in requires all three GitHub
variables documented below; with all three absent, guest access works without
an `.env` file. Do not use the placeholder OAuth credentials from `.env.example`
as real configuration. Keep the local callback URL as
`http://localhost:8080/auth/github/callback`.

This Compose setup defaults `AUTH_INSECURE_COOKIES` to `true` for local HTTP.
For production, set it to `false`, serve through an HTTPS reverse proxy, and
configure the public GitHub callback URL and frontend origin accordingly.

To back up SQLite, stop the API so the entire database directory can be copied
consistently, then restart it. Use a new destination folder for each backup:

```sh
docker compose stop api
docker compose cp api:/data ./gorequest-backup
docker compose start api
```

Keep that backup outside the container and volume. To verify persistence locally,
sign in, run `docker compose down`, and start the service again; accounts and
unexpired sessions should still be available. The Docker volume is separate from
the repository-local database; moving existing data into it requires a separate
copy while the API is stopped, with ownership set to UID/GID `10001:10001`.

### Execute an HTTP request

No sign-in is required. Send
`POST http://localhost:8080/requests/execute` with
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
| 403 | Browser origin rejected by CSRF protection |
| 413 | JSON request exceeds 1 MiB |
| 415 | Content-Type is not application/json |
| 429 | Execution rate, concurrency, or client tracking capacity limit reached; retry after the `Retry-After` seconds |
| 502 | Connection/TLS/DNS failure, blocked destination, or response exceeds 5 MiB |
| 504 | Execution timeout, including time spent reading the response |

Only absolute HTTP/HTTPS URLs resolving exclusively to public IP addresses are
allowed. Localhost, private, link-local, and reserved destinations are blocked at
connection time, and the client dials the validated IP to prevent DNS rebinding.
Proxy environment variables are ignored. URL credentials and fragments are
rejected. Host, framing, proxy, and connection headers are managed by the client.
TLS certificate verification uses Go's defaults.

`GET /health` returns `{"status":"ok"}` without authentication.

### Execution limits

Guests and signed-in users share limits by direct client IP: by default, 60
execution attempts per minute, 2 concurrent requests per IP, and 16 concurrent
requests across the server. Each IP's one-minute window starts with its first
attempt. Invalid requests and attempts rejected for concurrency count toward the
rate limit. Excess requests are rejected immediately with JSON status 429 and
`Retry-After` in seconds; there is no waiting queue. Concurrency slots cover
authentication, body decoding, execution, and response writing and are released
when the handler exits.

Limits are in memory per server process and reset on restart. At most 4096 IPs
are tracked; new IPs receive 429 when this table is full, until inactive expired
entries can be reclaimed. Forwarded IP headers are ignored, so clients behind a
reverse proxy share its limits. Configure per-client limits at the proxy and
adjust these server limits to the deployment's capacity. Multiple replicas need
shared limiting at the proxy or gateway for deployment-wide limits.

### Optional GitHub sign-in

Guests can execute requests without an account. GitHub OAuth is the only sign-in
method; email/password signup and login endpoints have been removed. Signed-in
users are available to handlers through `auth.CurrentUser`. Use `OptionalUser`
for public routes and `RequireUser` for future account-only features. Saved
requests, collections, and history are not implemented yet.

1. In your GitHub OAuth App settings, set **Authorization callback URL** to
   `http://localhost:8080/auth/github/callback` for local development.
2. Fill in `.env` using `.env.example` as a reference (keep your existing credentials):

   ```dotenv
   GITHUB_CLIENT_ID=your-client-id
   GITHUB_CLIENT_SECRET=your-client-secret
   GITHUB_REDIRECT_URL=http://localhost:8080/auth/github/callback
   AUTH_INSECURE_COOKIES=true
   FRONTEND_ORIGIN=http://localhost:3000
   ```

3. Run `go run ./cmd/server` from the repository root. The server loads `.env`
   automatically; existing process environment variables take precedence.
4. Navigate the browser to `http://localhost:8080/auth/github` to sign in.

Never put the client secret in frontend code. `.env` is ignored by Git. Use HTTPS
and leave `AUTH_INSECURE_COOKIES` unset in production; update the callback URL in
both the environment and GitHub OAuth App settings.

| Method | Endpoint | Behavior |
| --- | --- | --- |
| GET | `/auth/github` | Redirect the browser to GitHub for authorization |
| GET | `/auth/github/callback` | Validate the OAuth flow and create an app session |
| GET | `/auth/me` | Return `{"user":{"id":1,"email":"you@example.com"}}`, or 401 for guests |
| POST | `/auth/logout` | Revoke the current session and clear its cookie; returns 204 |
| POST | `/requests/execute` | Execute a request for guests or signed-in users |

The callback redirects to `FRONTEND_ORIGIN/` when configured; otherwise it returns
the signed-in user as JSON. Rejected callbacks return JSON errors. Successful
sign-in sets an HttpOnly cookie. Use browser navigation for sign-in, not `fetch`:

```typescript
// Sign-in button:
window.location.assign("http://localhost:8080/auth/github");

// After returning to the frontend (401 means guest):
const response = await fetch("http://localhost:8080/auth/me", {
  credentials: "include",
});

// Sign out:
await fetch("http://localhost:8080/auth/logout", {
  method: "POST",
  credentials: "include",
});
```

Use `credentials: "include"` for authenticated API calls. Guest request execution
also works without cookies. An expired or invalid session is treated as a guest
on public routes. Account-only routes return 401.

GitHub authorization uses a random, browser-bound, one-time state with a ten-minute
expiry and PKCE S256. The server requests `read:user user:email`, verifies the
GitHub identity and primary verified email, and identifies returning users by
GitHub's numeric user ID. GitHub access tokens are used only during sign-in and
are not persisted or returned to the frontend. See the
[GitHub OAuth documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps).

App sessions expire after seven days and rotate on sign-in. Only SHA-256 hashes
of session tokens are stored in SQLite. Cookies use HttpOnly, SameSite=Lax, and,
by default, Secure with the `__Host-` prefix. Authentication responses are not
cached. Unsafe browser requests are protected by origin checks.

OAuth start and callback requests share a limit of ten requests per minute per
direct client IP, with at most two concurrent authentication operations. Excess
requests return 429 with `Retry-After`. Forwarded IP headers are ignored; behind
a reverse proxy callers share its IP limit. Configure per-client limiting at the
proxy for that deployment. Execution has separate limits as documented above.

| Variable | Default | Purpose |
| --- | --- | --- |
| `GITHUB_CLIENT_ID` | unset | GitHub OAuth App client ID |
| `GITHUB_CLIENT_SECRET` | unset | Server-only GitHub OAuth App secret |
| `GITHUB_REDIRECT_URL` | unset | Callback URL ending in `/auth/github/callback` |
| `AUTH_DB_PATH` | `gorequest.db` | SQLite database file; parent directory must exist |
| `AUTH_INSECURE_COOKIES` | unset | Set exactly `true` only for local HTTP development |
| `FRONTEND_ORIGIN` | unset | Exact frontend origin, e.g. `http://localhost:3000`, for CORS, CSRF trust, and the post-login redirect |
| `EXECUTE_REQUESTS_PER_MINUTE` | `60` | Execution attempts per minute per direct client IP |
| `EXECUTE_CONCURRENT_PER_IP` | `2` | Maximum active execution handlers per direct client IP |
| `EXECUTE_CONCURRENT_TOTAL` | `16` | Maximum active execution handlers across the server |

Execution settings must be positive integers; invalid or empty values stop startup.

If all three GitHub settings are absent, sign-in routes are disabled and guest
access remains available. Partial GitHub configuration stops startup with an
error. Frontend and API should share the same site: localhost with different
ports works in development; use HTTPS on the same domain or sibling subdomains
in production. Cookies will not work for API fetches from an unrelated frontend
domain, even when CORS allows it.

On the first startup with a legacy database, a migration removes password hashes
and revokes old sessions while preserving user records. Legacy users are not
automatically linked by email. A GitHub sign-in whose email already belongs to
an unlinked legacy account returns 409 and requires an explicit account migration;
there is no self-service linking endpoint yet. Back up your database before
upgrading. SQLite storage and in-memory throttling target a single backend instance.

### Prerequisites

- Go 1.26.5
- Git

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
