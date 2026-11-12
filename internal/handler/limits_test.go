package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestLimiter(limits ExecutionLimits) *executionLimiter {
	return &executionLimiter{limits: limits, clients: make(map[string]*executionClient), now: time.Now}
}

func limitedRequest(h http.Handler, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/requests/execute", nil)
	r.RemoteAddr = peer
	r.Header.Set("X-Forwarded-For", peer)
	r.Header.Set("X-Real-IP", peer)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func assertLimited(t *testing.T, w *httptest.ResponseRecorder, retry string) {
	t.Helper()
	var body map[string]string
	if w.Code != 429 || w.Header().Get("Retry-After") != retry ||
		w.Header().Get("Content-Type") != "application/json" ||
		w.Header().Get("Cache-Control") != "no-store" ||
		json.Unmarshal(w.Body.Bytes(), &body) != nil || body["error"] == "" {
		t.Fatalf("invalid rejection: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
	}
}

func TestExecutionRateLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newTestLimiter(ExecutionLimits{2, 2, 4})
	l.now = func() time.Time { return now }
	calls := 0
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest) // Failed attempts also consume quota.
	}))
	for _, peer := range []string{"[2001:db8::1]:1000", "[2001:db8::1]:2000"} {
		if w := limitedRequest(h, peer); w.Code != 400 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	now = now.Add(500 * time.Millisecond)
	r := httptest.NewRequest(http.MethodPost, "/requests/execute", nil)
	r.RemoteAddr = "[2001:db8::1]:3000"
	r.Header.Set("X-Forwarded-For", "203.0.113.42")
	r.Header.Set("X-Real-IP", "203.0.113.42")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	assertLimited(t, w, "60")
	if calls != 2 {
		t.Fatalf("rejected request reached handler: calls=%d", calls)
	}
	if w := limitedRequest(h, "[2001:db8::2]:1000"); w.Code != 400 {
		t.Fatal("independent IP was limited")
	}
	now = now.Add(59500 * time.Millisecond)
	if w := limitedRequest(h, "[2001:db8::1]:4000"); w.Code != 400 {
		t.Fatal("quota did not reset at window boundary")
	}
}

func TestExecutionConcurrencyLimits(t *testing.T) {
	l := newTestLimiter(ExecutionLimits{100, 1, 2})
	entered := make(chan struct{}, 2)
	finish := make(chan struct{})
	var wg sync.WaitGroup
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-finish
	}))
	start := func(peer string) {
		wg.Add(1)
		go func() { defer wg.Done(); limitedRequest(h, peer) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("handler did not start")
		}
	}
	defer func() { close(finish); wg.Wait() }()
	start("192.0.2.1:1000")
	assertLimited(t, limitedRequest(h, "192.0.2.1:2000"), "1")
	start("192.0.2.2:1000")
	assertLimited(t, limitedRequest(h, "192.0.2.3:1000"), "1")
}

func TestExecutionConcurrentAdmission(t *testing.T) {
	l := newTestLimiter(ExecutionLimits{100, 3, 3})
	var wg sync.WaitGroup
	var admitted atomic.Int32
	releaseAll := make(chan struct{})
	attempted := make(chan struct{}, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, _, _ := l.acquire("192.0.2.1")
			if release != nil {
				admitted.Add(1)
			}
			attempted <- struct{}{}
			if release != nil {
				<-releaseAll
				release()
			}
		}()
	}
	for i := 0; i < 50; i++ {
		<-attempted
	}
	close(releaseAll)
	wg.Wait()
	if admitted.Load() != 3 || l.active != 0 {
		t.Fatalf("admitted=%d active=%d", admitted.Load(), l.active)
	}
}

func TestExecutionSlotsReleased(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			l := newTestLimiter(ExecutionLimits{10, 1, 1})
			h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "panic" {
					panic("handler failed")
				}
				if mode == "cancel" {
					<-r.Context().Done()
				}
				w.WriteHeader(502)
			}))
			func() {
				defer func() {
					if p := recover(); p != nil && mode != "panic" {
						t.Fatalf("unexpected panic: %v", p)
					}
				}()
				r := httptest.NewRequest(http.MethodPost, "/requests/execute", nil)
				if mode == "cancel" {
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
				}
				h.ServeHTTP(httptest.NewRecorder(), r)
			}()
			w := limitedRequest(l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })), "192.0.2.1:1000")
			if w.Code != 204 || l.active != 0 {
				t.Fatalf("slot leaked: status=%d active=%d", w.Code, l.active)
			}
		})
	}
}

func TestExecutionClientCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newTestLimiter(ExecutionLimits{100, 1, 2})
	l.now = func() time.Time { return now }
	for i := 0; i < maxExecutionClients; i++ {
		l.clients[strconv.Itoa(i)] = &executionClient{reset: now.Add(time.Minute)}
	}
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	assertLimited(t, limitedRequest(h, "192.0.2.1:1000"), "60")
	// Keep one active client across expiry; cleanup must not forget its slot.
	l.clients["0"].active = 1
	l.active = 1
	now = now.Add(time.Minute)
	if w := limitedRequest(h, "192.0.2.1:1000"); w.Code != 204 {
		t.Fatalf("expired clients were not reclaimed: status=%d", w.Code)
	}
	if len(l.clients) != 2 || l.clients["0"] == nil {
		t.Fatalf("cleanup dropped active client or retained expired clients: %d", len(l.clients))
	}
	assertLimited(t, limitedRequest(h, "0"), "1")
}

func TestExecutionLimitsValidation(t *testing.T) {
	for _, limits := range []ExecutionLimits{{0, 1, 1}, {1, -1, 1}, {1, 1, 0}} {
		if _, err := NewExecutionLimiter(limits); err == nil {
			t.Fatalf("accepted invalid limits: %+v", limits)
		}
	}
}
