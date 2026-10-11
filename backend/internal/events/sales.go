package events

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Sales buckets.
const (
	BucketHour = "hour"
	BucketDay  = "day"
)

// nairobi is fixed at UTC+3: Kenya has no daylight saving, and the slim container image has
// no time zone database for Go to load by name. The SQL side buckets by 'Africa/Nairobi',
// which Postgres carries itself.
var nairobi = time.FixedZone("EAT", 3*60*60)

// bucketStart is the start of the bucket t falls in, on Nairobi clock boundaries.
func bucketStart(bucket string, t time.Time) time.Time {
	l := t.In(nairobi)
	if bucket == BucketDay {
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, nairobi).UTC()
	}
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), 0, 0, 0, nairobi).UTC()
}

func nextBucket(bucket string, start time.Time) time.Time {
	if bucket == BucketDay {
		return start.Add(24 * time.Hour)
	}
	return start.Add(time.Hour)
}

// SalesCategory is one category's row on the dashboard.
type SalesCategory struct {
	Row    store.EventCategory
	Counts domain.SeatCounts
	// RevenueCents is the sum of the prices actually paid in this category.
	RevenueCents int64
}

// SalesBlock is one block's cell of the heatmap.
type SalesBlock struct {
	BlockID, StandID int64
	Sold, Held       int
	// Total is the block's seats that are not blocked.
	Total int
}

// SalesPoint is one bucket of the time series.
type SalesPoint struct {
	Start        time.Time
	Bookings     int
	Tickets      int
	RevenueCents int64
}

// Sales is everything the sales dashboard shows for one event.
type Sales struct {
	EventID             int64
	GeneratedAt         time.Time
	RevenueCents        int64
	Seats               domain.SeatCounts
	RefundRequiredCount int
	RefundRequiredCents int64
	OpenAlertCount      int
	Categories          []SalesCategory
	Blocks              []SalesBlock
	Bucket              string
	Points              []SalesPoint
}

// SalesQuery is the time series part of a sales request.
type SalesQuery struct {
	// Bucket is BucketHour or BucketDay; empty lets the range decide (seriesRange).
	Bucket string
	// From defaults to the first paid booking (or later, to keep the series within
	// MaxSeriesPoints), To to now.
	From, To *time.Time
}

// Sales returns the dashboard figures. Seat counts come from the short-lived cache; money
// is read fresh.
func (s *Service) Sales(ctx context.Context, eventID int64, query SalesQuery) (Sales, error) {
	row, err := s.getEvent(ctx, eventID)
	if err != nil {
		return Sales{}, err
	}
	ev := row.Event
	now := s.now()
	out := Sales{EventID: eventID, GeneratedAt: now, Categories: []SalesCategory{}, Blocks: []SalesBlock{}, Points: []SalesPoint{}}

	totals, err := s.q.GetEventSalesTotals(ctx, eventID)
	if err != nil {
		return Sales{}, fmt.Errorf("sales totals of event %d: %w", eventID, err)
	}
	out.RevenueCents = totals.RevenueCents
	out.RefundRequiredCount = int(totals.RefundRequiredCount)
	out.RefundRequiredCents = totals.RefundRequiredCents
	out.OpenAlertCount = int(totals.OpenAlertCount)

	inv := &Inventory{}
	if domain.EventStatus(ev.Status).HasInventory() {
		if inv, err = s.inventory(ctx, eventID); err != nil {
			return Sales{}, err
		}
	}
	out.Seats = inv.Total()

	cats, err := s.q.ListEventCategoriesCheapestFirst(ctx, eventID)
	if err != nil {
		return Sales{}, fmt.Errorf("list categories of event %d: %w", eventID, err)
	}
	paid, err := s.q.SumPaidRevenueByCategory(ctx, eventID)
	if err != nil {
		return Sales{}, fmt.Errorf("revenue by category of event %d: %w", eventID, err)
	}
	revenue := make(map[int64]int64, len(paid))
	for _, p := range paid {
		revenue[p.CategoryID] = p.RevenueCents
	}
	byCategory := inv.ByCategory()
	for _, c := range cats {
		out.Categories = append(out.Categories, SalesCategory{Row: c, Counts: byCategory[c.ID], RevenueCents: revenue[c.ID]})
	}

	blocks, err := s.q.ListBlockStandsByVenue(ctx, ev.VenueID)
	if err != nil {
		return Sales{}, fmt.Errorf("list blocks of venue %d: %w", ev.VenueID, err)
	}
	byBlock := inv.ByBlock()
	for _, b := range blocks {
		c := byBlock[b.BlockID]
		out.Blocks = append(out.Blocks, SalesBlock{BlockID: b.BlockID, StandID: b.StandID, Sold: c.Sold, Held: c.Held, Total: c.Sellable()})
	}
	sort.Slice(out.Blocks, func(i, j int) bool { return out.Blocks[i].BlockID < out.Blocks[j].BlockID })

	out.Bucket, out.Points, err = s.series(ctx, eventID, query, now)
	return out, err
}

// hourBucketSpan is the longest range that gets hour buckets when the caller did not choose a
// bucket: two days of hours is a readable chart, two months of them is not.
const hourBucketSpan = 48 * time.Hour

// seriesRange settles the bucket and the start of a time series.
//
// A caller who sends neither bucket nor from must always get an answer, however long the
// event has been on sale (the default call used to fail with a 400 on `from`, a field the
// caller never sent, 83 days after the first sale). So what the caller left out is chosen
// to fit: no bucket means hours up to hourBucketSpan and days beyond it; no from means the
// first sale, moved forward to the most recent maxPoints buckets when there would be more.
// Only a from the caller sent can be too far back, and that is still refused.
//
// givenFrom is nil when the caller sent none; first is then where the data starts.
func seriesRange(bucket string, givenFrom *time.Time, first, to time.Time, maxPoints int) (string, time.Time, error) {
	from := first
	if givenFrom != nil {
		from = *givenFrom
	}
	if bucket == "" {
		bucket = BucketHour
		if to.Sub(from) > hourBucketSpan {
			bucket = BucketDay
		}
	}
	width := nextBucket(bucket, time.Time{}).Sub(time.Time{})
	// `to` is exclusive: the last bucket is the one holding the instant before it.
	last := bucketStart(bucket, to.Add(-time.Nanosecond))
	if points := int(last.Sub(bucketStart(bucket, from))/width) + 1; points > maxPoints {
		if givenFrom != nil {
			return bucket, from, domain.InvalidField("from", "range", fmt.Sprintf(
				"That range has more than %d %s buckets. Use a shorter range or bucket=day.", maxPoints, bucket))
		}
		from = last.Add(-time.Duration(maxPoints-1) * width)
	}
	return bucket, from, nil
}

// series builds the time series with a point for every bucket in range, sales or not, and
// says which bucket it used.
func (s *Service) series(ctx context.Context, eventID int64, query SalesQuery, now time.Time) (string, []SalesPoint, error) {
	points := []SalesPoint{}
	to := now
	if query.To != nil {
		to = *query.To
	}
	first := to
	if query.From == nil {
		var err error
		first, err = s.q.GetFirstPaidAt(ctx, eventID)
		if errors.Is(err, store.ErrNoRows) || (err == nil && !to.After(first)) {
			// No sale yet, or the first one is later than the requested end: nothing to show.
			return cmp.Or(query.Bucket, BucketHour), points, nil
		}
		if err != nil {
			return "", nil, fmt.Errorf("first paid booking of event %d: %w", eventID, err)
		}
	} else if !to.After(*query.From) {
		return "", nil, domain.InvalidField("to", "after_from", "Must be later than from.")
	}

	bucket, from, err := seriesRange(query.Bucket, query.From, first, to, s.policy.MaxSeriesPoints)
	if err != nil {
		return "", nil, err
	}
	for start := bucketStart(bucket, from); start.Before(to); start = nextBucket(bucket, start) {
		points = append(points, SalesPoint{Start: start})
	}

	rows, err := s.q.ListPaidBookingBuckets(ctx, store.ListPaidBookingBucketsParams{
		Bucket: bucket, EventID: eventID, FromAt: from, ToAt: to,
	})
	if err != nil {
		return "", nil, fmt.Errorf("sales series of event %d: %w", eventID, err)
	}
	index := make(map[int64]int, len(points))
	for i, p := range points {
		index[p.Start.Unix()] = i
	}
	for _, r := range rows {
		i, ok := index[r.BucketStart.Unix()]
		if !ok {
			return "", nil, fmt.Errorf("sales series of event %d: bucket %s from the database is not on a %s boundary", eventID, r.BucketStart, bucket)
		}
		points[i].Bookings, points[i].Tickets, points[i].RevenueCents = int(r.Bookings), int(r.Tickets), r.RevenueCents
	}
	return bucket, points, nil
}

// AdminBooking is one booking on the organiser's list.
type AdminBooking struct {
	Row       store.Booking
	UserName  *string
	UserPhone *string
	// SeatLabels is "<block code> <seat label>" per seat, for example "N12 C14".
	SeatLabels    []string
	LatestPayment *store.Payment
	Refund        *store.ListLatestRefundsForBookingsRow
}

// BookingCursor is the position after the last booking of a page.
type BookingCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        int64     `json:"i"`
}

// Bookings lists an event's bookings, newest first.
func (s *Service) Bookings(ctx context.Context, eventID int64, status *string, limit int, after *BookingCursor) ([]AdminBooking, *BookingCursor, error) {
	if _, err := s.getEvent(ctx, eventID); err != nil {
		return nil, nil, err
	}
	arg := store.ListEventBookingsParams{EventID: eventID, Status: status, PageLimit: int32(limit + 1)}
	if after != nil {
		arg.HasCursor, arg.CursorCreatedAt, arg.CursorID = true, after.CreatedAt, after.ID
	}
	rows, err := s.q.ListEventBookings(ctx, arg)
	if err != nil {
		return nil, nil, fmt.Errorf("list bookings of event %d: %w", eventID, err)
	}
	var next *BookingCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1].Booking
		next = &BookingCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]AdminBooking, 0, len(rows))
	if len(rows) == 0 {
		return out, nil, nil
	}

	ids := make([]int64, 0, len(rows))
	index := make(map[int64]int, len(rows))
	for i, r := range rows {
		ids = append(ids, r.Booking.ID)
		index[r.Booking.ID] = i
		out = append(out, AdminBooking{Row: r.Booking, UserName: r.UserName, UserPhone: r.UserPhone, SeatLabels: []string{}})
	}
	labels, err := s.q.ListSeatLabelsForBookings(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("seat labels of bookings: %w", err)
	}
	for _, l := range labels {
		i := index[l.BookingID]
		out[i].SeatLabels = append(out[i].SeatLabels, l.BlockCode+" "+l.SeatLabel)
	}
	payments, err := s.q.ListLatestPaymentsForBookings(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("payments of bookings: %w", err)
	}
	for i := range payments {
		out[index[payments[i].BookingID]].LatestPayment = &payments[i]
	}
	refunds, err := s.q.ListLatestRefundsForBookings(ctx, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("refunds of bookings: %w", err)
	}
	for i := range refunds {
		out[index[refunds[i].Refund.BookingID]].Refund = &refunds[i]
	}
	return out, next, nil
}
