package mock

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// A callback is only a hint to query the provider, so the mock's Query must agree with a
// callback it has delivered, whatever its clock says; and its errors, all raised before
// anything is "sent", carry the NotSent mark.
func TestQueryAgreesWithADeliveredCallback(t *testing.T) {
	p, tm, _, got := newMock(t)
	ctx := payments.WithMockOutcome(context.Background(), WrongAmount)
	init, err := p.Initiate(ctx, payments.PaymentRequest{AmountCents: 150_000, Phone: "+254712345678"})
	require.NoError(t, err)

	st, err := p.Query(context.Background(), init.CheckoutRequestID)
	require.NoError(t, err)
	assert.Equal(t, payments.StatePending, st.State, "the clock has not moved and nothing was delivered")

	require.Len(t, tm.due, 1)
	tm.due[0].fn()
	require.Len(t, *got, 1)
	cb := (*got)[0]
	assert.Equal(t, "+254712345678", cb.Phone, "the callback names the paying number")
	assert.Contains(t, string(cb.Raw), `"PhoneNumber"`)

	st, err = p.Query(context.Background(), init.CheckoutRequestID)
	require.NoError(t, err)
	assert.Equal(t, payments.StateSuccess, st.State, "delivered: the query now says what the callback said")
	require.NotNil(t, st.AmountCents)
	assert.Equal(t, *cb.AmountCents, *st.AmountCents, "the mock's query names the amount (Daraja's does not): wrong_amount is the provider's word")
	assert.EqualValues(t, 150_100, *st.AmountCents)

	_, err = p.Initiate(context.Background(), payments.PaymentRequest{AmountCents: 150, Phone: "+254712345678"})
	assert.True(t, payments.IsNotSent(err))
	_, err = p.Initiate(payments.WithMockOutcome(context.Background(), "free_tickets"), payments.PaymentRequest{AmountCents: 100, Phone: "+254712345678"})
	assert.True(t, payments.IsNotSent(err))
}
