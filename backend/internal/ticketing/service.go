package ticketing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// Config is what the service is built from.
type Config struct {
	DB     DB
	Logger *slog.Logger
	// Keys signs new tickets and verifies old ones. Required.
	Keys *Keyring
	// SMS delivers the "you're going" message. Nil means nothing is sent.
	SMS notify.SMSSender
	// PublicBaseURL is the origin the API is reached at from a phone's browser
	// (PUBLIC_BASE_URL); the web ticket link is PublicBaseURL + "/v1/t/" + web token.
	PublicBaseURL string
	// SMSTimeout bounds one send; zero means 10 seconds.
	SMSTimeout time.Duration
}

// Service issues, voids and reads tickets. It is safe for concurrent use.
type Service struct {
	db         DB
	q          *store.Queries
	log        *slog.Logger
	keys       *Keyring
	sms        notify.SMSSender
	baseURL    string
	smsTimeout time.Duration
}

func NewService(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, errors.New("ticketing: DB is required")
	}
	if cfg.Keys == nil || cfg.Keys.Signer == nil || cfg.Keys.Verifier == nil {
		return nil, errors.New("ticketing: Keys is required")
	}
	base := strings.TrimRight(cfg.PublicBaseURL, "/")
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("ticketing: PublicBaseURL must be an http(s) URL")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.SMSTimeout <= 0 {
		cfg.SMSTimeout = 10 * time.Second
	}
	return &Service{
		db: cfg.DB, q: store.New(cfg.DB), log: cfg.Logger, keys: cfg.Keys,
		sms: cfg.SMS, baseURL: base, smsTimeout: cfg.SMSTimeout,
	}, nil
}

// Verifier checks ticket tokens against every key this server trusts.
func (s *Service) Verifier() *Verifier { return s.keys.Verifier }

// SigningKeyID is the key ID new tickets are signed with.
func (s *Service) SigningKeyID() string { return s.keys.Signer.KeyID() }

// Ticket is one ticket with everything needed to show it offline.
type Ticket struct {
	// Row is the ticket with its event, booking reference and seat in words.
	Row store.TicketView
	// EventStatus is the event's effective status (domain.EffectiveEventStatus) when the
	// ticket was read.
	EventStatus domain.EventStatus
}

// ID is the ticket's UUID in text form.
func (t Ticket) ID() string { return FormatUUID(t.Row.Ticket.ID.Bytes) }

// Status is the ticket's stored status.
func (t Ticket) Status() domain.TicketStatus { return domain.TicketStatus(t.Row.Ticket.Status) }

// QRToken is the signed text to draw as a QR code.
func (t Ticket) QRToken() string { return t.Row.Ticket.QrToken }

func newTicket(row store.TicketView) Ticket {
	e := row.Event
	return Ticket{
		Row:         row,
		EventStatus: domain.EffectiveEventStatus(domain.EventStatus(e.Status), e.SalesOpenAt, e.SalesCloseAt, row.NowAt),
	}
}

func newTickets(rows []store.TicketView) []Ticket {
	out := make([]Ticket, 0, len(rows))
	for _, r := range rows {
		out = append(out, newTicket(r))
	}
	return out
}

// Filter narrows ListMine.
type Filter struct {
	EventID *int64
	Status  *domain.TicketStatus
}

// Cursor is the position after the last ticket of a page: the four columns the list is
// ordered by. The HTTP layer turns it into the opaque next_cursor.
type Cursor struct {
	KickoffAt   time.Time `json:"k"`
	EventID     int64     `json:"e"`
	EventSeatID int64     `json:"s"`
	TicketID    string    `json:"t"`
}

// ListMine returns one page of a fan's tickets, soonest kickoff first, then seat. next is nil
// on the last page.
func (s *Service) ListMine(ctx context.Context, userID int64, f Filter, limit int, after *Cursor) (tickets []Ticket, next *Cursor, err error) {
	params := store.ListTicketViewsOfUserParams{UserID: userID, EventID: f.EventID, RowLimit: int32(limit) + 1}
	if f.Status != nil {
		status := string(*f.Status)
		params.Status = &status
	}
	if after != nil {
		id, err := ParseUUID(after.TicketID)
		if err != nil || after.EventID < 1 || after.EventSeatID < 1 {
			return nil, nil, domain.InvalidField("cursor", "cursor", "Use the next_cursor value from the previous page.")
		}
		params.HasCursor = true
		params.AfterKickoffAt, params.AfterEventID, params.AfterEventSeatID = after.KickoffAt, after.EventID, after.EventSeatID
		params.AfterTicketID = pgtype.UUID{Bytes: id, Valid: true}
	}
	rows, err := s.q.ListTicketViewsOfUser(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list tickets of user %d: %w", userID, err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = &Cursor{
			KickoffAt: last.Event.KickoffAt, EventID: last.Ticket.EventID,
			EventSeatID: last.Ticket.EventSeatID, TicketID: FormatUUID(last.Ticket.ID.Bytes),
		}
	}
	tickets = make([]Ticket, 0, len(rows))
	for _, r := range rows {
		tickets = append(tickets, newTicket(store.TicketView(r)))
	}
	return tickets, next, nil
}

// Get returns one ticket of the user. A ticket that does not exist and a ticket that is
// someone else's are the same answer, domain.ErrNotFound, so IDs cannot be probed.
func (s *Service) Get(ctx context.Context, userID int64, ticketID [16]byte) (Ticket, error) {
	row, err := s.q.GetTicketViewForUser(ctx, store.GetTicketViewForUserParams{
		ID: pgtype.UUID{Bytes: ticketID, Valid: true}, UserID: userID,
	})
	if errors.Is(err, store.ErrNoRows) {
		return Ticket{}, domain.ErrNotFound
	}
	if err != nil {
		return Ticket{}, fmt.Errorf("get ticket %s: %w", FormatUUID(ticketID), err)
	}
	return newTicket(store.TicketView(row)), nil
}

// ListForBookings returns the tickets of the given bookings, in any status, keyed by booking
// ID. It does no owner check: it is for a caller that has already loaded those bookings for
// their owner (the booking answers carry `tickets`). db may be the pool or a transaction.
func (s *Service) ListForBookings(ctx context.Context, db store.DBTX, bookingIDs []int64) (map[int64][]Ticket, error) {
	out := make(map[int64][]Ticket, len(bookingIDs))
	if len(bookingIDs) == 0 {
		return out, nil
	}
	rows, err := store.New(db).ListTicketViewsOfBookings(ctx, bookingIDs)
	if err != nil {
		return nil, fmt.Errorf("list tickets of bookings: %w", err)
	}
	for _, r := range rows {
		out[r.Ticket.BookingID] = append(out[r.Ticket.BookingID], newTicket(r))
	}
	return out, nil
}

// WebBooking is what the web ticket page shows: one booking and its tickets.
type WebBooking struct {
	Reference string
	Status    string
	Tickets   []Ticket
}

// webTokenLength is the length of bookings.web_token: 32 random bytes in hex.
const webTokenLength = 64

// ValidWebToken reports whether s has the shape of a web token. It says nothing about
// whether one exists.
func ValidWebToken(s string) bool {
	if len(s) != webTokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ByWebToken returns the booking a web ticket link points at. A token of the wrong shape, an
// unknown token and a booking with no tickets (never paid) are all domain.ErrNotFound: the
// page has one answer for "nothing here".
func (s *Service) ByWebToken(ctx context.Context, webToken string) (WebBooking, error) {
	if !ValidWebToken(webToken) {
		return WebBooking{}, domain.ErrNotFound
	}
	b, err := s.q.GetBookingByWebToken(ctx, webToken)
	if errors.Is(err, store.ErrNoRows) {
		return WebBooking{}, domain.ErrNotFound
	}
	if err != nil {
		return WebBooking{}, fmt.Errorf("get booking by web token: %w", err)
	}
	rows, err := s.q.ListTicketViewsOfBookings(ctx, []int64{b.ID})
	if err != nil {
		return WebBooking{}, fmt.Errorf("list tickets of booking %d: %w", b.ID, err)
	}
	if len(rows) == 0 {
		return WebBooking{}, domain.ErrNotFound
	}
	return WebBooking{Reference: b.Reference, Status: b.Status, Tickets: newTickets(rows)}, nil
}

// WebTicketURL is the link sent by SMS for a booking's web token.
func (s *Service) WebTicketURL(webToken string) string { return s.baseURL + "/v1/t/" + webToken }
