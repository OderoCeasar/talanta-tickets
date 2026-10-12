package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/gate"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// gateRoutes: GATE_STAFF and ADMIN. Paths are relative to /v1/gate.
func (s *Server) gateRoutes(r chi.Router) {
	// A router built without the gate service (some unit tests) has no gate routes; the
	// sub-router's catch-all still answers 401, 403 and 404 for everything under /gate.
	if s.gate == nil {
		return
	}
	// What a manifest and a scan answer say about tickets is for the device only.
	r.Use(noStore)
	r.Get("/events", s.listGateEvents)
	r.Get("/events/{eventId}/manifest", s.getGateManifest)
	// Per user before the body is read; per device once it has been (uploadScans).
	r.With(s.LimitByUser("gate-scans", s.limits.ScanUploadPerUser)).Post("/scans", s.uploadScans)
}

// gateEventDTO is the contract's GateEvent.
type gateEventDTO struct {
	ID          int64   `json:"id"`
	Title       string  `json:"title"`
	KickoffAt   string  `json:"kickoff_at"`
	GatesOpenAt *string `json:"gates_open_at"`
	Status      string  `json:"status"`
	VenueName   string  `json:"venue_name"`
}

func newGateEventDTO(e gate.Event) gateEventDTO {
	return gateEventDTO{
		ID: e.ID, Title: e.Title, KickoffAt: ts(e.KickoffAt), GatesOpenAt: tsPtr(e.GatesOpenAt),
		Status: string(e.Status), VenueName: e.VenueName,
	}
}

type gateEventListItemDTO struct {
	Event           gateEventDTO `json:"event"`
	GateGroups      []string     `json:"gate_groups"`
	TicketCount     int          `json:"ticket_count"`
	ManifestVersion string       `json:"manifest_version"`
}

func (s *Server) listGateEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.gate.ListEvents(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]gateEventListItemDTO, 0, len(events))
	for _, e := range events {
		items = append(items, gateEventListItemDTO{
			Event: newGateEventDTO(e.Event), GateGroups: e.GateGroups, TicketCount: e.TicketCount, ManifestVersion: e.StateMarker,
		})
	}
	writeJSON(w, http.StatusOK, struct {
		Items []gateEventListItemDTO `json:"items"`
	}{Items: items})
}

// manifestKeyDTO is one entry of GateManifest.keys. It has no field a private key could be
// put in, and gate.Manifest carries ticketing.PublicKey values only.
type manifestKeyDTO struct {
	KeyID     string `json:"key_id"`
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
}

// manifestHeadDTO is GateManifest without `tickets`, which is written by hand.
type manifestHeadDTO struct {
	Event       gateEventDTO     `json:"event"`
	Version     string           `json:"version"`
	Full        bool             `json:"full"`
	GeneratedAt string           `json:"generated_at"`
	Keys        []manifestKeyDTO `json:"keys"`
	GateGroups  []string         `json:"gate_groups"`
	TicketCount int              `json:"ticket_count"`
}

// maxVersionLength is the contract's maxLength of `since`.
const maxVersionLength = 128

func (s *Server) getGateManifest(w http.ResponseWriter, r *http.Request) {
	eventID, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	since := r.URL.Query().Get("since")
	if r.URL.Query().Has("since") && (since == "" || len(since) > maxVersionLength) {
		WriteError(w, r, domain.InvalidField("since", "len", "Send the version of the manifest you have, or leave since out."))
		return
	}
	m, err := s.gate.Manifest(r.Context(), eventID, since)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	body, err := encodeManifest(m)
	if err != nil {
		WriteError(w, r, fmt.Errorf("encode manifest of event %d: %w", eventID, err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// encodeManifest builds the manifest's JSON. The head goes through encoding/json; the
// tickets, 60,000 fixed-shape tuples for a full stadium, are appended as bytes: one
// allocation for the whole body instead of several per ticket.
func encodeManifest(m gate.Manifest) ([]byte, error) {
	head := manifestHeadDTO{
		Event: newGateEventDTO(m.Event), Version: m.Version, Full: m.Full, GeneratedAt: ts(m.GeneratedAt),
		Keys: make([]manifestKeyDTO, 0, len(m.Keys)), GateGroups: m.GateGroups, TicketCount: m.TicketCount,
	}
	for _, k := range m.Keys {
		head.Keys = append(head.Keys, manifestKeyDTO{
			KeyID: k.KeyID, Algorithm: "Ed25519", PublicKey: base64.RawURLEncoding.EncodeToString(k.Key),
		})
	}
	raw, err := json.Marshal(head)
	if err != nil {
		return nil, err
	}
	// Each tuple is ["<36 characters>",<index>,"V"] and a comma: 46 bytes and the index.
	const perTicket = 50
	out := make([]byte, 0, len(raw)+16+perTicket*len(m.Tickets))
	out = append(out, raw[:len(raw)-1]...)
	out = append(out, `,"tickets":[`...)
	for i, t := range m.Tickets {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, '[', '"')
		out = appendUUID(out, t.ID)
		out = append(out, '"', ',')
		out = strconv.AppendInt(out, int64(t.Group), 10)
		out = append(out, ',', '"', t.Code, '"', ']')
	}
	out = append(out, "]}\n"...)
	return out, nil
}

const hexDigits = "0123456789abcdef"

// appendUUID appends the text form of a UUID (8-4-4-4-12, lower case).
func appendUUID(dst []byte, id [16]byte) []byte {
	for i, b := range id {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			dst = append(dst, '-')
		}
		dst = append(dst, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return dst
}

// scanDTO is the contract's Scan. Every field is read as its JSON type and checked by
// validScan, so that a bad value is reported by its place in the batch.
type scanDTO struct {
	ClientScanID string  `json:"client_scan_id"`
	TicketID     string  `json:"ticket_id"`
	EventID      int64   `json:"event_id"`
	GateGroup    string  `json:"gate_group"`
	ScannedAt    string  `json:"scanned_at"`
	Result       string  `json:"result"`
	Token        *string `json:"token"`
}

type scanBatchDTO struct {
	DeviceID string    `json:"device_id" validate:"required,max=64"`
	Scans    []scanDTO `json:"scans" validate:"required,min=1"`
}

type scanResultDTO struct {
	ClientScanID    string  `json:"client_scan_id"`
	TicketID        string  `json:"ticket_id"`
	Result          string  `json:"result"`
	Overridden      bool    `json:"overridden"`
	TicketStatus    *string `json:"ticket_status"`
	FirstAdmittedAt *string `json:"first_admitted_at"`
}

var gateGroupPattern = regexp.MustCompile(`^[A-Z0-9]+-(LOWER|UPPER)$`)

const (
	maxTokenLength = 512
	// maxScanFieldErrors keeps a batch of 500 broken scans from answering with 3,000 lines.
	maxScanFieldErrors = 20
)

// validScans turns the body into the service's batch, or lists what is wrong with it.
func validScans(in []scanDTO) ([]gate.Scan, error) {
	out := make([]gate.Scan, 0, len(in))
	var bad []domain.FieldError
	add := func(i int, field, rule, message string) {
		if len(bad) < maxScanFieldErrors {
			bad = append(bad, domain.FieldError{Field: fmt.Sprintf("scans[%d].%s", i, field), Rule: rule, Message: message})
		}
	}
	for i, d := range in {
		sc := gate.Scan{EventID: d.EventID, GateGroup: d.GateGroup, Result: domain.ScanResult(d.Result)}
		var err error
		if sc.ClientScanID, err = ticketing.ParseUUID(d.ClientScanID); err != nil {
			add(i, "client_scan_id", "uuid", "Must be a UUID made on the device for this scan.")
		}
		if sc.TicketID, err = ticketing.ParseUUID(d.TicketID); err != nil {
			add(i, "ticket_id", "uuid", "Must be the ticket ID (a UUID) read from the QR payload.")
		}
		if d.EventID < 1 {
			add(i, "event_id", "id", "Must be a positive whole number.")
		}
		if len(d.GateGroup) > 64 || !gateGroupPattern.MatchString(d.GateGroup) {
			add(i, "gate_group", "gategroup", "Must be a gate group such as N-LOWER.")
		}
		if sc.ScannedAt, err = time.Parse(time.RFC3339Nano, d.ScannedAt); err != nil {
			add(i, "scanned_at", "datetime", timeFormatMessage)
		}
		if !sc.Result.Valid() {
			add(i, "result", "oneof", "Must be one of ADMITTED, DUPLICATE, INVALID, WRONG_EVENT, WRONG_GATE.")
		}
		if d.Token != nil {
			if len(*d.Token) > maxTokenLength {
				add(i, "token", "max", "Must be at most 512 characters.")
			}
			sc.Token = *d.Token
		}
		out = append(out, sc)
	}
	if len(bad) > 0 {
		return nil, domain.Invalid(bad...)
	}
	return out, nil
}

func (s *Server) uploadScans(w http.ResponseWriter, r *http.Request) {
	var body scanBatchDTO
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if len(body.Scans) > domain.MaxScansPerBatch {
		WriteError(w, r, &domain.Error{
			Code:    domain.CodePayloadTooLarge,
			Message: fmt.Sprintf("A batch may carry at most %d scans. Split it and send the parts.", domain.MaxScansPerBatch),
		})
		return
	}
	scans, err := validScans(body.Scans)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	user := MustPrincipal(r.Context()).UserID
	// Per device: the ID is the device's own word, so the count is kept per user and
	// device. A steward's account cannot dodge it by renaming the device faster than the
	// per-user limit in front of this handler allows.
	key := "gate-scans-device:" + strconv.FormatInt(user, 10) + ":" + body.DeviceID
	if d := s.limiter.Take(key, s.limits.ScanUploadPerDevice); !d.Allowed {
		WriteError(w, r, domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter)))
		return
	}

	outcomes, err := s.gate.UploadScans(r.Context(), gate.Batch{UserID: user, DeviceID: body.DeviceID, Scans: scans})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	results := make([]scanResultDTO, 0, len(outcomes))
	for _, o := range outcomes {
		dto := scanResultDTO{
			ClientScanID: ticketing.FormatUUID(o.ClientScanID), TicketID: ticketing.FormatUUID(o.TicketID),
			Result: string(o.Result), Overridden: o.Overridden, FirstAdmittedAt: tsPtr(o.FirstAdmittedAt),
		}
		if o.TicketStatus != nil {
			status := string(*o.TicketStatus)
			dto.TicketStatus = &status
		}
		results = append(results, dto)
	}
	writeJSON(w, http.StatusOK, struct {
		Results []scanResultDTO `json:"results"`
	}{Results: results})
}
