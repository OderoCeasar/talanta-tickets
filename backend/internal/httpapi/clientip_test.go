package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClientIP(t *testing.T) {
	proxies := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("10.0.0.0/8")}

	cases := []struct {
		name     string
		resolver ClientIPResolver
		remote   string
		headers  map[string][]string
		want     string
	}{
		{
			name:     "no trusted proxies: every forged header is ignored",
			resolver: ClientIPResolver{Header: "CF-Connecting-IP"},
			remote:   "203.0.113.9:4444",
			headers: map[string][]string{
				"Cf-Connecting-Ip": {"1.1.1.1"},
				"X-Forwarded-For":  {"2.2.2.2"},
				"X-Real-Ip":        {"3.3.3.3"},
				"True-Client-Ip":   {"4.4.4.4"},
			},
			want: "203.0.113.9",
		},
		{
			name:     "untrusted peer: forged header is ignored even with proxies configured",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "203.0.113.9:4444",
			headers:  map[string][]string{"Cf-Connecting-Ip": {"1.1.1.1"}, "X-Forwarded-For": {"2.2.2.2"}},
			want:     "203.0.113.9",
		},
		{
			name:     "trusted peer: the named header is used",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"Cf-Connecting-Ip": {"197.248.10.20"}, "X-Forwarded-For": {"6.6.6.6, 104.16.0.1"}},
			want:     "197.248.10.20",
		},
		{
			name:     "trusted peer, header missing: right-most untrusted X-Forwarded-For entry",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "172.18.0.5:51000",
			// The left-most entry is what the client typed; the proxy appended the real one.
			headers: map[string][]string{"X-Forwarded-For": {"6.6.6.6, 197.248.10.20, 10.0.0.7"}},
			want:    "197.248.10.20",
		},
		{
			name:     "X-Forwarded-For split across header lines",
			resolver: ClientIPResolver{TrustedProxies: proxies},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"X-Forwarded-For": {"6.6.6.6", "197.248.10.20", "10.0.0.7"}},
			want:     "197.248.10.20",
		},
		{
			name:     "duplicate named header is not believed; falls back to X-Forwarded-For",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"Cf-Connecting-Ip": {"1.1.1.1", "197.248.10.20"}, "X-Forwarded-For": {"41.90.1.1"}},
			want:     "41.90.1.1",
		},
		{
			name:     "garbage in the named header falls back",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"Cf-Connecting-Ip": {"not-an-ip"}, "X-Forwarded-For": {"41.90.1.1"}},
			want:     "41.90.1.1",
		},
		{
			name:     "unreadable hop stops the walk: the peer is used",
			resolver: ClientIPResolver{TrustedProxies: proxies},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"X-Forwarded-For": {"41.90.1.1, unknown, 10.0.0.7"}},
			want:     "172.18.0.5",
		},
		{
			name:     "trusted peer and no headers: the peer",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "172.18.0.5:51000",
			want:     "172.18.0.5",
		},
		{
			name:     "every hop is one of ours: the peer",
			resolver: ClientIPResolver{TrustedProxies: proxies},
			remote:   "172.18.0.5:51000",
			headers:  map[string][]string{"X-Forwarded-For": {"10.0.0.1, 10.0.0.2"}},
			want:     "172.18.0.5",
		},
		{
			name:     "IPv6 peer",
			resolver: ClientIPResolver{},
			remote:   "[2001:db8::1]:443",
			want:     "2001:db8::1",
		},
		{
			name:     "IPv4-mapped IPv6 is unmapped, so it matches an IPv4 proxy range",
			resolver: ClientIPResolver{TrustedProxies: proxies, Header: "CF-Connecting-IP"},
			remote:   "[::ffff:172.18.0.5]:51000",
			headers:  map[string][]string{"Cf-Connecting-Ip": {"197.248.10.20"}},
			want:     "197.248.10.20",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			req.RemoteAddr = tc.remote
			for k, v := range tc.headers {
				req.Header[k] = v
			}
			assert.Equal(t, tc.want, tc.resolver.ClientIP(req).String())
		})
	}
}

// The middleware is the only way an address reaches handlers, logs and rate limits.
func TestClientIPMiddlewarePutsResolvedAddressOnContext(t *testing.T) {
	var seen netip.Addr
	h := clientIP(ClientIPResolver{})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r.Context())
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = "203.0.113.9:4444"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "203.0.113.9", seen.String())
}

func TestRateLimitKeyGroupsIPv6BySlash64(t *testing.T) {
	a := rateLimitKey(netip.MustParseAddr("2001:db8:1:2::1"))
	b := rateLimitKey(netip.MustParseAddr("2001:db8:1:2:ffff::9"))
	c := rateLimitKey(netip.MustParseAddr("2001:db8:1:3::1"))
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
	assert.Equal(t, "197.248.10.20", rateLimitKey(netip.MustParseAddr("197.248.10.20")))
	assert.Equal(t, "unknown", rateLimitKey(netip.Addr{}))
}
