package gate

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// Event is the part of an event a gate device shows.
type Event struct {
	ID          int64
	Title       string
	KickoffAt   time.Time
	GatesOpenAt *time.Time
	// Status is the effective status (domain.EffectiveEventStatus), as on every other read.
	Status    domain.EventStatus
	VenueName string
}

func newEvent(e store.Event, venueName string, now time.Time) Event {
	return Event{
		ID: e.ID, Title: e.Title, KickoffAt: e.KickoffAt, GatesOpenAt: e.GatesOpenAt, VenueName: venueName,
		Status: domain.EffectiveEventStatus(domain.EventStatus(e.Status), e.SalesOpenAt, e.SalesCloseAt, now),
	}
}

// ListedEvent is one event of the gate's list.
type ListedEvent struct {
	Event      Event
	GateGroups []string
	// TicketCount is how many tickets are VALID or USED.
	TicketCount int
	// StateMarker changes whenever a ticket of the event is issued, used, voided or
	// transferred (and when the event's details, gate groups or the key set change). It is
	// not a manifest version and is never accepted as one.
	StateMarker string
}

// ListEvents returns the published events that kick off later than Policy.EventsSince ago,
// soonest first, each with its gate groups.
func (s *Service) ListEvents(ctx context.Context) ([]ListedEvent, error) {
	now, err := s.q.DatabaseClock(ctx)
	if err != nil {
		return nil, fmt.Errorf("read database clock: %w", err)
	}
	rows, err := s.q.ListGateEvents(ctx, store.ListGateEventsParams{
		KickoffAfter: now.Add(-s.policy.EventsSince), RowLimit: int32(s.policy.MaxEvents),
	})
	if err != nil {
		return nil, fmt.Errorf("list gate events: %w", err)
	}
	if len(rows) == 0 {
		return []ListedEvent{}, nil
	}
	eventIDs := make([]int64, 0, len(rows))
	venueIDs := make([]int64, 0, len(rows))
	for _, r := range rows {
		eventIDs = append(eventIDs, r.Event.ID)
		venueIDs = append(venueIDs, r.Event.VenueID)
	}
	venueGroups, err := s.venueGateGroups(ctx, s.q, venueIDs)
	if err != nil {
		return nil, err
	}
	counts, err := s.q.CountTicketsOfEvents(ctx, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("count tickets of gate events: %w", err)
	}
	byEvent := map[int64][]store.CountTicketsOfEventsRow{}
	for _, c := range counts {
		byEvent[c.EventID] = append(byEvent[c.EventID], c)
	}

	keys := s.verifier.PublicKeys()
	out := make([]ListedEvent, 0, len(rows))
	for _, r := range rows {
		item := ListedEvent{Event: newEvent(r.Event, r.VenueName, r.NowAt)}
		seen := map[string]struct{}{}
		for _, c := range byEvent[r.Event.ID] {
			seen[c.GateGroup] = struct{}{}
			if domain.TicketStatus(c.Status).IsLive() {
				item.TicketCount += int(c.N)
			}
		}
		item.GateGroups = mergeGateGroups(venueGroups[r.Event.VenueID], seen)
		item.StateMarker = stateMarker(item.Event, keys, item.GateGroups, byEvent[r.Event.ID])
		out = append(out, item)
	}
	return out, nil
}

// venueGateGroups returns each venue's gate groups: one per (stand code, tier).
func (s *Service) venueGateGroups(ctx context.Context, q *store.Queries, venueIDs []int64) (map[int64][]string, error) {
	stands, err := q.ListGateGroupsOfVenues(ctx, venueIDs)
	if err != nil {
		return nil, fmt.Errorf("list gate groups of venues: %w", err)
	}
	out := map[int64][]string{}
	for _, st := range stands {
		out[st.VenueID] = append(out[st.VenueID], domain.GateGroup(st.Code, st.Tier))
	}
	return out, nil
}

// mergeGateGroups is the venue's gate groups plus any group a ticket carries that the venue
// no longer has, sorted. A ticket's group is fixed at issue; the list a device picks its
// gate from must always contain it.
func mergeGateGroups(venue []string, ofTickets map[string]struct{}) []string {
	set := make(map[string]struct{}, len(venue)+len(ofTickets))
	for _, g := range venue {
		set[g] = struct{}{}
	}
	for g := range ofTickets {
		set[g] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for g := range set {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

// stateMarker digests everything a device stores about an event. counts must be in a fixed
// order (the query sorts them).
func stateMarker(ev Event, keys []ticketing.PublicKey, groups []string, counts []store.CountTicketsOfEventsRow) string {
	h := sha256.New()
	gates := ""
	if ev.GatesOpenAt != nil {
		gates = strconv.FormatInt(ev.GatesOpenAt.UnixMicro(), 10)
	}
	fmt.Fprintf(h, "s1\x00%d\x00%s\x00%d\x00%s\x00%s\x00%s\x00", ev.ID, ev.Title, ev.KickoffAt.UnixMicro(), gates, ev.Status, ev.VenueName)
	for _, k := range keys {
		fmt.Fprintf(h, "k\x00%s\x00%x\x00", k.KeyID, []byte(k.Key))
	}
	for _, g := range groups {
		fmt.Fprintf(h, "g\x00%s\x00", g)
	}
	for _, c := range counts {
		fmt.Fprintf(h, "c\x00%s\x00%s\x00%d\x00", c.GateGroup, c.Status, c.N)
	}
	// The dot keeps it from ever decoding as a manifest version.
	return stateMarkerPrefix + base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:15])
}

const stateMarkerPrefix = "s1."

// ManifestTicket is one ticket of a manifest.
type ManifestTicket struct {
	ID [16]byte
	// Group is the position of the ticket's gate group in Manifest.GateGroups.
	Group int32
	// Code is the one-letter status: V, U, X or T.
	Code byte
}

// Manifest is what a device needs to check tickets offline.
type Manifest struct {
	Event Event
	// Version is opaque to the device, which sends it back as `since`.
	Version string
	// Full: Tickets is every VALID or USED ticket and replaces what the device has. Not
	// full: Tickets is what changed, in any status, to apply on top.
	Full        bool
	GeneratedAt time.Time
	// Keys are public keys. This package is never given a private one.
	Keys       []ticketing.PublicKey
	GateGroups []string
	// TicketCount is the number of VALID or USED tickets, whatever Tickets holds.
	TicketCount int
	Tickets     []ManifestTicket
}

// Manifest returns the manifest of an event: everything when since is empty or cannot be
// used, otherwise the tickets that changed since that version.
//
// # Versions
//
// A version is the database clock read BEFORE anything else, with the event it belongs to.
// tickets.updated_at is stamped by a trigger when a row is written (clock_timestamp), not
// when its transaction commits, so a ticket written before a version was issued can commit
// after it, carrying a stamp the version has already passed. A changes-only answer
// therefore starts Policy.VersionOverlap BEFORE the version it was asked about; the late
// commit is delivered then. Tickets inside the overlap are sent again and again, which is
// harmless: a change is the ticket's status now, not an event to replay.
//
// The answer is the full manifest when since is empty, cannot be read, is for another
// event, is from the future, or is older than Policy.VersionMaxAge.
//
// Tickets and ticket_count are read in one read-only snapshot, so the count is the size of
// the set a device holds after applying the answer.
func (s *Service) Manifest(ctx context.Context, eventID int64, since string) (Manifest, error) {
	// First, before any other read: nothing selected below can be newer than this.
	issuedAt, err := s.q.DatabaseClock(ctx)
	if err != nil {
		return Manifest{}, fmt.Errorf("read database clock: %w", err)
	}
	out := Manifest{Version: encodeVersion(eventID, issuedAt), GeneratedAt: issuedAt, Keys: s.verifier.PublicKeys(), Full: true}
	var from time.Time
	if seenAt, ok := decodeVersion(since, eventID); ok && s.versionUsable(seenAt, issuedAt) {
		out.Full = false
		from = seenAt.Add(-s.policy.VersionOverlap)
	}

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Manifest{}, fmt.Errorf("begin manifest snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := store.New(tx)

	row, err := q.GetEventWithVenue(ctx, eventID)
	if errors.Is(err, store.ErrNoRows) {
		return Manifest{}, domain.ErrNotFound
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("get event %d: %w", eventID, err)
	}
	// A draft has no inventory and no tickets, and is the organiser's business only. A
	// cancelled or finished event still answers: its devices must hear that tickets are void.
	if !domain.EventStatus(row.Event.Status).HasInventory() {
		return Manifest{}, domain.ErrNotFound
	}
	out.Event = newEvent(row.Event, row.VenueName, issuedAt)
	venueGroups, err := s.venueGateGroups(ctx, q, []int64{row.Event.VenueID})
	if err != nil {
		return Manifest{}, err
	}

	// Rows name their gate group in words; the answer wants an index into a sorted list
	// that is only known once every row has been read. So number the groups as they turn
	// up, then renumber.
	provisional := map[string]int32{}
	var seenGroups []string
	collect := func(id [16]byte, gateGroup, status []byte) {
		idx, ok := provisional[string(gateGroup)]
		if !ok {
			idx = int32(len(seenGroups))
			seenGroups = append(seenGroups, string(gateGroup))
			provisional[seenGroups[idx]] = idx
		}
		out.Tickets = append(out.Tickets, ManifestTicket{ID: id, Group: idx, Code: domain.TicketStatus(status).ManifestCode()})
	}
	if out.Full {
		out.Tickets = make([]ManifestTicket, 0, 1024)
		if err := store.ForEachManifestTicket(ctx, tx, eventID, collect); err != nil {
			return Manifest{}, fmt.Errorf("read manifest of event %d: %w", eventID, err)
		}
		out.TicketCount = len(out.Tickets)
	} else {
		out.Tickets = []ManifestTicket{}
		if err := store.ForEachManifestTicketChange(ctx, tx, eventID, from, collect); err != nil {
			return Manifest{}, fmt.Errorf("read manifest changes of event %d: %w", eventID, err)
		}
		n, err := q.CountLiveTicketsOfEvent(ctx, eventID)
		if err != nil {
			return Manifest{}, fmt.Errorf("count tickets of event %d: %w", eventID, err)
		}
		out.TicketCount = int(n)
	}

	ofTickets := make(map[string]struct{}, len(seenGroups))
	for _, g := range seenGroups {
		ofTickets[g] = struct{}{}
	}
	out.GateGroups = mergeGateGroups(venueGroups[row.Event.VenueID], ofTickets)
	final := make([]int32, len(seenGroups))
	for i, g := range seenGroups {
		final[i] = int32(sort.SearchStrings(out.GateGroups, g))
	}
	for i := range out.Tickets {
		out.Tickets[i].Group = final[out.Tickets[i].Group]
	}
	return out, nil
}

// versionUsable reports whether a version read at issuedAt may be answered with changes only.
func (s *Service) versionUsable(seenAt, issuedAt time.Time) bool {
	// A version from the future was not issued by this database; a second of slack covers
	// a clock step.
	if seenAt.After(issuedAt.Add(time.Second)) {
		return false
	}
	return issuedAt.Sub(seenAt) <= s.policy.VersionMaxAge
}

// versionFormat changes whenever a version's meaning does, so old ones are refused (and
// answered with a full manifest) instead of being misread.
const versionFormat = "g1"

// encodeVersion builds the opaque version: the event and the database time, so a version
// handed to another event is recognised and refused.
func encodeVersion(eventID int64, at time.Time) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%d.%d", versionFormat, eventID, at.UnixMicro())))
}

// decodeVersion reads a version for exactly this event. Anything else is not ok.
func decodeVersion(version string, eventID int64) (time.Time, bool) {
	if version == "" || len(version) > 128 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(version)
	if err != nil {
		return time.Time{}, false
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != 3 || parts[0] != versionFormat {
		return time.Time{}, false
	}
	ev, err1 := strconv.ParseInt(parts[1], 10, 64)
	micros, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || ev != eventID || micros <= 0 {
		return time.Time{}, false
	}
	return time.UnixMicro(micros), true
}
