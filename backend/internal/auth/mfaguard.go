package auth

import (
	"sync"
	"time"
)

// mfaGuard holds the small amount of state the TOTP step of a staff sign-in needs:
//
//   - how many codes each MFA token has tried, and whether it has already signed someone in
//     (an MFA token is single-use on success and dies after a few wrong codes);
//   - the last TOTP time step each user signed in with, so a code that was just used —
//     overheard, or read off a screen — cannot be used again while it is still inside its
//     ±1 step window.
//
// It is in memory, per process: with more than one API instance, or across a restart, a
// replay inside the 90-second window would get through. The password step still stands in
// front of it. Moving it to the database needs a column on users.
type mfaGuard struct {
	mu        sync.Mutex
	tokens    map[string]*mfaTokenState
	lastStep  map[int64]int64
	nextSweep time.Time
}

type mfaTokenState struct {
	attempts int
	used     bool
	expires  time.Time
}

func newMFAGuard() *mfaGuard {
	return &mfaGuard{tokens: make(map[string]*mfaTokenState), lastStep: make(map[int64]int64)}
}

// attempt records one try with the token and reports whether the token may still be used.
func (g *mfaGuard) attempt(id string, expires, now time.Time, max int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweep(now)

	st, ok := g.tokens[id]
	if !ok {
		st = &mfaTokenState{expires: expires}
		g.tokens[id] = st
	}
	if st.used || st.attempts >= max {
		return false
	}
	st.attempts++
	return true
}

// finish claims the user's TOTP step and burns the token, as one decision. It returns false
// when the step was already used (a replayed code); the token then stays alive so the real
// user can wait for the next code.
func (g *mfaGuard) finish(id string, userID, step int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.claimStepLocked(userID, step) {
		return false
	}
	if st, ok := g.tokens[id]; ok {
		st.used = true
	}
	return true
}

// claimStep is the replay check alone, for turning TOTP on.
func (g *mfaGuard) claimStep(userID, step int64) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.claimStepLocked(userID, step)
}

func (g *mfaGuard) claimStepLocked(userID, step int64) bool {
	if last, ok := g.lastStep[userID]; ok && step <= last {
		return false
	}
	g.lastStep[userID] = step
	return true
}

// forget drops a user's last step when their secret is deleted; a new secret starts clean.
func (g *mfaGuard) forget(userID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.lastStep, userID)
}

func (g *mfaGuard) sweep(now time.Time) {
	if now.Before(g.nextSweep) {
		return
	}
	for id, st := range g.tokens {
		if !now.Before(st.expires) {
			delete(g.tokens, id)
		}
	}
	g.nextSweep = now.Add(time.Minute)
}
