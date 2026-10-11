package auth

import (
	"sync"
	"time"
)

// LoginLimiter throttles failed logins per client key plus a global ceiling
// that blunts distributed guessing. State is in memory: it resets on restart,
// which is acceptable for a single-admin app.
type LoginLimiter struct {
	mu        sync.Mutex
	fails     map[string][]time.Time
	perKey    int
	global    int
	window    time.Duration
	Now       func() time.Time
	globalKey string
}

// NewLoginLimiter allows perKey failures per key (and global overall) per window.
func NewLoginLimiter(perKey, global int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{fails: map[string][]time.Time{}, perKey: perKey, global: global, window: window,
		Now: time.Now, globalKey: "\x00global"}
}

func (l *LoginLimiter) prune(key string, now time.Time) []time.Time {
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

// Allow reports whether a login attempt may proceed, or how long to wait.
func (l *LoginLimiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	for _, c := range []struct {
		key   string
		limit int
	}{{key, l.perKey}, {l.globalKey, l.global}} {
		if f := l.prune(c.key, now); len(f) >= c.limit {
			return false, f[0].Add(l.window).Sub(now)
		}
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	l.fails[key] = append(l.fails[key], now)
	l.fails[l.globalKey] = append(l.fails[l.globalKey], now)
	if len(l.fails) > 10000 { // bound memory under a flood of distinct keys
		for k := range l.fails {
			l.prune(k, now)
		}
	}
}

// Succeed clears the per-key counter after a successful login.
func (l *LoginLimiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
