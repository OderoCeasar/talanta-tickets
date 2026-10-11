package auth

import (
	"context"
	"sync/atomic"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// DefaultArgon2MaxConcurrent is how many argon2 computations run at once when
// ARGON2_MAX_CONCURRENT is not set. Each one holds 19 MiB and a CPU core for its whole run, and
// the API shares a small server with Postgres: without a cap, a burst of login attempts (which
// need no account, only an email-shaped string) would take the memory and every core.
const DefaultArgon2MaxConcurrent = 4

// hashBusyRetrySeconds is the Retry-After sent when a request gave up waiting for a slot.
const hashBusyRetrySeconds = 5

// passwordHasher is the only way the service hashes or verifies a password. It lets at most
// cap(slots) argon2 computations run at a time; the rest wait in line for as long as their
// request context lives.
type passwordHasher struct {
	slots chan struct{}
	// hash and verify are HashPassword and VerifyPassword, replaceable in tests.
	hash   func(password string) (string, error)
	verify func(password, encoded string) (bool, error)
	// verifies counts completed verifications, so a test can prove that a login for an
	// unknown email paid for one.
	verifies atomic.Uint64
}

func newPasswordHasher(maxConcurrent int) *passwordHasher {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultArgon2MaxConcurrent
	}
	return &passwordHasher{
		slots:  make(chan struct{}, maxConcurrent),
		hash:   HashPassword,
		verify: VerifyPassword,
	}
}

// acquire takes a slot or gives up when ctx ends. Giving up is a RATE_LIMITED answer: the
// caller did nothing wrong, the server is busy, and trying again shortly is the right move.
func (h *passwordHasher) acquire(ctx context.Context) error {
	select {
	case h.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		e := domain.RateLimited(hashBusyRetrySeconds)
		e.Message = "The server is busy. Try again in a few seconds."
		e.Err = ctx.Err()
		return e
	}
}

func (h *passwordHasher) release() { <-h.slots }

// Hash is HashPassword behind the cap.
func (h *passwordHasher) Hash(ctx context.Context, password string) (string, error) {
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	return h.hash(password)
}

// Verify is VerifyPassword behind the cap. A RATE_LIMITED error means no slot came free in
// time and nothing was checked.
func (h *passwordHasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	if err := h.acquire(ctx); err != nil {
		return false, err
	}
	defer h.release()
	ok, err := h.verify(password, encoded)
	h.verifies.Add(1)
	return ok, err
}

// isHashBusy reports whether err is the hasher giving up on a full queue.
func isHashBusy(err error) bool { return domain.CodeOf(err) == domain.CodeRateLimited }
