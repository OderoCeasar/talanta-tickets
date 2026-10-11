package store

// The text of the seat map queries, for tests that EXPLAIN them against a full venue to
// prove they stay off a sequential scan of event_seats.
const (
	SQLListBlockSeatsForViewer       = listBlockSeatsForViewer
	SQLListBlockSeatChangesForViewer = listBlockSeatChangesForViewer
	SQLCountEventSeatsByBlock        = countEventSeatsByBlock
	SQLCountLapsedHoldsByBlock       = countLapsedHoldsByBlock
)
