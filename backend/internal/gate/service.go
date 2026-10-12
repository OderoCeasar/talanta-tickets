// Package gate is gate mode (M10): the events a gate device can scan for, the manifest a
// device checks tickets against while it is offline, and the upload of the scans it made.
//
// What it promises and what it does not is written for the people who build and run the
// scanner in docs/API_NOTES.md ("Gate mode") and docs/TICKET_FORMAT.md (section 4).
package gate

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// DB is what the service needs from the pool: run queries and open transactions, including
// a read-only snapshot for the manifest.
type DB interface {
	store.DBTX
	store.TxBeginner
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Policy is the gate's fixed numbers. Zero values mean the defaults.
type Policy struct {
	// EventsSince is how long after kickoff an event stays on the gate's list.
	EventsSince time.Duration
	// MaxEvents caps the list, which is not paged.
	MaxEvents int

	// VersionOverlap is how far before a manifest version the change poll looks. A ticket's
	// updated_at is stamped when its row is written, not when the transaction commits, so a
	// transaction can commit a stamp older than a version already handed out. The overlap
	// must be longer than any transaction that writes tickets can stay open after the
	// write: the API's statement and idle-in-transaction limits are 15 s each, and an
	// event cancel, the longest, is cut off at 55 s.
	VersionOverlap time.Duration
	// VersionMaxAge is the oldest version answered with changes only. An older one gets the
	// full manifest.
	VersionMaxAge time.Duration

	// EventLockWait bounds the wait for an event an organiser has locked (EVENT_BUSY).
	EventLockWait time.Duration
	// MaxScanAge is how far in the past a scan's device time is believed. Older than this,
	// or in the future, or before the ticket existed, and the server's receive time is
	// stored instead.
	MaxScanAge time.Duration
	// MaxBatch is the most scans one upload may carry.
	MaxBatch int
}

// DefaultPolicy is what production runs.
func DefaultPolicy() Policy {
	return Policy{
		EventsSince:    24 * time.Hour,
		MaxEvents:      50,
		VersionOverlap: 2 * time.Minute,
		VersionMaxAge:  7 * 24 * time.Hour,
		EventLockWait:  3 * time.Second,
		MaxScanAge:     7 * 24 * time.Hour,
		MaxBatch:       domain.MaxScansPerBatch,
	}
}

func (p Policy) withDefaults() Policy {
	def := DefaultPolicy()
	setDuration := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	setDuration(&p.EventsSince, def.EventsSince)
	setDuration(&p.VersionOverlap, def.VersionOverlap)
	setDuration(&p.VersionMaxAge, def.VersionMaxAge)
	setDuration(&p.EventLockWait, def.EventLockWait)
	setDuration(&p.MaxScanAge, def.MaxScanAge)
	if p.MaxEvents <= 0 {
		p.MaxEvents = def.MaxEvents
	}
	if p.MaxBatch <= 0 || p.MaxBatch > def.MaxBatch {
		p.MaxBatch = def.MaxBatch
	}
	return p
}

// Config is what the service is built from.
type Config struct {
	DB     DB
	Logger *slog.Logger
	// Verifier holds the PUBLIC ticket keys: it checks uploaded tokens and its key list is
	// what the manifest sends. The service is never given the signer.
	Verifier *ticketing.Verifier
	Policy   Policy
}

// Service serves the gate endpoints. It is safe for concurrent use.
type Service struct {
	db       DB
	q        *store.Queries
	log      *slog.Logger
	verifier *ticketing.Verifier
	policy   Policy
}

func NewService(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, errors.New("gate: DB is required")
	}
	if cfg.Verifier == nil || len(cfg.Verifier.PublicKeys()) == 0 {
		return nil, errors.New("gate: Verifier with at least one public key is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Service{
		db: cfg.DB, q: store.New(cfg.DB), log: cfg.Logger, verifier: cfg.Verifier, policy: cfg.Policy.withDefaults(),
	}, nil
}

// Policy returns the numbers the service runs with.
func (s *Service) Policy() Policy { return s.policy }
