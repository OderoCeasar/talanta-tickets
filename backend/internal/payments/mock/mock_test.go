package mock

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

type timers struct {
	mu  sync.Mutex
	due []struct {
		d  time.Duration
		fn func()
	}
}

func (t *timers) after(d time.Duration, fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.due = append(t.due, struct {
		d  time.Duration
		fn func()
	}{d, fn})
}

func newMock(t *testing.T) (*Provider, *timers, *time.Time, *[]payments.Callback) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	tm := &timers{}
	var got []payments.Callback
	p, err := New(Config{Allowed: true, Now: func() time.Time { return now }, After: tm.after})
	require.NoError(t, err)
	p.SetSink(func(_ context.Context, cb payments.Callback) { got = append(got, cb) })
	return p, tm, &now, &got
}

// The mock provider cannot exist in production.
func TestNew_RefusedUnlessDevShortcutsAreAllowed(t *testing.T) {
	_, err := New(Config{})
	require.ErrorIs(t, err, ErrNotAllowed)
	_, err = New(Config{Allowed: false, CallbackDelay: time.Second})
	require.ErrorIs(t, err, ErrNotAllowed)
	p, err := New(Config{Allowed: true})
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestOutcomes(t *testing.T) {
	cases := []struct {
		outcome   string
		callbacks int
		delay     time.Duration
		code      int
		success   bool
		extra     int64
	}{
		{Success, 1, 2 * time.Second, 0, true, 0},
		{InsufficientFunds, 1, 2 * time.Second, 1, false, 0},
		{WrongPIN, 1, 2 * time.Second, 2001, false, 0},
		{UserCancel, 1, 2 * time.Second, 1032, false, 0},
		{Timeout, 1, 2 * time.Second, 1037, false, 0},
		{NoCallback, 0, 0, 0, true, 0},
		{DuplicateCallback, 2, 2 * time.Second, 0, true, 0},
		{LateCallback, 1, 7 * time.Minute, 0, true, 0},
		{WrongAmount, 1, 2 * time.Second, 0, true, 100},
	}
	require.Len(t, cases, len(payments.MockOutcomes))
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			p, tm, now, got := newMock(t)
			ctx := payments.WithMockOutcome(context.Background(), tc.outcome)
			init, err := p.Initiate(ctx, payments.PaymentRequest{PaymentID: 1, AmountCents: 250_000, Phone: "+254712345678"})
			require.NoError(t, err)
			require.NotEmpty(t, init.CheckoutRequestID)
			require.Len(t, tm.due, tc.callbacks)

			// Before the outcome's delay the provider has no answer.
			st, err := p.Query(context.Background(), init.CheckoutRequestID)
			require.NoError(t, err)
			assert.Equal(t, payments.StatePending, st.State)

			for _, due := range tm.due {
				assert.Equal(t, tc.delay, due.d)
				due.fn()
			}
			require.Len(t, *got, tc.callbacks)
			for _, cb := range *got {
				assert.Equal(t, init.CheckoutRequestID, cb.CheckoutRequestID)
				assert.Equal(t, tc.code, cb.ResultCode)
				assert.True(t, json.Valid(cb.Raw), "the raw body is JSON")
				if tc.success {
					require.NotNil(t, cb.AmountCents)
					assert.EqualValues(t, 250_000+tc.extra, *cb.AmountCents)
					assert.NotEmpty(t, cb.Receipt)
				} else {
					assert.Nil(t, cb.AmountCents)
				}
			}

			// After it, Query gives the same verdict as the callback, from the ID alone:
			// a second provider (another process) answers identically.
			*now = now.Add(8 * time.Minute)
			other, err := New(Config{Allowed: true, Now: func() time.Time { return *now }, After: func(time.Duration, func()) {}})
			require.NoError(t, err)
			for _, provider := range []*Provider{p, other} {
				st, err = provider.Query(context.Background(), init.CheckoutRequestID)
				require.NoError(t, err)
				assert.Equal(t, tc.code, st.ResultCode)
				if tc.success {
					assert.Equal(t, payments.StateSuccess, st.State)
					require.NotNil(t, st.AmountCents)
					assert.EqualValues(t, 250_000+tc.extra, *st.AmountCents)
					if tc.callbacks > 0 {
						assert.Equal(t, (*got)[0].Receipt, st.Receipt, "one receipt per payment, whoever reports it")
					}
				} else {
					assert.Equal(t, payments.StateFailed, st.State)
				}
			}
		})
	}
}

func TestOutcomeSelection(t *testing.T) {
	p, tm, _, got := newMock(t)
	req := payments.PaymentRequest{AmountCents: 100_000, Phone: "+254712345678"}

	// No header, ordinary phone: success.
	_, err := p.Initiate(context.Background(), req)
	require.NoError(t, err)
	// A magic phone.
	req.Phone = "+254700000103"
	_, err = p.Initiate(context.Background(), req)
	require.NoError(t, err)
	// The header beats the phone.
	_, err = p.Initiate(payments.WithMockOutcome(context.Background(), WrongPIN), req)
	require.NoError(t, err)
	for _, due := range tm.due {
		due.fn()
	}
	require.Len(t, *got, 3)
	assert.Equal(t, 0, (*got)[0].ResultCode)
	assert.Equal(t, 1032, (*got)[1].ResultCode)
	assert.Equal(t, 2001, (*got)[2].ResultCode)

	_, err = p.Initiate(payments.WithMockOutcome(context.Background(), "free_tickets"), req)
	require.Error(t, err)
	req.AmountCents = 100_050
	_, err = p.Initiate(context.Background(), req)
	require.ErrorIs(t, err, payments.ErrAmountNotWholeShillings)

	for _, id := range []string{"", "ws_CO_191220191020363925", "ws_MOCK.success.x.1.aa", "ws_MOCK.jackpot.100.1.aa", "ws_MOCK.success.100.1"} {
		_, err := p.Query(context.Background(), id)
		require.Error(t, err, "id %q is not one of the mock's", id)
	}
	assert.Len(t, MagicPhones, len(payments.MockOutcomes)-1, "a magic phone for every outcome but success")
}

// With no sink (the worker) nothing is delivered and nothing breaks.
func TestNoSink(t *testing.T) {
	tm := &timers{}
	p, err := New(Config{Allowed: true, After: tm.after})
	require.NoError(t, err)
	_, err = p.Initiate(context.Background(), payments.PaymentRequest{AmountCents: 100_000, Phone: "+254712345678"})
	require.NoError(t, err)
	require.Len(t, tm.due, 1)
	tm.due[0].fn()
}
