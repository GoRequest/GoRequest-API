package handler

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const maxExecutionClients = 4096

// ExecutionLimits applies per-process limits to the entire execution handler,
// including authentication, body decoding, execution, and response writing.
type ExecutionLimits struct {
	RequestsPerMinute int
	ConcurrentPerIP   int
	ConcurrentTotal   int
}

type executionClient struct {
	count  int
	active int
	reset  time.Time
}

type executionLimiter struct {
	mu          sync.Mutex
	limits      ExecutionLimits
	clients     map[string]*executionClient
	active      int
	nextCleanup time.Time
	now         func() time.Time
}

// NewExecutionLimiter returns middleware sharing one limiter across wrapped handlers.
func NewExecutionLimiter(limits ExecutionLimits) (func(http.Handler) http.Handler, error) {
	if limits.RequestsPerMinute < 1 || limits.ConcurrentPerIP < 1 || limits.ConcurrentTotal < 1 {
		return nil, fmt.Errorf("execution limits must be positive integers")
	}
	l := &executionLimiter{limits: limits, clients: make(map[string]*executionClient), now: time.Now}
	return l.wrap, nil
}

func (l *executionLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Trust only the direct peer, never caller-controlled forwarding headers.
		ip := r.RemoteAddr
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
		release, retry, message := l.acquire(ip)
		if release == nil {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeError(w, http.StatusTooManyRequests, message)
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}

func (l *executionLimiter) acquire(ip string) (func(), int, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !now.Before(l.nextCleanup) {
		for key, client := range l.clients {
			if client.active == 0 && !now.Before(client.reset) {
				delete(l.clients, key)
			}
		}
		l.nextCleanup = now.Add(time.Minute)
	}
	client := l.clients[ip]
	if client == nil {
		if len(l.clients) >= maxExecutionClients {
			return nil, 60, "execution client capacity reached; try again later"
		}
		client = &executionClient{reset: now.Add(time.Minute)}
		l.clients[ip] = client
	}
	if !now.Before(client.reset) {
		client.count = 0
		client.reset = now.Add(time.Minute)
	}
	if client.count >= l.limits.RequestsPerMinute {
		retry := int((client.reset.Sub(now) + time.Second - 1) / time.Second)
		return nil, retry, "execution rate limit exceeded; try again later"
	}
	// Count attempts rejected for concurrency as well, to bound repeated retries.
	client.count++
	if client.active >= l.limits.ConcurrentPerIP || l.active >= l.limits.ConcurrentTotal {
		return nil, 1, "execution concurrency limit reached; try again later"
	}
	client.active++
	l.active++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		client.active--
		l.active--
	}, 0, ""
}
