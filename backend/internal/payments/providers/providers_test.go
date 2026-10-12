package providers

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

func TestNoneRefusesEverything(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, mockProvider, err := New(config.Payments{Provider: config.ProviderNone}, config.Base{AppEnv: config.EnvProduction}, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if mockProvider != nil {
		t.Fatal("the none provider must not hand back a mock")
	}
	// Refused, and marked as never sent: the attempt is closed FAILED, not left live.
	if _, err := p.Initiate(context.Background(), payments.PaymentRequest{AmountCents: 100}); !errors.Is(err, ErrPaymentsDisabled) || !payments.IsNotSent(err) {
		t.Fatalf("Initiate err = %v, want ErrPaymentsDisabled marked NotSent", err)
	}
	// The payment service ignores callbacks for a provider that takes no payments.
	if sw, ok := p.(payments.Switch); !ok || sw.TakesPayments() {
		t.Fatal("the none provider must report that it takes no payments")
	}
	// A query that "succeeded" would let reconciliation mark a payment paid.
	if st, err := p.Query(context.Background(), "any"); err != ErrPaymentsDisabled || st.State == payments.StateSuccess {
		t.Fatalf("Query = %+v, %v; want ErrPaymentsDisabled and no success", st, err)
	}
}

func TestMockCannotBeBuiltInProduction(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, _, err := New(config.Payments{Provider: config.ProviderMock}, config.Base{AppEnv: config.EnvProduction}, logger); err == nil {
		t.Fatal("want an error: the mock must not exist in production")
	}
}
