package httpapi

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// listDTO is the shape of every unpaged list: { items }.
type listDTO[T any] struct {
	Items []T `json:"items"`
}

// compactJSON strips the spaces Postgres puts into jsonb text ("[[1, 2], [3, 4]]"). On a
// stand polygon that is one byte in six, and the seat map payloads have size targets.
func compactJSON(raw json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}

type eventDTO struct {
	ID                int64              `json:"id"`
	VenueID           int64              `json:"venue_id"`
	VenueName         string             `json:"venue_name"`
	Title             string             `json:"title"`
	Competition       *string            `json:"competition"`
	HomeTeam          *string            `json:"home_team"`
	AwayTeam          *string            `json:"away_team"`
	KickoffAt         string             `json:"kickoff_at"`
	GatesOpenAt       *string            `json:"gates_open_at"`
	PosterURL         *string            `json:"poster_url"`
	Status            domain.EventStatus `json:"status"`
	SalesOpenAt       *string            `json:"sales_open_at"`
	SalesCloseAt      *string            `json:"sales_close_at"`
	MaxTicketsPerUser int32              `json:"max_tickets_per_user"`
	Currency          string             `json:"currency"`
	PriceFromCents    *int64             `json:"price_from_cents"`
	SeatsAvailable    int                `json:"seats_available"`
	SeatsTotal        int                `json:"seats_total"`
	PublishedAt       *string            `json:"published_at"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
}

func newEventDTO(e events.Event) eventDTO {
	r := e.Row
	return eventDTO{
		ID: r.ID, VenueID: r.VenueID, VenueName: e.VenueName, Title: r.Title,
		Competition: r.Competition, HomeTeam: r.HomeTeam, AwayTeam: r.AwayTeam,
		KickoffAt: ts(r.KickoffAt), GatesOpenAt: tsPtr(r.GatesOpenAt), PosterURL: r.PosterUrl,
		Status: e.Status, SalesOpenAt: tsPtr(r.SalesOpenAt), SalesCloseAt: tsPtr(r.SalesCloseAt),
		MaxTicketsPerUser: r.MaxTicketsPerUser, Currency: domain.Currency,
		PriceFromCents: e.PriceFromCents, SeatsAvailable: e.SeatsAvailable, SeatsTotal: e.SeatsTotal,
		PublishedAt: tsPtr(r.PublishedAt), CreatedAt: ts(r.CreatedAt), UpdatedAt: ts(r.UpdatedAt),
	}
}

type categoryDTO struct {
	ID             int64   `json:"id"`
	EventID        int64   `json:"event_id"`
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	PriceCents     int64   `json:"price_cents"`
	Currency       string  `json:"currency"`
	ColorHex       string  `json:"color_hex"`
	SeatsAvailable int     `json:"seats_available"`
	SeatsTotal     int     `json:"seats_total"`
}

func newCategoryDTO(c events.Category) categoryDTO {
	return categoryDTO{
		ID: c.Row.ID, EventID: c.Row.EventID, Name: c.Row.Name, Description: c.Row.Description,
		PriceCents: c.Row.PriceCents, Currency: domain.Currency, ColorHex: c.Row.ColorHex,
		SeatsAvailable: c.SeatsAvailable, SeatsTotal: c.SeatsTotal,
	}
}

type mapCategoryDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	ColorHex   string `json:"color_hex"`
}

func newMapCategories(cats []store.EventCategory) []mapCategoryDTO {
	out := make([]mapCategoryDTO, 0, len(cats))
	for _, c := range cats {
		out = append(out, mapCategoryDTO{ID: c.ID, Name: c.Name, PriceCents: c.PriceCents, ColorHex: c.ColorHex})
	}
	return out
}

type mapVenueDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PlanWidth  int32  `json:"plan_width"`
	PlanHeight int32  `json:"plan_height"`
}

type mapStandDTO struct {
	ID             int64           `json:"id"`
	Code           string          `json:"code"`
	Name           string          `json:"name"`
	Tier           string          `json:"tier"`
	GateGroup      string          `json:"gate_group"`
	Polygon        json.RawMessage `json:"polygon"`
	SeatsTotal     int             `json:"seats_total"`
	SeatsAvailable int             `json:"seats_available"`
	PriceFromCents *int64          `json:"price_from_cents"`
}

type stadiumMapDTO struct {
	EventID  int64         `json:"event_id"`
	Venue    mapVenueDTO   `json:"venue"`
	Currency string        `json:"currency"`
	Stands   []mapStandDTO `json:"stands"`
}

func newStadiumMapDTO(m events.StadiumMap) stadiumMapDTO {
	out := stadiumMapDTO{
		EventID:  m.EventID,
		Venue:    mapVenueDTO{ID: m.Venue.ID, Name: m.Venue.Name, PlanWidth: m.Venue.PlanWidth, PlanHeight: m.Venue.PlanHeight},
		Currency: domain.Currency,
		Stands:   make([]mapStandDTO, 0, len(m.Stands)),
	}
	for _, st := range m.Stands {
		out.Stands = append(out.Stands, mapStandDTO{
			ID: st.Stand.ID, Code: st.Stand.Code, Name: st.Stand.Name, Tier: st.Stand.Tier,
			GateGroup: domain.GateGroup(st.Stand.Code, st.Stand.Tier), Polygon: compactJSON(st.Stand.Polygon),
			SeatsTotal: st.SeatsTotal, SeatsAvailable: st.SeatsAvailable, PriceFromCents: st.PriceFromCents,
		})
	}
	return out
}

type standHeaderDTO struct {
	ID        int64           `json:"id"`
	Code      string          `json:"code"`
	Name      string          `json:"name"`
	Tier      string          `json:"tier"`
	GateGroup string          `json:"gate_group"`
	Polygon   json.RawMessage `json:"polygon"`
}

type badgeDTO struct {
	CategoryID     int64 `json:"category_id"`
	SeatsAvailable int   `json:"seats_available"`
}

type mapBlockDTO struct {
	ID             int64           `json:"id"`
	Code           string          `json:"code"`
	Label          string          `json:"label"`
	Outline        json.RawMessage `json:"outline"`
	SeatsTotal     int             `json:"seats_total"`
	SeatsAvailable int             `json:"seats_available"`
	PriceFromCents *int64          `json:"price_from_cents"`
	Badges         []badgeDTO      `json:"badges"`
}

type standBlocksDTO struct {
	EventID    int64            `json:"event_id"`
	Stand      standHeaderDTO   `json:"stand"`
	Currency   string           `json:"currency"`
	Categories []mapCategoryDTO `json:"categories"`
	Blocks     []mapBlockDTO    `json:"blocks"`
}

func newStandBlocksDTO(sb events.StandBlocks) standBlocksDTO {
	st := sb.Stand
	out := standBlocksDTO{
		EventID: sb.EventID,
		Stand: standHeaderDTO{
			ID: st.ID, Code: st.Code, Name: st.Name, Tier: st.Tier,
			GateGroup: domain.GateGroup(st.Code, st.Tier), Polygon: compactJSON(st.Polygon),
		},
		Currency:   domain.Currency,
		Categories: newMapCategories(sb.Categories),
		Blocks:     make([]mapBlockDTO, 0, len(sb.Blocks)),
	}
	for _, b := range sb.Blocks {
		badges := make([]badgeDTO, 0, len(b.Badges))
		for _, bd := range b.Badges {
			badges = append(badges, badgeDTO{CategoryID: bd.CategoryID, SeatsAvailable: bd.SeatsAvailable})
		}
		out.Blocks = append(out.Blocks, mapBlockDTO{
			ID: b.Block.ID, Code: b.Block.Code, Label: b.Block.Label, Outline: compactJSON(b.Block.Outline),
			SeatsTotal: b.SeatsTotal, SeatsAvailable: b.SeatsAvailable, PriceFromCents: b.PriceFromCents, Badges: badges,
		})
	}
	return out
}

type boundsDTO struct {
	MinX float64 `json:"min_x"`
	MinY float64 `json:"min_y"`
	MaxX float64 `json:"max_x"`
	MaxY float64 `json:"max_y"`
}

func newBoundsDTO(b seatmap.Bounds) boundsDTO {
	return boundsDTO{MinX: b.MinX, MinY: b.MinY, MaxX: b.MaxX, MaxY: b.MaxY}
}

// appendNumber writes a coordinate the shortest way that reads back the same.
func appendNumber(b []byte, v float64) []byte {
	return strconv.AppendFloat(b, v, 'f', -1, 64)
}

// seatTuple is SeatTuple in the contract:
// [event_seat_id, seat_number, x, y, type_code, category_index, status_code].
// It is written by hand so 1,300 seats cost one small allocation each and no key names.
type seatTuple events.Seat

func (s seatTuple) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 48)
	b = append(b, '[')
	b = strconv.AppendInt(b, s.EventSeatID, 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(s.Number), 10)
	b = append(b, ',')
	b = appendNumber(b, s.X)
	b = append(b, ',')
	b = appendNumber(b, s.Y)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(s.TypeCode), 10)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(s.CategoryIndex), 10)
	b = append(b, ',', '"')
	b = append(b, s.Code...)
	b = append(b, '"', ']')
	return b, nil
}

// seatChangeTuple is SeatChangeTuple in the contract: [event_seat_id, status_code].
type seatChangeTuple events.SeatChange

func (s seatChangeTuple) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 16)
	b = append(b, '[')
	b = strconv.AppendInt(b, s.EventSeatID, 10)
	b = append(b, ',', '"')
	b = append(b, s.Code...)
	b = append(b, '"', ']')
	return b, nil
}

type seatRowDTO struct {
	Label string      `json:"label"`
	Seats []seatTuple `json:"seats"`
}

type seatBlockDTO struct {
	ID          int64     `json:"id"`
	StandID     int64     `json:"stand_id"`
	Code        string    `json:"code"`
	Label       string    `json:"label"`
	RotationDeg float64   `json:"rotation_deg"`
	SeatPitchX  float64   `json:"seat_pitch_x"`
	SeatPitchY  float64   `json:"seat_pitch_y"`
	Bounds      boundsDTO `json:"bounds"`
}

type blockSeatsFullDTO struct {
	Kind                string           `json:"kind"`
	Marker              string           `json:"marker"`
	PollIntervalSeconds int              `json:"poll_interval_seconds"`
	Block               seatBlockDTO     `json:"block"`
	Categories          []mapCategoryDTO `json:"categories"`
	Rows                []seatRowDTO     `json:"rows"`
}

type blockSeatsDeltaDTO struct {
	Kind                string            `json:"kind"`
	Marker              string            `json:"marker"`
	PollIntervalSeconds int               `json:"poll_interval_seconds"`
	Changes             []seatChangeTuple `json:"changes"`
}

// newBlockSeatsDTO picks the full or the delta shape.
func newBlockSeatsDTO(bs events.BlockSeats) any {
	if !bs.Full {
		changes := make([]seatChangeTuple, 0, len(bs.Changes))
		for _, c := range bs.Changes {
			changes = append(changes, seatChangeTuple(c))
		}
		return blockSeatsDeltaDTO{Kind: "delta", Marker: bs.Marker, PollIntervalSeconds: bs.PollIntervalSeconds, Changes: changes}
	}
	b := bs.Block
	out := blockSeatsFullDTO{
		Kind: "full", Marker: bs.Marker, PollIntervalSeconds: bs.PollIntervalSeconds,
		Block: seatBlockDTO{
			ID: b.ID, StandID: b.StandID, Code: b.Code, Label: b.Label, RotationDeg: b.RotationDeg,
			SeatPitchX: b.SeatPitchX, SeatPitchY: b.SeatPitchY, Bounds: newBoundsDTO(bs.Bounds),
		},
		Categories: newMapCategories(bs.Categories),
		Rows:       make([]seatRowDTO, 0, len(bs.Rows)),
	}
	for _, r := range bs.Rows {
		seats := make([]seatTuple, 0, len(r.Seats))
		for _, st := range r.Seats {
			seats = append(seats, seatTuple(st))
		}
		out.Rows = append(out.Rows, seatRowDTO{Label: r.Label, Seats: seats})
	}
	return out
}

// --- Admin: venues, blocks, layout ------------------------------------------------------

type venueDTO struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	City       string `json:"city"`
	Capacity   int32  `json:"capacity"`
	PlanWidth  int32  `json:"plan_width"`
	PlanHeight int32  `json:"plan_height"`
}

func newVenueDTO(v store.Venue) venueDTO {
	return venueDTO{ID: v.ID, Name: v.Name, City: v.City, Capacity: v.Capacity, PlanWidth: v.PlanWidth, PlanHeight: v.PlanHeight}
}

type blockDTO struct {
	ID                 int64           `json:"id"`
	StandID            int64           `json:"stand_id"`
	Code               string          `json:"code"`
	Label              string          `json:"label"`
	OriginX            float64         `json:"origin_x"`
	OriginY            float64         `json:"origin_y"`
	RotationDeg        float64         `json:"rotation_deg"`
	ArcRadius          *float64        `json:"arc_radius"`
	SeatPitchX         float64         `json:"seat_pitch_x"`
	SeatPitchY         float64         `json:"seat_pitch_y"`
	RowCount           int32           `json:"row_count"`
	SeatsPerRow        int32           `json:"seats_per_row"`
	NumberingDirection string          `json:"numbering_direction"`
	SeatCount          int64           `json:"seat_count"`
	Outline            json.RawMessage `json:"outline"`
	HasInventory       bool            `json:"has_inventory"`
}

func newBlockDTO(b venue.Block) blockDTO {
	return blockDTO{
		ID: b.ID, StandID: b.StandID, Code: b.Code, Label: b.Label, OriginX: b.OriginX, OriginY: b.OriginY,
		RotationDeg: b.RotationDeg, ArcRadius: b.ArcRadius, SeatPitchX: b.SeatPitchX, SeatPitchY: b.SeatPitchY,
		RowCount: b.RowCount, SeatsPerRow: b.SeatsPerRow, NumberingDirection: b.NumberingDirection,
		SeatCount: b.SeatCount, Outline: compactJSON(b.Outline), HasInventory: b.HasInventory,
	}
}

type venueStandDTO struct {
	ID           int64           `json:"id"`
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	Tier         string          `json:"tier"`
	GateGroup    string          `json:"gate_group"`
	Polygon      json.RawMessage `json:"polygon"`
	DisplayOrder int32           `json:"display_order"`
	Blocks       []blockDTO      `json:"blocks"`
}

type venueDetailDTO struct {
	venueDTO
	SeatCount int64           `json:"seat_count"`
	Stands    []venueStandDTO `json:"stands"`
}

func newVenueDetailDTO(d venue.Detail) venueDetailDTO {
	out := venueDetailDTO{venueDTO: newVenueDTO(d.Venue), SeatCount: d.SeatCount, Stands: make([]venueStandDTO, 0, len(d.Stands))}
	for _, st := range d.Stands {
		blocks := make([]blockDTO, 0, len(st.Blocks))
		for _, b := range st.Blocks {
			blocks = append(blocks, newBlockDTO(b))
		}
		out.Stands = append(out.Stands, venueStandDTO{
			ID: st.ID, Code: st.Code, Name: st.Name, Tier: st.Tier, GateGroup: domain.GateGroup(st.Code, st.Tier),
			Polygon: compactJSON(st.Polygon), DisplayOrder: st.DisplayOrder, Blocks: blocks,
		})
	}
	return out
}

// physicalSeatTuple is PhysicalSeatTuple in the contract:
// [seat_id, seat_number, x, y, type_code, active]. seat_id is null in a dry run.
type physicalSeatTuple venue.LayoutSeat

func (s physicalSeatTuple) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 48)
	b = append(b, '[')
	if s.ID == nil {
		b = append(b, "null"...)
	} else {
		b = strconv.AppendInt(b, *s.ID, 10)
	}
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(s.Number), 10)
	b = append(b, ',')
	b = appendNumber(b, s.X)
	b = append(b, ',')
	b = appendNumber(b, s.Y)
	b = append(b, ',')
	b = strconv.AppendInt(b, int64(domain.SeatTypeCode(s.Type)), 10)
	if s.Active {
		b = append(b, ",1]"...)
	} else {
		b = append(b, ",0]"...)
	}
	return b, nil
}

type layoutRowDTO struct {
	ID       *int64              `json:"id"`
	Label    string              `json:"label"`
	RowIndex int                 `json:"row_index"`
	XOffset  float64             `json:"x_offset"`
	Seats    []physicalSeatTuple `json:"seats"`
}

type blockLayoutDTO struct {
	BlockID   int64           `json:"block_id"`
	DryRun    bool            `json:"dry_run"`
	SeatCount int             `json:"seat_count"`
	Outline   json.RawMessage `json:"outline"`
	Bounds    *boundsDTO      `json:"bounds"`
	Rows      []layoutRowDTO  `json:"rows"`
}

func newBlockLayoutDTO(l venue.Layout) blockLayoutDTO {
	out := blockLayoutDTO{
		BlockID: l.BlockID, DryRun: l.DryRun, SeatCount: l.SeatCount, Outline: compactJSON(l.Outline),
		Rows: make([]layoutRowDTO, 0, len(l.Rows)),
	}
	if l.Bounds != nil {
		b := newBoundsDTO(*l.Bounds)
		out.Bounds = &b
	}
	for _, r := range l.Rows {
		seats := make([]physicalSeatTuple, 0, len(r.Seats))
		for _, st := range r.Seats {
			seats = append(seats, physicalSeatTuple(st))
		}
		out.Rows = append(out.Rows, layoutRowDTO{ID: r.ID, Label: r.Label, RowIndex: r.Index, XOffset: r.XOffset, Seats: seats})
	}
	return out
}

type templateCategoryDTO struct {
	Name        string  `json:"name"`
	ColorHex    string  `json:"color_hex"`
	PriceCents  int64   `json:"price_cents"`
	Description *string `json:"description"`
}

type templateDTO struct {
	ID                   int64                 `json:"id"`
	VenueID              int64                 `json:"venue_id"`
	Name                 string                `json:"name"`
	Categories           []templateCategoryDTO `json:"categories"`
	BlockAssignmentCount int                   `json:"block_assignment_count"`
	RowAssignmentCount   int                   `json:"row_assignment_count"`
	CreatedAt            string                `json:"created_at"`
}

func newTemplateDTO(t events.Template) templateDTO {
	out := templateDTO{
		ID: t.Row.ID, VenueID: t.Row.VenueID, Name: t.Row.Name, Categories: make([]templateCategoryDTO, 0, len(t.Categories)),
		BlockAssignmentCount: t.BlockAssignments, RowAssignmentCount: t.RowAssignments, CreatedAt: ts(t.Row.CreatedAt),
	}
	for _, c := range t.Categories {
		dto := templateCategoryDTO{Name: c.Name, ColorHex: c.ColorHex, PriceCents: c.PriceCents}
		if c.Description != "" {
			d := c.Description
			dto.Description = &d
		}
		out.Categories = append(out.Categories, dto)
	}
	return out
}

// --- Admin: readiness, publish, cancel, painting -----------------------------------------

type readinessDTO struct {
	EventID           int64                     `json:"event_id"`
	EventStatus       domain.EventStatus        `json:"event_status"`
	Ready             bool                      `json:"ready"`
	SeatTotal         int                       `json:"seat_total"`
	SeatsWithCategory int                       `json:"seats_with_category"`
	Problems          []domain.ReadinessProblem `json:"problems"`
}

type publishResultDTO struct {
	Event            eventDTO `json:"event"`
	SeatsOnSale      int      `json:"seats_on_sale"`
	AlreadyPublished bool     `json:"already_published"`
}

type cancelResultDTO struct {
	Event             eventDTO `json:"event"`
	TicketsVoided     int      `json:"tickets_voided"`
	BookingsCancelled int      `json:"bookings_cancelled"`
	RefundsQueued     int      `json:"refunds_queued"`
	RefundTotalCents  int64    `json:"refund_total_cents"`
	HoldsReleased     int      `json:"holds_released"`
}

type blockAssignmentDTO struct {
	BlockID    int64 `json:"block_id"`
	CategoryID int64 `json:"category_id"`
}

type rowAssignmentDTO struct {
	RowID      int64 `json:"row_id"`
	BlockID    int64 `json:"block_id"`
	CategoryID int64 `json:"category_id"`
}

type assignmentsDTO struct {
	EventID int64                `json:"event_id"`
	Blocks  []blockAssignmentDTO `json:"blocks"`
	Rows    []rowAssignmentDTO   `json:"rows"`
}

func newAssignmentsDTO(a events.Assignments) assignmentsDTO {
	out := assignmentsDTO{EventID: a.EventID, Blocks: make([]blockAssignmentDTO, 0, len(a.Blocks)), Rows: make([]rowAssignmentDTO, 0, len(a.Rows))}
	for _, b := range a.Blocks {
		out.Blocks = append(out.Blocks, blockAssignmentDTO{BlockID: b.BlockID, CategoryID: b.CategoryID})
	}
	for _, r := range a.Rows {
		out.Rows = append(out.Rows, rowAssignmentDTO{RowID: r.RowID, BlockID: r.BlockID, CategoryID: r.CategoryID})
	}
	return out
}

// --- Admin: sales -----------------------------------------------------------------------

type seatCountsDTO struct {
	Sold      int `json:"sold"`
	Held      int `json:"held"`
	Available int `json:"available"`
	Blocked   int `json:"blocked"`
	Total     int `json:"total"`
}

type salesCategoryDTO struct {
	CategoryID   int64  `json:"category_id"`
	Name         string `json:"name"`
	ColorHex     string `json:"color_hex"`
	PriceCents   int64  `json:"price_cents"`
	Sold         int    `json:"sold"`
	Held         int    `json:"held"`
	Available    int    `json:"available"`
	Blocked      int    `json:"blocked"`
	Total        int    `json:"total"`
	RevenueCents int64  `json:"revenue_cents"`
}

type salesBlockDTO struct {
	BlockID int64 `json:"block_id"`
	StandID int64 `json:"stand_id"`
	Sold    int   `json:"sold"`
	Held    int   `json:"held"`
	Total   int   `json:"total"`
}

type salesPointDTO struct {
	BucketStart  string `json:"bucket_start"`
	Bookings     int    `json:"bookings"`
	Tickets      int    `json:"tickets"`
	RevenueCents int64  `json:"revenue_cents"`
}

type salesSeriesDTO struct {
	Bucket string          `json:"bucket"`
	Points []salesPointDTO `json:"points"`
}

type salesSummaryDTO struct {
	EventID             int64              `json:"event_id"`
	GeneratedAt         string             `json:"generated_at"`
	Currency            string             `json:"currency"`
	RevenueCents        int64              `json:"revenue_cents"`
	Seats               seatCountsDTO      `json:"seats"`
	RefundRequiredCount int                `json:"refund_required_count"`
	RefundRequiredCents int64              `json:"refund_required_cents"`
	OpenAlertCount      int                `json:"open_alert_count"`
	Categories          []salesCategoryDTO `json:"categories"`
	Blocks              []salesBlockDTO    `json:"blocks"`
	Series              salesSeriesDTO     `json:"series"`
}

func newSalesSummaryDTO(s events.Sales) salesSummaryDTO {
	out := salesSummaryDTO{
		EventID: s.EventID, GeneratedAt: ts(s.GeneratedAt), Currency: domain.Currency, RevenueCents: s.RevenueCents,
		Seats: seatCountsDTO{
			Sold: s.Seats.Sold, Held: s.Seats.Held, Available: s.Seats.Available, Blocked: s.Seats.Blocked, Total: s.Seats.Total(),
		},
		RefundRequiredCount: s.RefundRequiredCount, RefundRequiredCents: s.RefundRequiredCents, OpenAlertCount: s.OpenAlertCount,
		Categories: make([]salesCategoryDTO, 0, len(s.Categories)),
		Blocks:     make([]salesBlockDTO, 0, len(s.Blocks)),
		Series:     salesSeriesDTO{Bucket: s.Bucket, Points: make([]salesPointDTO, 0, len(s.Points))},
	}
	for _, c := range s.Categories {
		out.Categories = append(out.Categories, salesCategoryDTO{
			CategoryID: c.Row.ID, Name: c.Row.Name, ColorHex: c.Row.ColorHex, PriceCents: c.Row.PriceCents,
			Sold: c.Counts.Sold, Held: c.Counts.Held, Available: c.Counts.Available, Blocked: c.Counts.Blocked,
			Total: c.Counts.Total(), RevenueCents: c.RevenueCents,
		})
	}
	for _, b := range s.Blocks {
		out.Blocks = append(out.Blocks, salesBlockDTO{BlockID: b.BlockID, StandID: b.StandID, Sold: b.Sold, Held: b.Held, Total: b.Total})
	}
	for _, p := range s.Points {
		out.Series.Points = append(out.Series.Points, salesPointDTO{
			BucketStart: ts(p.Start), Bookings: p.Bookings, Tickets: p.Tickets, RevenueCents: p.RevenueCents,
		})
	}
	return out
}

type paymentDTO struct {
	ID                int64   `json:"id"`
	BookingID         int64   `json:"booking_id"`
	BookingReference  string  `json:"booking_reference"`
	BookingStatus     string  `json:"booking_status"`
	Provider          string  `json:"provider"`
	Status            string  `json:"status"`
	AmountCents       int64   `json:"amount_cents"`
	Currency          string  `json:"currency"`
	Phone             string  `json:"phone"`
	MerchantRequestID *string `json:"merchant_request_id"`
	CheckoutRequestID *string `json:"checkout_request_id"`
	MpesaReceipt      *string `json:"mpesa_receipt"`
	FailureReason     *string `json:"failure_reason"`
	ResultCode        *int32  `json:"result_code"`
	ResultDesc        *string `json:"result_desc"`
	CreatedAt         string  `json:"created_at"`
	CompletedAt       *string `json:"completed_at"`
}

func newPaymentDTO(p store.Payment, booking store.Booking) paymentDTO {
	return paymentDTO{
		ID: p.ID, BookingID: p.BookingID, BookingReference: booking.Reference, BookingStatus: booking.Status,
		Provider: p.Provider, Status: p.Status, AmountCents: p.AmountCents, Currency: domain.Currency, Phone: p.PhoneE164,
		MerchantRequestID: p.MerchantRequestID, CheckoutRequestID: p.CheckoutRequestID, MpesaReceipt: p.MpesaReceipt,
		FailureReason: domain.PaymentFailureReason(p.Status, p.ResultCode),
		ResultCode:    p.ResultCode, ResultDesc: p.ResultDesc, CreatedAt: ts(p.CreatedAt), CompletedAt: tsPtr(p.CompletedAt),
	}
}

type refundDTO struct {
	ID               int64   `json:"id"`
	BookingID        int64   `json:"booking_id"`
	BookingReference string  `json:"booking_reference"`
	EventID          int64   `json:"event_id"`
	PaymentID        int64   `json:"payment_id"`
	AmountCents      int64   `json:"amount_cents"`
	Currency         string  `json:"currency"`
	Phone            string  `json:"phone"`
	MpesaReceipt     *string `json:"mpesa_receipt"`
	Reason           string  `json:"reason"`
	Status           string  `json:"status"`
	CreatedAt        string  `json:"created_at"`
	CompletedAt      *string `json:"completed_at"`
}

type bookingUserDTO struct {
	ID    int64   `json:"id"`
	Name  *string `json:"name"`
	Phone *string `json:"phone"`
}

type adminBookingDTO struct {
	ID            int64          `json:"id"`
	Reference     string         `json:"reference"`
	Status        string         `json:"status"`
	EventID       int64          `json:"event_id"`
	User          bookingUserDTO `json:"user"`
	SeatCount     int            `json:"seat_count"`
	SeatLabels    []string       `json:"seat_labels"`
	Currency      string         `json:"currency"`
	SubtotalCents int64          `json:"subtotal_cents"`
	FeeCents      int64          `json:"fee_cents"`
	TotalCents    int64          `json:"total_cents"`
	LatestPayment *paymentDTO    `json:"latest_payment"`
	Refund        *refundDTO     `json:"refund"`
	CreatedAt     string         `json:"created_at"`
	PaidAt        *string        `json:"paid_at"`
	UpdatedAt     string         `json:"updated_at"`
}

func newAdminBookingDTO(b events.AdminBooking) adminBookingDTO {
	r := b.Row
	out := adminBookingDTO{
		ID: r.ID, Reference: r.Reference, Status: r.Status, EventID: r.EventID,
		User:      bookingUserDTO{ID: r.UserID, Name: b.UserName, Phone: b.UserPhone},
		SeatCount: len(b.SeatLabels), SeatLabels: b.SeatLabels, Currency: domain.Currency,
		SubtotalCents: r.SubtotalCents, FeeCents: r.FeeCents, TotalCents: r.TotalCents,
		CreatedAt: ts(r.CreatedAt), PaidAt: tsPtr(r.PaidAt), UpdatedAt: ts(r.UpdatedAt),
	}
	if b.LatestPayment != nil {
		p := newPaymentDTO(*b.LatestPayment, r)
		out.LatestPayment = &p
	}
	if b.Refund != nil {
		rf := b.Refund.Refund
		out.Refund = &refundDTO{
			ID: rf.ID, BookingID: rf.BookingID, BookingReference: r.Reference, EventID: r.EventID, PaymentID: rf.PaymentID,
			AmountCents: rf.AmountCents, Currency: domain.Currency, Phone: b.Refund.PaymentPhone, MpesaReceipt: b.Refund.MpesaReceipt,
			Reason: rf.Reason, Status: rf.Status, CreatedAt: ts(rf.CreatedAt), CompletedAt: tsPtr(rf.CompletedAt),
		}
	}
	return out
}
