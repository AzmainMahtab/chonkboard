// Package ratelimit is an in-memory token bucket, keyed by whatever the caller
// wants to limit on.
//
// In-memory is the right scope here: there is one process, and the thing being
// protected is a login form rather than a metered API. A shared limiter would mean
// Redis, which is most of the reason this application has no second container.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter hands out tokens per key, refilling continuously.
//
// A token bucket rather than a fixed window because a fixed window lets an
// attacker send the whole allowance twice across a boundary, and because it lets a
// person who mistyped their password once recover after a second rather than
// waiting out a window.
type Limiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	capacity float64
	// refillPerSecond is how fast a bucket returns to full.
	refillPerSecond float64
	now             func() time.Time
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// New returns a limiter allowing burst attempts, refilled at burst per window.
//
// So New(5, time.Minute) permits five attempts immediately and then roughly one
// every twelve seconds.
func New(burst int, window time.Duration) *Limiter {
	if burst < 1 {
		burst = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Limiter{
		buckets:         make(map[string]*bucket),
		capacity:        float64(burst),
		refillPerSecond: float64(burst) / window.Seconds(),
		now:             time.Now,
	}
}

// WithClock replaces the clock, so a test does not have to sleep.
func (l *Limiter) WithClock(now func() time.Time) *Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.now = now
	return l
}

// Allow takes a token for key, reporting whether one was available.
//
// An unknown key starts full, so a first attempt is never refused.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.capacity, lastSeen: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.lastSeen).Seconds()
		if elapsed > 0 {
			b.tokens = min(l.capacity, b.tokens+elapsed*l.refillPerSecond)
		}
		b.lastSeen = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Reset empties the record for a key. A successful login calls this, so a person
// who eventually remembers their password is not still rate limited afterwards.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// Sweep drops buckets untouched for idleFor, and reports how many went.
//
// Without it the map grows once per distinct key seen -- which, keyed on IP and
// email, is an unbounded memory cost an attacker controls.
func (l *Limiter) Sweep(idleFor time.Duration) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-idleFor)
	removed := 0
	for key, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, key)
			removed++
		}
	}
	return removed
}

// Len reports how many keys are tracked. For tests and for a debug log line.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
