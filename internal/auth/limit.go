package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type attempts struct {
	count int
	reset time.Time
}
type limiter struct {
	mu      sync.Mutex
	clients map[string]attempts
}

// Use the direct peer address; untrusted forwarding headers cannot bypass limits.
func (l *limiter) allow(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for key, entry := range l.clients {
		if !now.Before(entry.reset) {
			delete(l.clients, key)
		}
	}
	entry, exists := l.clients[ip]
	if !exists {
		if len(l.clients) >= 4096 {
			return false
		}
		entry.reset = now.Add(time.Minute)
	}
	if entry.count >= 10 {
		return false
	}
	entry.count++
	l.clients[ip] = entry
	return true
}
