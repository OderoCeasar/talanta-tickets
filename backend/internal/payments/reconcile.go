package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// resultOfCallback turns a parsed callback into the report Finish takes.
func resultOfCallback(cb Callback) Result {
	code := int32(cb.ResultCode)
	res := Result{
		Source: SourceCallback, Outcome: OutcomeFailed, ResultCode: &code, ResultDesc: cb.ResultDesc,
		AmountCents: cb.AmountCents, Receipt: cb.Receipt, Raw: cb.Raw,
	}
	if cb.ResultCode == domain.ResultSuccess {
		res.Outcome = OutcomeSuccess
	}
	return res
}

// resultOfQuery turns a final query answer into the report Finish takes.
func resultOfQuery(st PaymentStatus) Result {
	code := int32(st.ResultCode)
	res := Result{
		Source: SourceQuery, Outcome: OutcomeFailed, ResultCode: &code, ResultDesc: st.ResultDesc,
		AmountCents: st.AmountCents, Receipt: st.Receipt,
	}
	if st.State == StateSuccess {
		res.Outcome = OutcomeSuccess
	}
	return res
}

// HandleCallback applies a provider callback: the webhook's work, and the mock's.
//
// The body is never trusted alone (§8.2). It is matched to a payment we know by its
// CheckoutRequestID; an unknown ID is logged and nothing changes. What it says is then put
// through Finish, which accepts it only for a payment that is still open and checks the
// amount against the booking total. A "paid" with no readable amount cannot be checked
// against anything, so it is not applied: the provider is asked instead (an authenticated
// call), and its answer goes through Finish like any reconciliation result.
//
// It reports what was done; the webhook ignores that and always answers 200.
func (s *Service) HandleCallback(ctx context.Context, cb Callback) (Finished, error) {
	if cb.CheckoutRequestID == "" {
		s.log.WarnContext(ctx, "payment callback ignored: no CheckoutRequestID")
		return Finished{Action: ActionNone}, nil
	}
	paymentID, err := s.q.GetPaymentIDByCheckoutRequestID(ctx, cb.CheckoutRequestID)
	if errors.Is(err, store.ErrNoRows) {
		// Forged, or for a request whose ID we never got to record. Either way there is
		// nothing of ours to change. Logged loudly when it claims money moved.
		level := s.log.WarnContext
		if cb.ResultCode == domain.ResultSuccess {
			level = s.log.ErrorContext
		}
		level(ctx, "payment callback ignored: unknown CheckoutRequestID",
			"checkout_request_id", cb.CheckoutRequestID, "result_code", cb.ResultCode)
		return Finished{Action: ActionNone}, nil
	}
	if err != nil {
		return Finished{}, fmt.Errorf("find payment of callback: %w", err)
	}

	out, err := s.Finish(ctx, paymentID, resultOfCallback(cb))
	if err != nil {
		return Finished{}, err
	}
	if out.Action != ActionUnverifiable {
		return out, nil
	}
	return s.resolveByQuery(ctx, paymentID, cb.CheckoutRequestID)
}

// HandleCallbackAsync is HandleCallback off the caller's path: the webhook answers 200 at
// once and the work runs in the background, with its own deadline. It reports whether the
// work was started; when it was not (too much in flight), reconciliation will find the
// payment's result.
func (s *Service) HandleCallbackAsync(ctx context.Context, cb Callback) bool {
	ctx = context.WithoutCancel(ctx)
	return s.bg.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := s.HandleCallback(ctx, cb); err != nil {
			s.log.ErrorContext(ctx, "payment callback could not be applied; reconciliation will finish the payment",
				"checkout_request_id", cb.CheckoutRequestID, "err", err)
		}
	})
}

// resolveByQuery asks the provider about one payment and applies a final answer.
func (s *Service) resolveByQuery(ctx context.Context, paymentID int64, checkoutRequestID string) (Finished, error) {
	qctx, cancel := context.WithTimeout(ctx, s.policy.QueryTimeout)
	st, err := s.provider.Query(qctx, checkoutRequestID)
	cancel()
	if err != nil {
		return Finished{}, fmt.Errorf("query provider about payment %d: %w", paymentID, err)
	}
	if st.State == StatePending {
		return Finished{PaymentID: paymentID, Action: ActionNone, Reason: "the provider has no result yet"}, nil
	}
	return s.Finish(ctx, paymentID, resultOfQuery(st))
}

// ReconcileResult counts what one reconciliation run did.
type ReconcileResult struct {
	// Claimed is how many live payments this run took.
	Claimed int
	// Sold, Refunds and Failed count the payments it finished, by how they ended.
	Sold, Refunds, Failed int
	// TimedOut counts the payments it closed as TIMEOUT.
	TimedOut int
	// Pending counts the payments the provider had no answer for yet.
	Pending int
	// Errors counts the payments it could not deal with this time.
	Errors int
}

// Reconcile finds payments whose result never arrived and settles them (§8.2). The worker
// runs it every RECONCILE_INTERVAL.
//
//   - A PENDING payment older than Policy.ReconcileAfter (60 s): the provider is asked, and a
//     final answer goes through Finish, the same function the callback uses. It sells or
//     queues a refund exactly as a callback would have.
//   - No final answer by Policy.GiveUpAfter (5 minutes): TIMEOUT, with an alert, because
//     money may have moved. A TIMEOUT payment is still corrected if M-Pesa reports later.
//   - An INITIATED payment that old was written just before a crash and never sent: TIMEOUT.
//
// Payments are claimed with FOR UPDATE SKIP LOCKED and a lease, so two workers never ask
// about the same payment, and a payment a callback is finishing right now is skipped. Every
// call to the provider is made outside any transaction.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	var out ReconcileResult
	claimed, err := store.ClaimPaymentsForReconcile(ctx, s.db, s.policy.ReconcileAfter, s.policy.ReconcileLease, s.policy.ReconcileBatch)
	if err != nil {
		return out, fmt.Errorf("claim payments to reconcile: %w", err)
	}
	out.Claimed = len(claimed)
	for _, p := range claimed {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		fin, err := s.reconcileOne(ctx, p)
		switch {
		case err != nil:
			out.Errors++
			s.log.ErrorContext(ctx, "payment reconciliation: could not settle a payment; it will be tried again",
				"payment_id", p.ID, "attempt", p.QueryAttempts, "err", err)
		case fin.Action == ActionSold:
			out.Sold++
		case fin.Action == ActionRefund:
			out.Refunds++
		case fin.Action == ActionFailed && fin.PaymentStatus == domain.PaymentTimeout:
			out.TimedOut++
		case fin.Action == ActionFailed:
			out.Failed++
		default:
			out.Pending++
		}
	}
	return out, nil
}

func (s *Service) reconcileOne(ctx context.Context, p store.ClaimedPayment) (Finished, error) {
	if p.CheckoutRequestID == nil {
		// Never accepted by the provider: there is nothing to ask about.
		return s.Finish(ctx, p.ID, Result{
			Source: SourceInternal, Outcome: OutcomeTimedOut, ResultDesc: "The request never reached M-Pesa.",
		})
	}

	qctx, cancel := context.WithTimeout(ctx, s.policy.QueryTimeout)
	st, err := s.provider.Query(qctx, *p.CheckoutRequestID)
	cancel()
	if err == nil && st.State != StatePending {
		return s.Finish(ctx, p.ID, resultOfQuery(st))
	}
	if p.Age < s.policy.GiveUpAfter {
		if err != nil {
			return Finished{}, fmt.Errorf("query provider: %w", err)
		}
		return Finished{PaymentID: p.ID, Action: ActionNone}, nil
	}
	if err != nil {
		s.log.WarnContext(ctx, "payment reconciliation: giving up on a payment the provider could not be asked about",
			"payment_id", p.ID, "attempts", p.QueryAttempts, "err", err)
	}
	return s.Finish(ctx, p.ID, Result{
		Source: SourceInternal, Outcome: OutcomeTimedOut, Unresolved: true,
		ResultDesc: fmt.Sprintf("No answer from M-Pesa within %d minutes.", int(s.policy.GiveUpAfter.Minutes())),
	})
}
