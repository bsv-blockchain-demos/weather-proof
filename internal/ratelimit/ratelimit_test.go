package ratelimit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// Header names are re-declared here as LITERALS rather than referencing the
// package's own constants: a test that compares against the constant it is
// meant to pin mutates together with the implementation and can never fail.
const (
	testCFHeader  = "CF-Connecting-IP"
	testXFFHeader = "X-Forwarded-For"
)

// Fixture addresses. Distinct values throughout, never a repeated placeholder,
// so a fixture cannot accidentally make two different answers look equal.
const (
	testTrustedCIDR   = "10.0.0.0/8"
	testTrustedPeer   = "10.0.0.5:1234"
	testTrustedHost   = "10.0.0.5"
	testUntrustedPeer = "198.51.100.50:9999"
	testUntrustedHost = "198.51.100.50"
	testClientIP      = "203.0.113.7"
	testOtherClientIP = "198.51.100.1"
)

// prefixList is a local TrustChecker so these tests need no internal/config
// fixture and internal/ratelimit stays a leaf.
//
// It deliberately does NOT call addr.Unmap() before Contains. That is what
// makes TestKeyTrustsAFourInSixMappedPeer discriminating: the Unmap under test
// is the resolver's own, and if the resolver drops it the checker will not
// silently compensate.
type prefixList struct {
	prefixes []netip.Prefix
}

func (p prefixList) Contains(addr netip.Addr) bool {
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func trustList(t *testing.T, cidrs ...string) TrustChecker {
	t.Helper()
	prefixes := make([]netip.Prefix, 0, len(cidrs))
	for _, cidr := range cidrs {
		prefixes = append(prefixes, netip.MustParsePrefix(cidr))
	}
	return prefixList{prefixes: prefixes}
}

func req(t *testing.T, remoteAddr string, headers map[string]string) *http.Request {
	t.Helper()
	r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/weather", nil)
	r.RemoteAddr = remoteAddr
	for name, value := range headers {
		r.Header.Set(name, value)
	}
	return r
}

func TestKeyUsesCFConnectingIPFromATrustedPeer(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{testCFHeader: testClientIP}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want %q", got, testClientIP)
	}
}

func TestKeyPrefersCFConnectingIPOverXForwardedFor(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testCFHeader:  testClientIP,
		testXFFHeader: testOtherClientIP,
	}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want the CF-Connecting-IP value %q", got, testClientIP)
	}
}

func TestKeyWalksXForwardedForFromTheRight(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testXFFHeader: testOtherClientIP + ", " + testClientIP + ", 10.0.0.9",
	}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want the rightmost untrusted hop %q", got, testClientIP)
	}
}

func TestKeyWalksPastSeveralTrustedHops(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testXFFHeader: testClientIP + ", 10.0.0.9, 10.0.0.8, 10.0.0.7",
	}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want %q past every trailing trusted hop", got, testClientIP)
	}
}

func TestKeyFallsBackToRemoteAddrWhenEveryXFFHopIsTrusted(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testXFFHeader: "10.0.0.9, 10.0.0.8",
	}))
	if got != testTrustedHost {
		t.Fatalf("Key() = %q, want the peer %q", got, testTrustedHost)
	}
}

func TestKeySkipsAnUnparseableXFFHop(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testXFFHeader: "not-an-ip, " + testClientIP + ", 10.0.0.9",
	}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want %q with the garbage hop skipped", got, testClientIP)
	}
}

func TestKeyIgnoresCFConnectingIPFromAnUntrustedPeer(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testUntrustedPeer, map[string]string{testCFHeader: testClientIP}))
	if got != testUntrustedHost {
		t.Fatalf("Key() = %q, want the peer %q — the header must not be read", got, testUntrustedHost)
	}
}

func TestKeyIgnoresXForwardedForFromAnUntrustedPeer(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testUntrustedPeer, map[string]string{testXFFHeader: testClientIP}))
	if got != testUntrustedHost {
		t.Fatalf("Key() = %q, want the peer %q — the header must not be read", got, testUntrustedHost)
	}
}

func TestKeyIgnoresBothHeadersWhenTheTrustListIsEmpty(t *testing.T) {
	res := Resolver{Trusted: trustList(t)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testCFHeader:  testClientIP,
		testXFFHeader: testOtherClientIP,
	}))
	if got != testTrustedHost {
		t.Fatalf("Key() = %q, want the peer %q — an empty trust list trusts nothing, RFC1918 included", got, testTrustedHost)
	}
}

func TestKeyIgnoresBothHeadersWhenTrustedIsNil(t *testing.T) {
	var res Resolver
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testCFHeader:  testClientIP,
		testXFFHeader: testOtherClientIP,
	}))
	if got != testTrustedHost {
		t.Fatalf("Key() = %q, want the peer %q with a nil checker", got, testTrustedHost)
	}
}

func TestKeyTrustsAFourInSixMappedPeer(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, "[::ffff:10.0.0.5]:1234", map[string]string{testCFHeader: testClientIP}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want %q: a 4-in-6 mapped peer must be Unmapped before the trust check", got, testClientIP)
	}
}

// TestKeyUnmapsAFourInSixKeyFromAnUntrustedPeer pins the Unmap inside
// canonicalKey, which is a SEPARATE requirement from the one in the trust check
// that TestKeyTrustsAFourInSixMappedPeer covers. Without it a mapped address is
// not Is4 and renders as the /64 "::/64", so every 4-in-6 client on a
// dual-stack listener shares one bucket — the httprate.CanonicalizeIP defect in
// assertion form. The peer here is UNTRUSTED, so the trust-check Unmap cannot
// mask the result.
func TestKeyUnmapsAFourInSixKeyFromAnUntrustedPeer(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, "[::ffff:198.51.100.9]:1", nil))
	if got != "198.51.100.9" {
		t.Fatalf("Key() = %q, want the dotted-quad 198.51.100.9", got)
	}
	if strings.Contains(got, ":") {
		t.Fatalf("Key() = %q, want an IPv4-shaped key: an IPv6-shaped one collapses every mapped client into one bucket", got)
	}

	// And it must still be a bucket of its OWN: a second mapped client at a
	// different address cannot land in the same one.
	other := res.Key(req(t, "[::ffff:198.51.100.10]:1", nil))
	if other == got {
		t.Fatalf("two distinct 4-in-6 clients both keyed as %q", got)
	}
}

// TestKeyFallsThroughAGarbageCFConnectingIPToXForwardedFor pins the stated
// decision that an unparseable header value is never a key: it is skipped, and
// resolution continues down the same ordered list.
func TestKeyFallsThroughAGarbageCFConnectingIPToXForwardedFor(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testTrustedPeer, map[string]string{
		testCFHeader:  "not-an-ip",
		testXFFHeader: testClientIP + ", 10.0.0.9",
	}))
	if got != testClientIP {
		t.Fatalf("Key() = %q, want the XFF hop %q after a garbage CF-Connecting-IP", got, testClientIP)
	}

	// With no usable XFF either, it falls through once more to the peer.
	peerKey := res.Key(req(t, testTrustedPeer, map[string]string{testCFHeader: "not-an-ip"}))
	if peerKey != testTrustedHost {
		t.Fatalf("Key() = %q, want the peer %q when neither header is usable", peerKey, testTrustedHost)
	}
}

func TestKeyCanonicalizesIPv6ToASlash64(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	first := res.Key(req(t, "[2001:db8::1]:1", nil))
	second := res.Key(req(t, "[2001:db8::2]:1", nil))
	if first != second {
		t.Fatalf("Key() = %q and %q, want one key for two addresses in the same /64", first, second)
	}
	other := res.Key(req(t, "[2001:db8:0:1::1]:1", nil))
	if other == first {
		t.Fatalf("Key() = %q for a different /64, want a distinct key from %q", other, first)
	}
}

func TestKeyDoesNotCollapseDistinctIPv4Clients(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	first := res.Key(req(t, testOtherClientIP+":1111", nil))
	second := res.Key(req(t, "198.51.100.2:2222", nil))
	if first == second {
		t.Fatalf("Key() collapsed two distinct IPv4 clients into %q", first)
	}
	if first != testOtherClientIP {
		t.Fatalf("Key() = %q, want %q", first, testOtherClientIP)
	}
}

func TestKeyHandlesARemoteAddrWithNoPort(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	got := res.Key(req(t, testOtherClientIP, nil))
	if got != testOtherClientIP {
		t.Fatalf("Key() = %q, want %q for a portless RemoteAddr", got, testOtherClientIP)
	}
}

func TestKeyReturnsAStableNonEmptyKeyForAGarbageRemoteAddr(t *testing.T) {
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	r := req(t, "garbage", map[string]string{testCFHeader: testClientIP})
	first := res.Key(r)
	if first == "" {
		t.Fatal("Key() = \"\", want a non-empty fail-closed key")
	}
	if first == testClientIP {
		t.Fatalf("Key() = %q, want the header ignored for an unparseable peer", first)
	}
	second := res.Key(r)
	if first != second {
		t.Fatalf("Key() = %q then %q, want a stable key", first, second)
	}
}

// fixedClock is an injectable clock. Every limiter test constructs its own so
// no test depends on state another test left behind.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fixedClock {
	return &fixedClock{now: time.Date(2026, time.July, 30, 12, 0, 0, 0, time.UTC)}
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestAllowPermitsUpToTheLimit(t *testing.T) {
	clock := newClock()
	l := New(5, 0, time.Minute, 10, clock.Now)
	for i := 0; i < 5; i++ {
		if d := l.Allow("a"); !d.OK {
			t.Fatalf("call %d refused, want allowed", i+1)
		}
	}
	if d := l.Allow("a"); d.OK {
		t.Fatal("call 6 allowed, want refused")
	}
}

func TestAllowCountsBurstOnTopOfTheLimit(t *testing.T) {
	clock := newClock()
	l := New(5, 2, time.Minute, 10, clock.Now)
	for i := 0; i < 7; i++ {
		if d := l.Allow("a"); !d.OK {
			t.Fatalf("call %d refused, want allowed with a burst of 2", i+1)
		}
	}
	if d := l.Allow("a"); d.OK {
		t.Fatal("call 8 allowed, want refused")
	}
}

// TestAllowLimitIsTheSumOfLimitAndBurst pins the ruling that burst is a
// permanent capacity add-on and that Decision.Limit advertises the total. Every
// expected value is a LITERAL: New(600, 120, …) advertises 720, which is what
// Task 16's general scope will put in RateLimit-Limit.
func TestAllowLimitIsTheSumOfLimitAndBurst(t *testing.T) {
	clock := newClock()
	l := New(5, 2, time.Minute, 10, clock.Now)

	first := l.Allow("a")
	if first.Limit != 7 {
		t.Fatalf("Limit = %d, want 7 — limit 5 plus burst 2, not the limit alone", first.Limit)
	}
	if first.Remaining != 6 {
		t.Fatalf("Remaining = %d, want 6 — it must count down from the advertised total", first.Remaining)
	}

	for i := 0; i < 6; i++ {
		l.Allow("a")
	}
	refused := l.Allow("a")
	if refused.OK {
		t.Fatal("call 8 allowed, want refused")
	}
	if refused.Limit != 7 {
		t.Fatalf("refused Limit = %d, want 7 on the 429 as well", refused.Limit)
	}

	// The burst is not a first-window grace: the SECOND window carries the same
	// total of 7.
	clock.advance(time.Minute)
	for i := 0; i < 7; i++ {
		if d := l.Allow("a"); !d.OK {
			t.Fatalf("second-window call %d refused, want allowed: burst is not start-of-window-only", i+1)
		}
	}
	if d := l.Allow("a"); d.OK {
		t.Fatal("second-window call 8 allowed, want refused")
	}

	// The general scope's numbers, spelled out, so the 720 Task 16 advertises is
	// pinned here and not only in a doc comment.
	general := New(600, 120, time.Minute, DefaultMaxKeys, newClock().Now)
	if d := general.Allow("a"); d.Limit != 720 {
		t.Fatalf("general scope Limit = %d, want 720", d.Limit)
	}
}

// countOf reads a key's raw window count. White-box on purpose: "a refusal does
// not increment the counter" is invisible through Decision alone, because a
// fixed window zeroes the count at the boundary either way, so the only honest
// way to pin the decision is to look at the counter itself.
func countOf(t *testing.T, l *Limiter, key string) int {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.entries[key]
	if !ok {
		t.Fatalf("key %q is not tracked", key)
	}
	w, ok := el.Value.(*window)
	if !ok {
		t.Fatalf("key %q holds a %T, want a *window", key, el.Value)
	}
	return w.count
}

// TestAllowRefusalsDoNotExtendTheWindow pins both halves of the fixed-window
// decision: a refused call neither increments the counter nor moves the window
// start, so a client hammering a closed window cannot push its own reset out.
// A sliding window here would mean a retry loop — which is exactly what the
// SPA's un-throttled 429 handling produces — never draining.
func TestAllowRefusalsDoNotExtendTheWindow(t *testing.T) {
	clock := newClock()
	l := New(2, 0, time.Minute, 10, clock.Now)
	l.Allow("a")
	l.Allow("a")
	if got := countOf(t, l, "a"); got != 2 {
		t.Fatalf("count = %d after 2 allowed calls, want 2", got)
	}

	clock.advance(10 * time.Second)
	for i := 0; i < 5; i++ {
		refused := l.Allow("a")
		if refused.OK {
			t.Fatalf("refusal %d allowed, want refused", i+1)
		}
		if refused.ResetAfter != 50*time.Second {
			t.Fatalf("refusal %d ResetAfter = %v, want 50s — a refusal must not push the reset out", i+1, refused.ResetAfter)
		}
	}
	if got := countOf(t, l, "a"); got != 2 {
		t.Fatalf("count = %d after 5 refusals, want 2 — a refusal must not increment", got)
	}

	// The window still opens at its original boundary, 60s after the first hit.
	clock.advance(50 * time.Second)
	d := l.Allow("a")
	if !d.OK {
		t.Fatal("call at the original boundary refused: the refusals extended the window")
	}
	if d.Remaining != 1 {
		t.Fatalf("Remaining = %d in the new window, want 1", d.Remaining)
	}
}

func TestAllowReportsDecreasingRemaining(t *testing.T) {
	clock := newClock()
	l := New(5, 0, time.Minute, 10, clock.Now)
	want := []int{4, 3, 2, 1, 0}
	for i, expected := range want {
		d := l.Allow("a")
		if !d.OK {
			t.Fatalf("call %d refused, want allowed", i+1)
		}
		if d.Remaining != expected {
			t.Fatalf("call %d Remaining = %d, want %d", i+1, d.Remaining, expected)
		}
		if d.Limit != 5 {
			t.Fatalf("call %d Limit = %d, want 5", i+1, d.Limit)
		}
	}
	refused := l.Allow("a")
	if refused.OK {
		t.Fatal("call 6 allowed, want refused")
	}
	if refused.Remaining != 0 {
		t.Fatalf("refused Remaining = %d, want 0", refused.Remaining)
	}
}

func TestAllowResetAfterShrinksWithinAWindow(t *testing.T) {
	clock := newClock()
	l := New(5, 0, time.Minute, 10, clock.Now)
	if d := l.Allow("a"); d.ResetAfter != time.Minute {
		t.Fatalf("first ResetAfter = %v, want 1m0s", d.ResetAfter)
	}
	clock.advance(20 * time.Second)
	if d := l.Allow("a"); d.ResetAfter != 40*time.Second {
		t.Fatalf("ResetAfter = %v 20s into a 60s window, want 40s", d.ResetAfter)
	}
}

func TestAllowResetAfterOnARefusalIsAPlausibleRetryAfter(t *testing.T) {
	clock := newClock()
	l := New(1, 0, time.Minute, 10, clock.Now)
	if d := l.Allow("a"); !d.OK {
		t.Fatal("first call refused, want allowed")
	}
	clock.advance(15 * time.Second)
	refused := l.Allow("a")
	if refused.OK {
		t.Fatal("second call allowed, want refused")
	}
	if refused.ResetAfter != 45*time.Second {
		t.Fatalf("refused ResetAfter = %v, want 45s", refused.ResetAfter)
	}
	seconds := int(refused.ResetAfter.Seconds())
	if seconds < 1 || seconds > 60 {
		t.Fatalf("Retry-After would be %d seconds, want a plausible 1..60", seconds)
	}
}

func TestAllowResetsAtTheWindowBoundary(t *testing.T) {
	clock := newClock()
	l := New(2, 0, time.Minute, 10, clock.Now)
	l.Allow("a")
	l.Allow("a")
	if d := l.Allow("a"); d.OK {
		t.Fatal("third call allowed, want refused before the boundary")
	}
	clock.advance(time.Minute)
	d := l.Allow("a")
	if !d.OK {
		t.Fatal("call at exactly the window edge refused, want allowed")
	}
	if d.Remaining != 1 {
		t.Fatalf("Remaining = %d after the reset, want 1", d.Remaining)
	}
}

func TestAllowKeepsDistinctKeysIndependent(t *testing.T) {
	clock := newClock()
	l := New(1, 0, time.Minute, 10, clock.Now)
	if d := l.Allow("a"); !d.OK {
		t.Fatal("a call 1 refused, want allowed")
	}
	if d := l.Allow("a"); d.OK {
		t.Fatal("a call 2 allowed, want refused")
	}
	if d := l.Allow("b"); !d.OK {
		t.Fatal("b call 1 refused, want allowed — keys must not share a counter")
	}
}

func TestKeysIsBoundedAtMaxKeys(t *testing.T) {
	clock := newClock()
	l := New(5, 0, time.Minute, 10, clock.Now)
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if got := l.Keys(); got != 10 {
		t.Fatalf("Keys() = %d after 1000 distinct keys, want 10", got)
	}
}

func TestKeysAtTheMaxKeysBoundary(t *testing.T) {
	for _, n := range []int{9999, 10000, 10001} {
		clock := newClock()
		l := New(5, 0, time.Minute, DefaultMaxKeys, clock.Now)
		for i := 0; i < n; i++ {
			l.Allow(fmt.Sprintf("k%d", i))
		}
		want := n
		if want > 10000 {
			want = 10000
		}
		if got := l.Keys(); got != want {
			t.Fatalf("Keys() = %d after %d distinct keys, want %d", got, n, want)
		}
	}
}

func TestOneThousandSpoofedKeysFromAnUntrustedPeerProduceExactlyOneEntry(t *testing.T) {
	clock := newClock()
	res := Resolver{Trusted: trustList(t, testTrustedCIDR)}
	l := New(100, 0, time.Minute, DefaultMaxKeys, clock.Now)

	var last Decision
	for i := 0; i < 1000; i++ {
		spoofed := fmt.Sprintf("203.0.113.%d", i%256)
		r := req(t, testUntrustedPeer, map[string]string{testCFHeader: spoofed})
		last = l.Allow(res.Key(r))
	}
	if got := l.Keys(); got != 1 {
		t.Fatalf("Keys() = %d after 1000 spoofed headers from one untrusted peer, want 1", got)
	}
	if last.OK {
		t.Fatal("the 1000th spoofed request was allowed, want refused")
	}
}

func TestEvictionRemovesTheLeastRecentlySeenKey(t *testing.T) {
	clock := newClock()
	l := New(5, 0, time.Minute, 2, clock.Now)
	l.Allow("a")
	l.Allow("b")
	l.Allow("a")
	l.Allow("c")

	if got := l.Keys(); got != 2 {
		t.Fatalf("Keys() = %d, want 2", got)
	}
	// a was refreshed by its second hit, so its count of 2 must have survived
	// and this third hit leaves 2 of 5. Asserted BEFORE touching b, because
	// re-adding b evicts whatever is least recently seen at that moment.
	if d := l.Allow("a"); d.Remaining != 2 {
		t.Fatalf("a Remaining = %d, want 2 (its window must survive eviction of b)", d.Remaining)
	}
	// b was least recently seen, so it was evicted and starts a fresh window:
	// had it survived, its earlier hit would leave 3 rather than 4.
	if d := l.Allow("b"); d.Remaining != 4 {
		t.Fatalf("b Remaining = %d, want 4 (a fresh window after eviction)", d.Remaining)
	}
}

func TestEvictionDoesNotResurrectAnExhaustedKeyAsAllowed(t *testing.T) {
	clock := newClock()
	l := New(1, 0, time.Minute, 1, clock.Now)
	if d := l.Allow("a"); !d.OK {
		t.Fatal("a call 1 refused, want allowed")
	}
	if d := l.Allow("a"); d.OK {
		t.Fatal("a call 2 allowed, want refused")
	}
	l.Allow("b")
	if d := l.Allow("a"); !d.OK {
		t.Fatal("a after eviction refused: eviction resets the window by design, refusing to track must never mean refusing to serve")
	}
}

func TestAllowIsSafeUnderConcurrentKeys(t *testing.T) {
	clock := newClock()
	const limit = 100
	l := New(limit, 0, time.Hour, DefaultMaxKeys, clock.Now)

	var (
		mu      sync.Mutex
		allowed int
		wg      sync.WaitGroup
	)
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			local := 0
			for i := 0; i < 50; i++ {
				if l.Allow(fmt.Sprintf("k%d", (g*50+i)%10)).OK {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}(g)
	}
	wg.Wait()

	if allowed != 10*limit {
		t.Fatalf("allowed = %d, want exactly %d — a lossy counter under-counts and raises the effective limit", allowed, 10*limit)
	}
	if got := l.Keys(); got != 10 {
		t.Fatalf("Keys() = %d, want 10", got)
	}
}
