# GoRequest API

GoRequest API is an open-source HTTP API testing backend built with **Go**.

The goal is to provide a simple and efficient way to execute HTTP requests, inspect responses, and help developers test and debug APIs.

This project focuses on exploring HTTP, networking, concurrency, and backend development using Go.

## Features

The following features are planned:

- Support for GET, POST, PUT, PATCH, and DELETE requests
- Custom request headers and body
- HTTP response status, headers, and body
- Request execution time
- Bearer Token authentication
- Request timeout handling
- Safe HTTP request execution

## Tech Stack

- **Language:** Go
- **HTTP Server:** `net/http`
- **HTTP Client:** `net/http`
- **Communication:** REST API with JSON

## Getting Started

GoRequest API is currently under development.

Installation and setup instructions will be added as the project evolves.

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

- [ ] Set up the HTTP server
- [ ] Implement HTTP request execution
- [ ] Support different HTTP methods
- [ ] Handle custom headers and request bodies
- [ ] Return HTTP response details
- [ ] Implement request timeout handling
- [ ] Add request validation and security
- [ ] Write unit and integration tests
- [ ] Document API endpoints

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