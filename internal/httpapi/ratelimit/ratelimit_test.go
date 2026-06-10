package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLimiterAllowsUpToBurst(t *testing.T) {
	l := New(1, 5, time.Minute)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "1.2.3.4:5000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("request %d: code = %d, want 200", i, rr.Code)
		}
	}

	// 6th request should be rate-limited.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request: code = %d, want 429", rr.Code)
	}
}

func TestLimiterIsPerIP(t *testing.T) {
	l := New(1, 1, time.Minute)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// IP A consumes its burst.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "1.1.1.1:1000"
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
	}

	// IP B should still be allowed.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "2.2.2.2:2000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("different IP: code = %d, want 200", rr.Code)
	}

	// IP A's 3rd request should be limited.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.1.1.1:1000"
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("same IP 3rd: code = %d, want 429", rr.Code)
	}
}

func TestLimiterRefills(t *testing.T) {
	l := New(10, 1, time.Minute) // 10 tokens/sec
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Consume the single token.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "3.3.3.3:3000"
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first: code = %d, want 200", rr.Code)
	}

	// Immediately: should be limited.
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("immediate: code = %d, want 429", rr.Code)
	}

	// After 200ms (refill of ~2 tokens at 10/s): should be allowed.
	time.Sleep(200 * time.Millisecond)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("after refill: code = %d, want 200", rr.Code)
	}
}

func TestLimiterHonorsXForwardedFor(t *testing.T) {
	l := New(1, 1, time.Minute)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Two requests from different X-Forwarded-For should not share a bucket.
	for i, ip := range []string{"10.0.0.1", "10.0.0.2"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", ip)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("xff request %d (%s): code = %d, want 200", i, ip, rr.Code)
		}
	}
}

func TestLimiterConcurrentSafe(t *testing.T) {
	l := New(1000, 100, time.Minute)
	handler := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				req := httptest.NewRequest("GET", "/", nil)
				req.RemoteAddr = "5.5.5.5:5000"
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)
			}
		}()
	}
	wg.Wait()
}
