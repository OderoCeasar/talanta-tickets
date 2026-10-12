package booking

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// bestWindow is how many of the best runs try number `attempt` (from 1) picks among.
//
// The first try always takes the single best run, so a fan with nobody else around gets the
// best seats there are. A lost try means others are after the same seats; if every loser
// then went for the next best run they would all collide again, one winner per round. So
// each later try works through a window of the best runs that grows fourfold, in an order
// shuffled per request (up to Policy.BestAvailableWalk runs a try), which spreads a crowd
// out in a few rounds while still keeping everyone near the front.
func bestWindow(attempt int) int {
	if attempt <= 1 {
		return 1
	}
	return min(2<<(2*(attempt-1)), 2048) // 8, 32, 128, 512, 2048
}

// HoldBestAvailable holds `quantity` neighbouring seats in one row of the category
// (correction 2).
//
// It never locks while it looks. It reads the free seats with plain reads, finds runs of
// neighbours in Go, and then tries to hold one run with the ordinary hold statement, which
// is the only thing that decides. If someone else got there first it looks again and tries
// another run, up to Policy.BestAvailableTries times, then answers NO_ADJACENT_SEATS.
//
// What "best" means: the block nearest the middle of its stand (lower tier first on a tie),
// in it the row nearest the pitch, in it the run nearest the middle of the row.
//
// Seat types: runs are made of STANDARD seats. Only when the whole category has no such run
// are RESTRICTED_VIEW seats allowed into a run. ACCESSIBLE and COMPANION seats are never
// handed out here.
func (s *Service) HoldBestAvailable(ctx context.Context, userID, eventID, categoryID int64, quantity int) (Hold, error) {
	if quantity < 1 || quantity > domain.MaxSeatsPerHold {
		return Hold{}, domain.InvalidField("quantity", "range", fmt.Sprintf("Must be from 1 to %d.", domain.MaxSeatsPerHold))
	}
	// The same checks a hold makes, before any seat is read, so "not on sale" and "over
	// your limit" are reported as such and not as "no seats found". Each try repeats them
	// under its own locks.
	if err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error { return s.admit(ctx, tx, userID, eventID, quantity) }); err != nil {
		return Hold{}, err
	}
	category, err := s.q.GetEventCategory(ctx, store.GetEventCategoryParams{ID: categoryID, EventID: eventID})
	if errors.Is(err, store.ErrNoRows) {
		return Hold{}, domain.ErrNotFound
	}
	if err != nil {
		return Hold{}, fmt.Errorf("get category %d: %w", categoryID, err)
	}

	blocks, err := s.categoryBlocks(ctx, eventID, categoryID)
	if err != nil {
		return Hold{}, err
	}
	for attempt := 1; attempt <= s.policy.BestAvailableTries; attempt++ {
		runs, err := s.bestRuns(ctx, eventID, categoryID, blocks, quantity, bestWindow(attempt))
		if err != nil {
			return Hold{}, err
		}
		if len(runs) == 0 {
			break
		}
		// The first try takes the single best run. A later try means others are after the
		// same seats: it walks several of the candidate runs, in an order shuffled for this
		// request alone, before it reads the free seats again. One random pick per try was
		// not enough: with as many groups as runs, a few groups lost all six picks while a
		// whole run stood free.
		if attempt > 1 {
			rand.Shuffle(len(runs), func(i, j int) { runs[i], runs[j] = runs[j], runs[i] })
			runs = runs[:min(len(runs), s.policy.BestAvailableWalk)]
		} else {
			runs = runs[:1]
		}
		for _, pick := range runs {
			hold, err := s.Hold(ctx, userID, eventID, pick.SeatIDs)
			if domain.CodeOf(err) == domain.CodeSeatsUnavailable {
				continue
			}
			return hold, err
		}
	}

	free := int32(0)
	if len(blocks) > 0 {
		free, err = s.q.CountFreeSeatsOfCategory(ctx, store.CountFreeSeatsOfCategoryParams{EventID: eventID, BlockIds: blocks, CategoryID: categoryID})
		if err != nil {
			return Hold{}, fmt.Errorf("count free seats of category %d: %w", categoryID, err)
		}
	}
	return Hold{}, domain.NoAdjacentSeats(categoryID, category.Name, quantity, int(free))
}

// categoryBlocks returns the blocks that have seats of the category, best block first: the
// one nearest the middle of its stand, lower tier first on a tie.
func (s *Service) categoryBlocks(ctx context.Context, eventID, categoryID int64) ([]int64, error) {
	blocks, err := s.q.ListCategoryBlocks(ctx, store.ListCategoryBlocksParams{EventID: eventID, CategoryID: categoryID})
	if err != nil {
		return nil, fmt.Errorf("list blocks of category %d: %w", categoryID, err)
	}
	slices.SortFunc(blocks, func(a, b store.ListCategoryBlocksRow) int {
		return cmp.Or(
			cmp.Compare(a.OffCentre, b.OffCentre),
			cmp.Compare(a.Tier, b.Tier), // LOWER sorts before UPPER
			cmp.Compare(a.DisplayOrder, b.DisplayOrder),
			cmp.Compare(a.BlockID, b.BlockID),
		)
	})
	ids := make([]int64, len(blocks))
	for i, b := range blocks {
		ids[i] = b.BlockID
	}
	return ids, nil
}

// bestRuns returns up to `want` of the best runs of `quantity` seats in the category, best
// first. blocks are the category's blocks, best first.
//
// It reads the free seats a few blocks at a time, best blocks first (1 block, then 3, then
// 11, then 43 …), and stops as soon as it has enough runs. So the usual request reads one
// block (at most about 1,300 seats), a contended one a handful, and only a category that is
// nearly sold out is read to the end, by which time there is little left in it to read.
func (s *Service) bestRuns(ctx context.Context, eventID, categoryID int64, blocks []int64, quantity, want int) ([]run, error) {
	// Kept so the second pass does not read the blocks again.
	var read [][]candidate
	var runs []run
	for start, size := 0, 1; start < len(blocks); start, size = start+size, size*4-1 {
		chunk := blocks[start:min(start+size, len(blocks))]
		rows, err := s.q.ListBestAvailableCandidates(ctx, store.ListBestAvailableCandidatesParams{
			EventID: eventID, BlockIds: chunk, CategoryID: categoryID,
		})
		if err != nil {
			return nil, fmt.Errorf("list free seats of %d blocks: %w", len(chunk), err)
		}
		byBlock := make(map[int64][]candidate, len(chunk))
		for _, r := range rows {
			byBlock[r.BlockID] = append(byBlock[r.BlockID], candidate{
				ID: r.ID, RowID: r.RowID, RowIndex: int(r.RowIndex), RowSeats: int(r.SeatCount),
				SeatNumber: int(r.SeatNumber), SeatType: r.SeatType,
			})
		}
		for _, blockID := range chunk { // in rank order, not in the query's id order
			seats := byBlock[blockID]
			if len(seats) < quantity {
				continue
			}
			read = append(read, seats)
			runs = append(runs, runsInBlock(seats, quantity, standardOnly)...)
		}
		if len(runs) >= want {
			return runs[:want], nil
		}
	}
	if len(runs) > 0 {
		return runs, nil
	}
	// Nothing made of standard seats anywhere in the category: restricted-view seats are
	// better than no seats.
	for _, seats := range read {
		runs = append(runs, runsInBlock(seats, quantity, withRestrictedView)...)
		if len(runs) >= want {
			return runs[:want], nil
		}
	}
	return runs, nil
}
