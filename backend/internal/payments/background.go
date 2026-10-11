package payments

import (
	"sync"
)

// Background runs work that must not hold up an answer: finishing a payment after the
// webhook has already said 200, and the "you're going" SMS, which can block for ten seconds.
// It is tied to the process's shutdown: Wait returns when everything started has finished.
//
// At most Limit pieces of work run at once. Work offered beyond that is refused, not queued:
// the callers are a public endpoint and a notification, and both have a safety net
// (reconciliation finishes any payment a dropped callback would have; the fan can open the
// tickets in the app without the SMS).
type Background struct {
	wg  sync.WaitGroup
	sem chan struct{}
}

// NewBackground makes a runner that allows limit pieces of work at once (zero means 256).
func NewBackground(limit int) *Background {
	if limit <= 0 {
		limit = 256
	}
	return &Background{sem: make(chan struct{}, limit)}
}

// Go runs fn on its own goroutine and reports whether it was started.
func (b *Background) Go(fn func()) bool {
	select {
	case b.sem <- struct{}{}:
	default:
		return false
	}
	b.wg.Add(1)
	go func() {
		defer func() {
			<-b.sem
			b.wg.Done()
		}()
		fn()
	}()
	return true
}

// Wait blocks until everything started with Go has returned. Call it at shutdown, and in
// tests instead of sleeping.
func (b *Background) Wait() { b.wg.Wait() }
