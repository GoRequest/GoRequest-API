package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MarlonBBo/GoRequest/internal/service"
)

func TestExecuteEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer upstream.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /requests/execute", Execute(service.NewExecutor(upstream.Client())))
	for _, tt := range []struct {
		name, method, contentType, body string
		want                            int
	}{
		{"success with upstream error", "POST", "application/json", `{"method":"GET","url":"` + upstream.URL + `"}`, 200},
		{"timeout", "POST", "application/json", `{"method":"GET","url":"` + upstream.URL + `/slow","timeout_ms":20}`, 504},
		{"invalid URL", "POST", "application/json", `{"method":"GET","url":"/relative"}`, 400},
		{"missing media type", "POST", "", `{}`, 415},
		{"malformed JSON", "POST", "application/json", `{`, 400},
		{"unknown field", "POST", "application/json", `{"extra":true}`, 400},
		{"multiple values", "POST", "application/json", `{} {}`, 400},
		{"null", "POST", "application/json", `null`, 400},
		{"oversized body", "POST", "application/json", `{"body":"` + strings.Repeat("x", MaxRequestBytes) + `"}`, 413},
		{"wrong method", "GET", "application/json", `{}`, 405},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/requests/execute", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
			if tt.want == 405 {
				return
			}
			if w.Header().Get("Content-Type") != "application/json" || !json.Valid(w.Body.Bytes()) {
				t.Fatalf("expected JSON response: %s", w.Body.String())
			}
			if tt.want == 200 {
				var response service.ExecuteResponse
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != 404 || response.Body != "not found" {
					t.Fatalf("upstream response was not preserved: %+v", response)
				}
			}
		})
	}
}
