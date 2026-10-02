// Package ratelimit resolves the real client address behind a trusted proxy
// chain and counts requests per client in a BOUNDED fixed window.
//
// The two halves are one package on purpose: the bound exists BECAUSE the key
// source is spoofable, and the peer gate exists BECAUSE an unbounded key
// source is a memory DoS. Either half alone looks finished and is not.
//
// This package is a leaf: it imports nothing from this module, so it can never
// pull internal/store/postgres into the API's dependency graph.
package ratelimit

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Header names read by the resolver, and ONLY when the immediate peer is
// trusted. Both are client-settable on any path that did not traverse our
// proxies.
const (
	headerCFConnectingIP = "CF-Connecting-IP"
	headerXForwardedFor  = "X-Forwarded-For"
)

// ipv6KeyBits is the prefix length IPv6 keys are canonicalized to. A single
// residential IPv6 allocation is commonly a /64 or shorter, so keying on all
// 128 bits would hand one client 2^64 buckets.
const ipv6KeyBits = 64

// unparseablePeerKey is the fail-closed key for a RemoteAddr that is not an
// address at all. A constant rather than "" so malformed peers share one
// bounded bucket instead of an empty key, and so the key is stable across
// calls.
const unparseablePeerKey = "unparseable-peer"

// TrustChecker is the peer gate's whole dependency. config.TrustedProxies
// satisfies it. A one-method interface rather than an import, so this package
// is a leaf and its tests need no config fixture.
type TrustChecker interface {
	Contains(addr netip.Addr) bool
}

// Resolver turns a request into a rate-limit key.
type Resolver struct {
	Trusted TrustChecker
}

// Key resolves the real client address and returns the bucket key.
//
// STEP 0 (mandatory, and the reason this function is safe at all): is the
// IMMEDIATE peer one of ours? If the host of r.RemoteAddr is NOT inside
// Trusted, return that host's canonical key and consult NO header. Both
// CF-Connecting-IP and X-Forwarded-For are client-settable on any path that
// did not traverse our proxies, so an untrusted peer gets exactly ONE key: its
// own address.
//
// Only when the peer IS trusted, in this order and nothing else:
//  1. CF-Connecting-IP
//  2. X-Forwarded-For, walked from the RIGHT, returning the first hop NOT in
//     Trusted
//  3. r.RemoteAddr
//
// NEVER take X-Forwarded-For[0] blindly: that is both a limiter bypass (a
// fresh key per request) and the memory-DoS vector for the key map.
//
// FAIL CLOSED: a nil Trusted or an empty trust list keys on the peer address,
// which degrades to one bucket per proxy pod — safe, and loud because of
// config's boot WARN. An unparseable RemoteAddr is not an address at all and
// so cannot be keyed on: it gets the single constant unparseablePeerKey
// bucket, and no header is read for it either.
func (res Resolver) Key(r *http.Request) string {
	peer, ok := parseHost(r.RemoteAddr)
	if !ok {
		return unparseablePeerKey
	}
	if !res.trusts(peer) {
		return canonicalKey(peer)
	}

	// An unparseable CF-Connecting-IP falls through to X-Forwarded-For and then
	// to the peer, rather than becoming a key: a non-address is never a key.
	if cf, cfOK := parseHost(strings.TrimSpace(r.Header.Get(headerCFConnectingIP))); cfOK {
		return canonicalKey(cf)
	}
	if hop, hopOK := res.rightmostUntrustedHop(r.Header.Get(headerXForwardedFor)); hopOK {
		return canonicalKey(hop)
	}
	return canonicalKey(peer)
}

// trusts reports whether addr is one of our proxies. A nil checker trusts
// nothing, which is the fail-closed default and must not panic: a nil-checker
// panic here would be a boot-time crash on a misconfigured deployment.
//
// Unmap is called here and not left to the checker. netip.Prefix("10.0.0.0/8")
// does NOT contain ::ffff:10.1.2.3, so without it a dual-stack listener
// silently stops resolving forwarding headers.
func (res Resolver) trusts(addr netip.Addr) bool {
	if res.Trusted == nil {
		return false
	}
	return res.Trusted.Contains(addr.Unmap())
}

// rightmostUntrustedHop walks X-Forwarded-For from the RIGHT and returns the
// first hop that is neither trusted nor unparseable. An unparseable hop is
// skipped rather than returned: it is not a valid key and must never become
// one. Reports false when every hop is trusted or unparseable, so the caller
// falls back to r.RemoteAddr.
func (res Resolver) rightmostUntrustedHop(header string) (netip.Addr, bool) {
	if header == "" {
		return netip.Addr{}, false
	}
	hops := strings.Split(header, ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, ok := parseHost(strings.TrimSpace(hops[i]))
		if !ok {
			continue
		}
		if res.trusts(hop) {
			continue
		}
		return hop, true
	}
	return netip.Addr{}, false
}

// parseHost accepts either "host:port" or a bare host — a portless RemoteAddr
// is legal on some transports — and parses it as an address.
//
// A BRACKETED IPv6 with no port ("[2001:db8::1]") is deliberately not accepted:
// SplitHostPort rejects it and ParseAddr rejects the brackets, so it reports
// false. As a RemoteAddr that means the unparseablePeerKey bucket and as an XFF
// hop it means the hop is skipped — in both directions such a client merges
// into a shared bucket rather than getting one of its own, which is the safe
// direction. Unbracketing here would mean accepting attacker-shaped syntax from
// a header, which is the opposite direction.
func parseHost(raw string) (netip.Addr, bool) {
	if raw == "" {
		return netip.Addr{}, false
	}
	host := raw
	if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	// Strip an IPv6 zone: it is interface-local and never part of a bucket key.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

// canonicalKey renders an address as a bucket key: an IPv4 (including a
// 4-in-6 mapped address, after Unmap) as its own string, and an IPv6 as its
// /64 prefix. The /64 is not cosmetic: a single residential IPv6 allocation is
// commonly a /64 or shorter, so keying on the full 128 bits gives one client
// 2^64 buckets.
//
// The Unmap here is a SECOND, independent requirement from the one in trusts:
// without it a mapped address is not Is4, so ::ffff:198.51.100.9 would render
// as the /64 "::/64" and EVERY mapped IPv4 client would share one bucket. That
// is precisely the httprate.CanonicalizeIP defect the Global Constraints forbid
// by name. Pinned by TestKeyUnmapsAFourInSixKeyFromAnUntrustedPeer.
func canonicalKey(addr netip.Addr) string {
	addr = addr.Unmap()
	if addr.Is4() {
		return addr.String()
	}
	return netip.PrefixFrom(addr, ipv6KeyBits).Masked().String()
}
