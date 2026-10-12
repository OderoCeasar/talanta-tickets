package gate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Scan is one scan a device recorded.
type Scan struct {
	// ClientScanID is made on the device, once per scan. The upload is idempotent on it.
	ClientScanID [16]byte
	// TicketID is the ticket ID the device read from the QR payload.
	TicketID [16]byte
	// EventID and GateGroup are what the device was set to, not what the ticket says.
	EventID   int64
	GateGroup string
	// ScannedAt is the device's clock.
	ScannedAt time.Time
	// Result is what the device decided and showed the steward.
	Result domain.ScanResult
	// Token is the full QR text, or empty when the device did not send it.
	Token string
}

// Batch is one upload: the scans of one device, sent by one signed-in steward.
type Batch struct {
	UserID   int64
	DeviceID string
	Scans    []Scan
}

// Outcome is the server's answer for one uploaded scan.
type Outcome struct {
	ClientScanID [16]byte
	// TicketID is the ticket the stored scan is about.
	TicketID [16]byte
	// Result is the final result, as stored.
	Result domain.ScanResult
	// Overridden is true when Result differs from what the device sent.
	Overridden bool
	// TicketStatus is the ticket's status now; nil when there is no such ticket.
	TicketStatus *domain.TicketStatus
	// FirstAdmittedAt is the scan time of the admission that holds the ticket (used_at).
	FirstAdmittedAt *time.Time
}

// errScanRace: a scan this transaction decided was stored by another one in the meantime.
// Only possible when two devices send one client_scan_id for tickets that share no lock;
// the upload is simply run again and then finds the stored row.
var errScanRace = errors.New("gate: a scan of the batch was stored concurrently")

// UploadScans stores a batch of scans and returns the server's result for each, in the
// order they were sent.
//
// THE RULES (the same table is in docs/API_NOTES.md, "Gate mode").
//
// A scan whose client_scan_id is already stored is not looked at again: the answer is the
// stored row's result as it is now. That is what makes resending a batch safe, and also how
// a device learns later that one of its admissions lost (see below).
//
// A new scan is checked in this order, and the first failure is its result:
//
//  1. a token, when sent, must verify with a trusted key and be for the scan's ticket_id:
//     else INVALID; it must be for the scan's event: else WRONG_EVENT; and for the scan's
//     gate group: else WRONG_GATE;
//  2. the ticket must exist, and a token must be exactly the text the ticket was issued
//     with: else INVALID;
//  3. the ticket must belong to the scan's event: else WRONG_EVENT; and to its gate group:
//     else WRONG_GATE;
//  4. the ticket must be VALID or USED (not VOID, not TRANSFERRED): else INVALID.
//
// A scan that passes and that the device did NOT report as ADMITTED keeps the device's
// result: the steward turned the person away, and the server never turns a refusal into an
// admission after the fact.
//
// A scan that passes and that the device reported as ADMITTED competes for the ticket. The
// admission with the earliest scan time holds it (ties go to the smaller client_scan_id);
// that scan is ADMITTED, the ticket is USED and its used_at is that scan time. Every other
// such scan of the ticket is DUPLICATE, whether it was uploaded before or after. So when an
// earlier scan arrives after a later one was already answered ADMITTED, the stored result of
// the later one is changed to DUPLICATE and used_at moves back to the earlier time. The
// device that sent the later scan is not told (nothing pushes to it): it learns when it
// uploads that scan again, or sees nothing at all. Both people are already inside.
//
// SCAN TIMES are the device's and are bounded, not trusted: a time in the future, older
// than Policy.MaxScanAge, or before the ticket was issued cannot be right and is replaced
// by the time the server received the batch.
//
// CONCURRENCY. One transaction per batch. It takes the scanned events shared (the lock
// order's first step; an organiser's cancel or edit makes it wait at most
// Policy.EventLockWait, then EVENT_BUSY), then the batch's tickets in id order, then
// writes: scan_logs first, tickets last. Two batches carrying the same ticket take turns at
// its row and the second is judged against what the first stored.
func (s *Service) UploadScans(ctx context.Context, b Batch) ([]Outcome, error) {
	if len(b.Scans) == 0 {
		return nil, domain.InvalidField("scans", "required", "Send at least one scan.")
	}
	if len(b.Scans) > s.policy.MaxBatch {
		return nil, &domain.Error{
			Code:    domain.CodePayloadTooLarge,
			Message: fmt.Sprintf("A batch may carry at most %d scans. Split it and send the parts.", s.policy.MaxBatch),
		}
	}
	for attempt := 1; ; attempt++ {
		var (
			out   []Outcome
			stats uploadStats
		)
		err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) (err error) {
			out, stats, err = s.upload(ctx, tx, b)
			return err
		})
		if errors.Is(err, errScanRace) && attempt < 3 {
			continue
		}
		if err != nil {
			return nil, err
		}
		s.log.InfoContext(ctx, "gate scans uploaded",
			"user_id", b.UserID, "device_id", b.DeviceID, "scans", len(b.Scans), "stored", stats.stored,
			"already_stored", len(b.Scans)-stats.stored, "admitted", stats.admitted, "overridden", stats.overridden,
			"demoted", stats.demoted, "times_replaced", stats.timesReplaced)
		return out, nil
	}
}

type uploadStats struct {
	stored, admitted, overridden, demoted, timesReplaced int
}

// ticketFact is the part of a ticket that never changes after issue.
type ticketFact struct {
	EventID   int64
	GateGroup string
	IssuedAt  time.Time
	QRToken   string
}

// ticketState is a locked ticket as the decisions of this batch leave it.
type ticketState struct {
	status domain.TicketStatus
	usedAt *time.Time
	// holder is the scan whose admission holds the ticket; nil when the ticket is not USED,
	// or is USED with no admitted scan on record.
	holder *[16]byte
	// holderStored: holder is a scan_logs row from an earlier upload.
	holderStored bool
	// becameUsed: this batch moved the ticket VALID -> USED. moved: it changed used_at of a
	// ticket that was USED already.
	becameUsed, moved bool
}

// decision is a new scan and what the server made of it.
type decision struct {
	scan   Scan
	at     time.Time
	result domain.ScanResult
}

func (s *Service) upload(ctx context.Context, tx pgx.Tx, b Batch) ([]Outcome, uploadStats, error) {
	var stats uploadStats
	q := store.New(tx)

	clientIDs, ticketIDs, eventIDs := batchKeys(b.Scans)

	// The unchanging columns of the tickets, without a lock. A ticket's event decides
	// whether its row is locked at all (only tickets of an event taken below are), and that
	// has to be known before the event locks, which come before the ticket locks.
	factRows, err := q.ListTicketFactsByIDs(ctx, pgUUIDs(ticketIDs))
	if err != nil {
		return nil, stats, fmt.Errorf("read tickets of the batch: %w", err)
	}
	facts := make(map[[16]byte]ticketFact, len(factRows))
	for _, f := range factRows {
		facts[f.ID.Bytes] = ticketFact{EventID: f.EventID, GateGroup: f.GateGroup, IssuedAt: f.IssuedAt, QRToken: f.QrToken}
	}

	// Lock order steps 0 and 1: every scanned event, shared, in id order.
	for _, eventID := range eventIDs {
		_, err := store.LockEventForHoldWithin(ctx, tx, eventID, s.policy.EventLockWait)
		switch {
		case store.IsLockTimeout(err):
			busy := domain.EventBusy("This event is being updated. Send the scans again in a moment.")
			busy.Err = err
			return nil, stats, busy
		case errors.Is(err, store.ErrNoRows):
			return nil, stats, domain.InvalidField(fmt.Sprintf("scans[%d].event_id", firstScanOfEvent(b.Scans, eventID)), "event", "There is no such event.")
		case err != nil:
			return nil, stats, fmt.Errorf("lock event %d for scans: %w", eventID, err)
		}
	}

	// The receive time, read after the waits above so no scan can be stamped later than it.
	now, err := store.Clock(ctx, tx)
	if err != nil {
		return nil, stats, fmt.Errorf("read database clock: %w", err)
	}
	now = now.UTC().Truncate(time.Microsecond)

	storedRows, err := q.ListScanLogsByClientIDs(ctx, pgUUIDs(clientIDs))
	if err != nil {
		return nil, stats, fmt.Errorf("read stored scans: %w", err)
	}
	stored := make(map[[16]byte]store.ScanLog, len(storedRows))
	for _, row := range storedRows {
		stored[row.ClientScanID.Bytes] = row
	}

	// The new scans: the first appearance of each client_scan_id that is not stored yet.
	fresh := make(map[[16]byte]*decision, len(b.Scans))
	var order []*decision
	var contenders []*decision
	var lockIDs [][16]byte
	lockSeen := map[[16]byte]bool{}
	for _, sc := range b.Scans {
		if _, ok := stored[sc.ClientScanID]; ok {
			continue
		}
		if _, ok := fresh[sc.ClientScanID]; ok {
			continue
		}
		d := &decision{scan: sc}
		fact, known := facts[sc.TicketID]
		var issuedAt *time.Time
		if known {
			issuedAt = &fact.IssuedAt
		}
		var replaced bool
		if d.at, replaced = s.scanTime(sc.ScannedAt, now, issuedAt); replaced {
			stats.timesReplaced++
		}
		if d.result = s.checkScan(sc, fact, known); d.result == "" {
			// Passed every check that needs no lock. Its ticket belongs to sc.EventID,
			// which is locked above, so the ticket's row may be locked too.
			contenders = append(contenders, d)
			if !lockSeen[sc.TicketID] {
				lockSeen[sc.TicketID] = true
				lockIDs = append(lockIDs, sc.TicketID)
			}
		}
		fresh[sc.ClientScanID] = d
		order = append(order, d)
	}

	states := map[[16]byte]*ticketState{}
	var demote [][16]byte
	if len(lockIDs) > 0 {
		locked, err := store.LockTicketsForScan(ctx, tx, lockIDs)
		if err != nil {
			return nil, stats, fmt.Errorf("lock tickets of the batch: %w", err)
		}
		for _, t := range locked {
			states[t.ID] = &ticketState{status: domain.TicketStatus(t.Status), usedAt: t.UsedAt}
		}
		holders, err := q.ListAdmittedScansOfTickets(ctx, pgUUIDs(lockIDs))
		if err != nil {
			return nil, stats, fmt.Errorf("read admitted scans: %w", err)
		}
		for _, h := range holders {
			st := states[h.TicketID.Bytes]
			if st == nil || st.status != domain.TicketUsed {
				continue
			}
			// More than one ADMITTED row for a ticket cannot be written by this code; if
			// one is ever found, the earliest stands and the rest are put right.
			id := h.ClientScanID.Bytes
			if st.holder != nil {
				if earlier(h.ScannedAt, id, st.usedAt, st.holder) {
					demote = append(demote, *st.holder)
					st.holder, st.usedAt, st.moved = &id, &h.ScannedAt, true
				} else {
					demote = append(demote, id)
				}
				continue
			}
			st.holder, st.holderStored = &id, true
		}

		// Earliest first, so that within one batch the first admission by scan time is the
		// one that takes a VALID ticket.
		sort.SliceStable(contenders, func(i, j int) bool {
			a, b := contenders[i], contenders[j]
			if !a.at.Equal(b.at) {
				return a.at.Before(b.at)
			}
			return bytes.Compare(a.scan.ClientScanID[:], b.scan.ClientScanID[:]) < 0
		})
		for _, d := range contenders {
			newlyDemoted, err := decide(d, states[d.scan.TicketID], fresh)
			if err != nil {
				return nil, stats, err
			}
			if newlyDemoted != nil {
				demote = append(demote, *newlyDemoted)
			}
		}
	}

	// Writes. scan_logs first; the tickets last (see store/gate.go).
	if len(demote) > 0 {
		n, err := q.DemoteAdmittedScans(ctx, pgUUIDs(demote))
		if err != nil {
			return nil, stats, fmt.Errorf("demote admitted scans: %w", err)
		}
		if int(n) != len(demote) {
			return nil, stats, fmt.Errorf("demote admitted scans: changed %d of %d under the ticket locks", n, len(demote))
		}
		stats.demoted = len(demote)
	}
	if len(order) > 0 {
		ins := store.InsertScanLogsParams{ScannedBy: b.UserID, DeviceID: b.DeviceID}
		for _, d := range order {
			ins.ClientScanIds = append(ins.ClientScanIds, pgtype.UUID{Bytes: d.scan.ClientScanID, Valid: true})
			ins.TicketIds = append(ins.TicketIds, pgtype.UUID{Bytes: d.scan.TicketID, Valid: true})
			ins.EventIds = append(ins.EventIds, d.scan.EventID)
			ins.GateGroups = append(ins.GateGroups, d.scan.GateGroup)
			ins.ScannedAts = append(ins.ScannedAts, d.at)
			ins.Results = append(ins.Results, string(d.result))
		}
		inserted, err := q.InsertScanLogs(ctx, ins)
		if err != nil {
			return nil, stats, fmt.Errorf("insert scans: %w", err)
		}
		if len(inserted) != len(order) {
			return nil, stats, errScanRace
		}
		stats.stored = len(order)
	}
	var usedIDs, movedIDs [][16]byte
	var usedAt, movedAt []time.Time
	for _, id := range lockIDs {
		switch st := states[id]; {
		case st == nil:
		case st.becameUsed:
			usedIDs, usedAt = append(usedIDs, id), append(usedAt, *st.usedAt)
		case st.moved:
			movedIDs, movedAt = append(movedIDs, id), append(movedAt, *st.usedAt)
		}
	}
	if len(usedIDs) > 0 {
		n, err := store.MarkTicketsUsed(ctx, tx, usedIDs, usedAt, domain.TicketStatusesThatCanBecome(domain.TicketUsed))
		if err != nil {
			return nil, stats, fmt.Errorf("mark tickets used: %w", err)
		}
		if int(n) != len(usedIDs) {
			return nil, stats, fmt.Errorf("mark tickets used: changed %d of %d under their locks", n, len(usedIDs))
		}
	}
	if len(movedIDs) > 0 {
		n, err := store.MoveTicketsUsedAt(ctx, tx, movedIDs, movedAt)
		if err != nil {
			return nil, stats, fmt.Errorf("move used_at of tickets: %w", err)
		}
		if int(n) != len(movedIDs) {
			return nil, stats, fmt.Errorf("move used_at of tickets: changed %d of %d under their locks", n, len(movedIDs))
		}
	}

	// The answer. A stored scan speaks for itself, with the ticket it was stored for.
	demoted := make(map[[16]byte]bool, len(demote))
	for _, id := range demote {
		demoted[id] = true
	}
	answerIDs := append([][16]byte(nil), ticketIDs...)
	for _, row := range storedRows {
		answerIDs = append(answerIDs, row.TicketID.Bytes)
	}
	stateRows, err := q.ListTicketStatesByIDs(ctx, pgUUIDs(answerIDs))
	if err != nil {
		return nil, stats, fmt.Errorf("read ticket states for the answer: %w", err)
	}
	type shown struct {
		status domain.TicketStatus
		usedAt *time.Time
	}
	now2 := make(map[[16]byte]shown, len(stateRows))
	for _, r := range stateRows {
		now2[r.ID.Bytes] = shown{status: domain.TicketStatus(r.Status), usedAt: r.UsedAt}
	}

	out := make([]Outcome, 0, len(b.Scans))
	for _, sc := range b.Scans {
		o := Outcome{ClientScanID: sc.ClientScanID, TicketID: sc.TicketID}
		if row, ok := stored[sc.ClientScanID]; ok {
			o.TicketID, o.Result = row.TicketID.Bytes, domain.ScanResult(row.Result)
			if demoted[sc.ClientScanID] {
				o.Result = domain.ScanDuplicate
			}
		} else {
			o.Result = fresh[sc.ClientScanID].result
			o.TicketID = fresh[sc.ClientScanID].scan.TicketID
		}
		o.Overridden = o.Result != sc.Result
		if t, ok := now2[o.TicketID]; ok {
			status := t.status
			o.TicketStatus, o.FirstAdmittedAt = &status, t.usedAt
		}
		if o.Overridden {
			stats.overridden++
		}
		if o.Result == domain.ScanAdmitted {
			stats.admitted++
		}
		out = append(out, o)
	}
	return out, stats, nil
}

// checkScan runs the checks that need no lock (steps 1 to 3 of UploadScans) and returns the
// failure, or "" when the scan passed them.
func (s *Service) checkScan(sc Scan, fact ticketFact, known bool) domain.ScanResult {
	if sc.Token != "" {
		claims, err := s.verifier.Verify(sc.Token)
		if err != nil || claims.TicketID != sc.TicketID {
			return domain.ScanInvalid
		}
		if claims.CheckEvent(sc.EventID) != nil {
			return domain.ScanWrongEvent
		}
		if claims.CheckGate(sc.GateGroup) != nil {
			return domain.ScanWrongGate
		}
	}
	if !known {
		return domain.ScanInvalid
	}
	// A ticket is signed once and its text stored. A different text with a good signature
	// for the same ticket was not issued by this system.
	if sc.Token != "" && sc.Token != fact.QRToken {
		return domain.ScanInvalid
	}
	if fact.EventID != sc.EventID {
		return domain.ScanWrongEvent
	}
	if fact.GateGroup != sc.GateGroup {
		return domain.ScanWrongGate
	}
	return ""
}

// decide settles one scan that passed checkScan, against its locked ticket, and records the
// outcome on both. It returns the stored scan that lost the ticket to this one, if any.
func decide(d *decision, st *ticketState, fresh map[[16]byte]*decision) (demoted *[16]byte, err error) {
	// Step 4: the status, under the lock. A ticket row is never deleted, so a missing
	// state can only mean the ID was unknown after all.
	if st == nil || !st.status.AdmitsAtGate() {
		d.result = domain.ScanInvalid
		return nil, nil
	}
	if d.scan.Result != domain.ScanAdmitted {
		// The steward turned this person away. Nobody entered on this scan.
		d.result = d.scan.Result
		return nil, nil
	}
	id := d.scan.ClientScanID
	if st.status == domain.TicketValid {
		if err := domain.TransitionTicket(st.status, domain.TicketUsed); err != nil {
			return nil, err
		}
		st.status, st.usedAt, st.holder, st.holderStored, st.becameUsed = domain.TicketUsed, &d.at, &id, false, true
		d.result = domain.ScanAdmitted
		return nil, nil
	}
	// USED: the earlier admission holds the ticket.
	if !earlier(d.at, id, st.usedAt, st.holder) {
		d.result = domain.ScanDuplicate
		return nil, nil
	}
	if st.holder != nil {
		if st.holderStored {
			demoted = st.holder
		} else if prev := fresh[*st.holder]; prev != nil {
			prev.result = domain.ScanDuplicate
		}
	}
	st.usedAt, st.holder, st.holderStored = &d.at, &id, false
	st.moved = !st.becameUsed
	d.result = domain.ScanAdmitted
	return demoted, nil
}

// earlier reports whether an admission at (at, id) comes before the one that holds a
// ticket. A USED ticket with no used_at loses to any scan; equal times go to the smaller
// client_scan_id, and to the holder when it has no scan on record.
func earlier(at time.Time, id [16]byte, heldAt *time.Time, holder *[16]byte) bool {
	if heldAt == nil {
		return true
	}
	if !at.Equal(*heldAt) {
		return at.Before(*heldAt)
	}
	return holder != nil && bytes.Compare(id[:], holder[:]) < 0
}

// scanTime bounds a device's scan time. A scan cannot have been made after the server
// received it, before its ticket was issued, or longer ago than MaxScanAge; such a time is
// replaced by the receive time, the one thing the server does know.
func (s *Service) scanTime(at, received time.Time, issuedAt *time.Time) (time.Time, bool) {
	at = at.UTC().Truncate(time.Microsecond)
	if at.After(received) || at.Before(received.Add(-s.policy.MaxScanAge)) || (issuedAt != nil && at.Before(*issuedAt)) {
		return received, true
	}
	return at, false
}

// batchKeys lists the distinct client scan IDs, ticket IDs and (ascending) event IDs of a batch.
func batchKeys(scans []Scan) (clientIDs, ticketIDs [][16]byte, eventIDs []int64) {
	seenClient, seenTicket, seenEvent := map[[16]byte]bool{}, map[[16]byte]bool{}, map[int64]bool{}
	for _, sc := range scans {
		if !seenClient[sc.ClientScanID] {
			seenClient[sc.ClientScanID] = true
			clientIDs = append(clientIDs, sc.ClientScanID)
		}
		if !seenTicket[sc.TicketID] {
			seenTicket[sc.TicketID] = true
			ticketIDs = append(ticketIDs, sc.TicketID)
		}
		if !seenEvent[sc.EventID] {
			seenEvent[sc.EventID] = true
			eventIDs = append(eventIDs, sc.EventID)
		}
	}
	sort.Slice(eventIDs, func(i, j int) bool { return eventIDs[i] < eventIDs[j] })
	return clientIDs, ticketIDs, eventIDs
}

func firstScanOfEvent(scans []Scan, eventID int64) int {
	for i, sc := range scans {
		if sc.EventID == eventID {
			return i
		}
	}
	return 0
}

func pgUUIDs(ids [][16]byte) []pgtype.UUID {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		out[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	return out
}
