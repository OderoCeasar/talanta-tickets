package events

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// MapStand is one stand on level 1 of the seat map.
type MapStand struct {
	Stand          store.Stand
	SeatsTotal     int
	SeatsAvailable int
	// PriceFromCents is the cheapest available seat in the stand; nil when none is.
	PriceFromCents *int64
}

// StadiumMap is level 1: the venue and its stands.
type StadiumMap struct {
	EventID int64
	Venue   store.Venue
	Stands  []MapStand
}

// StadiumMap returns level 1 of the seat map. Counts come from the short-lived cache.
func (s *Service) StadiumMap(ctx context.Context, eventID int64) (StadiumMap, error) {
	row, err := s.publicEvent(ctx, eventID)
	if err != nil {
		return StadiumMap{}, err
	}
	ev := row.Event
	venue, err := s.q.GetVenue(ctx, ev.VenueID)
	if err != nil {
		return StadiumMap{}, fmt.Errorf("get venue %d: %w", ev.VenueID, err)
	}
	stands, err := s.q.ListStandsByVenue(ctx, ev.VenueID)
	if err != nil {
		return StadiumMap{}, fmt.Errorf("list stands of venue %d: %w", ev.VenueID, err)
	}
	blocks, err := s.q.ListBlockStandsByVenue(ctx, ev.VenueID)
	if err != nil {
		return StadiumMap{}, fmt.Errorf("list blocks of venue %d: %w", ev.VenueID, err)
	}
	inv, err := s.inventory(ctx, eventID)
	if err != nil {
		return StadiumMap{}, err
	}
	prices, err := s.prices(ctx, eventID)
	if err != nil {
		return StadiumMap{}, err
	}

	byBlock := inv.ByBlock()
	inStand := map[int64]map[int64]bool{}
	for _, b := range blocks {
		if inStand[b.StandID] == nil {
			inStand[b.StandID] = map[int64]bool{}
		}
		inStand[b.StandID][b.BlockID] = true
	}
	out := StadiumMap{EventID: eventID, Venue: venue, Stands: make([]MapStand, 0, len(stands))}
	for _, st := range stands {
		ms := MapStand{Stand: st}
		for blockID := range inStand[st.ID] {
			c := byBlock[blockID]
			ms.SeatsTotal += c.Total()
			ms.SeatsAvailable += c.Available
		}
		if ms.SeatsAvailable > 0 {
			ms.PriceFromCents = inv.PriceFrom(prices, inStand[st.ID])
		}
		out.Stands = append(out.Stands, ms)
	}
	return out, nil
}

// Badge is how many seats of one category are free in a block.
type Badge struct {
	CategoryID     int64
	SeatsAvailable int
}

// MapBlock is one block on level 2 of the seat map.
type MapBlock struct {
	Block          store.Block
	SeatsTotal     int
	SeatsAvailable int
	PriceFromCents *int64
	// Badges has one entry per category present in the block, cheapest first.
	Badges []Badge
}

// StandBlocks is level 2: one stand and its blocks.
type StandBlocks struct {
	EventID int64
	Stand   store.Stand
	// Categories is every category that appears in the stand, cheapest first.
	Categories []store.EventCategory
	Blocks     []MapBlock
}

// StandBlocks returns level 2 of the seat map. Counts come from the short-lived cache.
func (s *Service) StandBlocks(ctx context.Context, eventID, standID int64) (StandBlocks, error) {
	row, err := s.publicEvent(ctx, eventID)
	if err != nil {
		return StandBlocks{}, err
	}
	stand, err := s.q.GetStand(ctx, standID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && stand.VenueID != row.Event.VenueID) {
		return StandBlocks{}, domain.ErrNotFound
	}
	if err != nil {
		return StandBlocks{}, fmt.Errorf("get stand %d: %w", standID, err)
	}
	blocks, err := s.q.ListBlocksByStand(ctx, standID)
	if err != nil {
		return StandBlocks{}, fmt.Errorf("list blocks of stand %d: %w", standID, err)
	}
	cats, err := s.q.ListEventCategoriesCheapestFirst(ctx, eventID)
	if err != nil {
		return StandBlocks{}, fmt.Errorf("list categories of event %d: %w", eventID, err)
	}
	inv, err := s.inventory(ctx, eventID)
	if err != nil {
		return StandBlocks{}, err
	}

	prices := make(map[int64]int64, len(cats))
	for _, c := range cats {
		prices[c.ID] = c.PriceCents
	}
	out := StandBlocks{EventID: eventID, Stand: stand, Categories: []store.EventCategory{}, Blocks: make([]MapBlock, 0, len(blocks))}
	present := map[int64]bool{}
	for _, b := range blocks {
		mb := MapBlock{Block: b, Badges: []Badge{}}
		perCategory := inv.BlockCategories(b.ID)
		for _, c := range perCategory {
			mb.SeatsTotal += c.Total()
			mb.SeatsAvailable += c.Available
		}
		// cats is cheapest first, so the badges come out in that order too.
		for _, c := range cats {
			counts, ok := perCategory[c.ID]
			if !ok {
				continue
			}
			present[c.ID] = true
			mb.Badges = append(mb.Badges, Badge{CategoryID: c.ID, SeatsAvailable: counts.Available})
		}
		if mb.SeatsAvailable > 0 {
			mb.PriceFromCents = inv.PriceFrom(prices, map[int64]bool{b.ID: true})
		}
		out.Blocks = append(out.Blocks, mb)
	}
	for _, c := range cats {
		if present[c.ID] {
			out.Categories = append(out.Categories, c)
		}
	}
	return out, nil
}

// Seat is one seat on level 3, already reduced to what the viewer may know.
type Seat struct {
	EventSeatID int64
	Number      int
	X, Y        float64
	TypeCode    int
	// CategoryIndex points into BlockSeats.Categories; -1 means no category.
	CategoryIndex int
	Code          domain.SeatCode
}

// SeatRow is one row of seats, front row first.
type SeatRow struct {
	Label string
	Seats []Seat
}

// SeatChange is one seat's status now.
type SeatChange struct {
	EventSeatID int64
	Code        domain.SeatCode
}

// BlockSeats is level 3: either the full block, or what changed since a marker.
type BlockSeats struct {
	// Full says which of the two shapes this is.
	Full                bool
	Marker              string
	PollIntervalSeconds int

	// Set when Full.
	Block      store.Block
	Bounds     seatmap.Bounds
	Categories []store.EventCategory
	Rows       []SeatRow

	// Set when not Full.
	Changes []SeatChange
}

// BlockSeats returns level 3 of the seat map. It is never cached: a seat map that shows a
// seat free after it was taken sends the fan into a 409.
//
// viewerID is the signed-in caller, or 0. since is the marker of the caller's previous
// answer for this block, or "" for the full block.
//
// # Markers
//
// A marker is the database clock read BEFORE the seats were selected. event_seats.updated_at
// is stamped when a row is written, not when its transaction commits, so a transaction that
// wrote a seat before the marker was issued may commit after it, with a stamp the marker has
// already passed. The poll therefore looks back MarkerOverlap before the marker; that late
// commit is delivered on the next poll. Seats inside the overlap are sent again, which is
// harmless: a change is the seat's status now, not an event to replay.
//
// The caller gets the full block instead of a delta when the marker cannot be read, is for
// another event or block, is from the future, is older than MarkerMaxAge, or when the event
// itself changed inside the same window (a price or colour change touches the event).
func (s *Service) BlockSeats(ctx context.Context, eventID, blockID, viewerID int64, since string) (BlockSeats, error) {
	// First, before any other read: nothing selected below can be newer than this.
	issuedAt, err := s.q.DatabaseClock(ctx)
	if err != nil {
		return BlockSeats{}, fmt.Errorf("read database clock: %w", err)
	}
	row, err := s.publicEvent(ctx, eventID)
	if err != nil {
		return BlockSeats{}, err
	}
	info, err := s.q.GetBlockWithVenue(ctx, blockID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && info.VenueID != row.Event.VenueID) {
		return BlockSeats{}, domain.ErrNotFound
	}
	if err != nil {
		return BlockSeats{}, fmt.Errorf("get block %d: %w", blockID, err)
	}

	out := BlockSeats{
		Marker:              encodeMarker(eventID, blockID, issuedAt),
		PollIntervalSeconds: int(s.policy.PollInterval / time.Second),
	}
	if since != "" {
		if seenAt, ok := decodeMarker(since, eventID, blockID); ok && s.markerUsable(seenAt, issuedAt) {
			from := seenAt.Add(-s.policy.MarkerOverlap)
			if !row.Event.UpdatedAt.After(from) {
				changed, err := s.q.ListBlockSeatChangesForViewer(ctx, store.ListBlockSeatChangesForViewerParams{
					EventID: eventID, BlockID: blockID, ViewerID: viewerID, Since: from,
				})
				if err != nil {
					return BlockSeats{}, fmt.Errorf("list changed seats of block %d: %w", blockID, err)
				}
				out.Changes = make([]SeatChange, 0, len(changed))
				for i, c := range changed {
					// The query's two branches can both return a seat (written recently and
					// lapsed since); rows come back ordered by id, so twins are neighbours.
					if i > 0 && changed[i-1].ID == c.ID {
						continue
					}
					out.Changes = append(out.Changes, SeatChange{
						EventSeatID: c.ID,
						Code:        domain.SeatCodeFor(domain.SeatStatus(c.Status), c.HoldLapsed, c.HeldByViewer),
					})
				}
				return out, nil
			}
		}
	}

	out.Full = true
	out.Block = info.Block
	rows, err := s.q.ListRowsByBlock(ctx, blockID)
	if err != nil {
		return BlockSeats{}, fmt.Errorf("list rows of block %d: %w", blockID, err)
	}
	seats, err := s.q.ListBlockSeatsForViewer(ctx, store.ListBlockSeatsForViewerParams{
		EventID: eventID, BlockID: blockID, ViewerID: viewerID,
	})
	if err != nil {
		return BlockSeats{}, fmt.Errorf("list seats of block %d: %w", blockID, err)
	}
	cats, err := s.q.ListEventCategoriesCheapestFirst(ctx, eventID)
	if err != nil {
		return BlockSeats{}, fmt.Errorf("list categories of event %d: %w", eventID, err)
	}

	// Only the categories this block uses are sent, cheapest first.
	used := map[int64]bool{}
	for _, st := range seats {
		if st.CategoryID != nil {
			used[*st.CategoryID] = true
		}
	}
	categoryIndex := map[int64]int{}
	out.Categories = []store.EventCategory{}
	for _, c := range cats {
		if used[c.ID] {
			categoryIndex[c.ID] = len(out.Categories)
			out.Categories = append(out.Categories, c)
		}
	}

	byRow := make(map[int64][]Seat, len(rows))
	points := make([]seatmap.Point, 0, len(seats))
	for _, st := range seats {
		index := -1
		if st.CategoryID != nil {
			if ci, ok := categoryIndex[*st.CategoryID]; ok {
				index = ci
			}
		}
		byRow[st.RowID] = append(byRow[st.RowID], Seat{
			EventSeatID: st.ID, Number: int(st.SeatNumber), X: st.X, Y: st.Y,
			TypeCode:      domain.SeatTypeCode(st.SeatType),
			CategoryIndex: index,
			Code:          domain.SeatCodeFor(domain.SeatStatus(st.Status), st.HoldLapsed, st.HeldByViewer),
		})
		points = append(points, seatmap.Point{X: st.X, Y: st.Y})
	}
	// rows is front row first; a row with no inventory (every seat switched off) is left out.
	out.Rows = make([]SeatRow, 0, len(rows))
	for _, r := range rows {
		if rowSeats := byRow[r.ID]; len(rowSeats) > 0 {
			out.Rows = append(out.Rows, SeatRow{Label: r.Label, Seats: rowSeats})
		}
	}

	if box, ok := seatmap.SeatBounds(points, info.Block.SeatPitchX, info.Block.SeatPitchY); ok {
		out.Bounds = box
	} else {
		// No inventory in this block: a box at its origin keeps the answer well-formed.
		o := info.Block
		out.Bounds = seatmap.Bounds{MinX: o.OriginX, MinY: o.OriginY, MaxX: o.OriginX, MaxY: o.OriginY}
	}
	return out, nil
}

// markerUsable reports whether a marker read at issuedAt may be answered with a delta.
func (s *Service) markerUsable(seenAt, issuedAt time.Time) bool {
	// A marker from the future was not issued by this database; a second of slack covers a
	// clock step.
	if seenAt.After(issuedAt.Add(time.Second)) {
		return false
	}
	return issuedAt.Sub(seenAt) <= s.policy.MarkerMaxAge
}

// markerVersion changes whenever the marker's meaning does, so old markers are refused
// (and answered with a full block) instead of being misread.
const markerVersion = "m1"

// encodeMarker builds the opaque marker: the event, the block and the database time, so a
// marker handed to another block or event is recognised and refused.
func encodeMarker(eventID, blockID int64, at time.Time) string {
	raw := fmt.Sprintf("%s.%d.%d.%d", markerVersion, eventID, blockID, at.UnixMicro())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeMarker reads a marker for exactly this event and block. Anything else is not ok.
func decodeMarker(marker string, eventID, blockID int64) (time.Time, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(marker)
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 4 || parts[0] != markerVersion {
		return time.Time{}, false
	}
	ev, err1 := strconv.ParseInt(parts[1], 10, 64)
	bl, err2 := strconv.ParseInt(parts[2], 10, 64)
	micros, err3 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || ev != eventID || bl != blockID || micros <= 0 {
		return time.Time{}, false
	}
	return time.UnixMicro(micros), true
}
