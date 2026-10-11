// Package ratelimit counts events per key in fixed windows, in memory.
//
// It is per process: two API instances each keep their own counts, and a restart forgets
// them. That is acceptable for one small server; a shared store would go behind the same
// interface.
package ratelimit

import (
	"sync"
	"time"
)

// Rule allows Limit events per Window for one key.
type Rule struct {
	Limit  int
	Window time.Duration
}

// Decision is the answer for one event.
type Decision struct {
	Allowed bool
	// RetryAfter is how long until the window ends. Zero when Allowed.
	RetryAfter time.Duration
}

// Limiter is what handlers and services depend on, so tests can swap the implementation.
type Limiter interface {
	// Take records one event against key and reports whether it is within rule. A refused
	// event is not counted, so hammering a closed window does not keep it closed.
	Take(key string, rule Rule) Decision
	// Reset forgets key, for example after a successful sign-in.
	Reset(key string)
}

// Clock lets tests move time.
type Clock func() time.Time

type window struct {
	count int
	ends  time.Time
}

// Memory is a fixed-window limiter. The zero value is not usable; call NewMemory.
type Memory struct {
	now Clock

	mu        sync.Mutex
	windows   map[string]*window
	nextSweep time.Time
}

// sweepEvery bounds memory: finished windows are dropped at most this often, on the next Take.
const sweepEvery = time.Minute

// NewMemory returns an empty limiter. A nil clock means time.Now.
func NewMemory(clock Clock) *Memory {
	if clock == nil {
		clock = time.Now
	}
	return &Memory{now: clock, windows: make(map[string]*window)}
}

func (m *Memory) Take(key string, rule Rule) Decision {
	if rule.Limit <= 0 || rule.Window <= 0 {
		return Decision{Allowed: true}
	}
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)

	w, ok := m.windows[key]
	if !ok || !now.Before(w.ends) {
		m.windows[key] = &window{count: 1, ends: now.Add(rule.Window)}
		return Decision{Allowed: true}
	}
	if w.count >= rule.Limit {
		return Decision{RetryAfter: w.ends.Sub(now)}
	}
	w.count++
	return Decision{Allowed: true}
}

func (m *Memory) Reset(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.windows, key)
}

// Len is the number of keys held, for tests of the sweep.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.windows)
}

func (m *Memory) sweep(now time.Time) {
	if now.Before(m.nextSweep) {
		return
	}
	for k, w := range m.windows {
		if !now.Before(w.ends) {
			delete(m.windows, k)
		}
	}
	m.nextSweep = now.Add(sweepEvery)
}

// RetryAfterSeconds rounds a wait up to whole seconds, never below one, which is what the
// Retry-After header needs.
func RetryAfterSeconds(d time.Duration) int {
	s := int((d + time.Second - 1) / time.Second)
	if s < 1 {
		return 1
	}
	return s
}
