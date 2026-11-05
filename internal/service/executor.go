package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultTimeoutMS = 30000
	MaxTimeoutMS     = 60000
	MaxResponseBytes = 5 << 20
)

var (
	ErrInvalidRequest   = errors.New("invalid request")
	ErrResponseTooLarge = errors.New("response exceeds 5 MiB limit")
)

type ExecuteRequest struct {
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

type ExecuteResponse struct {
	StatusCode int         `json:"status_code"`
	Status     string      `json:"status"`
	Headers    http.Header `json:"headers"`
	Body       string      `json:"body"`
	SizeBytes  int         `json:"size_bytes"`
	DurationMS float64     `json:"duration_ms"`
}

type Executor struct {
	client *http.Client
}

func NewExecutor(client *http.Client) *Executor {
	return &Executor{client: client}
}

func (e *Executor) Execute(ctx context.Context, input ExecuteRequest) (*ExecuteResponse, error) {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
	default:
		return nil, invalid("method must be GET, POST, PUT, PATCH, DELETE, HEAD, or OPTIONS")
	}
	target, err := url.Parse(input.URL)
	if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, invalid("url must be an absolute http or https URL")
	}
	if target.User != nil || target.Fragment != "" || target.Opaque != "" {
		return nil, invalid("url must not contain credentials or a fragment")
	}
	timeoutMS := input.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = DefaultTimeoutMS
	}
	if timeoutMS < 1 || timeoutMS > MaxTimeoutMS {
		return nil, invalid("timeout_ms must be between 1 and 60000")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(input.Body))
	if err != nil {
		return nil, invalid("could not construct HTTP request")
	}
	for name, value := range input.Headers {
		if !validHeaderName(name) || !validHeaderValue(value) {
			return nil, invalid("headers contain an invalid name or value")
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "transfer-encoding", "connection", "proxy-authorization",
			"proxy-connection", "trailer", "upgrade", "te", "keep-alive":
			return nil, invalid("header %q is managed by the HTTP client", name)
		}
		req.Header.Set(name, value)
	}
	start := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute HTTP request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read HTTP response: %w", err)
	}
	if len(body) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	return &ExecuteResponse{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Headers:    resp.Header,
		Body:       string(body),
		SizeBytes:  len(body),
		DurationMS: float64(time.Since(start)) / float64(time.Millisecond),
	}, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == 127 || value[i] < 32 && value[i] != '\t' {
			return false
		}
	}
	return true
}
