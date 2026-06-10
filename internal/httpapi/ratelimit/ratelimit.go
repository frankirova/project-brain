// Package ratelimit provides a simple in-memory token-bucket rate limiter
// for the HTTP API. One bucket per remote IP. When the bucket is empty,
// requests get 429 Too Many Requests.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Limiter holds per-IP token buckets.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64 // tokens per second
	burst    float64 // max tokens
	idleTTL  time.Duration
	lastSeen map[string]time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a Limiter that allows `rate` requests per second with a burst
// of `burst` requests, per remote IP. idleTTL controls how long an
// inactive IP's bucket is retained before being garbage-collected.
func New(rate, burst float64, idleTTL time.Duration) *Limiter {
	l := &Limiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		burst:    burst,
		idleTTL:  idleTTL,
		lastSeen: make(map[string]time.Time),
	}
	go l.gcLoop()
	return l
}

func (l *Limiter) gcLoop() {
	t := time.NewTicker(l.idleTTL)
	defer t.Stop()
	for range t.C {
		l.mu.Lock()
		now := time.Now()
		for ip, last := range l.lastSeen {
			if now.Sub(last) > l.idleTTL {
				delete(l.buckets, ip)
				delete(l.lastSeen, ip)
			}
		}
		l.mu.Unlock()
	}
}

// allow returns true if a token is available for ip; false otherwise.
// On success, one token is consumed.
func (l *Limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[ip] = b
	}
	// Refill since last call.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	l.lastSeen[ip] = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP extracts a best-effort client IP from r, honoring X-Forwarded-For.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Middleware returns an http middleware that enforces the rate limit.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if !l.allow(ip) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"Too Many Requests","code":"RATE_LIMITED"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
