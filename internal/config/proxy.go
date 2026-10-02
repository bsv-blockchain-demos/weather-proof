package config

import (
	"fmt"
	"log/slog"
	"net/netip"
)

// TrustedProxies is the parsed TRUSTED_PROXY_CIDRS list. An EMPTY value is
// legal and means trust nothing: forwarding headers are ignored and the
// limiter keys on r.RemoteAddr. That degrades rate limiting to one bucket per
// proxy pod, which is safe but must never be silent — see LogBootState.
type TrustedProxies struct {
	prefixes []netip.Prefix
}

// ParseCIDRList parses every entry. A malformed entry is an error naming the
// VARIABLE and the entry's INDEX, never the entry's text (rule 20).
func ParseCIDRList(entries []string) (TrustedProxies, error) {
	prefixes := make([]netip.Prefix, 0, len(entries))
	for i, entry := range entries {
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			return TrustedProxies{}, fmt.Errorf("TRUSTED_PROXY_CIDRS: entry %d is not a valid CIDR", i)
		}
		prefixes = append(prefixes, p)
	}
	return TrustedProxies{prefixes: prefixes}, nil
}

// Contains reports whether addr is inside any trusted prefix. It calls
// addr.Unmap() first: netip.MustParsePrefix("10.0.0.0/8").Contains does NOT
// match ::ffff:10.1.2.3 without it, which would make every 4-in-6 peer
// untrusted and silently disable header resolution behind an IPv6 listener.
func (t TrustedProxies) Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range t.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Len is the count the heartbeat reports as trustedProxyCIDRs.
func (t TrustedProxies) Len() int {
	return len(t.prefixes)
}

// LogBootState emits EXACTLY ONE WARN when the list is empty, and nothing at
// all otherwise. The message names the variable, the ConfigMap path and the
// consequence, per spec §6.1.
// A nil log is coerced to slog.Default(), the same coercion internal/api makes
// for the same reason: this method's whole job is to emit one warning at boot,
// and a nil logger turns that into a nil-pointer panic on the boot path — the one
// place a missing warning costs the most.
func (t TrustedProxies) LogBootState(log *slog.Logger) {
	if len(t.prefixes) > 0 {
		return
	}
	if log == nil {
		log = slog.Default()
	}
	log.Warn("TRUSTED_PROXY_CIDRS empty: forwarding headers ignored, rate limiting is per-proxy-pod and therefore effectively global")
}
