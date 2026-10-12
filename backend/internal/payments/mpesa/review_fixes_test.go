package mpesa

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// Daraja answers HTTP 200 with a non-zero ResultCode for more than final failures. Only a
// code known to be final closes a payment; "still under processing" and anything unknown is
// "ask again later". Reading these as failures closed payments the fan then paid for.
func TestSTKQuery_OnlyKnownFinalCodesAreFailures(t *testing.T) {
	for code, want := range map[string]payments.State{
		"4999": payments.StatePending, // the transaction is still under processing
		"17":   payments.StatePending, // not on the documented list of final results
		"26":   payments.StatePending,
		"8006": payments.StatePending,
		"-1":   payments.StatePending,
		"1":    payments.StateFailed,
		"1001": payments.StateFailed,
		"1019": payments.StateFailed,
		"1025": payments.StateFailed,
		"1032": payments.StateFailed,
		"1037": payments.StateFailed,
		"2001": payments.StateFailed,
		"9999": payments.StateFailed,
		"0":    payments.StateSuccess,
	} {
		t.Run(code, func(t *testing.T) {
			f := newFixture(t)
			f.d.set(func(d *daraja) {
				d.queryAnswer = func(w http.ResponseWriter, _ int) { queryResult(w, code, "whatever Daraja says") }
			})
			st, err := f.Query(context.Background(), "ws_CO_1")
			require.NoError(t, err)
			assert.Equal(t, want, st.State, "result code %s", code)
		})
	}
}

// An Initiate error must say whether the push can have reached Daraja: that decides whether
// the payment is closed (nothing was sent) or kept live (the fan may be looking at a prompt).
func TestSTKPush_ErrorsSayWhetherThePushWasSent(t *testing.T) {
	answer := func(status int, body string) func(d *daraja) {
		return func(d *daraja) {
			d.pushHandler = func(w http.ResponseWriter, _ map[string]any) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			}
		}
	}

	t.Run("not sent", func(t *testing.T) {
		cases := map[string]func(f fixture){
			"a refusal with Daraja's error body": func(f fixture) {
				f.d.set(answer(http.StatusBadRequest, `{"errorCode":"400.002.02","errorMessage":"Bad Request - Invalid PhoneNumber"}`))
			},
			"a 4xx with no body": func(f fixture) { f.d.set(answer(http.StatusForbidden, ``)) },
			"a 5xx carrying Daraja's own error": func(f fixture) {
				f.d.set(answer(http.StatusInternalServerError, `{"errorCode":"500.001.1001","errorMessage":"Unable to lock subscriber, a transaction is already in process for the current subscriber"}`))
			},
			"a 200 that refuses": func(f fixture) {
				f.d.set(answer(http.StatusOK, `{"ResponseCode":"1","ResponseDescription":"Rejected"}`))
			},
			"no token could be had": func(f fixture) {
				f.d.set(func(d *daraja) { d.tokenStatus = http.StatusUnauthorized })
			},
			"nothing is listening (dial failure)": func(f fixture) {
				_, err := f.Query(context.Background(), "ws_CO_1") // a token, while the server is up
				require.NoError(t, err)
				f.d.srv.Close()
			},
		}
		for name, arrange := range cases {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				arrange(f)
				_, err := f.Initiate(context.Background(), request)
				require.Error(t, err)
				assert.True(t, payments.IsNotSent(err), "must be marked NotSent: %v", err)
			})
		}
		t.Run("the amount was refused before anything was sent", func(t *testing.T) {
			f := newFixture(t)
			req := request
			req.AmountCents = 250_050
			_, err := f.Initiate(context.Background(), req)
			require.ErrorIs(t, err, payments.ErrAmountNotWholeShillings)
			assert.True(t, payments.IsNotSent(err))
		})
		t.Run("the deadline passed before the request was written", func(t *testing.T) {
			f := newFixture(t)
			_, err := f.Query(context.Background(), "ws_CO_1")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = f.Initiate(ctx, request)
			require.Error(t, err)
			assert.True(t, payments.IsNotSent(err), "nothing was written: %v", err)
			assert.Empty(t, f.d.sentPushes())
		})
	})

	t.Run("ambiguous", func(t *testing.T) {
		cases := map[string]func(f fixture){
			"the connection died after Daraja received the push": func(f fixture) {
				atomic.StoreInt32(&f.d.dropPushes, 100)
			},
			"a 200 whose body is not Daraja's JSON": func(f fixture) { f.d.set(answer(http.StatusOK, `<html>ok</html>`)) },
			"a 200 that accepts and names no CheckoutRequestID": func(f fixture) {
				f.d.set(answer(http.StatusOK, `{"ResponseCode":"0","ResponseDescription":"Success"}`))
			},
			"a 200 with an empty object": func(f fixture) { f.d.set(answer(http.StatusOK, `{}`)) },
			"a gateway's 504 with no Daraja error in it": func(f fixture) {
				f.d.set(answer(http.StatusGatewayTimeout, `<html>504 Gateway Time-out</html>`))
			},
			"a 502 with an empty body": func(f fixture) { f.d.set(answer(http.StatusBadGateway, ``)) },
		}
		for name, arrange := range cases {
			t.Run(name, func(t *testing.T) {
				f := newFixture(t)
				arrange(f)
				_, err := f.Initiate(context.Background(), request)
				require.Error(t, err)
				assert.False(t, payments.IsNotSent(err), "Daraja may have the push; must NOT be marked NotSent: %v", err)
				assert.Len(t, f.d.sentPushes(), 1, "and it is never sent a second time")
			})
		}
		t.Run("a timeout after the request was written", func(t *testing.T) {
			f := newFixture(t, func(c *Config) { c.Timeout = 150 * time.Millisecond })
			release := make(chan struct{})
			f.d.set(func(d *daraja) { d.pushHandler = func(http.ResponseWriter, map[string]any) { <-release } })
			defer close(release)
			_, err := f.Initiate(context.Background(), request)
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.False(t, payments.IsNotSent(err), "Daraja has the push: %v", err)
			assert.Len(t, f.d.sentPushes(), 1)
		})
	})
}

// A ResultCode that does not fit 32 bits is not a callback. 4294967296 cut down to 32 bits
// is 0, which reads as "paid".
func TestParseCallback_ResultCodeMustFit32Bits(t *testing.T) {
	body := func(code string) []byte {
		return []byte(`{"Body":{"stkCallback":{"CheckoutRequestID":"ws_CO_1","ResultCode":` + code + `,"ResultDesc":"x"}}}`)
	}
	for _, code := range []string{"4294967296", "2147483648", "-2147483649", "9223372036854775807", "4294967297", "1.5", "1e2"} {
		_, err := ParseCallback(body(code))
		require.ErrorIs(t, err, ErrBadCallback, "ResultCode %s", code)
	}
	for code, want := range map[string]int{"0": 0, "1032": 1032, "2147483647": 2147483647, "-2147483648": -2147483648, `"1037"`: 1037} {
		cb, err := ParseCallback(body(code))
		require.NoError(t, err, "ResultCode %s", code)
		assert.Equal(t, want, cb.ResultCode)
	}
}

// The paying number is read for matching a callback whose ID we never learned.
func TestParseCallback_Phone(t *testing.T) {
	for raw, want := range map[string]string{
		`254712345678`:    "+254712345678",
		`"254712345678"`:  "+254712345678",
		`"+254712345678"`: "+254712345678",
		// A masked or mangled number is no number: nothing can be matched by it.
		`"2547******78"`: "", `"0712"`: "", `null`: "", `2.54712345678e11`: "", `true`: "",
	} {
		body := `{"Body":{"stkCallback":{"CheckoutRequestID":"ws_CO_1","ResultCode":0,"ResultDesc":"ok","CallbackMetadata":{"Item":[{"Name":"PhoneNumber","Value":` + raw + `}]}}}}`
		cb, err := ParseCallback([]byte(body))
		require.NoError(t, err, raw)
		assert.Equal(t, want, cb.Phone, "PhoneNumber %s", raw)
	}
}
