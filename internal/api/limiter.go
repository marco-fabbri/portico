package api

import (
	"sync"
	"time"
)

// Limiter delays the answer to repeated authentication failures from one
// source address: nothing for the first three failures in ten minutes,
// then 1 s, 2 s, 4 s, capped at 8 s. It is shared by the API's Basic check
// and the UI's login form, so one password is braked once.
type Limiter struct {
	mu       sync.Mutex
	failures map[string]limitEntry
	now      func() time.Time
}

type limitEntry struct {
	count int
	last  time.Time
}

const (
	limiterFree   = 3
	limiterMax    = 8 * time.Second
	limiterIdle   = 10 * time.Minute
	limiterSource = "unknown"
)

// NewLimiter returns an empty limiter.
func NewLimiter() *Limiter {
	return &Limiter{failures: map[string]limitEntry{}, now: time.Now}
}

func (l *Limiter) key(source string) string {
	if source == "" {
		return limiterSource
	}
	return source
}

// Delay returns how long the next answer to source should wait.
func (l *Limiter) Delay(source string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.failures[l.key(source)]
	if !ok || l.now().Sub(e.last) > limiterIdle || e.count <= limiterFree {
		return 0
	}
	d := time.Second << uint(e.count-limiterFree-1)
	if d > limiterMax || d <= 0 {
		d = limiterMax
	}
	return d
}

// Failed records one failure for source.
func (l *Limiter) Failed(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.key(source)
	e := l.failures[k]
	if l.now().Sub(e.last) > limiterIdle {
		e.count = 0
	}
	e.count++
	e.last = l.now()
	l.failures[k] = e
}

// Reset forgets the failures of source after a success.
func (l *Limiter) Reset(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, l.key(source))
}

// Sweep drops sources idle for more than ten minutes.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, e := range l.failures {
		if now.Sub(e.last) > limiterIdle {
			delete(l.failures, k)
		}
	}
}

func (l *Limiter) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.failures)
}
