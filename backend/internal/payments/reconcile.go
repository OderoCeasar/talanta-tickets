package payments

import (
	"context"
	"fmt"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// resultOfQuery turns a final query answer into the report Finish takes. It is the only way
// a success reaches Finish.
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

// query asks the provider about one request, outside any transaction, with its own deadline.
func (s *Service) query(ctx context.Context, checkoutRequestID string) (PaymentStatus, error) {
	qctx, cancel := context.WithTimeout(ctx, s.policy.QueryTimeout)
	defer cancel()
	return s.provider.Query(qctx, checkoutRequestID)
}

// ReconcileResult counts what one reconciliation run did.
type ReconcileResult struct {
	// Claimed is how many live payments this run took.
	Claimed int
	// Requeried is how many TIMEOUT payments this run asked M-Pesa about again.
	Requeried int
	// Sold, Refunds and Failed count the payments it finished, by how they ended.
	Sold, Refunds, Failed int
	// TimedOut counts the payments it closed as TIMEOUT.
	TimedOut int
	// Pending counts the payments the provider had no answer for yet.
	Pending int
	// Errors counts the payments it could not deal with this time.
	Errors int
	// OutOfTime is true when the run stopped starting provider calls because
	// Policy.ReconcileBudget was spent; what it did not get to waits for the next run.
	OutOfTime bool
}

// reconcileChunk is how many payments are claimed at a time. A claim is a lease, so a small
// chunk keeps a run that runs out of time from sitting on payments it never got to.
const reconcileChunk = 10

// Reconcile finds payments whose result never arrived and settles them (§8.2). The worker
// runs it every RECONCILE_INTERVAL. In order:
//
//  1. Live payments with NO provider ID. They need no call to M-Pesa, so they go first and a
//     slow M-Pesa cannot hold them up. One that provably was never sent is closed TIMEOUT,
//     quietly. Any other (the request may have reached M-Pesa and its answer was lost, or
//     the process died around the call) waits until Policy.GiveUpAfter, in case its callback
//     turns up and is matched to it, and is then closed TIMEOUT WITH an alert: money may
//     have moved under an ID we never learned.
//  2. Live payments with a provider ID, older than Policy.ReconcileAfter (60 s): the
//     provider is asked, and a final answer goes through Finish, the same function a
//     callback's result goes through. No final answer by Policy.GiveUpAfter (5 minutes):
//     TIMEOUT, with an alert.
//  3. TIMEOUT payments with a provider ID: the provider is asked again on a slow schedule
//     (Policy.Requery: every 5 minutes for a day, then daily for a week). TIMEOUT is our
//     verdict, not M-Pesa's, and this is what makes M-Pesa's last word reach Finish even if
//     its callback never does.
//
// Payments are claimed with FOR UPDATE SKIP LOCKED and a lease, so two workers never ask
// about the same payment, and a payment being finished right now is skipped. Every call to
// the provider is made outside any transaction. Steps 2 and 3 stop starting calls once
// Policy.ReconcileBudget is spent.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	var out ReconcileResult
	deadline := s.now().Add(s.policy.ReconcileBudget)
	count := func(p store.ClaimedPayment, fin Finished, err error) {
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

	// 1. No provider ID: nothing to ask.
	idless, err := store.ClaimPaymentsForReconcile(ctx, s.db, s.policy.ReconcileAfter, s.policy.ReconcileLease, s.policy.ReconcileBatch, false, store.PaymentCursor{})
	if err != nil {
		return out, fmt.Errorf("claim payments to reconcile: %w", err)
	}
	out.Claimed += len(idless)
	for _, p := range idless {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		fin, err := s.reconcileIDLess(ctx, p)
		count(p, fin, err)
	}

	// 2 and 3. Ask the provider, in chunks, while there is time.
	steps := []struct {
		claim func(after store.PaymentCursor, limit int) ([]store.ClaimedPayment, error)
		seen  *int
	}{
		{func(after store.PaymentCursor, limit int) ([]store.ClaimedPayment, error) {
			return store.ClaimPaymentsForReconcile(ctx, s.db, s.policy.ReconcileAfter, s.policy.ReconcileLease, limit, true, after)
		}, &out.Claimed},
		{func(after store.PaymentCursor, limit int) ([]store.ClaimedPayment, error) {
			return store.ClaimTimedOutPaymentsForRequery(ctx, s.db, s.policy.Requery, limit, after)
		}, &out.Requeried},
	}
	for _, step := range steps {
		var after store.PaymentCursor
		for left := s.policy.ReconcileBatch; left > 0; {
			if !s.now().Before(deadline) {
				out.OutOfTime = true
				break
			}
			claimed, err := step.claim(after, min(left, reconcileChunk))
			if err != nil {
				return out, fmt.Errorf("claim payments to reconcile: %w", err)
			}
			if len(claimed) == 0 {
				break
			}
			*step.seen += len(claimed)
			left -= len(claimed)
			for _, p := range claimed {
				after = store.PaymentCursor{CreatedAt: p.CreatedAt, ID: p.ID}
				if err := ctx.Err(); err != nil {
					return out, err
				}
				if !s.now().Before(deadline) {
					// Claimed and not asked about: it is tried again once its lease is over.
					out.OutOfTime = true
					out.Pending++
					continue
				}
				fin, err := s.reconcileOne(ctx, p)
				count(p, fin, err)
			}
		}
	}
	if out.OutOfTime {
		s.log.WarnContext(ctx, "payment reconciliation: out of time; the rest waits for the next run",
			"budget", s.policy.ReconcileBudget.String(), "claimed", out.Claimed, "requeried", out.Requeried)
	}
	return out, nil
}

// reconcileIDLess settles a live payment that has no provider ID to ask about.
func (s *Service) reconcileIDLess(ctx context.Context, p store.ClaimedPayment) (Finished, error) {
	if p.Dispatch == store.DispatchNotSent {
		// Provably never sent: no money can have moved, so no alert.
		return s.Finish(ctx, p.ID, Result{
			Source: SourceInternal, Outcome: OutcomeTimedOut, ResultDesc: "The request never reached M-Pesa.",
		})
	}
	if p.Age < s.policy.GiveUpAfter {
		// The fan may be entering the PIN right now; the callback, when it comes, is
		// matched to this payment by phone and amount.
		return Finished{PaymentID: p.ID, Action: ActionNone}, nil
	}
	return s.Finish(ctx, p.ID, Result{
		Source: SourceInternal, Outcome: OutcomeTimedOut, Unresolved: true,
		ResultDesc: "We could not confirm whether the request reached M-Pesa.",
	})
}

// reconcileOne asks the provider about a payment that has a provider ID.
func (s *Service) reconcileOne(ctx context.Context, p store.ClaimedPayment) (Finished, error) {
	st, err := s.query(ctx, *p.CheckoutRequestID)
	if err == nil && st.State != StatePending {
		return s.Finish(ctx, p.ID, resultOfQuery(st))
	}
	if p.Status == string(domain.PaymentTimeout) || p.Age < s.policy.GiveUpAfter {
		// Already closed as TIMEOUT (it is only being asked about again), or not old enough
		// to give up on.
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

// retryDelay is how long a stored callback waits before its next attempt: the base doubled
// per attempt already made, up to the maximum.
func (s *Service) retryDelay(attempts int) time.Duration {
	d := s.policy.CallbackRetryBase
	for i := 1; i < attempts && d < s.policy.CallbackRetryMax; i++ {
		d *= 2
	}
	return min(d, s.policy.CallbackRetryMax)
}
