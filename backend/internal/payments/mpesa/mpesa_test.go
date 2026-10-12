package mpesa

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// Fake credentials. They are searched for in every log line and error below.
const (
	testKey      = "consumer-key-AAAA1111"
	testSecret   = "consumer-secret-BBBB2222-do-not-print"
	testPasskey  = "passkey-CCCC3333-do-not-print"
	testShort    = "174379"
	testCallback = "https://talanta.example.com/v1/webhooks/mpesa/callback"
)

// daraja is a fake Daraja: the three endpoints, with counters and switchable behaviour. No
// test in this package talks to Safaricom.
type daraja struct {
	t   *testing.T
	srv *httptest.Server

	mu          sync.Mutex
	tokens      int      // tokens issued
	issued      []string // every token handed out
	expiresIn   string   // the expires_in of the next token
	tokenDelay  time.Duration
	tokenStatus int
	pushes      []map[string]any
	queries     []map[string]any
	pushHandler func(w http.ResponseWriter, body map[string]any)
	queryAnswer func(w http.ResponseWriter, attempt int)
	queryCalls  int
	dropQueries int32 // drop this many query connections before answering
	dropPushes  int32
	authSeen    []string
}

// The test and the fake's handlers run on different goroutines, so the test reads and
// changes the fake only through these.
func (d *daraja) set(fn func(d *daraja)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fn(d)
}

func (d *daraja) tokenCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tokens
}

func (d *daraja) queryCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.queryCalls
}

func (d *daraja) sentPushes() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]map[string]any(nil), d.pushes...)
}

func (d *daraja) sentQueries() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]map[string]any(nil), d.queries...)
}

func (d *daraja) tokensIssued() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.issued...)
}

func (d *daraja) lastAuth() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.authSeen[len(d.authSeen)-1]
}

func newDaraja(t *testing.T) *daraja {
	d := &daraja{t: t, expiresIn: "3599", tokenStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/v1/generate", d.token)
	mux.HandleFunc("POST /mpesa/stkpush/v1/processrequest", d.push)
	mux.HandleFunc("POST /mpesa/stkpushquery/v1/query", d.query)
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	return d
}

func (d *daraja) token(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	d.mu.Lock()
	delay, status := d.tokenDelay, d.tokenStatus
	d.mu.Unlock()
	time.Sleep(delay)
	if !ok || user != testKey || pass != testSecret || r.URL.Query().Get("grant_type") != "client_credentials" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"errorCode":"400.008.01","errorMessage":"Invalid Authentication passed: `+pass+`"}`)
		return
	}
	d.mu.Lock()
	d.tokens++
	token := "token-" + strings.Repeat("x", 8) + "-" + string(rune('A'+d.tokens))
	d.issued = append(d.issued, token)
	expires := d.expiresIn
	d.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "expires_in": expires})
}

func (d *daraja) authed(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	auth := r.Header.Get("Authorization")
	d.mu.Lock()
	d.authSeen = append(d.authSeen, auth)
	valid := false
	for _, tok := range d.issued {
		valid = valid || auth == "Bearer "+tok
	}
	d.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errorCode":"404.001.03","errorMessage":"Invalid Access Token"}`)
		return nil, false
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

// drop closes the connection without answering: a network error to the client.
func drop(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

func (d *daraja) push(w http.ResponseWriter, r *http.Request) {
	body, ok := d.authed(w, r)
	if !ok {
		return
	}
	d.mu.Lock()
	d.pushes = append(d.pushes, body)
	handler := d.pushHandler
	d.mu.Unlock()
	if atomic.AddInt32(&d.dropPushes, -1) >= 0 {
		drop(w)
		return
	}
	if handler != nil {
		handler(w, body)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"MerchantRequestID": "29115-34620561-1", "CheckoutRequestID": "ws_CO_191220191020363925",
		"ResponseCode": "0", "ResponseDescription": "Success. Request accepted for processing",
		"CustomerMessage": "Success. Request accepted for processing",
	})
}

func (d *daraja) query(w http.ResponseWriter, r *http.Request) {
	body, ok := d.authed(w, r)
	if !ok {
		return
	}
	d.mu.Lock()
	d.queries = append(d.queries, body)
	d.queryCalls++
	attempt, answer := d.queryCalls, d.queryAnswer
	d.mu.Unlock()
	if atomic.AddInt32(&d.dropQueries, -1) >= 0 {
		drop(w)
		return
	}
	if answer != nil {
		answer(w, attempt)
		return
	}
	queryResult(w, "0", "The service request is processed successfully.")
}

func queryResult(w http.ResponseWriter, code, desc string) {
	_ = json.NewEncoder(w).Encode(map[string]string{
		"ResponseCode": "0", "ResponseDescription": "The service request has been accepted successsfully",
		"MerchantRequestID": "29115-34620561-1", "CheckoutRequestID": "ws_CO_191220191020363925",
		"ResultCode": code, "ResultDesc": desc,
	})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fixture struct {
	*Provider
	d     *daraja
	logs  *syncBuffer
	now   *atomic.Int64 // unix seconds
	slept *atomic.Int64 // retries waited for
}

func newFixture(t *testing.T, mods ...func(*Config)) fixture {
	d := newDaraja(t)
	logs := &syncBuffer{}
	now, slept := &atomic.Int64{}, &atomic.Int64{}
	now.Store(time.Date(2026, 10, 11, 9, 15, 2, 0, time.UTC).Unix())
	cfg := Config{
		BaseURL: d.srv.URL, ConsumerKey: testKey, ConsumerSecret: testSecret, ShortCode: testShort,
		Passkey: testPasskey, CallbackURL: testCallback,
		Timeout: 2 * time.Second,
		Logger:  slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:     func() time.Time { return time.Unix(now.Load(), 0) },
		Sleep:   func(context.Context, time.Duration) error { slept.Add(1); return nil },
	}
	for _, m := range mods {
		m(&cfg)
	}
	p, err := New(cfg)
	require.NoError(t, err)
	return fixture{Provider: p, d: d, logs: logs, now: now, slept: slept}
}

var request = payments.PaymentRequest{PaymentID: 4410, Reference: "TLT-8H3K2M", AmountCents: 250_000, Phone: "+254712345678"}

func TestSTKPush_SendsWhatDarajaExpects(t *testing.T) {
	f := newFixture(t)
	init, err := f.Initiate(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "ws_CO_191220191020363925", init.CheckoutRequestID)
	assert.Equal(t, "29115-34620561-1", init.MerchantRequestID)

	require.Len(t, f.d.sentPushes(), 1)
	sent := f.d.sentPushes()[0]
	assert.Equal(t, testShort, sent["BusinessShortCode"])
	assert.Equal(t, "20261011121502", sent["Timestamp"], "Nairobi time (UTC+3), yyyyMMddHHmmss")
	wantPassword := base64.StdEncoding.EncodeToString([]byte(testShort + testPasskey + "20261011121502"))
	assert.Equal(t, wantPassword, sent["Password"])
	assert.Equal(t, "CustomerPayBillOnline", sent["TransactionType"])
	assert.EqualValues(t, 2500, sent["Amount"], "whole shillings")
	assert.Equal(t, "254712345678", sent["PartyA"], "no plus sign")
	assert.Equal(t, "254712345678", sent["PhoneNumber"])
	assert.Equal(t, testShort, sent["PartyB"])
	assert.Equal(t, testCallback, sent["CallBackURL"])
	assert.Equal(t, "TLT-8H3K2M", sent["AccountReference"])
	assert.LessOrEqual(t, len(sent["TransactionDesc"].(string)), 13)

	till := newFixture(t, func(c *Config) { c.TransactionType = BuyGoods })
	_, err = till.Initiate(context.Background(), request)
	require.NoError(t, err)
	assert.Equal(t, "CustomerBuyGoodsOnline", till.d.sentPushes()[0]["TransactionType"])
}

// An amount that is not a whole number of shillings is refused, never rounded, and nothing
// is sent.
func TestSTKPush_RefusesAFractionOfAShilling(t *testing.T) {
	f := newFixture(t)
	for _, cents := range []int64{250_050, 1, 99, 0, -100} {
		req := request
		req.AmountCents = cents
		_, err := f.Initiate(context.Background(), req)
		require.ErrorIs(t, err, payments.ErrAmountNotWholeShillings, "%d cents", cents)
	}
	assert.Empty(t, f.d.sentPushes())
	assert.Zero(t, f.d.tokenCount(), "not even a token was fetched")
}

func TestSTKPush_RefusalIsAnErrorWithDarajasWords(t *testing.T) {
	f := newFixture(t)
	f.d.set(func(d *daraja) {
		d.pushHandler = func(w http.ResponseWriter, _ map[string]any) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"requestId":"1-2-3","errorCode":"400.002.02","errorMessage":"Bad Request - Invalid PhoneNumber"}`)
		}
	})
	_, err := f.Initiate(context.Background(), request)
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, 400, de.HTTPStatus)
	assert.Equal(t, "400.002.02", de.Code)
	assert.Contains(t, de.Message, "Invalid PhoneNumber")
	assert.Len(t, f.d.sentPushes(), 1, "a response is never retried")

	// HTTP 200 with a refusal inside.
	f.d.set(func(d *daraja) {
		d.pushHandler = func(w http.ResponseWriter, _ map[string]any) {
			_, _ = io.WriteString(w, `{"ResponseCode":"1","ResponseDescription":"Rejected"}`)
		}
	})
	_, err = f.Initiate(context.Background(), request)
	require.ErrorAs(t, err, &de)
	assert.Equal(t, "1", de.Code)
}

// The OAuth token is fetched once and reused until shortly before it expires.
func TestToken_CachedUntilShortlyBeforeExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := f.Initiate(ctx, request)
		require.NoError(t, err)
		_, err = f.Query(ctx, "ws_CO_191220191020363925")
		require.NoError(t, err)
	}
	assert.Equal(t, 1, f.d.tokenCount(), "ten calls, one token")

	// 3599 s life, replaced 60 s early: still good at +3500 s, replaced at +3540 s.
	f.now.Add(3500)
	_, err := f.Query(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, 1, f.d.tokenCount())
	f.now.Add(40)
	_, err = f.Query(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, 2, f.d.tokenCount(), "refreshed before the old token's stated expiry")
	assert.Equal(t, "Bearer "+f.d.tokensIssued()[1], f.d.lastAuth(), "the new token is the one sent")

	// A numeric expires_in is read too.
	f.d.set(func(d *daraja) { d.expiresIn = "120" })
	f.now.Add(4000)
	_, err = f.Query(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, 3, f.d.tokenCount())
	f.now.Add(59)
	_, err = f.Query(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, 3, f.d.tokenCount(), "120 s less the 60 s margin: good for 60 s")
	f.now.Add(2)
	_, err = f.Query(ctx, "x")
	require.NoError(t, err)
	assert.Equal(t, 4, f.d.tokenCount())
}

// A burst of calls with no token makes ONE token request (single flight).
func TestToken_SingleFlight(t *testing.T) {
	f := newFixture(t)
	f.d.set(func(d *daraja) { d.tokenDelay = 150 * time.Millisecond })
	const callers = 40
	var wg sync.WaitGroup
	errs := make([]error, callers)
	gate := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			_, errs[i] = f.Query(context.Background(), "ws_CO_191220191020363925")
		}()
	}
	close(gate)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, f.d.tokenCount(), "%d concurrent calls shared one token request", callers)
	assert.Len(t, f.d.sentQueries(), callers)
}

func TestToken_RefusedCredentials(t *testing.T) {
	f := newFixture(t)
	f.d.set(func(d *daraja) { d.tokenStatus = http.StatusBadRequest })
	_, err := f.Initiate(context.Background(), request)
	var de *Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, 400, de.HTTPStatus)
	assert.Empty(t, f.d.sentPushes())
	// The fake echoes the secret in its error body, as a careless server might. It must not
	// come back out of the provider.
	assert.NotContains(t, err.Error(), testSecret)
	assert.NotContains(t, f.logs.String(), testSecret)

	// A failed fetch is not cached: the next call tries again.
	f.d.set(func(d *daraja) { d.tokenStatus = http.StatusOK })
	_, err = f.Initiate(context.Background(), request)
	require.NoError(t, err)
}

func TestSTKQuery_MapsResultCodes(t *testing.T) {
	cases := []struct {
		name   string
		answer func(w http.ResponseWriter, attempt int)
		state  payments.State
		code   int
	}{
		{"paid", func(w http.ResponseWriter, _ int) {
			queryResult(w, "0", "The service request is processed successfully.")
		}, payments.StateSuccess, 0},
		{"insufficient funds", func(w http.ResponseWriter, _ int) {
			queryResult(w, "1", "The balance is insufficient for the transaction.")
		}, payments.StateFailed, 1},
		{"cancelled", func(w http.ResponseWriter, _ int) { queryResult(w, "1032", "Request cancelled by user") }, payments.StateFailed, 1032},
		{"timeout", func(w http.ResponseWriter, _ int) { queryResult(w, "1037", "DS timeout user cannot be reached") }, payments.StateFailed, 1037},
		{"wrong pin", func(w http.ResponseWriter, _ int) { queryResult(w, "2001", "The initiator information is invalid.") }, payments.StateFailed, 2001},
		{"numeric result code", func(w http.ResponseWriter, _ int) {
			_, _ = io.WriteString(w, `{"ResponseCode":"0","ResultCode":1032,"ResultDesc":"Request cancelled by user"}`)
		}, payments.StateFailed, 1032},
		{"still being processed", func(w http.ResponseWriter, _ int) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"requestId":"ws_CO_1","errorCode":"500.001.1001","errorMessage":"The transaction is being processed"}`)
		}, payments.StatePending, 0},
		{"accepted, no result yet", func(w http.ResponseWriter, _ int) {
			_, _ = io.WriteString(w, `{"ResponseCode":"0","ResponseDescription":"accepted"}`)
		}, payments.StatePending, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.d.set(func(d *daraja) { d.queryAnswer = tc.answer })
			st, err := f.Query(context.Background(), "ws_CO_191220191020363925")
			require.NoError(t, err)
			assert.Equal(t, tc.state, st.State)
			assert.Equal(t, tc.code, st.ResultCode)
			assert.Nil(t, st.AmountCents, "Daraja's query names no amount")
			require.Len(t, f.d.sentQueries(), 1)
			assert.Equal(t, "ws_CO_191220191020363925", f.d.sentQueries()[0]["CheckoutRequestID"])
			assert.Equal(t, testShort, f.d.sentQueries()[0]["BusinessShortCode"])
			assert.NotEmpty(t, f.d.sentQueries()[0]["Password"])
		})
	}

	t.Run("another error is an error, not pending", func(t *testing.T) {
		f := newFixture(t)
		f.d.set(func(d *daraja) {
			d.queryAnswer = func(w http.ResponseWriter, _ int) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"errorCode":"500.003.1001","errorMessage":"Internal Server Error"}`)
			}
		})
		_, err := f.Query(context.Background(), "ws_CO_1")
		var de *Error
		require.ErrorAs(t, err, &de)
		assert.Equal(t, "500.003.1001", de.Code)
		assert.Equal(t, 1, f.d.queryCount(), "a response, even a 500, is never retried")
		assert.Zero(t, f.slept.Load())
	})
}

// Retries: on network errors only, a few times, and never after a response.
func TestRetry_OnlyOnNetworkErrors(t *testing.T) {
	t.Run("a dropped query connection is retried", func(t *testing.T) {
		f := newFixture(t)
		atomic.StoreInt32(&f.d.dropQueries, 2)
		st, err := f.Query(context.Background(), "ws_CO_1")
		require.NoError(t, err)
		assert.Equal(t, payments.StateSuccess, st.State)
		assert.Equal(t, 3, f.d.queryCount(), "two drops, then the answer")
		assert.EqualValues(t, 2, f.slept.Load())
	})

	t.Run("and gives up after the retries", func(t *testing.T) {
		f := newFixture(t)
		atomic.StoreInt32(&f.d.dropQueries, 100)
		_, err := f.Query(context.Background(), "ws_CO_1")
		require.Error(t, err)
		var de *Error
		assert.False(t, errors.As(err, &de), "a network error, not a Daraja answer")
		assert.Equal(t, 3, f.d.queryCount(), "the first try and two retries")
	})

	t.Run("an STK push that may have been delivered is not sent again", func(t *testing.T) {
		f := newFixture(t)
		atomic.StoreInt32(&f.d.dropPushes, 100)
		_, err := f.Initiate(context.Background(), request)
		require.Error(t, err)
		assert.Len(t, f.d.sentPushes(), 1, "Daraja received the push before the connection died: a second one would prompt the fan twice")
		assert.Zero(t, f.slept.Load())
	})

	t.Run("an STK push that could not connect at all is retried", func(t *testing.T) {
		f := newFixture(t)
		// A token first, while the server is up; then nothing is listening.
		_, err := f.Query(context.Background(), "ws_CO_1")
		require.NoError(t, err)
		f.d.srv.Close()
		_, err = f.Initiate(context.Background(), request)
		require.Error(t, err)
		var opErr *net.OpError
		require.ErrorAs(t, err, &opErr)
		assert.Equal(t, "dial", opErr.Op)
		assert.EqualValues(t, 2, f.slept.Load(), "tried three times")
	})
}

// Each call has its own timeout: a Daraja that does not answer does not hang the caller.
func TestTimeout_PerCall(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.Timeout = 100 * time.Millisecond; c.Retries = 1 })
	release := make(chan struct{})
	f.d.set(func(d *daraja) { d.queryAnswer = func(w http.ResponseWriter, _ int) { <-release } })
	defer close(release)

	started := time.Now()
	_, err := f.Query(context.Background(), "ws_CO_1")
	took := time.Since(started)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "got %v", err)
	assert.Less(t, took, 2*time.Second)
	assert.Equal(t, 2, f.d.queryCount(), "a timeout with no response is a network error: one retry (Retries: 1)")
}

// Credentials appear in no log line and no error, whatever happens.
func TestSecretsAreNeverLoggedOrReturned(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var texts []string
	note := func(err error) {
		if err != nil {
			texts = append(texts, err.Error())
		}
	}
	_, err := f.Initiate(ctx, request)
	note(err)
	_, err = f.Query(ctx, "ws_CO_1")
	note(err)
	// Failures of every kind.
	f.d.set(func(d *daraja) {
		d.pushHandler = func(w http.ResponseWriter, body map[string]any) {
			w.WriteHeader(http.StatusBadRequest)
			// A server that quotes the request back, password included.
			raw, _ := json.Marshal(body)
			_, _ = io.WriteString(w, `{"errorCode":"400.002.02","errorMessage":"Bad Request"} `+string(raw))
		}
	})
	_, err = f.Initiate(ctx, request)
	note(err)
	atomic.StoreInt32(&f.d.dropQueries, 100)
	_, err = f.Query(ctx, "ws_CO_1")
	note(err)
	f.d.srv.Close()
	_, err = f.Initiate(ctx, request)
	note(err)
	f.now.Add(4000)
	_, err = f.Query(ctx, "ws_CO_1") // token refresh against a dead server
	note(err)
	// A provider built with a bad URL.
	_, err = New(Config{BaseURL: "://bad", ConsumerKey: testKey, ConsumerSecret: testSecret, ShortCode: testShort, Passkey: testPasskey, CallbackURL: testCallback})
	note(err)
	_, err = New(Config{ConsumerKey: testKey, ConsumerSecret: testSecret})
	note(err)
	require.GreaterOrEqual(t, len(texts), 5)

	password := base64.StdEncoding.EncodeToString([]byte(testShort + testPasskey + "20261011121502"))
	basic := base64.StdEncoding.EncodeToString([]byte(testKey + ":" + testSecret))
	texts = append(texts, f.logs.String())
	require.NotEmpty(t, f.logs.String(), "the provider does log")
	for i, text := range texts {
		for name, secret := range map[string]string{
			"consumer secret": testSecret, "passkey": testPasskey, "request password": password,
			"basic auth": basic, "access token": f.d.tokensIssued()[0],
		} {
			assert.NotContains(t, text, secret, "text %d contains the %s", i, name)
		}
	}
}

func TestNew_RequiresEverySetting(t *testing.T) {
	full := Config{BaseURL: SandboxBaseURL, ConsumerKey: "k", ConsumerSecret: "s", ShortCode: "1", Passkey: "p", CallbackURL: testCallback}
	_, err := New(full)
	require.NoError(t, err)
	for name, strip := range map[string]func(*Config){
		"key": func(c *Config) { c.ConsumerKey = "" }, "secret": func(c *Config) { c.ConsumerSecret = " " },
		"short code": func(c *Config) { c.ShortCode = "" }, "passkey": func(c *Config) { c.Passkey = "" },
		"callback": func(c *Config) { c.CallbackURL = "" }, "base": func(c *Config) { c.BaseURL = "" },
	} {
		cfg := full
		strip(&cfg)
		_, err := New(cfg)
		require.ErrorContains(t, err, "not configured", name)
	}
	bad := full
	bad.TransactionType = "Till"
	_, err = New(bad)
	require.ErrorContains(t, err, "transaction type")
}

func TestParseCallback(t *testing.T) {
	paid := `{"Body":{"stkCallback":{"MerchantRequestID":"29115-34620561-1","CheckoutRequestID":"ws_CO_191220191020363925",
		"ResultCode":0,"ResultDesc":"The service request is processed successfully.","CallbackMetadata":{"Item":[
		{"Name":"Amount","Value":1000.00},{"Name":"MpesaReceiptNumber","Value":"NLJ7RT61SV"},{"Name":"Balance"},
		{"Name":"TransactionDate","Value":20261011121530},{"Name":"PhoneNumber","Value":254712345678}]}}},"extra":"kept"}`
	cb, err := ParseCallback([]byte(paid))
	require.NoError(t, err)
	assert.Equal(t, "ws_CO_191220191020363925", cb.CheckoutRequestID)
	assert.Equal(t, "29115-34620561-1", cb.MerchantRequestID)
	assert.Equal(t, 0, cb.ResultCode)
	require.NotNil(t, cb.AmountCents)
	assert.EqualValues(t, 100_000, *cb.AmountCents)
	assert.Equal(t, "NLJ7RT61SV", cb.Receipt)
	assert.Equal(t, paid, string(cb.Raw), "the raw body is kept byte for byte")

	cancelled := `{"Body":{"stkCallback":{"MerchantRequestID":"m","CheckoutRequestID":"ws_CO_1","ResultCode":1032,"ResultDesc":"Request cancelled by user."}}}`
	cb, err = ParseCallback([]byte(cancelled))
	require.NoError(t, err)
	assert.Equal(t, 1032, cb.ResultCode)
	assert.Nil(t, cb.AmountCents)
	assert.Empty(t, cb.Receipt)

	// Amounts: read from the digits, never through a float.
	amounts := map[string]any{
		`1`: int64(100), `1.5`: int64(150), `2500`: int64(250_000), `2500.0`: int64(250_000), `2500.00`: int64(250_000),
		`0.07`: int64(7), `1234567.89`: int64(123_456_789), `"1000.00"`: int64(100_000), `1000.000`: int64(100_000),
		// Not an amount of money: left unverifiable.
		`1000.005`: nil, `-5`: nil, `1e3`: nil, `"lots"`: nil, `null`: nil, `true`: nil, `{}`: nil,
	}
	for raw, want := range amounts {
		body := `{"Body":{"stkCallback":{"CheckoutRequestID":"ws_CO_1","ResultCode":0,"ResultDesc":"ok","CallbackMetadata":{"Item":[{"Name":"Amount","Value":` + raw + `}]}}}}`
		cb, err := ParseCallback([]byte(body))
		require.NoError(t, err, raw)
		if want == nil {
			assert.Nil(t, cb.AmountCents, "amount %s", raw)
			continue
		}
		require.NotNil(t, cb.AmountCents, "amount %s", raw)
		assert.Equal(t, want, *cb.AmountCents, "amount %s", raw)
	}

	for _, bad := range []string{``, `{}`, `[]`, `null`, `{"Body":{}}`, `{"Body":{"stkCallback":{"ResultCode":0}}}`,
		`{"Body":{"stkCallback":{"CheckoutRequestID":"x","ResultCode":"zero"}}}`, `not json`} {
		_, err := ParseCallback([]byte(bad))
		require.ErrorIs(t, err, ErrBadCallback, "body %q", bad)
	}
}
