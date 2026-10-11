package contracttest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureTB stands in for *testing.T so these tests can watch the helper fail.
type captureTB struct {
	testing.TB
	errors []string
}

func (c *captureTB) Helper() {}
func (c *captureTB) Errorf(format string, args ...any) {
	c.errors = append(c.errors, fmt.Sprintf(format, args...))
}
func (c *captureTB) Fatalf(format string, args ...any) {
	c.errors = append(c.errors, fmt.Sprintf(format, args...))
}

func respond(status int, header http.Header, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		if body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

const goodUser = `{"id":7,"phone":"+254712345678","email":null,"name":null,"role":"FAN","totp_enabled":false,"created_at":"2026-10-11T09:00:00Z"}`

// The helper is only worth having if it fails when the answer is wrong. Each case is an
// answer a careless handler could plausibly give.
func TestCheckCatchesDrift(t *testing.T) {
	c := Load(t)

	jsonReq := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		return req
	}

	cases := []struct {
		name    string
		req     *http.Request
		handler http.Handler
		want    string // "" means the answer is good and nothing may be reported
	}{
		{"good user", jsonReq("GET", "/v1/me", ""), respond(200, nil, goodUser), ""},
		{"good error", jsonReq("GET", "/v1/me", ""),
			respond(401, nil, `{"error":{"code":"TOKEN_EXPIRED","message":"x","details":null}}`), ""},
		{"good 204", jsonReq("POST", "/v1/auth/logout", `{"refresh_token":"rt_x"}`), respond(204, nil, ""), ""},
		{"good 429 with header", jsonReq("POST", "/v1/auth/otp/request", `{"phone":"+254712345678"}`),
			respond(429, http.Header{"Retry-After": {"30"}}, `{"error":{"code":"RATE_LIMITED","message":"x","details":{"retry_after_seconds":30}}}`), ""},

		{"missing required field", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"totp_enabled":false,`, "", 1)), "totp_enabled"},
		{"extra field", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"id":7,`, `"id":7,"password_hash":"x",`, 1)), "password_hash"},
		{"wrong type", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"id":7`, `"id":"7"`, 1)), "id"},
		{"value outside enum", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"FAN"`, `"SUPERUSER"`, 1)), "role"},
		{"null where not allowed", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"role":"FAN"`, `"role":null`, 1)), "role"},
		{"time not RFC 3339", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `2026-10-11T09:00:00Z`, `11 Oct 2026 09:00`, 1)), "created_at"},
		{"email that is not one", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `"email":null`, `"email":"not an email"`, 1)), "email"},
		{"phone not E.164", jsonReq("GET", "/v1/me", ""),
			respond(200, nil, strings.Replace(goodUser, `+254712345678`, `0712345678`, 1)), "phone"},
		{"undeclared status", jsonReq("GET", "/v1/me", ""),
			respond(418, nil, `{"error":{"code":"INTERNAL","message":"x","details":null}}`), "418"},
		{"unknown error code", jsonReq("GET", "/v1/me", ""),
			respond(401, nil, `{"error":{"code":"NOPE","message":"x","details":null}}`), "code"},
		{"error without details key", jsonReq("GET", "/v1/me", ""),
			respond(401, nil, `{"error":{"code":"TOKEN_EXPIRED","message":"x"}}`), "details"},
		{"body on a 204", jsonReq("POST", "/v1/auth/logout", `{"refresh_token":"rt_x"}`),
			respond(204, nil, `{"ok":true}`), "declares none"},
		{"no body where one is declared", jsonReq("GET", "/v1/me", ""), respond(200, nil, ""), "no body"},
		{"429 without Retry-After", jsonReq("POST", "/v1/auth/otp/request", `{"phone":"+254712345678"}`),
			respond(429, nil, `{"error":{"code":"RATE_LIMITED","message":"x","details":{"retry_after_seconds":30}}}`), "Retry-After"},
		{"429 with Retry-After 0", jsonReq("POST", "/v1/auth/otp/request", `{"phone":"+254712345678"}`),
			respond(429, http.Header{"Retry-After": {"0"}}, `{"error":{"code":"RATE_LIMITED","message":"x","details":{"retry_after_seconds":0}}}`), "minimum"},
		{"path not in contract", jsonReq("GET", "/v1/nope", ""), respond(200, nil, `{}`), "not in openapi.yaml"},
		{"success for a request the contract forbids", jsonReq("POST", "/v1/auth/otp/request", `{"phone":"0712345678"}`),
			respond(200, nil, `{"expires_in_seconds":300,"resend_after_seconds":60}`), "more lenient"},
		{"dev_code of the wrong shape", jsonReq("POST", "/v1/auth/otp/request", `{"phone":"+254712345678"}`),
			respond(200, nil, `{"expires_in_seconds":300,"resend_after_seconds":60,"dev_code":"12345"}`), "dev_code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := &captureTB{TB: t}
			c.Do(tb, tc.handler, tc.req)
			if tc.want == "" {
				assert.Empty(t, tb.errors)
				return
			}
			require.NotEmpty(t, tb.errors, "the helper accepted an answer that breaks the contract")
			assert.Contains(t, strings.Join(tb.errors, "\n"), tc.want)
		})
	}
}

func TestLenientRequestSkipsOnlyTheRequestCheck(t *testing.T) {
	c := Load(t)
	req := httptest.NewRequest("POST", "/v1/auth/otp/request", strings.NewReader(`{"phone":"0712345678"}`))
	req.Header.Set("Content-Type", "application/json")

	tb := &captureTB{TB: t}
	c.Do(tb, respond(200, nil, `{"expires_in_seconds":300,"resend_after_seconds":60}`), req, LenientRequest())
	assert.Empty(t, tb.errors)

	req = httptest.NewRequest("POST", "/v1/auth/otp/request", strings.NewReader(`{"phone":"0712345678"}`))
	req.Header.Set("Content-Type", "application/json")
	tb = &captureTB{TB: t}
	c.Do(tb, respond(200, nil, `{"expires_in_seconds":"soon"}`), req, LenientRequest())
	assert.NotEmpty(t, tb.errors, "the answer is still checked")
}

func TestSeenAndTags(t *testing.T) {
	c := Load(t)
	c.Do(&captureTB{TB: t}, respond(200, nil, goodUser), httptest.NewRequest("GET", "/v1/me", nil))
	assert.True(t, c.HasSeen("getMe", 200))
	assert.Contains(t, c.Seen(), "getMe 200")
	assert.Contains(t, c.OperationsWithTag("AdminUsers"), "createInvitation")
	assert.Contains(t, c.OperationsWithTag("Health"), "getHealth")
}
