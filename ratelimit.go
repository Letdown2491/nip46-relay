package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// keyLimiter holds token-bucket state for a single key with its own lock.
type keyLimiter struct {
	mu       sync.Mutex
	tokens   float64
	last     time.Time
	lastUsed atomic.Int64 // Unix timestamp for cleanup decisions
}

// RateLimiter is a per-key token-bucket limiter. Each key refills at
// limit/window tokens per second up to a burst of `limit`, so checking a
// request is O(1) in time and memory (no per-request slice scan).
// sync.Map allows concurrent access to different keys without contention.
type RateLimiter struct {
	keys   sync.Map // map[string]*keyLimiter
	burst  float64
	refill float64 // tokens per second
	window time.Duration
}

// NewRateLimiter creates a new rate limiter.
// limit: max requests per window (also the burst size)
// window: time window duration
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		burst:  float64(limit),
		refill: float64(limit) / window.Seconds(),
		window: window,
	}

	// Start cleanup goroutine
	go rl.cleanup()

	return rl
}

// Allow checks if a request from the given key should be allowed.
// Uses per-key locking so different keys don't block each other.
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()

	// Avoid allocating a new limiter for keys that already exist (the hot path).
	val, ok := rl.keys.Load(key)
	if !ok {
		val, _ = rl.keys.LoadOrStore(key, &keyLimiter{tokens: rl.burst, last: now})
	}
	kl := val.(*keyLimiter)

	// Update last used timestamp (atomic, no lock needed)
	kl.lastUsed.Store(now.Unix())

	// Lock only this key's limiter
	kl.mu.Lock()
	defer kl.mu.Unlock()

	// Refill tokens based on elapsed time, capped at the burst size.
	if elapsed := now.Sub(kl.last).Seconds(); elapsed > 0 {
		kl.tokens += elapsed * rl.refill
		if kl.tokens > rl.burst {
			kl.tokens = rl.burst
		}
		kl.last = now
	}

	if kl.tokens < 1 {
		return false
	}
	kl.tokens--
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
