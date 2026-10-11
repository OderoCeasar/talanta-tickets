// Package venue holds the rules of the physical layout: venues, stands, blocks and the seats
// generated for a block. Geometry comes from internal/seatmap; SQL lives in internal/store.
package venue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// Service implements the layout rules. It is safe for concurrent use.
type Service struct {
	db DB
	q  *store.Queries
	// lockWait is how long a block delete or regenerate waits for a publish at the same venue
	// before giving up with BLOCK_HAS_INVENTORY.
	lockWait time.Duration
}

// NewService builds the service.
func NewService(db DB) *Service {
	return &Service{db: db, q: store.New(db), lockWait: 3 * time.Second}
}

// Block is a block with the two facts the editor needs beside its columns.
type Block struct {
	store.Block
	// SeatCount is the number of active seats.
	SeatCount int64
	// HasInventory is true when some event was published with the block.
	HasInventory bool
}

// Stand is a stand with its blocks, ordered by code.
type Stand struct {
	store.Stand
	Blocks []Block
}

// Detail is a venue's whole plan without seats.
type Detail struct {
	store.Venue
	SeatCount int64
	Stands    []Stand
}

// LayoutSeat is one physical seat of a layout. ID is nil in a preview.
type LayoutSeat struct {
	ID     *int64
	Number int
	X, Y   float64
	Type   string
	Active bool
}

// LayoutRow is one row of a layout, front row first. ID is nil in a preview.
type LayoutRow struct {
	ID      *int64
	Label   string
	Index   int
	XOffset float64
	Seats   []LayoutSeat
}

// Layout is a block's rows and seats, saved or previewed.
type Layout struct {
	BlockID   int64
	DryRun    bool
	SeatCount int
	Outline   json.RawMessage
	// Bounds is nil when the block has no seats.
	Bounds *seatmap.Bounds
	Rows   []LayoutRow
}

// List returns every venue, by name.
func (s *Service) List(ctx context.Context) ([]store.Venue, error) {
	venues, err := s.q.ListVenues(ctx)
	if err != nil {
		return nil, fmt.Errorf("list venues: %w", err)
	}
	return venues, nil
}

// Get returns a venue's stands and blocks.
func (s *Service) Get(ctx context.Context, venueID int64) (Detail, error) {
	v, err := s.q.GetVenue(ctx, venueID)
	if errors.Is(err, store.ErrNoRows) {
		return Detail{}, domain.ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("get venue %d: %w", venueID, err)
	}
	stands, err := s.q.ListStandsByVenue(ctx, venueID)
	if err != nil {
		return Detail{}, fmt.Errorf("list stands of venue %d: %w", venueID, err)
	}
	blocks, err := s.q.ListBlocksByVenue(ctx, venueID)
	if err != nil {
		return Detail{}, fmt.Errorf("list blocks of venue %d: %w", venueID, err)
	}
	counts, err := s.q.CountActiveSeatsByBlockForVenue(ctx, venueID)
	if err != nil {
		return Detail{}, fmt.Errorf("count seats of venue %d: %w", venueID, err)
	}
	used, err := s.q.ListBlockIDsWithInventoryByVenue(ctx, venueID)
	if err != nil {
		return Detail{}, fmt.Errorf("list used blocks of venue %d: %w", venueID, err)
	}

	seatCount := make(map[int64]int64, len(counts))
	for _, c := range counts {
		seatCount[c.BlockID] = c.Seats
	}
	hasInventory := make(map[int64]bool, len(used))
	for _, id := range used {
		hasInventory[id] = true
	}

	out := Detail{Venue: v, Stands: make([]Stand, 0, len(stands))}
	index := make(map[int64]int, len(stands))
	for _, st := range stands {
		index[st.ID] = len(out.Stands)
		out.Stands = append(out.Stands, Stand{Stand: st, Blocks: []Block{}})
	}
	for _, b := range blocks {
		i, ok := index[b.StandID]
		if !ok {
			continue
		}
		out.Stands[i].Blocks = append(out.Stands[i].Blocks, Block{Block: b, SeatCount: seatCount[b.ID], HasInventory: hasInventory[b.ID]})
		out.SeatCount += seatCount[b.ID]
	}
	return out, nil
}

// CreateBlockInput is a new block's placement.
type CreateBlockInput struct {
	VenueID     int64
	StandID     int64
	Code        string
	Label       string
	OriginX     float64
	OriginY     float64
	RotationDeg float64
	// ArcRadius nil means a straight block.
	ArcRadius  *float64
	SeatPitchX float64
	SeatPitchY float64
}

// CreateBlock places an empty block on a stand of the venue.
func (s *Service) CreateBlock(ctx context.Context, in CreateBlockInput) (Block, error) {
	stand, err := s.q.GetStand(ctx, in.StandID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && stand.VenueID != in.VenueID) {
		// Also covers "no such venue": a stand cannot belong to one.
		return Block{}, domain.ErrNotFound
	}
	if err != nil {
		return Block{}, fmt.Errorf("get stand %d: %w", in.StandID, err)
	}

	b, err := s.q.CreateBlock(ctx, store.CreateBlockParams{
		StandID: in.StandID, Code: in.Code, Label: in.Label,
		OriginX: in.OriginX, OriginY: in.OriginY, RotationDeg: in.RotationDeg, ArcRadius: in.ArcRadius,
		SeatPitchX: in.SeatPitchX, SeatPitchY: in.SeatPitchY,
		Outline: json.RawMessage("[]"), NumberingDirection: string(seatmap.LeftToRight),
	})
	if errors.Is(err, store.ErrNoRows) {
		// The insert is ON CONFLICT DO NOTHING: no row back means the code is taken.
		return Block{}, codeTaken(in.Code)
	}
	if err != nil {
		return Block{}, fmt.Errorf("create block %q: %w", in.Code, err)
	}
	return Block{Block: b}, nil
}

func codeTaken(code string) *domain.Error {
	return domain.AlreadyExists("code", fmt.Sprintf("This stand already has a block with the code %s.", code))
}

// UpdateBlockInput changes a block. A nil field is left as it is.
type UpdateBlockInput struct {
	Code        *string
	Label       *string
	OriginX     *float64
	OriginY     *float64
	RotationDeg *float64
	// ArcRadiusSet says the caller sent arc_radius; ArcRadius nil then means "make it straight".
	ArcRadiusSet bool
	ArcRadius    *float64
	SeatPitchX   *float64
	SeatPitchY   *float64
}

// placementFields lists, by JSON name, the placement fields the caller sent.
func (in UpdateBlockInput) placementFields() []string {
	var out []string
	add := func(set bool, name string) {
		if set {
			out = append(out, name)
		}
	}
	add(in.OriginX != nil, "origin_x")
	add(in.OriginY != nil, "origin_y")
	add(in.RotationDeg != nil, "rotation_deg")
	add(in.ArcRadiusSet, "arc_radius")
	add(in.SeatPitchX != nil, "seat_pitch_x")
	add(in.SeatPitchY != nil, "seat_pitch_y")
	return out
}

// UpdateBlock renames or moves a block. Moving recomputes the outline and every seat's
// position; seat ids, rows and numbering stay, so inventory and tickets are untouched.
func (s *Service) UpdateBlock(ctx context.Context, blockID int64, in UpdateBlockInput) (Block, error) {
	var out Block
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		info, err := q.GetBlockWithVenue(ctx, blockID)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get block %d: %w", blockID, err)
		}
		b, err := q.LockBlock(ctx, blockID)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock block %d: %w", blockID, err)
		}

		if in.Code != nil {
			b.Code = *in.Code
		}
		if in.Label != nil {
			b.Label = *in.Label
		}
		moved := in.placementFields()
		if in.OriginX != nil {
			b.OriginX = *in.OriginX
		}
		if in.OriginY != nil {
			b.OriginY = *in.OriginY
		}
		if in.RotationDeg != nil {
			b.RotationDeg = *in.RotationDeg
		}
		if in.ArcRadiusSet {
			b.ArcRadius = in.ArcRadius
		}
		if in.SeatPitchX != nil {
			b.SeatPitchX = *in.SeatPitchX
		}
		if in.SeatPitchY != nil {
			b.SeatPitchY = *in.SeatPitchY
		}

		if len(moved) > 0 && b.RowCount > 0 {
			outline, err := moveSeats(ctx, q, b, info.PlanWidth, info.PlanHeight, moved)
			if err != nil {
				return err
			}
			b.Outline = outline
		}

		updated, err := q.UpdateBlock(ctx, store.UpdateBlockParams{
			ID: b.ID, Code: b.Code, Label: b.Label, OriginX: b.OriginX, OriginY: b.OriginY,
			RotationDeg: b.RotationDeg, ArcRadius: b.ArcRadius,
			SeatPitchX: b.SeatPitchX, SeatPitchY: b.SeatPitchY, Outline: b.Outline,
		})
		if store.IsUniqueViolation(err, "") {
			return codeTaken(b.Code)
		}
		if err != nil {
			return fmt.Errorf("update block %d: %w", blockID, err)
		}
		out, err = decorate(ctx, q, updated, info.VenueID)
		return err
	})
	return out, err
}

// moveSeats regenerates the block's geometry from its new placement and writes each seat's
// new position. It returns the new outline.
func moveSeats(ctx context.Context, q *store.Queries, b store.Block, planW, planH int32, moved []string) (json.RawMessage, error) {
	rows, err := q.ListRowsByBlock(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("list rows of block %d: %w", b.ID, err)
	}
	seats, err := q.ListSeatsByBlock(ctx, b.ID)
	if err != nil {
		return nil, fmt.Errorf("list seats of block %d: %w", b.ID, err)
	}
	if len(rows) != int(b.RowCount) {
		return nil, fmt.Errorf("block %d says %d rows but has %d", b.ID, b.RowCount, len(rows))
	}

	params := paramsOf(b)
	params.Rows = int(b.RowCount)
	params.SeatsPerRow = int(b.SeatsPerRow)
	params.Numbering = seatmap.Direction(b.NumberingDirection)
	offsets := make([]float64, len(rows))
	rowIndex := make(map[int64]int, len(rows))
	for i, r := range rows {
		offsets[i] = r.XOffset
		rowIndex[r.ID] = int(r.RowIndex)
	}
	params.RowOffsets = offsets

	layout, err := seatmap.Generate(params)
	if err != nil {
		return nil, paramError(err, moved)
	}
	if err := insidePlan(layout, planW, planH, moved); err != nil {
		return nil, err
	}

	type key struct{ row, number int }
	pos := make(map[key]seatmap.Seat, layout.SeatCount())
	for _, r := range layout.Rows {
		for _, st := range r.Seats {
			pos[key{r.Index, st.Number}] = st
		}
	}
	arg := store.UpdateSeatPositionsParams{
		Ids: make([]int64, 0, len(seats)), Xs: make([]float64, 0, len(seats)), Ys: make([]float64, 0, len(seats)),
	}
	for _, st := range seats {
		p, ok := pos[key{rowIndex[st.RowID], int(st.SeatNumber)}]
		if !ok {
			return nil, fmt.Errorf("block %d: seat %d has no place in the regenerated layout", b.ID, st.ID)
		}
		arg.Ids = append(arg.Ids, st.ID)
		arg.Xs = append(arg.Xs, p.X)
		arg.Ys = append(arg.Ys, p.Y)
	}
	if _, err := q.UpdateSeatPositions(ctx, arg); err != nil {
		return nil, fmt.Errorf("move seats of block %d: %w", b.ID, err)
	}
	outline, err := json.Marshal(layout.Outline)
	if err != nil {
		return nil, fmt.Errorf("encode outline of block %d: %w", b.ID, err)
	}
	return outline, nil
}

func paramsOf(b store.Block) seatmap.BlockParams {
	p := seatmap.BlockParams{
		OriginX: b.OriginX, OriginY: b.OriginY, RotationDeg: b.RotationDeg,
		PitchX: b.SeatPitchX, PitchY: b.SeatPitchY,
	}
	if b.ArcRadius != nil {
		p.ArcRadius = *b.ArcRadius
	}
	return p
}

// paramError turns a seatmap validation failure into VALIDATION_FAILED. The message is put
// on the request field that seatmap blames when the request has it, else on `fallback`.
func paramError(err error, fallback []string) error {
	var pe *seatmap.ParamError
	if !errors.As(err, &pe) {
		return fmt.Errorf("generate seats: %w", err)
	}
	message := strings.ToUpper(pe.Reason[:1]) + pe.Reason[1:] + "."
	fields := fallback
	for _, f := range fallback {
		if f == pe.Field || (pe.Field == "seat_pitch" && strings.HasPrefix(f, "seat_pitch")) {
			fields = []string{f}
			break
		}
	}
	errs := make([]domain.FieldError, 0, len(fields))
	for _, f := range fields {
		errs = append(errs, domain.FieldError{Field: f, Rule: "layout", Message: message})
	}
	return domain.Invalid(errs...)
}

// insidePlan refuses a layout whose outline leaves the venue's coordinate space
// [0, plan_width] × [0, plan_height]: the clients draw that space and nothing outside it.
func insidePlan(layout seatmap.Layout, planW, planH int32, fields []string) error {
	box, ok := seatmap.BoundsOf(layout.Outline)
	if !ok || box.Within(float64(planW), float64(planH)) {
		return nil
	}
	message := fmt.Sprintf(
		"The block would reach outside the venue plan: it spans x %s to %s and y %s to %s, and the plan is %d wide and %d high.",
		num(box.MinX), num(box.MaxX), num(box.MinY), num(box.MaxY), planW, planH)
	errs := make([]domain.FieldError, 0, len(fields))
	for _, f := range fields {
		errs = append(errs, domain.FieldError{Field: f, Rule: "plan_bounds", Message: message})
	}
	return domain.Invalid(errs...)
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// decorate adds the seat count and the inventory flag to a block.
func decorate(ctx context.Context, q *store.Queries, b store.Block, venueID int64) (Block, error) {
	n, err := q.CountActiveSeatsByBlock(ctx, b.ID)
	if err != nil {
		return Block{}, fmt.Errorf("count seats of block %d: %w", b.ID, err)
	}
	events, err := q.ListEventIDsWithInventoryForBlock(ctx, store.ListEventIDsWithInventoryForBlockParams{VenueID: venueID, BlockID: b.ID})
	if err != nil {
		return Block{}, fmt.Errorf("list events using block %d: %w", b.ID, err)
	}
	return Block{Block: b, SeatCount: n, HasInventory: len(events) > 0}, nil
}

// lockForLayoutChange takes the locks a destructive layout change needs, in a fixed order
// (venue, then block), and refuses the change when any event has inventory for the block.
func (s *Service) lockForLayoutChange(ctx context.Context, tx pgx.Tx, blockID int64) (store.GetBlockWithVenueRow, error) {
	q := store.New(tx)
	info, err := q.GetBlockWithVenue(ctx, blockID)
	if errors.Is(err, store.ErrNoRows) {
		return info, domain.ErrNotFound
	}
	if err != nil {
		return info, fmt.Errorf("get block %d: %w", blockID, err)
	}

	// A publish at this venue holds the venue row for as long as it copies seats, which can
	// be most of a minute. Waiting that long would run into the statement timeout and look
	// like a server fault, so the wait is short and ends in an answer the editor can show.
	wait := strconv.FormatInt(s.lockWait.Milliseconds(), 10)
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', $1, true)", wait); err != nil {
		return info, fmt.Errorf("set lock_timeout: %w", err)
	}
	if _, err := q.LockVenueForLayoutChange(ctx, info.VenueID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
			return info, domain.BlockHasInventory(nil, "An event at this venue is being published right now. Try again in a minute.")
		}
		return info, fmt.Errorf("lock venue %d: %w", info.VenueID, err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('lock_timeout', '0', true)"); err != nil {
		return info, fmt.Errorf("reset lock_timeout: %w", err)
	}

	info.Block, err = q.LockBlock(ctx, blockID)
	if errors.Is(err, store.ErrNoRows) {
		return info, domain.ErrNotFound
	}
	if err != nil {
		return info, fmt.Errorf("lock block %d: %w", blockID, err)
	}

	events, err := q.ListEventIDsWithInventoryForBlock(ctx, store.ListEventIDsWithInventoryForBlockParams{VenueID: info.VenueID, BlockID: blockID})
	if err != nil {
		return info, fmt.Errorf("list events using block %d: %w", blockID, err)
	}
	if len(events) > 0 {
		return info, domain.BlockHasInventory(events, fmt.Sprintf(
			"Block %s is part of a published event and can no longer be deleted or regenerated.", info.Block.Code))
	}
	return info, nil
}

// DeleteBlock removes a block with its rows and seats, unless an event has inventory for it.
func (s *Service) DeleteBlock(ctx context.Context, blockID int64) error {
	return store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		info, err := s.lockForLayoutChange(ctx, tx, blockID)
		if err != nil {
			return err
		}
		q := store.New(tx)
		if _, err := q.DeleteBlock(ctx, blockID); err != nil {
			return fmt.Errorf("delete block %d: %w", blockID, err)
		}
		if _, err := q.SetVenueCapacityFromSeats(ctx, info.VenueID); err != nil {
			return fmt.Errorf("set capacity of venue %d: %w", info.VenueID, err)
		}
		return nil
	})
}

// GenerateInput is the grid to generate for a block.
type GenerateInput struct {
	Rows        int
	SeatsPerRow int
	Labels      seatmap.LabelScheme
	Numbering   seatmap.Direction
	// RowOffsets is empty or has one entry per row.
	RowOffsets []float64
	DryRun     bool
}

// generateFields are the request fields a bad layout is blamed on when seatmap does not
// name one of them.
var generateFields = []string{"rows", "seats_per_row"}

// GenerateSeats builds a block's rows and seats with internal/seatmap. A dry run returns the
// preview and writes nothing. A real run replaces the block's rows and seats in one
// transaction, keeps venues.capacity right, and is refused with BLOCK_HAS_INVENTORY once
// any event has inventory for the block.
func (s *Service) GenerateSeats(ctx context.Context, blockID int64, in GenerateInput) (Layout, error) {
	info, err := s.q.GetBlockWithVenue(ctx, blockID)
	if errors.Is(err, store.ErrNoRows) {
		return Layout{}, domain.ErrNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("get block %d: %w", blockID, err)
	}
	if n := len(in.RowOffsets); n != 0 && n != in.Rows {
		return Layout{}, domain.InvalidField("row_offsets", "len", fmt.Sprintf("Give one offset per row (%d), or none.", in.Rows))
	}

	build := func(b store.Block) (seatmap.Layout, error) {
		params := paramsOf(b)
		params.Rows, params.SeatsPerRow = in.Rows, in.SeatsPerRow
		params.Numbering, params.Labels = in.Numbering, in.Labels
		if len(in.RowOffsets) > 0 {
			params.RowOffsets = in.RowOffsets
		}
		layout, err := seatmap.Generate(params)
		if err != nil {
			return layout, paramError(err, append([]string{"row_offsets", "numbering_direction", "row_label_scheme"}, generateFields...))
		}
		return layout, insidePlan(layout, info.PlanWidth, info.PlanHeight, generateFields)
	}

	if in.DryRun {
		layout, err := build(info.Block)
		if err != nil {
			return Layout{}, err
		}
		return previewLayout(info.Block, layout)
	}

	var out Layout
	err = store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		locked, err := s.lockForLayoutChange(ctx, tx, blockID)
		if err != nil {
			return err
		}
		// Generated from the block as it is now, under its lock, not as it was read above.
		layout, err := build(locked.Block)
		if err != nil {
			return err
		}
		q := store.New(tx)
		if _, err := q.DeleteRowsByBlock(ctx, blockID); err != nil {
			return fmt.Errorf("delete rows of block %d: %w", blockID, err)
		}

		rows := make([]store.CopyRowsParams, 0, len(layout.Rows))
		for _, r := range layout.Rows {
			rows = append(rows, store.CopyRowsParams{
				BlockID: blockID, Label: r.Label, RowIndex: int32(r.Index), SeatCount: int32(len(r.Seats)), XOffset: r.XOffset,
			})
		}
		if _, err := q.CopyRows(ctx, rows); err != nil {
			return fmt.Errorf("copy rows of block %d: %w", blockID, err)
		}
		// COPY returns no ids, so the rows are read back to give each seat its row_id.
		saved, err := q.ListRowsByBlock(ctx, blockID)
		if err != nil {
			return fmt.Errorf("read back rows of block %d: %w", blockID, err)
		}
		if len(saved) != len(layout.Rows) {
			return fmt.Errorf("block %d: copied %d rows, read back %d", blockID, len(layout.Rows), len(saved))
		}
		seats := make([]store.CopySeatsParams, 0, layout.SeatCount())
		for i, r := range layout.Rows {
			for _, st := range r.Seats {
				seats = append(seats, store.CopySeatsParams{
					RowID: saved[i].ID, BlockID: blockID, SeatNumber: int32(st.Number), Label: st.Label,
					X: st.X, Y: st.Y, SeatType: "STANDARD", IsActive: true,
				})
			}
		}
		if _, err := q.CopySeats(ctx, seats); err != nil {
			return fmt.Errorf("copy seats of block %d: %w", blockID, err)
		}

		outline, err := json.Marshal(layout.Outline)
		if err != nil {
			return fmt.Errorf("encode outline of block %d: %w", blockID, err)
		}
		block, err := q.SetBlockLayout(ctx, store.SetBlockLayoutParams{
			ID: blockID, Outline: outline, RowCount: int32(in.Rows), SeatsPerRow: int32(in.SeatsPerRow),
			NumberingDirection: string(in.Numbering),
		})
		if err != nil {
			return fmt.Errorf("save layout of block %d: %w", blockID, err)
		}
		if _, err := q.SetVenueCapacityFromSeats(ctx, locked.VenueID); err != nil {
			return fmt.Errorf("set capacity of venue %d: %w", locked.VenueID, err)
		}
		out, err = savedLayout(ctx, q, block)
		return err
	})
	return out, err
}

// BlockLayout returns a block's saved rows and seats.
func (s *Service) BlockLayout(ctx context.Context, blockID int64) (Layout, error) {
	b, err := s.q.GetBlock(ctx, blockID)
	if errors.Is(err, store.ErrNoRows) {
		return Layout{}, domain.ErrNotFound
	}
	if err != nil {
		return Layout{}, fmt.Errorf("get block %d: %w", blockID, err)
	}
	return savedLayout(ctx, s.q, b)
}

func previewLayout(b store.Block, layout seatmap.Layout) (Layout, error) {
	outline, err := json.Marshal(layout.Outline)
	if err != nil {
		return Layout{}, fmt.Errorf("encode outline of block %d: %w", b.ID, err)
	}
	out := Layout{BlockID: b.ID, DryRun: true, SeatCount: layout.SeatCount(), Outline: outline, Rows: make([]LayoutRow, 0, len(layout.Rows))}
	points := make([]seatmap.Point, 0, layout.SeatCount())
	for _, r := range layout.Rows {
		row := LayoutRow{Label: r.Label, Index: r.Index, XOffset: r.XOffset, Seats: make([]LayoutSeat, 0, len(r.Seats))}
		for _, st := range r.Seats {
			row.Seats = append(row.Seats, LayoutSeat{Number: st.Number, X: st.X, Y: st.Y, Type: "STANDARD", Active: true})
			points = append(points, seatmap.Point{X: st.X, Y: st.Y})
		}
		out.Rows = append(out.Rows, row)
	}
	if box, ok := seatmap.SeatBounds(points, b.SeatPitchX, b.SeatPitchY); ok {
		out.Bounds = &box
	}
	return out, nil
}

func savedLayout(ctx context.Context, q *store.Queries, b store.Block) (Layout, error) {
	rows, err := q.ListRowsByBlock(ctx, b.ID)
	if err != nil {
		return Layout{}, fmt.Errorf("list rows of block %d: %w", b.ID, err)
	}
	seats, err := q.ListSeatsByBlock(ctx, b.ID)
	if err != nil {
		return Layout{}, fmt.Errorf("list seats of block %d: %w", b.ID, err)
	}
	out := Layout{BlockID: b.ID, Outline: b.Outline, Rows: make([]LayoutRow, 0, len(rows))}
	index := make(map[int64]int, len(rows))
	for _, r := range rows {
		id := r.ID
		index[r.ID] = len(out.Rows)
		out.Rows = append(out.Rows, LayoutRow{ID: &id, Label: r.Label, Index: int(r.RowIndex), XOffset: r.XOffset, Seats: []LayoutSeat{}})
	}
	points := make([]seatmap.Point, 0, len(seats))
	for _, st := range seats {
		i, ok := index[st.RowID]
		if !ok {
			continue
		}
		id := st.ID
		out.Rows[i].Seats = append(out.Rows[i].Seats, LayoutSeat{
			ID: &id, Number: int(st.SeatNumber), X: st.X, Y: st.Y, Type: st.SeatType, Active: st.IsActive,
		})
		points = append(points, seatmap.Point{X: st.X, Y: st.Y})
		if st.IsActive {
			out.SeatCount++
		}
	}
	if box, ok := seatmap.SeatBounds(points, b.SeatPitchX, b.SeatPitchY); ok {
		out.Bounds = &box
	}
	return out, nil
}
