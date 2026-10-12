package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// staffPaymentRoutes: ORGANISER and ADMIN. Paths are relative to /v1/admin.
func (s *Server) staffPaymentRoutes(r chi.Router) {
	r.Get("/alerts", s.listAlerts)
	r.Post("/alerts/{alertId}/ack", s.ackAlert)
	r.Get("/refunds", s.listRefunds)
	r.Post("/refunds/{refundId}/complete", s.completeRefund)
}

type alertDTO struct {
	ID               int64   `json:"id"`
	EventID          int64   `json:"event_id"`
	EventTitle       string  `json:"event_title"`
	BookingID        *int64  `json:"booking_id"`
	BookingReference *string `json:"booking_reference"`
	Kind             string  `json:"kind"`
	Message          string  `json:"message"`
	CreatedAt        string  `json:"created_at"`
	AcknowledgedAt   *string `json:"acknowledged_at"`
	// Resolution is set when the system closed the alert itself, and says why.
	Resolution *string `json:"resolution"`
}

func newAlertDTO(v store.AlertView) alertDTO {
	a := v.OrganiserAlert
	return alertDTO{
		ID: a.ID, EventID: a.EventID, EventTitle: v.EventTitle, BookingID: a.BookingID, BookingReference: v.BookingReference,
		Kind: a.Kind, Message: a.Message, CreatedAt: ts(a.CreatedAt), AcknowledgedAt: tsPtr(a.AcknowledgedAt),
		Resolution: a.Resolution,
	}
}

func newRefundDTO(v store.RefundView) refundDTO {
	r := v.Refund
	return refundDTO{
		ID: r.ID, BookingID: r.BookingID, BookingReference: v.BookingReference, BookingStatus: v.BookingStatus,
		EventID: v.EventID, PaymentID: r.PaymentID,
		AmountCents: r.AmountCents, Currency: domain.Currency, Phone: v.PaymentPhone, MpesaReceipt: v.MpesaReceipt,
		Reason: r.Reason, Status: r.Status, CreatedAt: ts(r.CreatedAt), CompletedAt: tsPtr(r.CompletedAt),
	}
}

// queryID reads an optional positive int64 query value such as ?event_id=.
func queryID(r *http.Request, name string) (*int64, error) {
	if !r.URL.Query().Has(name) {
		return nil, nil
	}
	id, err := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	if err != nil || id < 1 {
		return nil, domain.InvalidField(name, "id", "Must be a positive whole number.")
	}
	return &id, nil
}

// pageCursor reads ?cursor= for the alert and refund lists.
func pageCursor(r *http.Request) (*payments.Cursor, error) {
	var cur payments.Cursor
	present, err := DecodeCursor(r, &cur)
	if err != nil || !present {
		return nil, err
	}
	return &cur, nil
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	var f payments.AlertFilter
	var err error
	if f.EventID, err = queryID(r, "event_id"); err != nil {
		WriteError(w, r, err)
		return
	}
	if r.URL.Query().Has("acknowledged") {
		v, perr := strconv.ParseBool(r.URL.Query().Get("acknowledged"))
		if raw := r.URL.Query().Get("acknowledged"); perr != nil || (raw != "true" && raw != "false") {
			WriteError(w, r, domain.InvalidField("acknowledged", "oneof", "Must be true or false."))
			return
		}
		f.Acknowledged = &v
	}
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	after, err := pageCursor(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	alerts, next, err := s.payments.ListAlerts(r.Context(), f, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]alertDTO, 0, len(alerts))
	for _, a := range alerts {
		items = append(items, newAlertDTO(a))
	}
	writeJSON(w, http.StatusOK, pageDTO[alertDTO]{Items: items, NextCursor: EncodeCursor(next)})
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "alertId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	a, err := s.payments.AckAlert(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAlertDTO(a))
}

func (s *Server) listRefunds(w http.ResponseWriter, r *http.Request) {
	var f payments.RefundFilter
	status, err := QueryEnum(r, "status", "PENDING", "DONE")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if status != "" {
		f.Status = &status
	}
	if f.EventID, err = queryID(r, "event_id"); err != nil {
		WriteError(w, r, err)
		return
	}
	if r.URL.Query().Has("booking_reference") {
		ref := r.URL.Query().Get("booking_reference")
		if !validBookingReference(ref) {
			WriteError(w, r, domain.InvalidField("booking_reference", "reference", "Must be a booking reference such as TLT-8H3K2M."))
			return
		}
		f.BookingReference = &ref
	}
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	after, err := pageCursor(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	refunds, next, err := s.payments.ListRefunds(r.Context(), f, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]refundDTO, 0, len(refunds))
	for _, rf := range refunds {
		items = append(items, newRefundDTO(rf))
	}
	writeJSON(w, http.StatusOK, pageDTO[refundDTO]{Items: items, NextCursor: EncodeCursor(next)})
}

func (s *Server) completeRefund(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "refundId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rf, alreadyDone, err := s.payments.CompleteRefund(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// Still 200 on a repeat, so the action stays safe to send twice; already_completed is
	// how the caller tells a repeat from the call that did the work.
	dto := newRefundDTO(rf)
	dto.AlreadyCompleted = &alreadyDone
	writeJSON(w, http.StatusOK, dto)
}
