package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/test?q=value" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("URL or custom header was not forwarded")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "request body" {
			t.Errorf("unexpected request body: %q", body)
		}
		w.Header().Add("X-Test", "first")
		w.Header().Add("X-Test", "second")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte("upstream error"))
		}
	}))
	defer upstream.Close()
	executor := NewExecutor(upstream.Client())
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		t.Run(method, func(t *testing.T) {
			response, err := executor.Execute(context.Background(), ExecuteRequest{
				Method: method, URL: upstream.URL + "/test?q=value",
				Headers: map[string]string{"Authorization": "Bearer test-token"}, Body: "request body",
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 422 || response.Status != "422 Unprocessable Entity" || len(response.Headers.Values("X-Test")) != 2 || response.DurationMS < 0 {
				t.Fatalf("unexpected response: %+v", response)
			}
			wantBody := "upstream error"
			if method == http.MethodHead {
				wantBody = ""
			}
			if response.Body != wantBody {
				t.Errorf("body = %q, want %q", response.Body, wantBody)
			}
			if response.SizeBytes != len(wantBody) {
				t.Errorf("size_bytes = %d, want %d", response.SizeBytes, len(wantBody))
			}
		})
	}
}

func TestValidation(t *testing.T) {
	for name, input := range map[string]ExecuteRequest{
		"missing method":      {URL: "https://example.com"},
		"unsupported method":  {Method: "CONNECT", URL: "https://example.com"},
		"relative URL":        {Method: "GET", URL: "/test"},
		"non HTTP URL":        {Method: "GET", URL: "file:///tmp/test"},
		"credentials":         {Method: "GET", URL: "https://user:pass@example.com"},
		"fragment":            {Method: "GET", URL: "https://example.com/#test"},
		"negative timeout":    {Method: "GET", URL: "https://example.com", TimeoutMS: -1},
		"excessive timeout":   {Method: "GET", URL: "https://example.com", TimeoutMS: MaxTimeoutMS + 1},
		"header injection":    {Method: "GET", URL: "https://example.com", Headers: map[string]string{"X-Test": "value\r\nInjected: yes"}},
		"invalid header name": {Method: "GET", URL: "https://example.com", Headers: map[string]string{"X Test": "value"}},
		"host override":       {Method: "GET", URL: "https://example.com", Headers: map[string]string{"hOsT": "localhost"}},
		"framing override":    {Method: "GET", URL: "https://example.com", Headers: map[string]string{"Content-Length": "100"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewExecutor(http.DefaultClient).Execute(context.Background(), input)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	executor := NewExecutor(upstream.Client())
	_, err := executor.Execute(context.Background(), ExecuteRequest{Method: "GET", URL: upstream.URL, TimeoutMS: 20})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout while reading body, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = executor.Execute(ctx, ExecuteRequest{Method: "GET", URL: upstream.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", MaxResponseBytes+1))
	}))
	defer upstream.Close()
	_, err := NewExecutor(upstream.Client()).Execute(context.Background(), ExecuteRequest{Method: "GET", URL: upstream.URL})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected size limit error, got %v", err)
	}
}
