package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// fanTicketRoutes: FAN only. Paths are relative to /v1. A ticket that is someone else's
// answers 404, the same as one that does not exist.
func (s *Server) fanTicketRoutes(r chi.Router) {
	r.Get("/me/tickets", s.listMyTickets)
	r.Get("/tickets/{ticketId}", s.getTicket)
	r.With(s.LimitByUser("ticket-qr", s.limits.TicketQRPerUser)).Get("/tickets/{ticketId}/qr", s.getTicketQR)
}

// eventSummaryDTO is the contract's EventSummary: the few event fields carried inside
// bookings and tickets.
type eventSummaryDTO struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	Competition *string `json:"competition"`
	HomeTeam    *string `json:"home_team"`
	AwayTeam    *string `json:"away_team"`
	KickoffAt   string  `json:"kickoff_at"`
	GatesOpenAt *string `json:"gates_open_at"`
	VenueName   string  `json:"venue_name"`
	PosterURL   *string `json:"poster_url"`
	Status      string  `json:"status"`
}

// ticketDTO is the contract's Ticket. The booking answers carry it too (Booking.tickets).
type ticketDTO struct {
	ID               string          `json:"id"`
	Status           string          `json:"status"`
	Event            eventSummaryDTO `json:"event"`
	BookingReference string          `json:"booking_reference"`
	Location         seatLocationDTO `json:"location"`
	CategoryName     string          `json:"category_name"`
	HolderName       *string         `json:"holder_name"`
	HolderPhone      *string         `json:"holder_phone"`
	QRToken          string          `json:"qr_token"`
	IssuedAt         string          `json:"issued_at"`
	UsedAt           *string         `json:"used_at"`
	UpdatedAt        string          `json:"updated_at"`
}

func newTicketDTO(t ticketing.Ticket) ticketDTO {
	row, tk, ev := t.Row, t.Row.Ticket, t.Row.Event
	return ticketDTO{
		ID:     t.ID(),
		Status: tk.Status,
		Event: eventSummaryDTO{
			ID: ev.ID, Title: ev.Title, Competition: ev.Competition, HomeTeam: ev.HomeTeam, AwayTeam: ev.AwayTeam,
			KickoffAt: ts(ev.KickoffAt), GatesOpenAt: tsPtr(ev.GatesOpenAt), VenueName: row.VenueName,
			PosterURL: ev.PosterUrl, Status: string(t.EventStatus),
		},
		BookingReference: row.BookingReference,
		Location: seatLocationDTO{
			StandID: row.StandID, StandCode: row.StandCode, StandName: row.StandName, Tier: row.Tier,
			// The gate group the ticket was signed with, not one worked out again from the
			// stand: what the app shows must be what the gate will read.
			GateGroup: tk.GateGroup,
			BlockID:   row.BlockID, BlockCode: row.BlockCode, BlockLabel: row.BlockLabel,
			RowLabel: row.RowLabel, SeatNumber: row.SeatNumber, SeatLabel: row.SeatLabel, SeatType: row.SeatType,
		},
		CategoryName: row.CategoryName,
		HolderName:   tk.HolderName,
		HolderPhone:  tk.HolderPhone,
		QRToken:      tk.QrToken,
		IssuedAt:     ts(tk.IssuedAt),
		UsedAt:       tsPtr(tk.UsedAt),
		UpdatedAt:    ts(tk.UpdatedAt),
	}
}

func newTicketDTOs(tickets []ticketing.Ticket) []ticketDTO {
	out := make([]ticketDTO, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, newTicketDTO(t))
	}
	return out
}

// ticketPrivate marks an answer that carries a ticket's QR text: it is a bearer credential
// for a seat and must not sit in any cache.
func ticketPrivate(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
}

// pathTicketID reads {ticketId}. Anything that is not a UUID is a 400: the URL is malformed.
func pathTicketID(r *http.Request) ([16]byte, error) {
	id, err := ticketing.ParseUUID(chi.URLParam(r, "ticketId"))
	if err != nil {
		return id, domain.InvalidField("ticketId", "id", "Must be a ticket ID (a UUID).")
	}
	return id, nil
}

func (s *Server) listMyTickets(w http.ResponseWriter, r *http.Request) {
	var filter ticketing.Filter
	if r.URL.Query().Has("event_id") {
		id, err := strconv.ParseInt(r.URL.Query().Get("event_id"), 10, 64)
		if err != nil || id < 1 {
			WriteError(w, r, domain.InvalidField("event_id", "id", "Must be a positive whole number."))
			return
		}
		filter.EventID = &id
	}
	status, err := QueryEnum(r, "status",
		string(domain.TicketValid), string(domain.TicketUsed), string(domain.TicketVoid), string(domain.TicketTransferred))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if status != "" {
		st := domain.TicketStatus(status)
		filter.Status = &st
	}
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var after *ticketing.Cursor
	var cur ticketing.Cursor
	present, err := DecodeCursor(r, &cur)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if present {
		after = &cur
	}

	tickets, next, err := s.tickets.ListMine(r.Context(), MustPrincipal(r.Context()).UserID, filter, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketPrivate(w)
	writeJSON(w, http.StatusOK, pageDTO[ticketDTO]{Items: newTicketDTOs(tickets), NextCursor: EncodeCursor(next)})
}

func (s *Server) getTicket(w http.ResponseWriter, r *http.Request) {
	id, err := pathTicketID(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	t, err := s.tickets.Get(r.Context(), MustPrincipal(r.Context()).UserID, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketPrivate(w)
	writeJSON(w, http.StatusOK, newTicketDTO(t))
}

func (s *Server) getTicketQR(w http.ResponseWriter, r *http.Request) {
	id, err := pathTicketID(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	size := ticketing.DefaultQRSize
	if r.URL.Query().Has("size") {
		size, err = strconv.Atoi(r.URL.Query().Get("size"))
		if err != nil || size < ticketing.MinQRSize || size > ticketing.MaxQRSize {
			WriteError(w, r, domain.InvalidField("size", "range",
				"Must be a whole number from "+strconv.Itoa(ticketing.MinQRSize)+" to "+strconv.Itoa(ticketing.MaxQRSize)+"."))
			return
		}
	}
	// The owner check is the lookup itself: Get finds a ticket only for the user it belongs to.
	t, err := s.tickets.Get(r.Context(), MustPrincipal(r.Context()).UserID, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	png, err := s.qr.PNG(t.QRToken(), size)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ticketPrivate(w)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}
