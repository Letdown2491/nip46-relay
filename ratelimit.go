package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// keyLimiter holds rate limit state for a single key with its own lock
type keyLimiter struct {
	mu       sync.Mutex
	requests []time.Time
	lastUsed atomic.Int64 // Unix timestamp for cleanup decisions
}

// RateLimiter implements a sliding window rate limiter with per-key locking
// Uses sync.Map to allow concurrent access to different keys without contention
type RateLimiter struct {
	keys   sync.Map // map[string]*keyLimiter
	limit  int
	window time.Duration
}

// NewRateLimiter creates a new rate limiter
// limit: max requests per window
// window: time window duration
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		limit:  limit,
		window: window,
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl
}

// Allow checks if a request from the given key should be allowed
// Uses per-key locking so different keys don't block each other
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()
	windowStart := now.Add(-rl.window)

	// Get or create limiter for this key
	val, _ := rl.keys.LoadOrStore(key, &keyLimiter{})
	kl := val.(*keyLimiter)

	// Update last used timestamp (atomic, no lock needed)
	kl.lastUsed.Store(now.Unix())

	// Lock only this key's limiter
	kl.mu.Lock()
	defer kl.mu.Unlock()

	// Filter to only requests within the window
	// Reuse slice capacity to reduce allocations
	validRequests := kl.requests[:0]
	for _, t := range kl.requests {
		if t.After(windowStart) {
			validRequests = append(validRequests, t)
		}
	}

	// Check if limit exceeded
	if len(validRequests) >= rl.limit {
		kl.requests = validRequests
		return false
	}

	// Add current request
	kl.requests = append(validRequests, now)

	return true
}

// cleanup periodically removes stale keys to prevent memory leaks
// Runs without blocking Allow() calls on active keys
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		staleThreshold := now.Add(-rl.window * 2).Unix()

		// Collect stale keys first (no locks held during iteration)
		var staleKeys []string
		rl.keys.Range(func(key, val any) bool {
			kl := val.(*keyLimiter)
			if kl.lastUsed.Load() < staleThreshold {
				staleKeys = append(staleKeys, key.(string))
			}
			return true
		})

		// Delete stale keys
		for _, key := range staleKeys {
			rl.keys.Delete(key)
		}
	}
}
