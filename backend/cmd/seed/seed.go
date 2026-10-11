package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// seedLockKey is the advisory lock that makes two seeders run one after the other. Events and
// users are found by a lookup before they are inserted, and two concurrent runs would both
// find nothing.
const seedLockKey int64 = 0x74616c616e7461 // "talanta"

const templateName = "Talanta default"

// Demo logins. They are public by design (this file is in git), which is why Seed refuses to
// create them in production.
const (
	demoFanPhone       = "+254700000001"
	demoGatePhone      = "+254700000002"
	demoOrganiserEmail = "organiser@talanta.test"
	demoOrganiserPass  = "organiser-dev-password"
	demoAdminEmail     = "admin@talanta.test"
	demoAdminPass      = "admin-dev-password"
)

// nairobi is fixed at UTC+3 instead of loaded by name: Kenya has no daylight saving, and the
// slim container image the seeder runs in has no time zone database.
var nairobi = time.FixedZone("EAT", 3*60*60)

// seededTables are the tables Seed can write to, in the order they are reported.
var seededTables = []string{
	"venues", "stands", "blocks", "rows", "seats",
	"venue_templates",
	"events", "event_categories", "event_block_categories", "event_row_categories", "event_seats",
	"users",
}

// Options controls one run of the seeder.
type Options struct {
	// Production is true when APP_ENV=production. The demo users are not created then.
	Production bool
	// Now is the time demo event dates are worked out from.
	Now time.Time
}

// TableCount is how many rows a run added to a table and how many it holds afterwards.
type TableCount struct {
	Table    string
	Inserted int64
	Total    int64
}

// Step is one timed part of a run.
type Step struct {
	Name     string
	Duration time.Duration
}

// Result is what a run did.
type Result struct {
	Tables []TableCount
	Steps  []Step
	// Layout is the time spent generating and inserting stands, blocks, rows and seats.
	Layout           time.Duration
	Total            time.Duration
	DemoUsersRefused bool
	// SeatChecksBatched is true when the seat load checked its foreign keys with one query
	// after the COPY instead of once per row. See copySeats.
	SeatChecksBatched bool
}

// Inserted is the number of rows the run added across all tables.
func (r Result) Inserted() int64 {
	var n int64
	for _, t := range r.Tables {
		n += t.Inserted
	}
	return n
}

// Count returns the counts for one table.
func (r Result) Count(table string) TableCount {
	for _, t := range r.Tables {
		if t.Table == table {
			return t
		}
	}
	return TableCount{Table: table}
}

// Seed brings the database to the demo state: the Talanta layout, the default template, two
// demo events and one user per role. Everything is found before it is created, so a second
// run adds nothing. It runs in one transaction; a failure leaves the database as it was.
func Seed(ctx context.Context, pool *pgxpool.Pool, opts Options) (Result, error) {
	var res Result
	started := time.Now()
	timed := func(name string, fn func() error) error {
		t := time.Now()
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		res.Steps = append(res.Steps, Step{Name: name, Duration: time.Since(t)})
		return nil
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin: %w", err)
	}
	// Rollback after a successful commit is a no-op.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", seedLockKey); err != nil {
		return res, fmt.Errorf("take seed lock: %w", err)
	}
	q := store.New(tx)

	before, err := countTables(ctx, tx)
	if err != nil {
		return res, err
	}

	var stands []standSpec
	if err := timed("generate layout in memory", func() error {
		stands, err = talantaLayout()
		return err
	}); err != nil {
		return res, err
	}

	var venueID int64
	if err := timed("insert venue, stands, blocks, rows, seats", func() error {
		venueID, res.SeatChecksBatched, err = insertLayout(ctx, tx, stands)
		return err
	}); err != nil {
		return res, err
	}
	res.Layout = res.Steps[0].Duration + res.Steps[1].Duration

	var templateID int64
	if err := timed("default template", func() error {
		templateID, err = ensureTemplate(ctx, q, venueID, stands)
		return err
	}); err != nil {
		return res, err
	}

	if err := timed("demo events and publish", func() error {
		return ensureEvents(ctx, tx, venueID, templateID, opts.Now)
	}); err != nil {
		return res, err
	}

	if opts.Production {
		res.DemoUsersRefused = true
	} else if err := timed("demo users", func() error {
		return ensureUsers(ctx, q)
	}); err != nil {
		return res, err
	}

	after, err := countTables(ctx, tx)
	if err != nil {
		return res, err
	}
	for _, table := range seededTables {
		res.Tables = append(res.Tables, TableCount{
			Table: table, Inserted: after[table] - before[table], Total: after[table],
		})
	}

	if err := timed("commit", func() error { return tx.Commit(ctx) }); err != nil {
		return res, err
	}

	if res.Inserted() > 0 {
		// A bulk load leaves the planner with no statistics until autovacuum gets round to
		// it, and until then it may pick a sequential scan of event_seats.
		if err := timed("analyze", func() error {
			_, err := pool.Exec(ctx, "ANALYZE seats, rows, blocks, event_seats")
			return err
		}); err != nil {
			return res, err
		}
	}

	res.Total = time.Since(started)
	return res, nil
}

func countTables(ctx context.Context, tx pgx.Tx) (map[string]int64, error) {
	counts := make(map[string]int64, len(seededTables))
	for _, table := range seededTables {
		var n int64
		// The names come from the fixed list above, never from input.
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", table, err)
		}
		counts[table] = n
	}
	return counts, nil
}

// insertLayout writes the venue. A stand or block that already exists is left exactly as it
// is, and only blocks created by this call get rows and seats, so a second run copies nothing.
func insertLayout(ctx context.Context, tx pgx.Tx, stands []standSpec) (venueID int64, batched bool, err error) {
	q := store.New(tx)
	venue, err := q.CreateVenue(ctx, store.CreateVenueParams{
		Name: venueName, City: venueCity, PlanWidth: planWidth, PlanHeight: planHeight,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		venue, err = q.GetVenueByName(ctx, venueName)
	}
	if err != nil {
		return 0, false, fmt.Errorf("venue: %w", err)
	}

	existing, err := q.ListStandsByVenue(ctx, venue.ID)
	if err != nil {
		return 0, false, fmt.Errorf("list stands: %w", err)
	}
	standIDs := map[string]int64{}
	for _, st := range existing {
		standIDs[st.Code+"-"+st.Tier] = st.ID
	}

	type newBlock struct {
		id   int64
		spec blockSpec
	}
	var created []newBlock

	for _, st := range stands {
		standID, ok := standIDs[st.Code+"-"+st.Tier]
		if !ok {
			polygon, err := json.Marshal(st.Polygon)
			if err != nil {
				return 0, false, fmt.Errorf("stand %s %s polygon: %w", st.Code, st.Tier, err)
			}
			row, err := q.CreateStand(ctx, store.CreateStandParams{
				VenueID: venue.ID, Code: st.Code, Name: st.Name, Tier: st.Tier,
				Polygon: polygon, DisplayOrder: st.Order,
			})
			if err != nil {
				return 0, false, fmt.Errorf("stand %s %s: %w", st.Code, st.Tier, err)
			}
			standID = row.ID
		}

		for _, b := range st.Blocks {
			outline, err := json.Marshal(b.Layout.Outline)
			if err != nil {
				return 0, false, fmt.Errorf("block %s outline: %w", b.Code, err)
			}
			var arc *float64
			if b.Params.ArcRadius > 0 {
				arc = &b.Params.ArcRadius
			}
			row, err := q.CreateBlock(ctx, store.CreateBlockParams{
				StandID: standID, Code: b.Code, Label: b.Label,
				OriginX: b.Params.OriginX, OriginY: b.Params.OriginY,
				RotationDeg: b.Params.RotationDeg, ArcRadius: arc,
				SeatPitchX: b.Params.PitchX, SeatPitchY: b.Params.PitchY,
				Outline:            outline,
				RowCount:           int32(b.Params.Rows),
				SeatsPerRow:        int32(b.Params.SeatsPerRow),
				NumberingDirection: string(b.Params.Numbering),
			})
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already there, with its seats
			}
			if err != nil {
				return 0, false, fmt.Errorf("block %s: %w", b.Code, err)
			}
			created = append(created, newBlock{id: row.ID, spec: b})
		}
	}
	if len(created) == 0 {
		return venue.ID, false, nil
	}

	var rows []store.CopyRowsParams
	blockIDs := make([]int64, 0, len(created))
	seatTotal := 0
	for _, nb := range created {
		blockIDs = append(blockIDs, nb.id)
		for _, r := range nb.spec.Layout.Rows {
			rows = append(rows, store.CopyRowsParams{
				BlockID: nb.id, Label: r.Label, RowIndex: int32(r.Index),
				SeatCount: int32(len(r.Seats)), XOffset: r.XOffset,
			})
			seatTotal += len(r.Seats)
		}
	}
	if _, err := q.CopyRows(ctx, rows); err != nil {
		return 0, false, fmt.Errorf("copy rows: %w", err)
	}

	// COPY returns no ids, so the new rows are read back to give each seat its row_id.
	inserted, err := q.ListRowsByBlocks(ctx, blockIDs)
	if err != nil {
		return 0, false, fmt.Errorf("read back rows: %w", err)
	}
	type rowKey struct {
		block int64
		index int32
	}
	rowIDs := make(map[rowKey]int64, len(inserted))
	for _, r := range inserted {
		rowIDs[rowKey{r.BlockID, r.RowIndex}] = r.ID
	}

	seats := make([]store.CopySeatsParams, 0, seatTotal)
	for _, nb := range created {
		for _, r := range nb.spec.Layout.Rows {
			rowID, ok := rowIDs[rowKey{nb.id, int32(r.Index)}]
			if !ok {
				return 0, false, fmt.Errorf("block %s row %s was not inserted", nb.spec.Code, r.Label)
			}
			for i, s := range r.Seats {
				seatType, active := nb.spec.seatKind(r.Index, i)
				seats = append(seats, store.CopySeatsParams{
					RowID: rowID, BlockID: nb.id, SeatNumber: int32(s.Number), Label: s.Label,
					X: s.X, Y: s.Y, SeatType: seatType, IsActive: active,
				})
			}
		}
	}
	batched, err = copySeats(ctx, tx, seats, blockIDs)
	if err != nil {
		return 0, false, err
	}

	if _, err := q.SetVenueCapacityFromSeats(ctx, venue.ID); err != nil {
		return 0, false, fmt.Errorf("set capacity: %w", err)
	}
	return venue.ID, batched, nil
}

// copySeats loads the seats of blocks created earlier in this transaction.
//
// Every seat has two foreign keys, and Postgres checks each with its own index lookup: about
// 120,000 lookups, which is most of the load time (and several times slower again on the
// Alpine image, whose allocator makes starting an index scan expensive). When the role is
// allowed to, the per-row checks are switched off for the COPY and replaced by one query that
// checks the same thing for all rows at once; anything wrong fails the whole seed.
//
// This is sound only because the parents were inserted by this same transaction: no other
// session can see the new blocks and rows yet, so none can delete one while the checks are
// off. It must not be copied to a load whose parent rows are already committed. Without the
// privilege the COPY runs with the ordinary checks, slower and just as correct.
func copySeats(ctx context.Context, tx pgx.Tx, seats []store.CopySeatsParams, newBlockIDs []int64) (batched bool, err error) {
	err = tx.QueryRow(ctx, `
		SELECT current_setting('is_superuser') = 'on'
		    OR has_parameter_privilege(current_user, 'session_replication_role', 'SET')`).Scan(&batched)
	if err != nil {
		return false, fmt.Errorf("check bulk load privilege: %w", err)
	}

	if batched {
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
			return false, fmt.Errorf("switch off per-row checks: %w", err)
		}
	}
	_, copyErr := store.New(tx).CopySeats(ctx, seats)
	if batched {
		// Back on before anything else runs, whether or not the COPY worked.
		if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = origin"); err != nil && copyErr == nil {
			return false, fmt.Errorf("switch per-row checks back on: %w", err)
		}
	}
	if copyErr != nil {
		return false, fmt.Errorf("copy seats: %w", copyErr)
	}
	if !batched {
		return false, nil
	}

	// Stricter than the two foreign keys: the row must exist and belong to the seat's block,
	// and every copied seat must be in one of the new blocks.
	var total, orphans int64
	err = tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE NOT EXISTS (
		           SELECT 1 FROM rows r WHERE r.id = s.row_id AND r.block_id = s.block_id))
		FROM seats s
		WHERE s.block_id = ANY($1::bigint[])`, newBlockIDs).Scan(&total, &orphans)
	if err != nil {
		return false, fmt.Errorf("check loaded seats: %w", err)
	}
	if orphans != 0 || total != int64(len(seats)) {
		return false, fmt.Errorf("loaded seats failed the reference check: %d of %d copied seats found, %d without a row in their block",
			total, len(seats), orphans)
	}
	return true, nil
}

func templateCategories() []store.TemplateCategory {
	// Colours are far apart from each other and from the seat map's status colours (grey for
	// sold, amber for held), because on the map the colour is the price.
	return []store.TemplateCategory{
		{Name: catVVIP, ColorHex: "#7C3AED", PriceCents: 1_000_000, Description: "West stand centre, padded seats, lounge access"},
		{Name: catVIP, ColorHex: "#2563EB", PriceCents: 500_000, Description: "West stand, halfway line"},
		{Name: catRegular, ColorHex: "#059669", PriceCents: 100_000, Description: "Covered seating on all sides"},
		{Name: catTerrace, ColorHex: "#DB2777", PriceCents: 50_000, Description: "Upper tier behind the goals"},
	}
}

// ensureTemplate saves the default categories and which block or row each one paints.
func ensureTemplate(ctx context.Context, q *store.Queries, venueID int64, stands []standSpec) (int64, error) {
	existing, err := q.GetVenueTemplateByName(ctx, store.GetVenueTemplateByNameParams{VenueID: venueID, Name: templateName})
	if err == nil {
		return existing.ID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find template: %w", err)
	}

	blocks, err := q.ListBlocksByVenue(ctx, venueID)
	if err != nil {
		return 0, fmt.Errorf("list blocks: %w", err)
	}
	blockIDs := make(map[string]int64, len(blocks))
	for _, b := range blocks {
		blockIDs[b.Code] = b.ID
	}

	var assignments store.TemplateAssignments
	for _, st := range stands {
		for _, b := range st.Blocks {
			id, ok := blockIDs[b.Code]
			if !ok {
				return 0, fmt.Errorf("block %s is not in the database", b.Code)
			}
			assignments.Blocks = append(assignments.Blocks, store.TemplateBlockAssignment{BlockID: id, Category: b.Category})
			if len(b.RowCategory) == 0 {
				continue
			}
			rows, err := q.ListRowsByBlock(ctx, id)
			if err != nil {
				return 0, fmt.Errorf("list rows of block %s: %w", b.Code, err)
			}
			for _, r := range rows {
				if cat, ok := b.RowCategory[int(r.RowIndex)]; ok {
					assignments.Rows = append(assignments.Rows, store.TemplateRowAssignment{RowID: r.ID, Category: cat})
				}
			}
		}
	}

	cats, assigns, err := store.EncodeTemplate(templateCategories(), assignments)
	if err != nil {
		return 0, err
	}
	created, err := q.CreateVenueTemplate(ctx, store.CreateVenueTemplateParams{
		VenueID: venueID, Name: templateName, Categories: cats, Assignments: assigns,
	})
	if err != nil {
		return 0, fmt.Errorf("create template: %w", err)
	}
	return created.ID, nil
}

type demoEvent struct {
	Title       string
	Competition string
	Home, Away  string
	// KickoffInDays places the match that many days after the run, at 16:00 in Nairobi, so
	// the demo never shows a match in the past.
	KickoffInDays int
	OnSale        bool
}

const (
	onSaleEventTitle = "Harambee Stars vs Uganda Cranes"
	draftEventTitle  = "Nairobi Derby: City Stars vs Tusker"
)

func demoEvents() []demoEvent {
	return []demoEvent{
		{Title: onSaleEventTitle, Competition: "International Friendly", Home: "Harambee Stars", Away: "Uganda Cranes", KickoffInDays: 21, OnSale: true},
		{Title: draftEventTitle, Competition: "Premier League", Home: "Nairobi City Stars", Away: "Tusker", KickoffInDays: 45},
	}
}

// ensureEvents creates the two demo events from the template. The first is published and on
// sale; the second stays a draft with its categories painted, ready to publish from the
// admin site.
func ensureEvents(ctx context.Context, tx pgx.Tx, venueID, templateID int64, now time.Time) error {
	q := store.New(tx)
	events, err := q.ListEvents(ctx)
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	have := map[string]bool{}
	for _, e := range events {
		if e.VenueID == venueID {
			have[e.Title] = true
		}
	}

	for _, d := range demoEvents() {
		if have[d.Title] {
			continue
		}
		day := now.In(nairobi).AddDate(0, 0, d.KickoffInDays)
		kickoff := time.Date(day.Year(), day.Month(), day.Day(), 16, 0, 0, 0, nairobi)
		gatesOpen := kickoff.Add(-3 * time.Hour)
		salesOpen := kickoff.AddDate(0, 0, -14)
		if d.OnSale {
			salesOpen = now.Add(-time.Hour)
		}

		event, err := q.CreateEvent(ctx, store.CreateEventParams{
			VenueID: venueID, Title: d.Title,
			Competition: &d.Competition, HomeTeam: &d.Home, AwayTeam: &d.Away,
			KickoffAt: kickoff, GatesOpenAt: &gatesOpen,
			SalesOpenAt: &salesOpen, SalesCloseAt: &kickoff,
			MaxTicketsPerUser: 6,
		})
		if err != nil {
			return fmt.Errorf("create event %q: %w", d.Title, err)
		}
		if _, err := store.ApplyVenueTemplate(ctx, tx, event.ID, templateID); err != nil {
			return err
		}
		if !d.OnSale {
			continue
		}
		if _, err := store.PublishEventSeats(ctx, tx, event.ID); err != nil {
			return err
		}
		moved, err := q.SetEventStatus(ctx, store.SetEventStatusParams{
			ID: event.ID, Status: "ON_SALE", FromStatuses: []string{"DRAFT"},
		})
		if err != nil {
			return fmt.Errorf("put event %q on sale: %w", d.Title, err)
		}
		if moved != 1 {
			return fmt.Errorf("put event %q on sale: it was not a draft", d.Title)
		}
	}
	return nil
}

// ensureUsers creates one user per role. Fans and gate staff sign in by phone OTP; the
// organiser and the admin by email and password. TOTP is left off: it is optional per user,
// and a demo login should not need an authenticator app.
func ensureUsers(ctx context.Context, q *store.Queries) error {
	phones := []struct{ phone, name, role string }{
		{demoFanPhone, "Demo Fan", "FAN"},
		{demoGatePhone, "Demo Gate Steward", "GATE_STAFF"},
	}
	for _, u := range phones {
		_, err := q.GetUserByPhone(ctx, &u.phone)
		if err == nil {
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find user %s: %w", u.phone, err)
		}
		if _, err := q.CreateUser(ctx, store.CreateUserParams{PhoneE164: &u.phone, Name: &u.name, Role: u.role}); err != nil {
			return fmt.Errorf("create user %s: %w", u.phone, err)
		}
	}

	staff := []struct{ email, password, name, role string }{
		{demoOrganiserEmail, demoOrganiserPass, "Demo Organiser", "ORGANISER"},
		{demoAdminEmail, demoAdminPass, "Demo Admin", "ADMIN"},
	}
	for _, u := range staff {
		_, err := q.GetUserByEmail(ctx, u.email)
		if err == nil {
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("find user %s: %w", u.email, err)
		}
		hash, err := auth.HashPassword(u.password)
		if err != nil {
			return err
		}
		if _, err := q.CreateStaffUser(ctx, store.CreateStaffUserParams{
			Email: u.email, Name: &u.name, Role: u.role, PasswordHash: hash,
		}); err != nil {
			return fmt.Errorf("create user %s: %w", u.email, err)
		}
	}
	return nil
}

// Print writes the run's timings and row counts, and the demo logins when they exist.
func (r Result) Print(w io.Writer) {
	fmt.Fprintln(w, "Timings")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, s := range r.Steps {
		fmt.Fprintf(tw, "  %s\t%s\n", s.Name, s.Duration.Round(time.Millisecond))
	}
	fmt.Fprintf(tw, "  layout (generate + insert)\t%s\n", r.Layout.Round(time.Millisecond))
	fmt.Fprintf(tw, "  total\t%s\n", r.Total.Round(time.Millisecond))
	tw.Flush()
	if r.SeatChecksBatched {
		fmt.Fprintln(w, "  (seat foreign keys were checked with one query after the COPY, not row by row)")
	}

	fmt.Fprintln(w, "\nRows")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "  table\tinserted\ttotal\t")
	for _, t := range r.Tables {
		fmt.Fprintf(tw, "  %s\t%d\t%d\t\n", t.Table, t.Inserted, t.Total)
	}
	tw.Flush()

	if r.Inserted() == 0 {
		fmt.Fprintln(w, "\nNothing new: everything was already seeded, 0 rows inserted.")
	} else {
		fmt.Fprintf(w, "\n%d rows inserted.\n", r.Inserted())
	}

	if r.DemoUsersRefused {
		fmt.Fprintln(w, "\nAPP_ENV=production: demo users were NOT created. Their passwords are public.")
		return
	}
	fmt.Fprintln(w, "\nDemo logins (development only)")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "  FAN\t%s\tphone OTP\n", demoFanPhone)
	fmt.Fprintf(tw, "  GATE_STAFF\t%s\tphone OTP\n", demoGatePhone)
	fmt.Fprintf(tw, "  ORGANISER\t%s\tpassword %s\n", demoOrganiserEmail, demoOrganiserPass)
	fmt.Fprintf(tw, "  ADMIN\t%s\tpassword %s\n", demoAdminEmail, demoAdminPass)
	tw.Flush()
}
