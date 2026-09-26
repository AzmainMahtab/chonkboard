package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clock is a hand-wound clock, so these tests never sleep.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock {
	return &clock{at: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func TestBurstIsAllowedThenRefused(t *testing.T) {
	c := newClock()
	l := New(5, time.Minute).WithClock(c.now)

	for i := range 5 {
		assert.True(t, l.Allow("ip:1"), "attempt %d should be allowed", i+1)
	}
	assert.False(t, l.Allow("ip:1"), "the sixth attempt exhausts the bucket")
}

func TestAFirstAttemptIsNeverRefused(t *testing.T) {
	// An unknown key starts full, so a person who has never signed in is not
	// throttled by somebody else's behaviour.
	l := New(1, time.Hour)
	assert.True(t, l.Allow("ip:brand-new"))
	assert.True(t, l.Allow("ip:also-new"))
}

func TestKeysAreIndependent(t *testing.T) {
	// The reason both IP and email are limited: neither may exhaust the other.
	l := New(2, time.Minute)

	require.True(t, l.Allow("ip:1"))
	require.True(t, l.Allow("ip:1"))
	assert.False(t, l.Allow("ip:1"))

	assert.True(t, l.Allow("ip:2"), "a different address is unaffected")
	assert.True(t, l.Allow("email:someone@example.com"))
}

func TestBucketRefillsContinuously(t *testing.T) {
	// A token bucket rather than a fixed window, so somebody who mistyped once
	// recovers in seconds instead of waiting out a window.
	c := newClock()
	l := New(5, time.Minute).WithClock(c.now) // one token per 12s

	for range 5 {
		require.True(t, l.Allow("ip:1"))
	}
	require.False(t, l.Allow("ip:1"))

	c.advance(11 * time.Second)
	assert.False(t, l.Allow("ip:1"), "not quite a whole token yet")

	c.advance(2 * time.Second)
	assert.True(t, l.Allow("ip:1"), "one token has refilled")
	assert.False(t, l.Allow("ip:1"), "and only one")
}

func TestRefillIsCappedAtTheBurst(t *testing.T) {
	// Idling for a week must not bank a week of attempts.
	c := newClock()
	l := New(3, time.Minute).WithClock(c.now)

	require.True(t, l.Allow("ip:1"))
	c.advance(7 * 24 * time.Hour)

	for i := range 3 {
		assert.True(t, l.Allow("ip:1"), "attempt %d", i+1)
	}
	assert.False(t, l.Allow("ip:1"), "the bucket refilled to its capacity and no further")
}

func TestResetClearsAKey(t *testing.T) {
	// A successful sign-in calls this, so somebody who eventually remembers their
	// password is not still limited afterwards.
	l := New(2, time.Minute)
	require.True(t, l.Allow("email:a@b.co"))
	require.True(t, l.Allow("email:a@b.co"))
	require.False(t, l.Allow("email:a@b.co"))

	l.Reset("email:a@b.co")

	assert.True(t, l.Allow("email:a@b.co"))
}

func TestSweepDropsIdleBuckets(t *testing.T) {
	// Without this the map grows once per distinct key seen, which -- keyed on IP
	// and email -- is an unbounded memory cost an attacker controls.
	c := newClock()
	l := New(5, time.Minute).WithClock(c.now)

	require.True(t, l.Allow("ip:old"))
	c.advance(20 * time.Minute)
	require.True(t, l.Allow("ip:fresh"))
	require.Equal(t, 2, l.Len())

	dropped := l.Sweep(15 * time.Minute)

	assert.Equal(t, 1, dropped)
	assert.Equal(t, 1, l.Len())
	// Sweeping an exhausted bucket is not a way to reset it, because a swept key
	// starts full again -- which is fine only because it was idle long enough to
	// have refilled anyway.
	assert.True(t, l.Allow("ip:fresh"))
}

func TestSweepKeepsActiveBuckets(t *testing.T) {
	c := newClock()
	l := New(5, time.Minute).WithClock(c.now)

	require.True(t, l.Allow("ip:busy"))
	c.advance(time.Minute)

	assert.Zero(t, l.Sweep(15*time.Minute))
	assert.Equal(t, 1, l.Len())
}

func TestZeroAndNegativeConstructionArgumentsAreClamped(t *testing.T) {
	// A misconfiguration must not produce a limiter that refuses everything or
	// divides by zero.
	l := New(0, 0)
	assert.True(t, l.Allow("k"), "burst is clamped to at least one")
	assert.False(t, l.Allow("k"))

	l = New(-3, -time.Second)
	assert.True(t, l.Allow("k"))
}

func TestConcurrentUseIsSafe(t *testing.T) {
	// Run with -race: every login request hits this from a different goroutine.
	l := New(1000, time.Minute)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				l.Allow("ip:shared")
				l.Allow("email:" + string(rune('a'+i%26)))
				l.Len()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			l.Sweep(time.Hour)
		}
	}()
	wg.Wait()
}
