package booking

import (
	"cmp"
	"slices"
)

// candidate is one free seat best available may hand out.
type candidate struct {
	ID         int64
	RowID      int64
	RowIndex   int
	RowSeats   int // seats in the whole row, for "how far from the middle"
	SeatNumber int
	SeatType   string
}

// Seat types, as stored in seats.seat_type.
const (
	seatStandard       = "STANDARD"
	seatRestrictedView = "RESTRICTED_VIEW"
)

// standardOnly and withRestrictedView are the two passes of best available. ACCESSIBLE and
// COMPANION seats are in neither: they are for the fans who need them and are only ever
// picked by hand on the map.
func standardOnly(seatType string) bool { return seatType == seatStandard }

func withRestrictedView(seatType string) bool {
	return seatType == seatStandard || seatType == seatRestrictedView
}

// run is n neighbouring seats in one row.
type run struct {
	SeatIDs   []int64
	rowIndex  int
	offCentre float64
}

// runsInBlock finds the runs of n neighbouring seats among the free seats of ONE block.
//
// Neighbouring means the same row and consecutive seat numbers. A run therefore never
// crosses a gap of any kind: a seat that is held, sold or blocked is not in `seats`; an
// inactive seat has no inventory row at all; a seat of a type `allowed` refuses breaks the
// run the same way; and an aisle is the edge of a block, which a row never crosses.
//
// Each stretch of free neighbours is cut into runs from its first seat, n at a time, so
// every request cuts the same stretch the same way and two groups do not leave a useless
// gap between them. Runs come back best first: the row nearest the pitch, then the run
// nearest the middle of its row.
func runsInBlock(seats []candidate, n int, allowed func(seatType string) bool) []run {
	if n < 1 {
		return nil
	}
	var runs []run
	var stretch []candidate
	flush := func() {
		for i := 0; i+n <= len(stretch); i += n {
			part := stretch[i : i+n]
			ids := make([]int64, n)
			for j, c := range part {
				ids[j] = c.ID
			}
			middleOfRun := float64(part[0].SeatNumber+part[n-1].SeatNumber) / 2
			middleOfRow := float64(part[0].RowSeats+1) / 2
			runs = append(runs, run{SeatIDs: ids, rowIndex: part[0].RowIndex, offCentre: abs(middleOfRun - middleOfRow)})
		}
		stretch = stretch[:0]
	}
	for _, c := range seats {
		if !allowed(c.SeatType) {
			flush()
			continue
		}
		if k := len(stretch); k > 0 && (stretch[k-1].RowID != c.RowID || stretch[k-1].SeatNumber+1 != c.SeatNumber) {
			flush()
		}
		stretch = append(stretch, c)
	}
	flush()

	slices.SortStableFunc(runs, func(a, b run) int {
		return cmp.Or(cmp.Compare(a.rowIndex, b.rowIndex), cmp.Compare(a.offCentre, b.offCentre))
	})
	return runs
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
