package identity

import (
	"sync"
	"time"
)

// limiter counts failures per key — an account, or a client address — within a window, and refuses a key that
// reached its limit until its oldest failure leaves the window. It lives in memory: a restart clears it.
type limiter struct {
	failures map[string][]time.Time
	window   time.Duration
	mu       sync.Mutex
}

func newLimiter(window time.Duration) *limiter {
	return &limiter{window: window, failures: map[string][]time.Time{}}
}

// wait returns how long key is refused for, having reached max failures; zero when it is not.
func (l *limiter) wait(key string, max int, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.prune(key, now)
	if len(recent) < max {
		return 0
	}
	return recent[len(recent)-max].Add(l.window).Sub(now)
}

// fail records a failure of key.
func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.prune(key, now), now)
	if len(l.failures) > 10000 {
		for k := range l.failures {
			l.prune(k, now)
		}
	}
}

// clear forgets key's failures, after it succeeds.
func (l *limiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// prune drops key's failures that left the window, and returns the rest. The caller holds the lock.
func (l *limiter) prune(key string, now time.Time) []time.Time {
	times := l.failures[key]
	i := 0
	for i < len(times) && now.Sub(times[i]) >= l.window {
		i++
	}
	times = times[i:]
	if len(times) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = times
	return times
}
