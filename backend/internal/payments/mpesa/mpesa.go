// Package mpesa is the payment provider for Safaricom's Daraja API: the OAuth token, the STK
// push ("Lipa na M-Pesa Online") and the STK push query. It also reads the callback Daraja
// sends when a prompt is answered (callback.go).
//
// Nothing here logs or returns a credential: the consumer secret, the passkey, the derived
// request password and the access token appear in no log line and no error text.
package mpesa

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// Daraja's two hosts.
const (
	SandboxBaseURL    = "https://sandbox.safaricom.co.ke"
	ProductionBaseURL = "https://api.safaricom.co.ke"
)

// The two kinds of STK push: a paybill number, or a till ("buy goods") number.
const (
	PayBill  = "CustomerPayBillOnline"
	BuyGoods = "CustomerBuyGoodsOnline"
)

// errorCodeProcessing is what the STK query answers while the payer has not finished.
const errorCodeProcessing = "500.001.1001"

// Config is what the provider is built from. Every field but the last five is required.
type Config struct {
	// BaseURL is SandboxBaseURL or ProductionBaseURL (or a fake server's URL in tests).
	BaseURL        string
	ConsumerKey    string
	ConsumerSecret string
	// ShortCode is the business short code (paybill or till head office number).
	ShortCode string
	// Passkey is the Lipa na M-Pesa Online passkey.
	Passkey string
	// CallbackURL is where Daraja posts the result: our webhook, public and https.
	CallbackURL string

	// TransactionType is PayBill (the default) or BuyGoods.
	TransactionType string
	// Timeout bounds one HTTP call (zero: 10 seconds).
	Timeout time.Duration
	// Retries is how many times a call is tried again after a network error (zero: 2).
	// A call that got any HTTP response is never tried again.
	Retries    int
	HTTPClient *http.Client
	Logger     *slog.Logger
	// Now is the clock (nil: time.Now); Sleep waits between retries (nil: a real wait that
	// ends with the context). Tests replace both.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// Provider talks to Daraja. It is safe for concurrent use.
type Provider struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger

	mu       sync.Mutex
	token    string
	expires  time.Time
	fetching chan struct{} // closed when the fetch in flight ends; nil when none is
	fetchErr error
}

// tokenSlack is how long before its stated expiry a token is replaced, so a request never
// leaves with a token that dies on the way.
const tokenSlack = 60 * time.Second

// nairobi is the time zone Daraja's timestamps are in. Kenya has no daylight saving.
var nairobi = time.FixedZone("EAT", 3*60*60)

// New checks the configuration and builds the provider. It makes no network call.
func New(cfg Config) (*Provider, error) {
	missing := []string{}
	for name, v := range map[string]string{
		"consumer key": cfg.ConsumerKey, "consumer secret": cfg.ConsumerSecret, "short code": cfg.ShortCode,
		"passkey": cfg.Passkey, "callback URL": cfg.CallbackURL, "base URL": cfg.BaseURL,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("mpesa: not configured (%d of 6 settings missing)", len(missing))
	}
	if u, err := url.Parse(cfg.BaseURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("mpesa: base URL must be an http(s) URL")
	}
	if u, err := url.Parse(cfg.CallbackURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("mpesa: callback URL must be an http(s) URL")
	}
	switch cfg.TransactionType {
	case "":
		cfg.TransactionType = PayBill
	case PayBill, BuyGoods:
	default:
		return nil, fmt.Errorf("mpesa: transaction type must be %s or %s", PayBill, BuyGoods)
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Retries <= 0 {
		cfg.Retries = 2
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	return &Provider{cfg: cfg, http: cfg.HTTPClient, log: cfg.Logger}, nil
}

// Error is a response from Daraja that refuses a request. It carries Daraja's own code and
// wording, which hold no secret of ours.
type Error struct {
	HTTPStatus int
	Code       string
	Message    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("mpesa: daraja answered %d: %s %s", e.HTTPStatus, e.Code, e.Message)
}

// networkError marks a failure in which no HTTP response was received.
type networkError struct {
	op string
	// dial is true when the connection was never made, so the request was certainly not
	// delivered.
	dial bool
	// unsent is true when the HTTP client reports that it looked for a connection and never
	// wrote a byte of the request on one: a failed DNS lookup, dial or TLS handshake, or a
	// deadline that passed before the write. The request cannot have reached Daraja.
	unsent bool
	err    error
}

func (e *networkError) Error() string {
	return fmt.Sprintf("mpesa: %s: network error: %v", e.op, e.err)
}
func (e *networkError) Unwrap() error { return e.err }

// retryPolicy says which network errors a call may be tried again after.
type retryPolicy int

const (
	// retryAnyNetworkError is for calls that are safe to repeat: the token and the query.
	retryAnyNetworkError retryPolicy = iota
	// retryUndelivered is for the STK push. A push that timed out after it was sent may
	// have reached Daraja, and sending it again would put a second prompt on the fan's
	// phone; it is only repeated when the connection could not be made at all.
	retryUndelivered
)

// sanitise strips a URL error down to its cause: *url.Error prints the full URL, and nothing
// that came from configuration belongs in a log line.
func sanitise(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// do sends one request, with the per-call timeout, and reads the body (at most 1 MiB).
func (p *Provider) do(ctx context.Context, op string, build func(ctx context.Context) (*http.Request, error)) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	// The trace says how far the request got. "Asked for a connection and wrote nothing" is
	// proof it was not sent. A transport that does not report (looked stays false) proves
	// nothing, and the error is then treated as one that may have been delivered.
	var looked, wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GetConn:      func(string) { looked.Store(true) },
		WroteHeaders: func() { wrote.Store(true) },
		WroteRequest: func(httptrace.WroteRequestInfo) { wrote.Store(true) },
	})
	req, err := build(ctx)
	if err != nil {
		return 0, nil, payments.NotSent(fmt.Errorf("mpesa: %s: build request: %w", op, sanitise(err)))
	}
	if err := ctx.Err(); err != nil {
		// Cancelled or out of time before the call was even made.
		return 0, nil, &networkError{op: op, unsent: true, err: err}
	}
	resp, err := p.http.Do(req)
	if err != nil {
		var opErr *net.OpError
		dial := errors.As(err, &opErr) && opErr.Op == "dial"
		return 0, nil, &networkError{op: op, dial: dial, unsent: dial || (looked.Load() && !wrote.Load()), err: sanitise(err)}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		// The status line arrived: Daraja saw and answered the request. Not retried.
		return resp.StatusCode, nil, fmt.Errorf("mpesa: %s: read response: %w", op, sanitise(err))
	}
	return resp.StatusCode, body, nil
}

// call is do with the small retry: network errors only, never after a response.
func (p *Provider) call(ctx context.Context, op string, policy retryPolicy, build func(ctx context.Context) (*http.Request, error)) (int, []byte, error) {
	backoff := 200 * time.Millisecond
	for attempt := 0; ; attempt++ {
		status, body, err := p.do(ctx, op, build)
		var ne *networkError
		if err == nil || !errors.As(err, &ne) || attempt >= p.cfg.Retries || (policy == retryUndelivered && !ne.dial) {
			return status, body, err
		}
		p.log.WarnContext(ctx, "mpesa: network error, trying again", "op", op, "attempt", attempt+1, "err", ne.err)
		if serr := p.cfg.Sleep(ctx, backoff); serr != nil {
			return 0, nil, err
		}
		backoff *= 2
	}
}

// accessToken returns a valid OAuth token, fetching one when there is none or it is about to
// expire. Concurrent callers share one fetch (single flight): a burst of payments after the
// token expires makes one token request, not one each.
func (p *Provider) accessToken(ctx context.Context) (string, error) {
	for {
		p.mu.Lock()
		if p.token != "" && p.cfg.Now().Before(p.expires) {
			token := p.token
			p.mu.Unlock()
			return token, nil
		}
		if p.fetching == nil {
			done := make(chan struct{})
			p.fetching = done
			p.mu.Unlock()

			token, expires, err := p.fetchToken(ctx)

			p.mu.Lock()
			if err == nil {
				p.token, p.expires = token, expires
			}
			p.fetchErr = err
			p.fetching = nil
			p.mu.Unlock()
			close(done)
			return token, err
		}
		wait := p.fetching
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-wait:
		}
		p.mu.Lock()
		err := p.fetchErr
		p.mu.Unlock()
		if err != nil {
			return "", err
		}
		// The fetch succeeded: go round again and read the token it stored.
	}
}

func (p *Provider) fetchToken(ctx context.Context) (string, time.Time, error) {
	const op = "oauth token"
	status, body, err := p.call(ctx, op, retryAnyNetworkError, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/oauth/v1/generate?grant_type=client_credentials", nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(p.cfg.ConsumerKey, p.cfg.ConsumerSecret)
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	if status != http.StatusOK {
		// The body of a refused token request is not echoed: it is not needed to act on
		// "check the consumer key and secret", and it is the one place Daraja might quote
		// what it was sent.
		return "", time.Time{}, &Error{HTTPStatus: status, Code: "oauth", Message: "the token request was refused; check the consumer key and secret"}
	}
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("mpesa: %s: the answer has no access token", op)
	}
	// Daraja sends expires_in as a string ("3599"); a number is accepted too.
	seconds, err := strconv.Atoi(strings.Trim(string(out.ExpiresIn), `"`))
	if err != nil || seconds <= 0 {
		seconds = 0
	}
	life := time.Duration(seconds)*time.Second - tokenSlack
	if life < 0 {
		life = 0
	}
	return out.AccessToken, p.cfg.Now().Add(life), nil
}

// password is Daraja's request password for a timestamp: base64(shortcode + passkey + timestamp).
func (p *Provider) password(timestamp string) string {
	return base64.StdEncoding.EncodeToString([]byte(p.cfg.ShortCode + p.cfg.Passkey + timestamp))
}

// postJSON sends an authenticated JSON request and returns the status and body.
func (p *Provider) postJSON(ctx context.Context, op, path string, policy retryPolicy, payload any) (int, []byte, error) {
	// Without a token, or a body, the request itself is never made: NotSent.
	token, err := p.accessToken(ctx)
	if err != nil {
		return 0, nil, payments.NotSent(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, payments.NotSent(fmt.Errorf("mpesa: %s: encode request: %w", op, err))
	}
	return p.call(ctx, op, policy, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.BaseURL+path, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
}

// darajaError reads Daraja's error body ({ requestId, errorCode, errorMessage }).
func darajaError(status int, body []byte) *Error {
	var e struct {
		Code    string `json:"errorCode"`
		Message string `json:"errorMessage"`
	}
	_ = json.Unmarshal(body, &e)
	if e.Message == "" {
		e.Message = "no error message in the answer"
	}
	return &Error{HTTPStatus: status, Code: e.Code, Message: e.Message}
}

// msisdn turns +254712345678 into Daraja's 254712345678.
func msisdn(e164 string) string { return strings.TrimPrefix(e164, "+") }

// Initiate sends the STK push. The amount goes to Daraja in whole shillings; an amount that
// is not a whole number of shillings is refused, never rounded.
//
// Its errors say whether the push can have reached Daraja (payments.NotSent):
//
//   - NOT sent: the amount was refused here; no token could be had; no byte of the request
//     was written (DNS, dial, TLS handshake, a deadline before the write); Daraja answered
//     with a refusal of its own (any 4xx, a 5xx carrying Daraja's errorCode, or a 200 whose
//     ResponseCode is not "0").
//   - Everything else is ambiguous and is NOT marked: a timeout or a reset after the request
//     was written, a 5xx with no Daraja error in it (a gateway in between may have passed the
//     request on), a 200 whose body could not be read or is not Daraja's JSON, a 200 that
//     says accepted and names no CheckoutRequestID. Daraja may have prompted the payer.
func (p *Provider) Initiate(ctx context.Context, req payments.PaymentRequest) (payments.PaymentInit, error) {
	const op = "stk push"
	if req.AmountCents <= 0 || req.AmountCents%100 != 0 {
		return payments.PaymentInit{}, payments.ErrAmountNotWholeShillings
	}
	timestamp := p.cfg.Now().In(nairobi).Format("20060102150405")
	phone := msisdn(req.Phone)
	status, body, err := p.postJSON(ctx, op, "/mpesa/stkpush/v1/processrequest", retryUndelivered, map[string]any{
		"BusinessShortCode": p.cfg.ShortCode,
		"Password":          p.password(timestamp),
		"Timestamp":         timestamp,
		"TransactionType":   p.cfg.TransactionType,
		"Amount":            req.AmountCents / 100,
		"PartyA":            phone,
		"PartyB":            p.cfg.ShortCode,
		"PhoneNumber":       phone,
		"CallBackURL":       p.cfg.CallbackURL,
		// At most 12 characters; a booking reference is 10.
		"AccountReference": req.Reference,
		// At most 13 characters.
		"TransactionDesc": "Tickets",
	})
	if err != nil {
		var ne *networkError
		if errors.As(err, &ne) && ne.unsent {
			return payments.PaymentInit{}, payments.NotSent(err)
		}
		// Already marked (no token), or ambiguous (sent and no answer; answer unreadable).
		return payments.PaymentInit{}, err
	}
	var out struct {
		MerchantRequestID   string `json:"MerchantRequestID"`
		CheckoutRequestID   string `json:"CheckoutRequestID"`
		ResponseCode        string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
	}
	if status != http.StatusOK {
		de := darajaError(status, body)
		if (status >= 400 && status < 500) || de.Code != "" {
			return payments.PaymentInit{}, payments.NotSent(de)
		}
		return payments.PaymentInit{}, de
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return payments.PaymentInit{}, fmt.Errorf("mpesa: %s: the answer is not JSON", op)
	}
	if out.ResponseCode != "" && out.ResponseCode != "0" {
		return payments.PaymentInit{}, payments.NotSent(&Error{HTTPStatus: status, Code: out.ResponseCode, Message: out.ResponseDescription})
	}
	if out.ResponseCode != "0" || out.CheckoutRequestID == "" {
		return payments.PaymentInit{}, &Error{HTTPStatus: status, Code: out.ResponseCode, Message: "accepted without a CheckoutRequestID: " + out.ResponseDescription}
	}
	p.log.InfoContext(ctx, "mpesa: stk push accepted", "payment_id", req.PaymentID, "checkout_request_id", out.CheckoutRequestID)
	return payments.PaymentInit{CheckoutRequestID: out.CheckoutRequestID, MerchantRequestID: out.MerchantRequestID}, nil
}

// Query asks Daraja what became of an STK push. While the payer has not answered, Daraja
// replies with an error ("The transaction is being processed"); that is StatePending here,
// not a failure. The answer names no amount and no receipt.
//
// StateFailed is returned only for a result code KNOWN to be a final failure
// (domain.IsFinalSTKFailure, which says where the list comes from). Daraja answers HTTP 200
// with a non-zero ResultCode for more than failures: "the transaction is still under
// processing" is one. Reading every non-zero code as a failure closed payments the fan was
// about to pay for. Any code that is not on the list is StatePending: the payment stays open
// and Daraja is asked again.
func (p *Provider) Query(ctx context.Context, checkoutRequestID string) (payments.PaymentStatus, error) {
	const op = "stk query"
	timestamp := p.cfg.Now().In(nairobi).Format("20060102150405")
	status, body, err := p.postJSON(ctx, op, "/mpesa/stkpushquery/v1/query", retryAnyNetworkError, map[string]any{
		"BusinessShortCode": p.cfg.ShortCode,
		"Password":          p.password(timestamp),
		"Timestamp":         timestamp,
		"CheckoutRequestID": checkoutRequestID,
	})
	if err != nil {
		return payments.PaymentStatus{}, err
	}
	if status != http.StatusOK {
		de := darajaError(status, body)
		if de.Code == errorCodeProcessing {
			return payments.PaymentStatus{State: payments.StatePending, ResultDesc: de.Message}, nil
		}
		return payments.PaymentStatus{}, de
	}
	var out struct {
		ResponseCode string          `json:"ResponseCode"`
		ResultCode   json.RawMessage `json:"ResultCode"`
		ResultDesc   string          `json:"ResultDesc"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return payments.PaymentStatus{}, fmt.Errorf("mpesa: %s: the answer is not JSON", op)
	}
	// ResultCode is a string in the query's answer ("0", "1032"); a number is accepted too.
	raw := strings.Trim(string(out.ResultCode), `"`)
	if raw == "" || raw == "null" {
		// Accepted for processing, no result yet.
		return payments.PaymentStatus{State: payments.StatePending, ResultDesc: out.ResultDesc}, nil
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		return payments.PaymentStatus{}, fmt.Errorf("mpesa: %s: result code %q is not a number", op, raw)
	}
	st := payments.PaymentStatus{State: payments.StatePending, ResultCode: code, ResultDesc: out.ResultDesc}
	switch {
	case code == domain.ResultSuccess:
		st.State = payments.StateSuccess
	case domain.IsFinalSTKFailure(code):
		st.State = payments.StateFailed
	default:
		p.log.WarnContext(ctx, "mpesa: stk query answered a result code that is not a known final result; treated as still pending",
			"checkout_request_id", checkoutRequestID, "result_code", code, "result_desc", out.ResultDesc)
	}
	return st, nil
}
