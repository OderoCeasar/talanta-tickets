package httpapi

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver works out who is really calling. It is the only code that reads
// forwarding headers; request logs, every per-IP rate limit and (later) the M-Pesa callback
// allowlist all use its answer.
//
// The rule: the TCP peer address is the client, unless the peer is one of our own reverse
// proxies (TrustedProxies). Only then is a header believed, because only then do we know who
// wrote it. With no trusted proxies configured, headers are ignored entirely: anybody can
// send "X-Forwarded-For: 1.2.3.4".
type ClientIPResolver struct {
	// TrustedProxies are the networks our proxies connect from (TRUSTED_PROXY_CIDRS).
	TrustedProxies []netip.Prefix
	// Header is an optional single-address header to believe before X-Forwarded-For
	// (CLIENT_IP_HEADER, for example CF-Connecting-IP). Empty by default, and it must stay
	// empty unless the origin accepts connections only from the CDN that sets it, or our
	// proxy overwrites it: a proxy that merely passes it through (Caddy does) would hand
	// any caller the choice of their own address. When it is empty, missing or unreadable,
	// X-Forwarded-For is used.
	Header string
}

// ClientIP returns the caller's address for r. The zero Addr is returned only when the
// connection itself has no readable address (a test request with an empty RemoteAddr).
func (c ClientIPResolver) ClientIP(r *http.Request) netip.Addr {
	peer := parseAddr(r.RemoteAddr)
	if !peer.IsValid() || !c.trusted(peer) {
		return peer
	}

	if c.Header != "" && !strings.EqualFold(c.Header, "X-Forwarded-For") {
		// Exactly one value: two copies of the header means someone upstream of our proxy
		// added their own, and there is no telling which is real.
		if vals := r.Header.Values(c.Header); len(vals) == 1 {
			if ip := parseAddr(strings.TrimSpace(vals[0])); ip.IsValid() {
				return ip
			}
		}
	}

	// X-Forwarded-For grows to the right: each proxy appends the address it saw. Walking
	// from the right and skipping our own proxies gives the first address we did not write
	// ourselves. Anything further left was supplied by the client and is not believed.
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := parseAddr(strings.TrimSpace(hops[i]))
		if !ip.IsValid() {
			// A hop we cannot read breaks the chain of custody.
			break
		}
		if !c.trusted(ip) {
			return ip
		}
	}
	return peer
}

func (c ClientIPResolver) trusted(ip netip.Addr) bool {
	for _, p := range c.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// parseAddr reads "ip", "ip:port" or "[ip]:port" and unmaps IPv4-in-IPv6, so 1.2.3.4 and
// ::ffff:1.2.3.4 are one client.
func parseAddr(s string) netip.Addr {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone("")
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}
	}
	return ip.Unmap().WithZone("")
}

type clientIPKey struct{}

// ClientIP is the caller's address as resolved by the router's middleware. Handlers and
// services use this and never r.RemoteAddr or a header.
func ClientIP(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return ip
}

// rateLimitKey groups an address for per-IP limits. An IPv6 customer is handed a whole /64,
// so limiting single addresses would be no limit at all.
func rateLimitKey(ip netip.Addr) string {
	if !ip.IsValid() {
		return "unknown"
	}
	if ip.Is6() {
		if p, err := ip.Prefix(64); err == nil {
			return p.String()
		}
	}
	return ip.String()
}
