package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// fanBookingRoutes: FAN only. Paths are relative to /v1. A booking or payment that is
// someone else's answers 404, the same as one that does not exist.
func (s *Server) fanBookingRoutes(r chi.Router) {
	// The two writes of checkout are limited per user: a fan taps Pay a handful of times.
	r.Group(func(r chi.Router) {
		r.Use(s.LimitByUser("checkout", s.limits.CheckoutPerUser))
		r.Post("/bookings", s.createBooking)
		r.Post("/payments/mpesa/stk", s.startMpesaStkPush)
	})
	r.Get("/bookings/{reference}", s.getBooking)
	r.Get("/me/bookings", s.listMyBookings)
	r.Get("/payments/{paymentId}", s.getPayment)
}

type createBookingBody struct {
	HoldID int64 `json:"hold_id" validate:"required,min=1"`
}

type bookingItemDTO struct {
	EventSeatID    int64           `json:"event_seat_id"`
	Location       seatLocationDTO `json:"location"`
	CategoryID     int64           `json:"category_id"`
	CategoryName   string          `json:"category_name"`
	UnitPriceCents int64           `json:"unit_price_cents"`
}

// bookingDTO is the contract's Booking.
type bookingDTO struct {
	ID            int64            `json:"id"`
	Reference     string           `json:"reference"`
	Status        string           `json:"status"`
	Event         eventSummaryDTO  `json:"event"`
	Items         []bookingItemDTO `json:"items"`
	Currency      string           `json:"currency"`
	SubtotalCents int64            `json:"subtotal_cents"`
	FeeCents      int64            `json:"fee_cents"`
	TotalCents    int64            `json:"total_cents"`
	HoldID        *int64           `json:"hold_id"`
	HoldExpiresAt *string          `json:"hold_expires_at"`
	LatestPayment *paymentDTO      `json:"latest_payment"`
	Tickets       []ticketDTO      `json:"tickets"`
	CreatedAt     string           `json:"created_at"`
	PaidAt        *string          `json:"paid_at"`
}

func newBookingDTO(b booking.Booking, tickets []ticketing.Ticket) bookingDTO {
	r, ev := b.Row, b.Event
	out := bookingDTO{
		ID: r.ID, Reference: r.Reference, Status: r.Status,
		Event: eventSummaryDTO{
			ID: ev.ID, Title: ev.Title, Competition: ev.Competition, HomeTeam: ev.HomeTeam, AwayTeam: ev.AwayTeam,
			KickoffAt: ts(ev.KickoffAt), GatesOpenAt: tsPtr(ev.GatesOpenAt), VenueName: b.VenueName,
			PosterURL: ev.PosterUrl, Status: string(b.EventStatus),
		},
		Items:    make([]bookingItemDTO, 0, len(b.Items)),
		Currency: domain.Currency, SubtotalCents: r.SubtotalCents, FeeCents: r.FeeCents, TotalCents: r.TotalCents,
		HoldID: r.HoldID, HoldExpiresAt: tsPtr(b.HoldExpiresAt),
		Tickets:   newTicketDTOs(tickets),
		CreatedAt: ts(r.CreatedAt), PaidAt: tsPtr(r.PaidAt),
	}
	for _, it := range b.Items {
		out.Items = append(out.Items, bookingItemDTO{
			EventSeatID: it.EventSeatID,
			Location: seatLocationDTO{
				StandID: it.StandID, StandCode: it.StandCode, StandName: it.StandName, Tier: it.Tier,
				GateGroup: domain.GateGroup(it.StandCode, it.Tier),
				BlockID:   it.BlockID, BlockCode: it.BlockCode, BlockLabel: it.BlockLabel,
				RowLabel: it.RowLabel, SeatNumber: it.SeatNumber, SeatLabel: it.SeatLabel, SeatType: it.SeatType,
			},
			CategoryID: it.CategoryID, CategoryName: it.CategoryName, UnitPriceCents: it.UnitPriceCents,
		})
	}
	if b.LatestPayment != nil {
		p := newPaymentDTO(*b.LatestPayment, r)
		out.LatestPayment = &p
	}
	return out
}

// bookingDTOs turns bookings into answers, reading the tickets of all of them in one query.
// The bookings were loaded for their owner, which is the owner check for the tickets too.
func (s *Server) bookingDTOs(r *http.Request, bookings []booking.Booking) ([]bookingDTO, error) {
	ids := make([]int64, 0, len(bookings))
	for _, b := range bookings {
		ids = append(ids, b.Row.ID)
	}
	tickets, err := s.tickets.ListForBookings(r.Context(), s.pool, ids)
	if err != nil {
		return nil, err
	}
	out := make([]bookingDTO, 0, len(bookings))
	for _, b := range bookings {
		out = append(out, newBookingDTO(b, tickets[b.Row.ID]))
	}
	return out, nil
}

// writeBooking answers with one booking. It can carry ticket QR texts, so it is never cached.
func (s *Server) writeBooking(w http.ResponseWriter, r *http.Request, status int, b booking.Booking) {
	dtos, err := s.bookingDTOs(r, []booking.Booking{b})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketPrivate(w)
	writeJSON(w, status, dtos[0])
}

func (s *Server) createBooking(w http.ResponseWriter, r *http.Request) {
	// The header first: without a key the request cannot be made safe to repeat, whatever
	// its body says.
	key := r.Header.Get("Idempotency-Key")
	if !booking.ValidIdempotencyKey(key) {
		WriteError(w, r, booking.IdempotencyKeyRequired())
		return
	}
	var body createBookingBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	b, created, err := s.holds.CreateBooking(r.Context(), MustPrincipal(r.Context()).UserID, body.HoldID, key)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	s.writeBooking(w, r, status, b)
}

// pathReference reads {reference}. Anything that is not shaped like a reference is a 400.
func pathReference(r *http.Request) (string, error) {
	ref := chi.URLParam(r, "reference")
	ok := len(ref) == 10 && ref[:4] == "TLT-"
	for i := 4; ok && i < len(ref); i++ {
		c := ref[i]
		ok = (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	}
	if !ok {
		return "", domain.InvalidField("reference", "reference", "Must be a booking reference such as TLT-8H3K2M.")
	}
	return ref, nil
}

func (s *Server) getBooking(w http.ResponseWriter, r *http.Request) {
	ref, err := pathReference(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	b, err := s.holds.GetBooking(r.Context(), MustPrincipal(r.Context()).UserID, ref)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	s.writeBooking(w, r, http.StatusOK, b)
}

func (s *Server) listMyBookings(w http.ResponseWriter, r *http.Request) {
	raw, err := QueryEnum(r, "status", bookingStatuses...)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var status *domain.BookingStatus
	if raw != "" {
		st := domain.BookingStatus(raw)
		status = &st
	}
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var after *booking.BookingCursor
	var cur booking.BookingCursor
	if present, err := DecodeCursor(r, &cur); err != nil {
		WriteError(w, r, err)
		return
	} else if present {
		after = &cur
	}
	bookings, next, err := s.holds.ListBookings(r.Context(), MustPrincipal(r.Context()).UserID, status, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items, err := s.bookingDTOs(r, bookings)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketPrivate(w)
	writeJSON(w, http.StatusOK, pageDTO[bookingDTO]{Items: items, NextCursor: EncodeCursor(next)})
}
