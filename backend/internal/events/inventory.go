package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// cell is one (block, category) pair. Category 0 stands for "no category".
type cell struct {
	block, category int64
}

// Inventory is an event's seat counts per block and category, with lapsed holds already
// counted as available. It is read-only once built, so one copy is shared by every request
// that hits the cache.
type Inventory struct {
	cells map[cell]domain.SeatCounts
}

// Total is the counts over the whole event.
func (inv *Inventory) Total() domain.SeatCounts {
	var out domain.SeatCounts
	for _, c := range inv.cells {
		out = out.Add(c)
	}
	return out
}

// ByBlock is the counts per block.
func (inv *Inventory) ByBlock() map[int64]domain.SeatCounts {
	out := map[int64]domain.SeatCounts{}
	for k, c := range inv.cells {
		out[k.block] = out[k.block].Add(c)
	}
	return out
}

// ByCategory is the counts per category; key 0 is seats with no category.
func (inv *Inventory) ByCategory() map[int64]domain.SeatCounts {
	out := map[int64]domain.SeatCounts{}
	for k, c := range inv.cells {
		out[k.category] = out[k.category].Add(c)
	}
	return out
}

// BlockCategories is, for one block, the counts per category.
func (inv *Inventory) BlockCategories(blockID int64) map[int64]domain.SeatCounts {
	out := map[int64]domain.SeatCounts{}
	for k, c := range inv.cells {
		if k.block == blockID {
			out[k.category] = c
		}
	}
	return out
}

// PriceFrom is the cheapest price among categories that have an available seat in the given
// blocks (nil means every block), or nil when no seat is available there.
func (inv *Inventory) PriceFrom(prices map[int64]int64, blocks map[int64]bool) *int64 {
	var best *int64
	for k, c := range inv.cells {
		if c.Available == 0 || (blocks != nil && !blocks[k.block]) {
			continue
		}
		price, ok := prices[k.category]
		if !ok {
			continue
		}
		if best == nil || price < *best {
			p := price
			best = &p
		}
	}
	return best
}

// loadInventory reads the counts from the database: one index-only scan of the event's
// inventory, plus the handful of holds that have lapsed but are still stored as HELD.
func (s *Service) loadInventory(ctx context.Context, eventID int64) (*Inventory, error) {
	rows, err := s.q.CountEventSeatsByBlock(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("count seats of event %d: %w", eventID, err)
	}
	lapsed, err := s.q.CountLapsedHoldsByBlock(ctx, eventID)
	if err != nil {
		return nil, fmt.Errorf("count lapsed holds of event %d: %w", eventID, err)
	}
	inv := &Inventory{cells: make(map[cell]domain.SeatCounts, len(rows))}
	key := func(block int64, category *int64) cell {
		if category == nil {
			return cell{block: block}
		}
		return cell{block: block, category: *category}
	}
	for _, r := range rows {
		k := key(r.BlockID, r.CategoryID)
		c := inv.cells[k]
		switch domain.SeatStatus(r.Status) {
		case domain.SeatAvailable:
			c.Available += int(r.Seats)
		case domain.SeatHeld:
			c.Held += int(r.Seats)
		case domain.SeatSold:
			c.Sold += int(r.Seats)
		default:
			c.Blocked += int(r.Seats)
		}
		inv.cells[k] = c
	}
	for _, r := range lapsed {
		k := key(r.BlockID, r.CategoryID)
		c := inv.cells[k]
		// The two reads are separate statements, so a hold can lapse between them; never
		// move more seats than the first read counted as held.
		n := min(int(r.Seats), c.Held)
		c.Held -= n
		c.Available += n
		inv.cells[k] = c
	}
	return inv, nil
}

// inventory returns the event's counts, at most CountsTTL old.
func (s *Service) inventory(ctx context.Context, eventID int64) (*Inventory, error) {
	return s.counts.get(eventID, func() (*Inventory, error) { return s.loadInventory(ctx, eventID) })
}

// countsCache keeps each event's counts for a few seconds. It is per process: two API
// instances each keep their own, which only means each reads the database once per TTL.
type countsCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[int64]*countsEntry
}

type countsEntry struct {
	// mu is held while loading, so a burst of requests for one event runs one query and
	// the rest wait for its answer.
	mu  sync.Mutex
	inv *Inventory
	at  time.Time
}

func newCountsCache(ttl time.Duration, now func() time.Time) *countsCache {
	return &countsCache{ttl: ttl, now: now, entries: map[int64]*countsEntry{}}
}

func (c *countsCache) get(eventID int64, load func() (*Inventory, error)) (*Inventory, error) {
	c.mu.Lock()
	e, ok := c.entries[eventID]
	if !ok {
		e = &countsEntry{}
		c.entries[eventID] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inv != nil && c.now().Sub(e.at) < c.ttl {
		return e.inv, nil
	}
	inv, err := load()
	if err != nil {
		return nil, err
	}
	e.inv, e.at = inv, c.now()
	return inv, nil
}

// invalidate forgets an event's counts. A load that is running at that moment finishes into
// the entry that was just dropped, so its (possibly pre-publish) answer is never served.
func (c *countsCache) invalidate(eventID int64) {
	c.mu.Lock()
	delete(c.entries, eventID)
	c.mu.Unlock()
}
