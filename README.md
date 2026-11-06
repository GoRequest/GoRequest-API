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
- Bearer Token authentication
- Request timeout handling
- Public destination validation and bounded request/response sizes

## Tech Stack

- **Language:** Go
- **HTTP Server:** `net/http`
- **HTTP Client:** `net/http`
- **Communication:** REST API with JSON

## Getting Started

Start the server on port 8080:

```sh
go run ./cmd/server
```

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
Bearer tokens are supplied through the `Authorization` header.

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

`GET /health` returns `{"status":"ok"}`. The API currently has no authentication
or rate limiting; keep access restricted to trusted callers.

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
- [ ] Add authentication and rate limiting

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
