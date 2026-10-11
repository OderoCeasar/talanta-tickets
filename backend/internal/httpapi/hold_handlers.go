package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// fanHoldRoutes: FAN only. Paths are relative to /v1.
func (s *Server) fanHoldRoutes(r chi.Router) {
	// Creating a hold is the only write a fan can repeat at will, so it is the only part
	// that is rate limited: per user (tight) and per IP (loose, see RateLimits). Reading
	// and releasing are not: a fan must always be able to give seats back.
	r.Group(func(r chi.Router) {
		r.Use(s.LimitByIP("hold", s.limits.HoldPerIP))
		r.Use(s.LimitByUser("hold", s.limits.HoldPerUser))
		r.Post("/holds", s.createHold)
		r.Post("/holds/best-available", s.createBestAvailableHold)
	})
	r.Get("/holds/{holdId}", s.getHold)
	r.Delete("/holds/{holdId}", s.releaseHold)
	r.Get("/me/holds", s.listMyHolds)
}

type createHoldBody struct {
	EventID int64   `json:"event_id" validate:"required,min=1"`
	SeatIDs []int64 `json:"seat_ids" validate:"required,min=1,max=50,unique,dive,min=1"`
}

type createBestAvailableHoldBody struct {
	EventID    int64 `json:"event_id" validate:"required,min=1"`
	CategoryID int64 `json:"category_id" validate:"required,min=1"`
	Quantity   int   `json:"quantity" validate:"required,min=1,max=50"`
}

type seatLocationDTO struct {
	StandID    int64  `json:"stand_id"`
	StandCode  string `json:"stand_code"`
	StandName  string `json:"stand_name"`
	Tier       string `json:"tier"`
	GateGroup  string `json:"gate_group"`
	BlockID    int64  `json:"block_id"`
	BlockCode  string `json:"block_code"`
	BlockLabel string `json:"block_label"`
	RowLabel   string `json:"row_label"`
	SeatNumber int32  `json:"seat_number"`
	SeatLabel  string `json:"seat_label"`
	SeatType   string `json:"seat_type"`
}

type holdSeatDTO struct {
	EventSeatID  int64           `json:"event_seat_id"`
	Location     seatLocationDTO `json:"location"`
	CategoryID   int64           `json:"category_id"`
	CategoryName string          `json:"category_name"`
	PriceCents   int64           `json:"price_cents"`
}

type holdDTO struct {
	ID               int64         `json:"id"`
	EventID          int64         `json:"event_id"`
	Status           string        `json:"status"`
	ExpiresAt        time.Time     `json:"expires_at"`
	RemainingSeconds int           `json:"remaining_seconds"`
	Extended         bool          `json:"extended"`
	BookingReference *string       `json:"booking_reference"`
	Currency         string        `json:"currency"`
	SubtotalCents    int64         `json:"subtotal_cents"`
	FeeCents         int64         `json:"fee_cents"`
	TotalCents       int64         `json:"total_cents"`
	Seats            []holdSeatDTO `json:"seats"`
	CreatedAt        time.Time     `json:"created_at"`
}

func newHoldDTO(h booking.Hold) holdDTO {
	seats := make([]holdSeatDTO, 0, len(h.Seats))
	for _, seat := range h.Seats {
		seats = append(seats, holdSeatDTO{
			EventSeatID: seat.EventSeatID,
			Location: seatLocationDTO{
				StandID: seat.StandID, StandCode: seat.StandCode, StandName: seat.StandName, Tier: seat.Tier,
				GateGroup: domain.GateGroup(seat.StandCode, seat.Tier),
				BlockID:   seat.BlockID, BlockCode: seat.BlockCode, BlockLabel: seat.BlockLabel,
				RowLabel: seat.RowLabel, SeatNumber: seat.SeatNumber, SeatLabel: seat.SeatLabel, SeatType: seat.SeatType,
			},
			CategoryID: seat.CategoryID, CategoryName: seat.CategoryName, PriceCents: seat.PriceCents,
		})
	}
	return holdDTO{
		ID: h.Row.ID, EventID: h.Row.EventID, Status: string(h.Status),
		ExpiresAt: h.Row.ExpiresAt.UTC(), RemainingSeconds: h.RemainingSeconds,
		Extended: h.Row.ExtendedAt != nil, BookingReference: h.BookingReference,
		Currency: domain.Currency, SubtotalCents: h.SubtotalCents, FeeCents: h.FeeCents, TotalCents: h.TotalCents,
		Seats:     seats,
		CreatedAt: h.Row.CreatedAt.UTC(),
	}
}

// writeHold answers with a hold. The body says who holds what and counts down, so it is
// never cached.
func writeHold(w http.ResponseWriter, status int, h booking.Hold) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, newHoldDTO(h))
}

func (s *Server) createHold(w http.ResponseWriter, r *http.Request) {
	var body createHoldBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	h, err := s.holds.Hold(r.Context(), MustPrincipal(r.Context()).UserID, body.EventID, body.SeatIDs)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeHold(w, http.StatusCreated, h)
}

func (s *Server) createBestAvailableHold(w http.ResponseWriter, r *http.Request) {
	var body createBestAvailableHoldBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	h, err := s.holds.HoldBestAvailable(r.Context(), MustPrincipal(r.Context()).UserID, body.EventID, body.CategoryID, body.Quantity)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeHold(w, http.StatusCreated, h)
}

func (s *Server) getHold(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "holdId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h, err := s.holds.Get(r.Context(), MustPrincipal(r.Context()).UserID, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeHold(w, http.StatusOK, h)
}

func (s *Server) releaseHold(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "holdId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.holds.Release(r.Context(), MustPrincipal(r.Context()).UserID, id); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listMyHolds(w http.ResponseWriter, r *http.Request) {
	var eventID *int64
	if r.URL.Query().Has("event_id") {
		id, err := strconv.ParseInt(r.URL.Query().Get("event_id"), 10, 64)
		if err != nil || id < 1 {
			WriteError(w, r, domain.InvalidField("event_id", "id", "Must be a positive whole number."))
			return
		}
		eventID = &id
	}
	holds, err := s.holds.ListMine(r.Context(), MustPrincipal(r.Context()).UserID, eventID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]holdDTO, 0, len(holds))
	for _, h := range holds {
		items = append(items, newHoldDTO(h))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, listDTO[holdDTO]{Items: items})
}
