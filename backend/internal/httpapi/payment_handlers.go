package httpapi

import (
	"io"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mpesa"
)

// MockOutcomeHeader chooses what the mock payment provider does. Development and test only.
const MockOutcomeHeader = "X-Mock-Outcome"

type startStkPushBody struct {
	BookingID int64  `json:"booking_id" validate:"required,min=1"`
	Phone     string `json:"phone" validate:"required,max=32"`
}

func (s *Server) startMpesaStkPush(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// The header is refused outright unless dev shortcuts are on, before anything else is
	// looked at: in production it must not be possible to learn even whether it is read.
	if values, sent := r.Header[http.CanonicalHeaderKey(MockOutcomeHeader)]; sent {
		if !s.allowMockOutcome {
			WriteError(w, r, domain.E(domain.CodeMockNotAllowed, "Mock payment outcomes are not available here."))
			return
		}
		outcome := ""
		if len(values) == 1 {
			outcome = values[0]
		}
		valid := false
		for _, o := range payments.MockOutcomes {
			valid = valid || o == outcome
		}
		if !valid {
			WriteError(w, r, domain.InvalidField(MockOutcomeHeader, "oneof", "Must be one of: "+joinComma(payments.MockOutcomes)+"."))
			return
		}
		ctx = payments.WithMockOutcome(ctx, outcome)
	}

	var body startStkPushBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	p, err := s.payments.Start(ctx, MustPrincipal(ctx).UserID, body.BookingID, body.Phone)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, newPaymentDTO(p.Row, p.Booking))
}

func (s *Server) getPayment(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "paymentId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	p, err := s.payments.Get(r.Context(), MustPrincipal(r.Context()).UserID, id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, newPaymentDTO(p.Row, p.Booking))
}

// webhookRoutes: public, no token is looked at. Paths are relative to /v1.
func (s *Server) webhookRoutes(r chi.Router) {
	r.Post("/webhooks/mpesa/callback", s.mpesaCallback)
}

// maxCallbackBody caps the M-Pesa callback body. A real one is under a kilobyte.
const maxCallbackBody = 64 << 10

type mpesaCallbackAck struct {
	ResultCode int    `json:"ResultCode"`
	ResultDesc string `json:"ResultDesc"`
}

// callbackAllowed reports whether the M-Pesa callback is accepted from ip. An empty list
// allows every address; production cannot start with an empty list.
func callbackAllowed(allowed []netip.Prefix, ip netip.Addr) bool {
	if len(allowed) == 0 {
		return true
	}
	if !ip.IsValid() {
		return false
	}
	for _, p := range allowed {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// mpesaCallback is Daraja's STK result callback. It ALWAYS answers 200
// { "ResultCode": 0, "ResultDesc": "Accepted" }, at once, whatever was sent and wherever it
// came from: Daraja must not retry, and a prober must learn nothing from the answer.
//
// The body is never trusted alone. Here it is only checked for where it came from (the
// address the router resolved, against MPESA_CALLBACK_ALLOWED_CIDRS) and for being Daraja's
// envelope. The payment service then matches it to a payment we know, requires that payment
// to be still open and checks the amount, on a background goroutine so this handler never
// waits on the database.
func (s *Server) mpesaCallback(w http.ResponseWriter, r *http.Request) {
	defer writeJSON(w, http.StatusOK, mpesaCallbackAck{ResultCode: 0, ResultDesc: "Accepted"})
	log := LoggerFrom(r.Context())

	ip := ClientIP(r.Context())
	if !callbackAllowed(s.callbackAllowed, ip) {
		log.WarnContext(r.Context(), "mpesa callback ignored: source address is not in MPESA_CALLBACK_ALLOWED_CIDRS", "client_ip", ip.String())
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCallbackBody))
	if err != nil {
		log.WarnContext(r.Context(), "mpesa callback ignored: body unreadable or too large", "err", err)
		return
	}
	cb, err := mpesa.ParseCallback(raw)
	if err != nil {
		log.WarnContext(r.Context(), "mpesa callback ignored: not an STK callback", "bytes", len(raw))
		return
	}
	if !s.payments.HandleCallbackAsync(r.Context(), cb) {
		log.ErrorContext(r.Context(), "mpesa callback not processed now: too much callback work in flight; reconciliation will finish the payment",
			"checkout_request_id", cb.CheckoutRequestID)
	}
}
