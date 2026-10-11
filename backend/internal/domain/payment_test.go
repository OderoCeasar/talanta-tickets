package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The whole booking table, both ways: every pair of statuses is either listed as legal here
// or must be refused.
func TestTransitionBooking(t *testing.T) {
	legal := map[BookingStatus][]BookingStatus{
		BookingPendingPayment: {BookingPaid, BookingFailed, BookingExpired, BookingCancelled, BookingRefundRequired},
		BookingPaid:           {BookingRefundRequired},
		// A late success must be recordable on a booking we had given up on.
		BookingFailed:         {BookingRefundRequired},
		BookingExpired:        {BookingRefundRequired},
		BookingCancelled:      {BookingRefundRequired},
		BookingRefundRequired: {BookingRefunded},
		BookingRefunded:       {BookingRefundRequired},
	}
	for _, from := range BookingStatuses {
		for _, to := range BookingStatuses {
			want := false
			for _, ok := range legal[from] {
				want = want || ok == to
			}
			err := TransitionBooking(from, to)
			assert.Equal(t, want, err == nil, "%s -> %s", from, to)
		}
	}
	// Nothing un-sells: a paid booking never goes back to unpaid, expired or cancelled.
	for _, to := range []BookingStatus{BookingPendingPayment, BookingExpired, BookingCancelled, BookingFailed, BookingRefunded, BookingPaid} {
		assert.Error(t, TransitionBooking(BookingPaid, to), "PAID -> %s", to)
	}
	assert.Error(t, TransitionBooking("NONSENSE", BookingPaid))
	assert.Error(t, TransitionBooking(BookingExpired, BookingPaid), "an expired booking is never revived")
	assert.Error(t, TransitionBooking(BookingCancelled, BookingPaid))
	assert.True(t, BookingPendingPayment.IsLive() && BookingPaid.IsLive())
	assert.False(t, BookingRefundRequired.IsLive() || BookingExpired.IsLive())
}

func TestTransitionPayment(t *testing.T) {
	all := []PaymentStatus{PaymentInitiated, PaymentPending, PaymentSuccess, PaymentFailed, PaymentTimeout, PaymentReversed}
	legal := map[PaymentStatus][]PaymentStatus{
		PaymentInitiated: {PaymentPending, PaymentFailed, PaymentTimeout},
		PaymentPending:   {PaymentSuccess, PaymentFailed, PaymentTimeout},
		// TIMEOUT is our verdict; a late answer from M-Pesa overrules it.
		PaymentTimeout: {PaymentSuccess, PaymentFailed},
		PaymentSuccess: {PaymentReversed},
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, ok := range legal[from] {
				want = want || ok == to
			}
			assert.Equal(t, want, TransitionPayment(from, to) == nil, "%s -> %s", from, to)
		}
	}
	// What makes a repeated callback harmless.
	assert.Error(t, TransitionPayment(PaymentSuccess, PaymentSuccess))
	assert.Error(t, TransitionPayment(PaymentFailed, PaymentSuccess))
	assert.Error(t, TransitionPayment(PaymentSuccess, PaymentFailed))
	assert.True(t, PaymentInitiated.IsLive() && PaymentPending.IsLive())
	assert.False(t, PaymentTimeout.IsLive() || PaymentSuccess.IsLive() || PaymentFailed.IsLive())
}

func TestPaymentFailureReason_EveryCase(t *testing.T) {
	code := func(n int32) *int32 { return &n }
	cases := []struct {
		status string
		code   *int32
		want   string
	}{
		{"FAILED", code(1), "insufficient_funds"},
		{"FAILED", code(2001), "wrong_pin"},
		{"FAILED", code(1032), "cancelled"},
		{"FAILED", code(1037), "timeout"},
		{"FAILED", code(1019), "timeout"},
		{"FAILED", code(0), "wrong_amount"},
		{"FAILED", code(9999), "other"},
		{"FAILED", nil, "other"},
		{"TIMEOUT", nil, "timeout"},
		{"TIMEOUT", code(0), "timeout"},
	}
	for _, tc := range cases {
		got := PaymentFailureReason(tc.status, tc.code)
		if assert.NotNil(t, got, "%s %v", tc.status, tc.code) {
			assert.Equal(t, tc.want, *got, "%s %v", tc.status, tc.code)
		}
	}
	for _, status := range []string{"INITIATED", "PENDING", "SUCCESS", "REVERSED"} {
		assert.Nil(t, PaymentFailureReason(status, code(1)), status)
	}
}
