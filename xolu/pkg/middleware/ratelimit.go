// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package middleware

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ha1tch/xolu/pkg/config"
)

// RateLimiter implements a fixed-window rate limiter. This allows up to 2x
// burst at window boundaries; a sliding-window approach would prevent that
// but adds complexity. Acceptable for the current use case.
type RateLimiter struct {
	mu      sync.RWMutex
	windows map[string]*window
	rate    int           // Max requests per window
	window  time.Duration // Window duration
	byIP    bool
	byKey   bool
	cleanup time.Duration
	stopCh  chan struct{}
}

type window struct {
	count     int
	startTime time.Time
}

// NewRateLimiter creates a new rate limiter
func NewRateLimiter(cfg *config.Config) *RateLimiter {
	rl := &RateLimiter{
		windows: make(map[string]*window),
		rate:    cfg.RateLimitRate,
		window:  time.Duration(cfg.RateLimitWindow) * time.Second,
		byIP:    cfg.RateLimitByIP,
		byKey:   cfg.RateLimitByKey,
		cleanup: time.Minute * 5,
		stopCh:  make(chan struct{}),
	}

	// Start cleanup goroutine
	go rl.cleanupLoop()

	return rl
}

// cleanupLoop periodically removes expired windows
func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.cleanupExpired()
		case <-rl.stopCh:
			return
		}
	}
}

// cleanupExpired removes expired rate limit windows
func (rl *RateLimiter) cleanupExpired() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for key, w := range rl.windows {
		if now.Sub(w.startTime) > rl.window*2 {
			delete(rl.windows, key)
		}
	}
}

// Stop stops the rate limiter cleanup goroutine
func (rl *RateLimiter) Stop() {
	close(rl.stopCh)
}

// Allow checks if a request is allowed and increments the counter
func (rl *RateLimiter) Allow(key string) (bool, int, time.Time) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	w, exists := rl.windows[key]
	if !exists || now.Sub(w.startTime) > rl.window {
		// New window
		rl.windows[key] = &window{
			count:     1,
			startTime: now,
		}
		return true, rl.rate - 1, now.Add(rl.window)
	}

	// Existing window
	if w.count >= rl.rate {
		resetTime := w.startTime.Add(rl.window)
		return false, 0, resetTime
	}

	w.count++
	return true, rl.rate - w.count, w.startTime.Add(rl.window)
}

// RateLimitMiddleware creates a rate limiting middleware
func RateLimitMiddleware(cfg *config.Config, limiter *RateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.RateLimitEnabled {
				next.ServeHTTP(w, r)
				return
			}

			// Skip rate limiting for excluded paths
			for _, path := range cfg.AuthExcludePaths {
				if r.URL.Path == path {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Determine rate limit key
			key := getRateLimitKey(r, cfg)
			if key == "" {
				// Can't determine key, allow through
				next.ServeHTTP(w, r)
				return
			}

			allowed, remaining, resetTime := limiter.Allow(key)

			// Set rate limit headers
			w.Header().Set("X-RateLimit-Limit", strconv.Itoa(cfg.RateLimitRate))
			w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))

			if !allowed {
				w.Header().Set("Retry-After", strconv.FormatInt(int64(time.Until(resetTime).Seconds())+1, 10))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": map[string]interface{}{
						"code":    "OLU-RL001",
						"message": "Rate limit exceeded",
						"status":  http.StatusTooManyRequests,
					},
					"retry_after": int(time.Until(resetTime).Seconds()) + 1,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// getRateLimitKey determines the key for rate limiting
func getRateLimitKey(r *http.Request, cfg *config.Config) string {
	var parts []string

	// Add IP if enabled
	if cfg.RateLimitByIP {
		ip := getClientIP(r)
		if ip != "" {
			parts = append(parts, "ip:"+ip)
		}
	}

	// Add auth key/subject if enabled
	if cfg.RateLimitByKey {
		subject := GetSubject(r.Context())
		if subject != "" {
			parts = append(parts, "sub:"+subject)
		}
	}

	if len(parts) == 0 {
		// Fall back to IP
		return "ip:" + getClientIP(r)
	}

	// Combine parts
	key := ""
	for i, p := range parts {
		if i > 0 {
			key += "|"
		}
		key += p
	}
	return key
}

// getClientIP extracts the client IP from the request
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Use RemoteAddr
	addr := r.RemoteAddr
	// Strip port if present
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
