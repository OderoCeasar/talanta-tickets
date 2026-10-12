package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// A callback is dealt with in two steps, on purpose.
//
//  1. StoreCallback writes it down: into the inbox (payment_callbacks) when its
//     CheckoutRequestID is a payment's, into unmatched_callbacks when it says "paid" for an
//     ID no payment has. The webhook does this BEFORE it answers 200. From then on the
//     report cannot be lost to a busy event, a database error, a full background queue or a
//     restart.
//  2. The stored row is worked on: at once, on the API's background runner (the fast path),
//     and again by the worker's drain job for as long as it has not been dealt with.
//
// A callback is an unauthenticated request. What it says is a hint: "ask M-Pesa now". A
// "paid" is confirmed with the provider's own STK query, outside any transaction, before
// anything is sold or queued for refund; if the query cannot be made the row simply stays
// due. The amount the body names is never treated as money (Finish says what is).

// Verdicts written on a stored callback once it has been dealt with.
const (
	verdictApplied   = "APPLIED"
	verdictRefuted   = "REFUTED"
	verdictDeferred  = "DEFERRED"
	verdictAbandoned = "ABANDONED"
	verdictMatched   = "MATCHED"
	verdictUnmatched = "UNMATCHED"
)

// errNotConfirmed means a callback said "paid" and the provider does not (yet) say so. The
// stored row stays due and is tried again; nothing was applied.
var errNotConfirmed = errors.New("payments: the provider has not confirmed the payment yet")

// StoredCallback names the row StoreCallback wrote. The zero value means nothing was stored.
type StoredCallback struct {
	// ID is the row; zero when the callback was ignored.
	ID int64
	// Unmatched is true for a row of unmatched_callbacks, false for the inbox.
	Unmatched bool
}

// StoreCallback writes a parsed callback down durably and reports where. It changes no
// payment. Ignored, with nothing stored and no error:
//
//   - every callback, when the provider takes no payments (PAYMENT_PROVIDER=none): nothing
//     of ours can be waiting for one;
//   - a callback with no CheckoutRequestID;
//   - a FAILURE for an ID no payment has: there is nothing to close and no money to find.
//
// A "paid" for an ID no payment has is stored: that is an STK push that reached M-Pesa while
// its answer never reached us, and somebody's money.
func (s *Service) StoreCallback(ctx context.Context, cb Callback) (StoredCallback, error) {
	if s.off {
		s.log.WarnContext(ctx, "payment callback ignored: payments are switched off (no provider is configured)",
			"checkout_request_id", cb.CheckoutRequestID, "result_code", cb.ResultCode)
		return StoredCallback{}, nil
	}
	if cb.CheckoutRequestID == "" {
		s.log.WarnContext(ctx, "payment callback ignored: no CheckoutRequestID")
		return StoredCallback{}, nil
	}
	if len(cb.Raw) == 0 {
		cb.Raw = []byte(`{}`)
	}
	code := int32(cb.ResultCode)
	paymentID, err := s.q.GetPaymentIDByCheckoutRequestID(ctx, cb.CheckoutRequestID)
	switch {
	case err == nil:
		id, err := s.q.UpsertPaymentCallback(ctx, store.UpsertPaymentCallbackParams{
			PaymentID: paymentID, CheckoutRequestID: cb.CheckoutRequestID, MerchantRequestID: strPtr(cb.MerchantRequestID),
			ResultCode: code, ResultDesc: strPtr(cb.ResultDesc), AmountCents: cb.AmountCents,
			MpesaReceipt: strPtr(cb.Receipt), PhoneE164: strPtr(cb.Phone), Raw: cb.Raw,
		})
		if err != nil {
			return StoredCallback{}, fmt.Errorf("store callback for payment %d: %w", paymentID, err)
		}
		return StoredCallback{ID: id}, nil
	case !errors.Is(err, store.ErrNoRows):
		return StoredCallback{}, fmt.Errorf("find payment of callback: %w", err)
	}

	if cb.ResultCode != domain.ResultSuccess {
		s.log.WarnContext(ctx, "payment callback ignored: a failure for an unknown CheckoutRequestID",
			"checkout_request_id", cb.CheckoutRequestID, "result_code", cb.ResultCode)
		return StoredCallback{}, nil
	}
	id, err := s.q.UpsertUnmatchedCallback(ctx, store.UpsertUnmatchedCallbackParams{
		CheckoutRequestID: cb.CheckoutRequestID, MerchantRequestID: strPtr(cb.MerchantRequestID),
		ResultCode: code, ResultDesc: strPtr(cb.ResultDesc), AmountCents: cb.AmountCents,
		MpesaReceipt: strPtr(cb.Receipt), PhoneE164: strPtr(cb.Phone), Raw: cb.Raw,
	})
	if err != nil {
		return StoredCallback{}, fmt.Errorf("store unmatched callback: %w", err)
	}
	s.log.ErrorContext(ctx, "payment callback says PAID for an unknown CheckoutRequestID; it was stored and will be matched to a payment or flagged for a person",
		"checkout_request_id", cb.CheckoutRequestID, "unmatched_callback_id", id, "mpesa_receipt", cb.Receipt)
	return StoredCallback{ID: id, Unmatched: true}, nil
}

// HandleCallback stores a callback and works on it at once, on the caller's goroutine. It
// is what the mock provider's sink and the tests call; the webhook uses HandleCallbackAsync.
//
// It reports what was done. A "paid" the provider has not confirmed yet is not an error: the
// row stays stored and due, and the answer is ActionNone.
func (s *Service) HandleCallback(ctx context.Context, cb Callback) (Finished, error) {
	stored, err := s.StoreCallback(ctx, cb)
	if err != nil || stored.ID == 0 {
		return Finished{Action: ActionNone}, err
	}
	fin, err := s.workStored(ctx, stored)
	if errors.Is(err, errNotConfirmed) {
		return Finished{PaymentID: fin.PaymentID, Action: ActionNone, Reason: "the provider has no result yet"}, nil
	}
	return fin, err
}

// HandleCallbackAsync is the webhook's path: the callback is stored before this returns, and
// worked on in the background with its own deadline. An error means it could NOT be stored;
// the webhook then does not answer 200. When the background runner is full the row is simply
// left for the drain job.
func (s *Service) HandleCallbackAsync(ctx context.Context, cb Callback) error {
	stored, err := s.StoreCallback(ctx, cb)
	if err != nil || stored.ID == 0 {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	started := s.bg.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if _, err := s.workStored(ctx, stored); err != nil {
			s.log.WarnContext(ctx, "payment callback not applied yet; it is stored and the drain job will try it again",
				"checkout_request_id", cb.CheckoutRequestID, "err", err)
		}
	})
	if !started {
		s.log.WarnContext(ctx, "payment callback stored but not worked on now: too much background work in flight; the drain job will pick it up",
			"checkout_request_id", cb.CheckoutRequestID)
	}
	return nil
}

// workStored claims one stored row and works on it. A row someone else holds is left alone.
func (s *Service) workStored(ctx context.Context, stored StoredCallback) (Finished, error) {
	claim := store.ClaimPaymentCallbacks
	if stored.Unmatched {
		claim = store.ClaimUnmatchedCallbacks
	}
	rows, err := claim(ctx, s.db, stored.ID, s.policy.CallbackLease, 1)
	if err != nil {
		return Finished{}, fmt.Errorf("claim stored callback %d: %w", stored.ID, err)
	}
	if len(rows) == 0 {
		// Already dealt with, or being worked on right now by someone else.
		return Finished{Action: ActionNone}, nil
	}
	if stored.Unmatched {
		return s.workUnmatched(ctx, rows[0])
	}
	return s.workInbox(ctx, rows[0])
}

// DrainResult counts what one drain run did.
type DrainResult struct {
	// Claimed is how many stored callbacks the run took.
	Claimed int
	// Done counts those dealt with for good; Retry those left due for another attempt.
	Done, Retry int
}

// DrainCallbacks works on every stored callback that is due: the ones the fast path could not
// finish (the event was busy, the database or M-Pesa did not answer, the queue was full) and
// the ones a restart left behind. The worker runs it every few seconds. Rows are claimed with
// FOR UPDATE SKIP LOCKED and a lease, so it never collides with the fast path or with another
// worker. It stops starting work once Policy.ReconcileBudget is spent.
func (s *Service) DrainCallbacks(ctx context.Context) (DrainResult, error) {
	var out DrainResult
	deadline := s.now().Add(s.policy.ReconcileBudget)
	kinds := []struct {
		claim func(ctx context.Context, db store.DBTX, onlyID int64, lease time.Duration, limit int) ([]store.ClaimedCallback, error)
		work  func(ctx context.Context, c store.ClaimedCallback) (Finished, error)
	}{
		{store.ClaimPaymentCallbacks, s.workInbox},
		{store.ClaimUnmatchedCallbacks, s.workUnmatched},
	}
	for _, kind := range kinds {
		for left := s.policy.CallbackBatch; left > 0 && s.now().Before(deadline); {
			rows, err := kind.claim(ctx, s.db, 0, s.policy.CallbackLease, min(left, reconcileChunk))
			if err != nil {
				return out, fmt.Errorf("claim stored callbacks: %w", err)
			}
			if len(rows) == 0 {
				break
			}
			out.Claimed += len(rows)
			left -= len(rows)
			for _, c := range rows {
				if err := ctx.Err(); err != nil {
					return out, err
				}
				if _, err := kind.work(ctx, c); err != nil {
					out.Retry++
					if !errors.Is(err, errNotConfirmed) {
						s.log.WarnContext(ctx, "stored payment callback could not be applied; it will be tried again",
							"callback_id", c.ID, "checkout_request_id", c.CheckoutRequestID, "attempt", c.Attempts, "err", err)
					}
					continue
				}
				out.Done++
			}
		}
	}
	return out, nil
}

// workInbox deals with one claimed inbox row and records what became of it: done, with a
// verdict; or due again later; or, after Policy.CallbackGiveUpAfter, abandoned with an alert.
func (s *Service) workInbox(ctx context.Context, c store.ClaimedCallback) (Finished, error) {
	fin, verdict, err := s.applyCallback(ctx, c)
	// What happened is recorded even if the caller has gone away.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err == nil {
		if _, ferr := s.q.FinishPaymentCallback(rctx, store.FinishPaymentCallbackParams{ID: c.ID, Verdict: verdict}); ferr != nil {
			// The work is done and is safe to repeat; the row comes round again.
			return fin, fmt.Errorf("mark stored callback %d done: %w", c.ID, ferr)
		}
		return fin, nil
	}
	msg := err.Error()
	if c.Age >= s.policy.CallbackGiveUpAfter {
		if aerr := s.abandonCallback(rctx, c, msg); aerr != nil {
			return fin, errors.Join(err, aerr)
		}
		return fin, err
	}
	if _, rerr := s.q.RetryPaymentCallback(rctx, store.RetryPaymentCallbackParams{
		ID: c.ID, DelaySeconds: s.retryDelay(c.Attempts).Seconds(), LastError: &msg,
	}); rerr != nil {
		// The lease runs out by itself and the row is due again then.
		return fin, errors.Join(err, fmt.Errorf("reschedule stored callback %d: %w", c.ID, rerr))
	}
	return fin, err
}

// applyCallback is the work of one inbox row. It returns the verdict to record when the row
// is done, or an error when it must be tried again.
func (s *Service) applyCallback(ctx context.Context, c store.ClaimedCallback) (Finished, string, error) {
	paymentID := *c.PaymentID
	desc := ""
	if c.ResultDesc != nil {
		desc = *c.ResultDesc
	}

	if c.ResultCode != domain.ResultSuccess {
		if domain.IsFinalSTKFailure(int(c.ResultCode)) {
			code := c.ResultCode
			fin, err := s.Finish(ctx, paymentID, Result{
				Source: SourceCallback, Outcome: OutcomeFailed, ResultCode: &code, ResultDesc: desc, Raw: c.Raw,
			})
			return fin, verdictApplied, err
		}
		// A code we do not know to be final. The body is not taken at its word: M-Pesa is
		// asked, and only a final answer of its own is applied.
		st, err := s.query(ctx, c.CheckoutRequestID)
		if err != nil {
			return Finished{PaymentID: paymentID}, "", fmt.Errorf("query provider about payment %d: %w", paymentID, err)
		}
		if st.State == StatePending {
			s.log.WarnContext(ctx, "payment callback carries a result code that is not a known final failure, and M-Pesa has no final answer; left to reconciliation",
				"payment_id", paymentID, "result_code", c.ResultCode)
			return Finished{PaymentID: paymentID, Action: ActionNone, Reason: "not a final result"}, verdictDeferred, nil
		}
		res := resultOfQuery(st)
		res.Raw = c.Raw
		fin, err := s.Finish(ctx, paymentID, res)
		return fin, verdictApplied, err
	}

	// The body says PAID. A payment that is already settled as paid needs no question asked.
	pay, err := s.q.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return Finished{PaymentID: paymentID}, "", fmt.Errorf("get payment %d: %w", paymentID, err)
	}
	if st := domain.PaymentStatus(pay.Status); st == domain.PaymentSuccess || st == domain.PaymentReversed {
		return Finished{PaymentID: paymentID, BookingID: pay.BookingID, Action: ActionNone, PaymentStatus: st}, verdictApplied, nil
	}

	// Confirm it with the provider, outside any transaction. The query is the authority.
	st, err := s.query(ctx, c.CheckoutRequestID)
	if err != nil {
		return Finished{PaymentID: paymentID}, "", fmt.Errorf("query provider about payment %d: %w", paymentID, err)
	}
	switch st.State {
	case StatePending:
		return Finished{PaymentID: paymentID}, "", errNotConfirmed
	case StateFailed:
		s.log.ErrorContext(ctx, "a payment callback said PAID but M-Pesa says the payment failed; M-Pesa's answer was applied",
			"payment_id", paymentID, "checkout_request_id", c.CheckoutRequestID, "provider_result_code", st.ResultCode)
		fin, err := s.Finish(ctx, paymentID, resultOfQuery(st))
		return fin, verdictRefuted, err
	}
	res := resultOfQuery(st)
	res.Raw, res.ClaimedAmountCents = c.Raw, c.AmountCents
	if res.Receipt == "" && c.MpesaReceipt != nil {
		res.Receipt = *c.MpesaReceipt
	}
	fin, err := s.Finish(ctx, paymentID, res)
	if err == nil && fin.Action == ActionFailed {
		// The provider "confirmed" a payment of nothing: no money moved.
		return fin, verdictRefuted, nil
	}
	return fin, verdictApplied, err
}

// abandonCallback gives up on an inbox row that could not be dealt with in
// Policy.CallbackGiveUpAfter, and tells the organiser: the row says money may have moved.
func (s *Service) abandonCallback(ctx context.Context, c store.ClaimedCallback, lastErr string) error {
	p, err := s.load(ctx, *c.PaymentID)
	if err != nil {
		return err
	}
	return store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		n, err := q.FinishPaymentCallback(ctx, store.FinishPaymentCallbackParams{ID: c.ID, Verdict: verdictAbandoned, LastError: &lastErr})
		if err != nil || n != 1 {
			return fmt.Errorf("abandon stored callback %d: %d rows: %w", c.ID, n, err)
		}
		bookingID, paymentID := p.Booking.ID, p.Row.ID
		_, err = q.InsertAlert(ctx, store.InsertAlertParams{
			EventID: p.Booking.EventID, BookingID: &bookingID, PaymentID: &paymentID, Kind: "PAYMENT_UNRESOLVED",
			Message: fmt.Sprintf("An M-Pesa callback for booking %s (result code %d, receipt %s) could not be confirmed with M-Pesa or applied in %d days. Check the M-Pesa statement; if the money is there, refund it.",
				p.Booking.Reference, c.ResultCode, orDash(deref(c.MpesaReceipt)), int(s.policy.CallbackGiveUpAfter.Hours()/24)),
		})
		return err
	})
}

// workUnmatched deals with one claimed "paid" callback whose ID no payment had.
//
//  1. If a payment has the ID by now (its IDs were recorded a moment after the callback
//     came), the callback moves to the inbox and is worked on there.
//  2. Otherwise it is matched to a payment with NO provider ID: the same phone, the same
//     amount, started shortly before. Exactly one must fit. Phone and amount come from the
//     body, so the match alone decides nothing: M-Pesa is asked about the callback's ID, and
//     only if M-Pesa itself says that ID was paid is the ID written on the payment and the
//     callback moved to the inbox, where the normal path sells or refunds.
//  3. Until then it stays stored, an alert names it for a person to look up and refund, and
//     it is tried again until Policy.UnmatchedGiveUpAfter.
func (s *Service) workUnmatched(ctx context.Context, c store.ClaimedCallback) (Finished, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	paymentID, err := s.q.GetPaymentIDByCheckoutRequestID(ctx, c.CheckoutRequestID)
	switch {
	case err == nil:
		return s.promoteUnmatched(ctx, c, paymentID)
	case !errors.Is(err, store.ErrNoRows):
		return Finished{}, s.unresolved(rctx, c, nil, fmt.Errorf("find payment of callback: %w", err))
	}

	var cand *store.ListIDLessPaymentCandidatesRow
	if c.AmountCents != nil && c.PhoneE164 != nil {
		rows, err := s.q.ListIDLessPaymentCandidates(ctx, store.ListIDLessPaymentCandidatesParams{
			PhoneE164: *c.PhoneE164, AmountCents: *c.AmountCents, ReceivedAt: c.ReceivedAt,
			WindowSeconds: s.policy.UnmatchedWindow.Seconds(),
		})
		if err != nil {
			return Finished{}, s.unresolved(rctx, c, nil, fmt.Errorf("look for a payment the callback fits: %w", err))
		}
		if len(rows) == 1 {
			cand = &rows[0]
		}
	}
	if cand == nil {
		return Finished{}, s.unresolved(rctx, c, nil, errors.New("no single payment fits the callback"))
	}

	st, err := s.query(ctx, c.CheckoutRequestID)
	if err != nil {
		return Finished{PaymentID: cand.PaymentID}, s.unresolved(rctx, c, cand, fmt.Errorf("query provider about the callback's ID: %w", err))
	}
	switch st.State {
	case StatePending:
		return Finished{PaymentID: cand.PaymentID}, s.unresolved(rctx, c, cand, errNotConfirmed)
	case StateFailed:
		s.log.ErrorContext(ctx, "an unmatched payment callback said PAID but M-Pesa says that ID failed; nothing was done with it",
			"checkout_request_id", c.CheckoutRequestID, "provider_result_code", st.ResultCode)
		_, err := s.q.FinishUnmatchedCallback(rctx, store.FinishUnmatchedCallbackParams{ID: c.ID, Verdict: verdictRefuted})
		return Finished{PaymentID: cand.PaymentID, Action: ActionNone}, err
	}

	// M-Pesa confirms the ID was paid. Tie it to the payment.
	bound, err := s.bindProviderIDs(ctx, cand.PaymentID, c.CheckoutRequestID, c.MerchantRequestID)
	if err != nil {
		return Finished{PaymentID: cand.PaymentID}, s.unresolved(rctx, c, cand, err)
	}
	if !bound {
		return Finished{PaymentID: cand.PaymentID}, s.unresolved(rctx, c, cand, errors.New("the payment got a different provider ID meanwhile"))
	}
	s.log.WarnContext(ctx, "an unmatched payment callback was confirmed by M-Pesa and tied to a payment that had no provider ID",
		"payment_id", cand.PaymentID, "checkout_request_id", c.CheckoutRequestID)
	return s.promoteUnmatched(ctx, c, cand.PaymentID)
}

// promoteUnmatched moves an unmatched callback into the inbox of the payment that has its ID,
// marks it matched, and works on the inbox row.
func (s *Service) promoteUnmatched(ctx context.Context, c store.ClaimedCallback, paymentID int64) (Finished, error) {
	var inboxID int64
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		inboxID, err = q.UpsertPaymentCallback(ctx, store.UpsertPaymentCallbackParams{
			PaymentID: paymentID, CheckoutRequestID: c.CheckoutRequestID, MerchantRequestID: c.MerchantRequestID,
			ResultCode: c.ResultCode, ResultDesc: c.ResultDesc, AmountCents: c.AmountCents,
			MpesaReceipt: c.MpesaReceipt, PhoneE164: c.PhoneE164, Raw: c.Raw,
		})
		if err != nil {
			return fmt.Errorf("move unmatched callback %d to the inbox: %w", c.ID, err)
		}
		if _, err = q.FinishUnmatchedCallback(ctx, store.FinishUnmatchedCallbackParams{ID: c.ID, Verdict: verdictMatched, MatchedPaymentID: &paymentID}); err != nil {
			return err
		}
		if c.AlertID != nil {
			// The alert raised while the callback was unmatched is now about this payment,
			// and is closed with it when the payment settles.
			pay, err := q.GetPaymentByID(ctx, paymentID)
			if err != nil {
				return fmt.Errorf("get payment %d: %w", paymentID, err)
			}
			_, err = q.SetAlertPayment(ctx, store.SetAlertPaymentParams{ID: *c.AlertID, PaymentID: paymentID, BookingID: pay.BookingID})
			return err
		}
		return nil
	})
	if err != nil {
		return Finished{PaymentID: paymentID}, err
	}
	return s.workStored(ctx, StoredCallback{ID: inboxID})
}

// bindProviderIDs writes a provider ID on a payment that has none. It reports false when the
// payment has one already. It locks the payment row alone (see store.LockPaymentAlone).
func (s *Service) bindProviderIDs(ctx context.Context, paymentID int64, checkoutRequestID string, merchantRequestID *string) (bool, error) {
	bound := false
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		pay, err := store.LockPaymentAlone(ctx, tx, paymentID)
		if err != nil {
			return fmt.Errorf("lock payment %d: %w", paymentID, err)
		}
		if pay.CheckoutRequestID != nil {
			return nil
		}
		n, err := store.New(tx).SetPaymentProviderIDs(ctx, store.SetPaymentProviderIDsParams{
			ID: paymentID, CheckoutRequestID: checkoutRequestID, MerchantRequestID: merchantRequestID,
		})
		if err != nil {
			return fmt.Errorf("record provider IDs on payment %d: %w", paymentID, err)
		}
		bound = n == 1
		return nil
	})
	return bound, err
}

// unresolved records that an unmatched callback could not be settled this time. The first
// time, it raises the alert that lets a person find and refund the money. Then the row is
// made due again, or, past Policy.UnmatchedGiveUpAfter, closed as UNMATCHED (the alert
// stands). It returns cause, so the caller reports the attempt as not done.
func (s *Service) unresolved(ctx context.Context, c store.ClaimedCallback, cand *store.ListIDLessPaymentCandidatesRow, cause error) error {
	if c.AlertID == nil {
		if err := s.alertUnmatched(ctx, c, cand); err != nil {
			s.log.ErrorContext(ctx, "could not raise the alert for an unmatched payment callback; it will be tried again",
				"unmatched_callback_id", c.ID, "err", err)
		}
	}
	msg := cause.Error()
	if c.Age >= s.policy.UnmatchedGiveUpAfter {
		if _, err := s.q.FinishUnmatchedCallback(ctx, store.FinishUnmatchedCallbackParams{ID: c.ID, Verdict: verdictUnmatched, LastError: &msg}); err != nil {
			return errors.Join(cause, err)
		}
		return cause
	}
	if _, err := s.q.RetryUnmatchedCallback(ctx, store.RetryUnmatchedCallbackParams{
		ID: c.ID, DelaySeconds: s.retryDelay(c.Attempts).Seconds(), LastError: &msg,
	}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// alertUnmatched raises PAYMENT_UNRESOLVED for an unmatched callback, naming the receipt, the
// amount and the phone the body carries so a person can find the payment on the M-Pesa
// statement. organiser_alerts belongs to an event: the alert goes to the event of the payment
// the callback seems to fit, or, when there is none, to the event the same phone last paid
// for, or the newest event (the message says it may not be that event's).
func (s *Service) alertUnmatched(ctx context.Context, c store.ClaimedCallback, cand *store.ListIDLessPaymentCandidatesRow) error {
	amount := "an unreadable amount"
	if c.AmountCents != nil {
		amount = "KES " + formatKES(*c.AmountCents)
	}
	var eventID int64
	var bookingID, paymentID *int64
	msg := fmt.Sprintf("M-Pesa reported a payment we have no record of asking for under that ID: receipt %s, %s, from %s (CheckoutRequestID %s).",
		orDash(deref(c.MpesaReceipt)), amount, orDash(deref(c.PhoneE164)), c.CheckoutRequestID)
	if cand != nil {
		eventID, bookingID, paymentID = cand.EventID, &cand.BookingID, &cand.PaymentID
		msg += " It looks like the payment for this booking, whose M-Pesa ID we never learned; M-Pesa has not confirmed it yet. If it stays unconfirmed, check the M-Pesa statement and refund it."
	} else {
		guess, err := s.q.GuessEventForUnmatchedCallback(ctx, deref(c.PhoneE164))
		if errors.Is(err, store.ErrNoRows) {
			s.log.ErrorContext(ctx, "an unmatched payment callback has no event to raise its alert under", "unmatched_callback_id", c.ID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("find an event for the alert: %w", err)
		}
		eventID = guess
		msg += " It could not be tied to any booking (it may not be for this event). Check the M-Pesa statement and refund it if the money arrived."
	}
	return store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		a, err := q.InsertAlert(ctx, store.InsertAlertParams{EventID: eventID, BookingID: bookingID, PaymentID: paymentID, Kind: "PAYMENT_UNRESOLVED", Message: msg})
		if err != nil {
			return err
		}
		_, err = q.SetUnmatchedCallbackAlert(ctx, store.SetUnmatchedCallbackAlertParams{ID: c.ID, AlertID: a.ID})
		return err
	})
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
