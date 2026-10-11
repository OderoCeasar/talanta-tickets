package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrLockTimeout is returned by the *Within functions below when a lock was not free within
// the time allowed. The transaction is aborted at that point; the caller must roll back.
var ErrLockTimeout = errors.New("store: lock not available in time")

// IsLockTimeout reports whether err is Postgres's "lock not available" (SQLSTATE 55P03),
// which is what a statement gets when it waits longer than lock_timeout for a lock.
func IsLockTimeout(err error) bool {
	if errors.Is(err, ErrLockTimeout) {
		return true
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// SetLocalLockTimeout bounds how long each later statement of the current transaction may
// wait for a lock (row, table or advisory); it reverts at commit or rollback. A statement
// that waits longer fails with SQLSTATE 55P03 (IsLockTimeout) and aborts the transaction.
// Zero or less puts the session's own setting back, which for our pools is "wait for ever",
// bounded only by statement_timeout.
//
// Why it exists: publishing a full venue holds the event's lock for most of a minute. A
// request queued behind it used to wait until the 15 s statement timeout and then looked
// like a server fault (500). With a short lock wait it gets a clear "busy, try again".
func SetLocalLockTimeout(ctx context.Context, tx DBTX, d time.Duration) error {
	if d <= 0 {
		if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout TO DEFAULT"); err != nil {
			return fmt.Errorf("reset local lock_timeout: %w", err)
		}
		return nil
	}
	// set_config(…, true) is SET LOCAL with a bound value: SET itself takes no parameters.
	// Postgres reads a bare number as milliseconds; below 1 ms would mean "no limit".
	ms := max(d.Milliseconds(), 1)
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", strconv.FormatInt(ms, 10)); err != nil {
		return fmt.Errorf("set local lock_timeout: %w", err)
	}
	return nil
}

// within runs one locking statement with lock_timeout set to wait and lifts the limit again
// afterwards, so the rest of the transaction waits for row locks as it always did. A lock
// that was not free in time comes back as ErrLockTimeout. wait <= 0 means no limit.
func within(ctx context.Context, db DBTX, wait time.Duration, lock func() error) error {
	if wait <= 0 {
		return lock()
	}
	if err := SetLocalLockTimeout(ctx, db, wait); err != nil {
		return err
	}
	if err := lock(); err != nil {
		if IsLockTimeout(err) {
			return fmt.Errorf("%w: %w", ErrLockTimeout, err)
		}
		return err
	}
	return SetLocalLockTimeout(ctx, db, 0)
}

// LockEventWithin is the LockEvent query (the event's gate, exclusive, then its row FOR
// UPDATE; see queries/events.sql for what it locks and why) with a bounded wait. It returns
// ErrLockTimeout when a publish, a cancel or another edit of the same event held the lock
// for longer than wait, and ErrNoRows when there is no such event. Must run in a transaction.
func (q *Queries) LockEventWithin(ctx context.Context, eventID int64, wait time.Duration) (Event, error) {
	var ev Event
	err := within(ctx, q.db, wait, func() (err error) {
		ev, err = q.LockEvent(ctx, eventID)
		return err
	})
	return ev, err
}

// LockVenueForLayoutChangeWithin is the LockVenueForLayoutChange query (the venue row FOR NO
// KEY UPDATE) with a bounded wait: ErrLockTimeout when a publish at the venue held the row
// FOR SHARE for longer than wait. Must run in a transaction.
func (q *Queries) LockVenueForLayoutChangeWithin(ctx context.Context, venueID int64, wait time.Duration) error {
	return within(ctx, q.db, wait, func() error {
		_, err := q.LockVenueForLayoutChange(ctx, venueID)
		return err
	})
}

// LockEventForHoldWithin is LockEventForHold (the event's gate and row, both shared) with a
// bounded wait: ErrLockTimeout when a writer of the event (publish, cancel, an edit) held
// the gate for longer than wait. Must run in a transaction.
func LockEventForHoldWithin(ctx context.Context, db DBTX, eventID int64, wait time.Duration) (EventForHold, error) {
	var ev EventForHold
	err := within(ctx, db, wait, func() (err error) {
		ev, err = LockEventForHold(ctx, db, eventID)
		return err
	})
	return ev, err
}

// LockEventOfHoldWithin is LockEventOfHold with a bounded wait, as LockEventForHoldWithin.
func LockEventOfHoldWithin(ctx context.Context, db DBTX, holdID int64, wait time.Duration) (int64, error) {
	var id int64
	err := within(ctx, db, wait, func() (err error) {
		id, err = LockEventOfHold(ctx, db, holdID)
		return err
	})
	return id, err
}
