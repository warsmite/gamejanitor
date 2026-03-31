package sftp

import (
	"fmt"
	"sync"
	"time"
)

// authRateLimiter tracks failed SSH login attempts per IP and rejects
// connections that exceed the threshold. Entries decay over time so
// legitimate users recover after a cooldown period.
type authRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord

	// MaxFailures is the number of failures before an IP is blocked.
	maxFailures int
	// Window is how long failure counts are retained before decaying.
	window time.Duration
}

type attemptRecord struct {
	failures int
	lastFail time.Time
}

func newAuthRateLimiter(maxFailures int, window time.Duration) *authRateLimiter {
	rl := &authRateLimiter{
		attempts:    make(map[string]*attemptRecord),
		maxFailures: maxFailures,
		window:      window,
	}
	return rl
}

// check returns an error if the IP has exceeded the failure threshold.
// Must be called before attempting authentication.
func (rl *authRateLimiter) check(ip string) error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rec, ok := rl.attempts[ip]
	if !ok {
		return nil
	}

	// Reset if the window has elapsed since the last failure
	if time.Since(rec.lastFail) > rl.window {
		delete(rl.attempts, ip)
		return nil
	}

	if rec.failures >= rl.maxFailures {
		remaining := rl.window - time.Since(rec.lastFail)
		return fmt.Errorf("too many failed login attempts, try again in %s", remaining.Truncate(time.Second))
	}
	return nil
}

// recordFailure increments the failure count for an IP.
func (rl *authRateLimiter) recordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rec, ok := rl.attempts[ip]
	if !ok {
		rl.attempts[ip] = &attemptRecord{failures: 1, lastFail: time.Now()}
		return
	}

	// Reset if the window elapsed
	if time.Since(rec.lastFail) > rl.window {
		rec.failures = 1
		rec.lastFail = time.Now()
		return
	}

	rec.failures++
	rec.lastFail = time.Now()
}

// recordSuccess clears the failure count for an IP.
func (rl *authRateLimiter) recordSuccess(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.attempts, ip)
}

// cleanup removes stale entries older than the window.
func (rl *authRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-rl.window)
	for ip, rec := range rl.attempts {
		if rec.lastFail.Before(cutoff) {
			delete(rl.attempts, ip)
		}
	}
}
