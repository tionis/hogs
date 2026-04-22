package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type RateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *RateLimiter) Cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for ip, timestamps := range rl.requests {
		var valid []time.Time
		for _, t := range timestamps {
			if now.Sub(t) < rl.window {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = valid
		}
	}
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)

		rl.mu.Lock()
		now := time.Now()
		requests := rl.requests[ip]
		var valid []time.Time
		for _, t := range requests {
			if now.Sub(t) < rl.window {
				valid = append(valid, t)
			}
		}

		if len(valid) >= rl.limit {
			rl.requests[ip] = valid
			rl.mu.Unlock()
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		valid = append(valid, now)
		rl.requests[ip] = valid
		rl.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

func getClientIP(r *http.Request) string {
	ip := r.RemoteAddr
	// Strip port if present
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	// Only trust X-Forwarded-For from localhost (indicating a local reverse proxy)
	// In production, configure a list of trusted proxy IPs
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") || strings.HasPrefix(r.RemoteAddr, "[::1]:") {
			// Take the leftmost (original client) IP
			parts := strings.Split(forwarded, ",")
			if len(parts) > 0 {
				candidate := strings.TrimSpace(parts[0])
				if net.ParseIP(candidate) != nil {
					ip = candidate
				}
			}
		}
	}
	return ip
}
