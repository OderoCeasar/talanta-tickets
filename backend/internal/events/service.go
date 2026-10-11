// Package events holds the rules of events and their inventory: event details, categories,
// painting, readiness, publish, cancel, the seat map reads and the sales figures. SQL lives
// in internal/store; state rules in internal/domain.
package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// Policy is every number in the seat map and publish rules, in one place.
type Policy struct {
	// PublishTimeout is the statement timeout of the publish transaction, which copies one
	// row per seat and outlasts the pool's ordinary limit.
	PublishTimeout time.Duration
	// CancelTimeout is the statement timeout of the cancel transaction, which on a well-sold
	// event rewrites every ticket, booking and sold seat's refund in one go.
	CancelTimeout time.Duration
	// LockWait is how long an edit, a publish or a cancel waits for the event's lock before
	// it answers EVENT_BUSY. A publish of the full venue holds that lock for most of a
	// minute; waiting it out would run into the statement timeout and look like a fault.
	// It has to cover the holds already in flight when a writer arrives at the gate (about
	// a second in the worst storm measured), with room to spare.
	LockWait time.Duration
	// CountsTTL is how long the per-event availability counts are reused. Level 1 and
	// level 2 of the seat map, the event list and the sales dashboard all read them, and
	// each fresh read scans about 60,000 index entries.
	CountsTTL time.Duration
	// ListCountsTTL is how long the event LIST reuses the counts. A list shows one line per
	// event and every line costs one full aggregate when its counts are stale, so the list
	// accepts older figures than a page about a single event does.
	ListCountsTTL time.Duration
	// PollInterval is how often a client should poll an open block.
	PollInterval time.Duration
	// MarkerOverlap is how far before a marker the change poll looks. It must be longer than
	// the longest time a transaction can sit between writing a seat and committing, which
	// the pool bounds with its statement and idle-in-transaction timeouts (15 s each).
	MarkerOverlap time.Duration
	// MarkerMaxAge is the oldest marker answered with a delta. An older one gets the full
	// block, which is cheaper than a long list of changes and bounds the index range read.
	MarkerMaxAge time.Duration
	// MaxSeriesPoints caps the sales time series.
	MaxSeriesPoints int
}

// DefaultPolicy is what production runs.
func DefaultPolicy() Policy {
	return Policy{
		PublishTimeout:  55 * time.Second,
		CancelTimeout:   55 * time.Second,
		LockWait:        5 * time.Second,
		CountsTTL:       3 * time.Second,
		ListCountsTTL:   20 * time.Second,
		PollInterval:    5 * time.Second,
		MarkerOverlap:   20 * time.Second,
		MarkerMaxAge:    5 * time.Minute,
		MaxSeriesPoints: 2000,
	}
}

// Config is what the service is built from.
type Config struct {
	DB     DB
	Logger *slog.Logger
	// Policy zero values are replaced by DefaultPolicy's.
	Policy Policy
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service implements the event rules. It is safe for concurrent use.
type Service struct {
	db     DB
	q      *store.Queries
	log    *slog.Logger
	policy Policy
	now    func() time.Time
	counts *countsCache
}

// NewService checks its dependencies and builds the service.
func NewService(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("events: DB is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	def := DefaultPolicy()
	p := cfg.Policy
	if p.PublishTimeout <= 0 {
		p.PublishTimeout = def.PublishTimeout
	}
	if p.CancelTimeout <= 0 {
		p.CancelTimeout = def.CancelTimeout
	}
	if p.LockWait <= 0 {
		p.LockWait = def.LockWait
	}
	if p.CountsTTL <= 0 {
		p.CountsTTL = def.CountsTTL
	}
	if p.ListCountsTTL <= 0 {
		p.ListCountsTTL = def.ListCountsTTL
	}
	// The list may show older counts than a single event's page, never fresher ones.
	p.ListCountsTTL = max(p.ListCountsTTL, p.CountsTTL)
	if p.PollInterval <= 0 {
		p.PollInterval = def.PollInterval
	}
	if p.MarkerOverlap <= 0 {
		p.MarkerOverlap = def.MarkerOverlap
	}
	if p.MarkerMaxAge <= 0 {
		p.MarkerMaxAge = def.MarkerMaxAge
	}
	if p.MaxSeriesPoints <= 0 {
		p.MaxSeriesPoints = def.MaxSeriesPoints
	}
	return &Service{
		db: cfg.DB, q: store.New(cfg.DB), log: cfg.Logger, policy: p, now: cfg.Now,
		counts: newCountsCache(p.CountsTTL, cfg.Now),
	}, nil
}

// Event is an event as every reader sees it: its row, the status it really has now, and the
// availability figures.
type Event struct {
	Row       store.Event
	VenueName string
	// Status is the effective status (domain.EffectiveEventStatus), not the stored one.
	Status domain.EventStatus
	// PriceFromCents is the cheapest seat available now; nil when none is.
	PriceFromCents *int64
	SeatsAvailable int
	SeatsTotal     int
}

// lockEvent takes the event's gate and row lock (see the LockEvent query for why) and returns
// the row. It waits at most Policy.LockWait: when a publish, a cancel or another edit keeps
// the lock longer, the answer is EVENT_BUSY and nothing has changed.
func (s *Service) lockEvent(ctx context.Context, q *store.Queries, eventID int64) (store.Event, error) {
	ev, err := q.LockEventWithin(ctx, eventID, s.policy.LockWait)
	if errors.Is(err, store.ErrNoRows) {
		return ev, domain.ErrNotFound
	}
	if store.IsLockTimeout(err) {
		busy := domain.EventBusy("")
		busy.Err = err
		return ev, busy
	}
	if err != nil {
		return ev, fmt.Errorf("lock event %d: %w", eventID, err)
	}
	return ev, nil
}

// view turns an event row into what readers see, reading the availability counts (from the
// short-lived cache, at most countsAge old) for anything that has inventory.
func (s *Service) view(ctx context.Context, row store.Event, venueName string, countsAge time.Duration) (Event, error) {
	out := Event{
		Row: row, VenueName: venueName,
		Status: domain.EffectiveEventStatus(domain.EventStatus(row.Status), row.SalesOpenAt, row.SalesCloseAt, s.now()),
	}
	if !domain.EventStatus(row.Status).HasInventory() {
		return out, nil
	}
	inv, err := s.inventoryWithin(ctx, row.ID, countsAge)
	if err != nil {
		return out, err
	}
	total := inv.Total()
	out.SeatsAvailable, out.SeatsTotal = total.Available, total.Total()
	if total.Available > 0 {
		prices, err := s.prices(ctx, row.ID)
		if err != nil {
			return out, err
		}
		out.PriceFromCents = inv.PriceFrom(prices, nil)
	}
	return out, nil
}

// prices maps category id to its price now.
func (s *Service) prices(ctx context.Context, eventID int64) (map[int64]int64, error) {
	cats, err := s.q.ListEventCategoriesCheapestFirst(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("list categories of event %d: %w", eventID, err)
	}
	out := make(map[int64]int64, len(cats))
	for _, c := range cats {
		out[c.ID] = c.PriceCents
	}
	return out, nil
}

// getEvent reads one event with its venue's name.
func (s *Service) getEvent(ctx context.Context, eventID int64) (store.GetEventWithVenueRow, error) {
	row, err := s.q.GetEventWithVenue(ctx, eventID)
	if errors.Is(err, store.ErrNoRows) {
		return row, domain.ErrNotFound
	}
	if err != nil {
		return row, fmt.Errorf("get event %d: %w", eventID, err)
	}
	return row, nil
}

// publicEvent reads an event a fan may see: a draft is reported as not found.
func (s *Service) publicEvent(ctx context.Context, eventID int64) (store.GetEventWithVenueRow, error) {
	row, err := s.getEvent(ctx, eventID)
	if err != nil {
		return row, err
	}
	if !domain.EventStatus(row.Event.Status).VisibleToPublic() {
		return row, domain.ErrNotFound
	}
	return row, nil
}
