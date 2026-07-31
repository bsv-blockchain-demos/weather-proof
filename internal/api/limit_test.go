package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
	"github.com/bsv-blockchain-demos/weather-proof/internal/ratelimit"
)

// The peer addresses every limiter test drives from. They are literal and
// pairwise distinct because a limiter keyed on a constant passes every
// single-client test — see TestTwoClientsFromDifferentAddressesDoNotShareABucket.
const (
	limitPeerA = "203.0.113.10:5555"
	limitPeerB = "203.0.113.11:5555"

	// limitTrustedPeer is inside the 10.0.0.0/8 fixture trust list, so and only
	// so its forwarding headers are read.
	limitTrustedPeer = "10.0.0.5:5555"
)

// generalCapacity is the general scope's ADVERTISED and ENFORCED ceiling,
// written as a literal on purpose.
//
// It is 720 and not 600: ratelimit.Decision.Limit is limit+burst, and burst is
// a permanent capacity add-on rather than a start-of-window grace, so
// New(600, 120, …) permits 720 per window and reports RateLimit-Limit: 720.
// That is a ruling recorded in internal/ratelimit/limiter.go's Decision.Limit
// comment and in Task 15's report; it is pinned here rather than "fixed".
//
// Never derived from generalLimitPerMin + generalBurst: an expected value
// computed from the package's own constants mutates together with them and
// asserts nothing.
const generalCapacity = 720

// exhaustCap bounds every exhaustion loop so a limiter that never refuses
// fails the test instead of running forever.
const exhaustCap = 5000

// limitRouter builds a router whose limiters are FRESH for this test. Every
// test calls this rather than sharing a package-level limiter: the limiter
// holds process-level state, and a shared one is exactly the cross-test
// coupling that makes a test discriminate only under a -run filter.
func limitRouter(t *testing.T, mutate func(d *Deps)) http.Handler {
	t.Helper()
	d := testDeps(t)
	if mutate != nil {
		mutate(&d)
	}
	return NewRouter(d)
}

// do drives one request with an explicit peer address and header set.
func do(t *testing.T, h http.Handler, method, target, peer string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	req.RemoteAddr = peer
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// exhaust drives requests until one is refused and returns how many were
// admitted before the refusal. It fails the test if nothing is ever refused.
func exhaust(t *testing.T, h http.Handler, method, target, peer string, headers map[string]string) int {
	t.Helper()
	for i := range exhaustCap {
		rec := do(t, h, method, target, peer, headers)
		if rec.Code == http.StatusTooManyRequests {
			return i
		}
	}
	t.Fatalf("%s %s was never refused within %d requests", method, target, exhaustCap)
	return 0
}

// remainingOf reads the RateLimit-Remaining header as an integer.
func remainingOf(t *testing.T, rec *httptest.ResponseRecorder) int {
	t.Helper()
	raw := rec.Header().Get(headerRateLimitRemaining)
	if raw == "" {
		t.Fatalf("response carries no %s header", headerRateLimitRemaining)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q does not parse as an integer: %v", headerRateLimitRemaining, raw, err)
	}
	return n
}

// trustTenSlashEight returns a Deps mutator that trusts 10.0.0.0/8, which is
// what makes limitTrustedPeer's forwarding headers readable.
func trustTenSlashEight(t *testing.T) func(d *Deps) {
	t.Helper()
	trusted, err := config.ParseCIDRList([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseCIDRList: %v", err)
	}
	return func(d *Deps) { d.Trusted = trusted }
}

func TestGeneralScopeAllowsSevenHundredTwentyRequestsPerMinute(t *testing.T) {
	h := limitRouter(t, nil)

	admitted := exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if admitted != generalCapacity {
		t.Fatalf("general scope admitted %d requests before refusing, want exactly %d", admitted, generalCapacity)
	}
}

func TestGeneralScopeRefusalCarriesAllFourHeaders(t *testing.T) {
	h := limitRouter(t, nil)
	exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", rec.Code)
	}

	for _, name := range []string{headerRateLimitLimit, headerRateLimitRemaining, headerRateLimitReset, headerRetryAfter} {
		if rec.Header().Get(name) == "" {
			t.Errorf("429 carries no %s header", name)
		}
	}

	if got := rec.Header().Get(headerRateLimitLimit); got != strconv.Itoa(generalCapacity) {
		t.Errorf("%s = %q, want %q", headerRateLimitLimit, got, strconv.Itoa(generalCapacity))
	}
	if got := remainingOf(t, rec); got != 0 {
		t.Errorf("%s = %d on a refusal, want 0", headerRateLimitRemaining, got)
	}

	retryAfter, err := strconv.Atoi(rec.Header().Get(headerRetryAfter))
	if err != nil {
		t.Fatalf("%s = %q does not parse as an integer: %v", headerRetryAfter, rec.Header().Get(headerRetryAfter), err)
	}
	if retryAfter < 1 || retryAfter > 60 {
		t.Errorf("%s = %d, want a plausible second count in [1,60]", headerRetryAfter, retryAfter)
	}

	reset, err := strconv.Atoi(rec.Header().Get(headerRateLimitReset))
	if err != nil {
		t.Fatalf("%s = %q does not parse as an integer: %v", headerRateLimitReset, rec.Header().Get(headerRateLimitReset), err)
	}
	if reset < 1 || reset > 60 {
		t.Errorf("%s = %d, want a plausible second count in [1,60]", headerRateLimitReset, reset)
	}
}

func TestGeneralScopeRefusalBodyIsJSON(t *testing.T) {
	h := limitRouter(t, nil)
	exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", rec.Code)
	}
	if ct := rec.Header().Get(headerContentType); ct != contentTypeJSON {
		t.Errorf("got %s %q, want %q", headerContentType, ct, contentTypeJSON)
	}

	var body clientErrorDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body does not unmarshal into clientErrorDTO: %v; body=%s", err, rec.Body.String())
	}
	if body.Error != msgTooManyRequests {
		t.Errorf("error = %q, want %q", body.Error, msgTooManyRequests)
	}
}

// TestRefusalCarriesTheSecurityHeadersAndExactlyOneBodyKey checks the two
// properties a 429 shares with every other client error, through the real
// NewRouter: the three spec §6.0 control 19 headers are present, and the body
// is clientErrorDTO-shaped — exactly one key, no request_id. A 429 written by
// a hand-rolled path inside the limiter middleware is the easiest place in the
// package to lose both.
func TestRefusalCarriesTheSecurityHeadersAndExactlyOneBodyKey(t *testing.T) {
	h := limitRouter(t, nil)
	exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", rec.Code)
	}

	want := map[string]string{
		headerContentTypeOptions: valueNoSniff,
		headerFrameOptions:       valueDeny,
		headerReferrerPolicy:     valueNoReferrer,
	}
	for name, value := range want {
		if got := rec.Header().Get(name); got != value {
			t.Errorf("429: %s = %q, want %q", name, got, value)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal body: %v; body=%s", err, rec.Body.String())
	}
	if len(decoded) != 1 {
		t.Errorf("429 body has %d keys (%v), want exactly 1", len(decoded), decoded)
	}
	if _, ok := decoded["request_id"]; ok {
		t.Errorf("429 body carries request_id, which belongs only on a 5xx: %v", decoded)
	}
}

func TestAllowedResponsesAlsoCarryTheRateLimitHeaders(t *testing.T) {
	h := limitRouter(t, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get(headerRateLimitLimit); got != strconv.Itoa(generalCapacity) {
		t.Errorf("%s = %q, want %q", headerRateLimitLimit, got, strconv.Itoa(generalCapacity))
	}
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Errorf("%s = %d after one request, want %d", headerRateLimitRemaining, got, generalCapacity-1)
	}
	if rec.Header().Get(headerRateLimitReset) == "" {
		t.Errorf("allowed response carries no %s header", headerRateLimitReset)
	}

	// Retry-After belongs only on a refusal. Both halves are asserted because
	// a middleware that sets all four unconditionally passes the presence half.
	if got := rec.Header().Get(headerRetryAfter); got != "" {
		t.Errorf("allowed response carries %s = %q, want absent", headerRetryAfter, got)
	}
}

// TestSSERequestDoesNotDecrementTheGeneralBucket is spec §6.1's named
// disjointness test. It measures the general bucket from OUTSIDE — through a
// later general response's Remaining — rather than by inspecting the limiter,
// because a shared bucket is only observable across scopes.
func TestSSERequestDoesNotDecrementTheGeneralBucket(t *testing.T) {
	h := limitRouter(t, nil)

	before := remainingOf(t, do(t, h, http.MethodGet, pathWeather, limitPeerA, nil))

	sse := do(t, h, http.MethodGet, pathEvents, limitPeerA, nil)
	if sse.Code == http.StatusTooManyRequests {
		t.Fatalf("the first SSE request was refused: %d", sse.Code)
	}

	after := remainingOf(t, do(t, h, http.MethodGet, pathWeather, limitPeerA, nil))

	if after != before-1 {
		t.Fatalf("general Remaining went %d -> %d across one SSE request; want a drop of exactly 1 "+
			"(the second general request's own cost), not %d", before, after, before-after)
	}
}

// TestGeneralRequestDoesNotDecrementTheSSEBucket is the mirror direction.
// Disjointness is symmetric and testing one direction leaves the other free
// to regress.
func TestGeneralRequestDoesNotDecrementTheSSEBucket(t *testing.T) {
	h := limitRouter(t, nil)

	for i := range 10 {
		rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("general request %d: got status %d, want 200", i, rec.Code)
		}
	}

	for i := range sseStreamsPerMin {
		rec := do(t, h, http.MethodGet, pathEvents, limitPeerA, nil)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("SSE request %d was refused after 10 general requests; the general scope is "+
				"decrementing the SSE bucket", i)
		}
	}
}

func TestSSEScopeAllowsThirtyNewStreamsPerMinute(t *testing.T) {
	h := limitRouter(t, nil)

	admitted := exhaust(t, h, http.MethodGet, pathEvents, limitPeerA, nil)
	if admitted != 30 {
		t.Fatalf("SSE scope admitted %d new streams before refusing, want exactly 30", admitted)
	}
}

func TestVerifyScopeAllowsSixtyPerMinute(t *testing.T) {
	h := limitRouter(t, nil)

	// The 501s are the point: the SCOPE is live before the handler is.
	for i := range 60 {
		rec := do(t, h, http.MethodPost, pathVerify, limitPeerA, nil)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("verify request %d: got status %d, want 501 (the B3 placeholder); body=%s",
				i, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, h, http.MethodPost, pathVerify, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("verify request 61: got status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get(headerRateLimitLimit); got != "60" {
		t.Errorf("verify %s = %q, want %q (burst 0, so the advertised number equals the enforced one)",
			headerRateLimitLimit, got, "60")
	}
}

func TestVerifyScopeIsDisjointFromTheGeneralScope(t *testing.T) {
	h := limitRouter(t, nil)

	exhaust(t, h, http.MethodPost, pathVerify, limitPeerA, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("a read request after a verify flood got status %d, want 200; body=%s",
			rec.Code, rec.Body.String())
	}
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Errorf("general %s = %d after a verify flood, want %d: the verify scope is spending the "+
			"general bucket", headerRateLimitRemaining, got, generalCapacity-1)
	}
}

func TestProofScopeUsesTheConfiguredPerMinuteValue(t *testing.T) {
	const configured = 5
	const txid = "0000000000000000000000000000000000000000000000000000000000000001"

	h := limitRouter(t, func(d *Deps) { d.ProofRateLimitPerMin = configured })

	for i := range configured {
		rec := do(t, h, http.MethodGet, "/api/proof/"+txid, limitPeerA, nil)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("proof request %d: got status %d, want 501 (the B3 placeholder); body=%s",
				i, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, h, http.MethodGet, "/api/proof/"+txid, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("proof request %d: got status %d, want 429 (the injected limit, not the default)",
			configured+1, rec.Code)
	}
	if got := rec.Header().Get(headerRateLimitLimit); got != strconv.Itoa(configured) {
		t.Errorf("proof %s = %q, want %q", headerRateLimitLimit, got, strconv.Itoa(configured))
	}
}

// TestHealthAndReadyAreNeverLimited is spec §6.0 control 3: a rate-limited
// liveness probe turns a traffic spike into a pod restart.
//
// The probe handlers themselves arrive in Task 19; what is asserted here is
// the SEAM — that the two patterns are registered OUTSIDE every limiter — so
// Task 19 inherits a working gate rather than an aspiration. The assertions
// are deliberately independent of the probe's status code: never 429, and no
// RateLimit-* header, because a limiter that touched the request would set
// them even on an allowed pass.
func TestHealthAndReadyAreNeverLimited(t *testing.T) {
	h := limitRouter(t, nil)

	exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)

	for _, target := range []string{pathHealth, pathReady} {
		for i := range 100 {
			rec := do(t, h, http.MethodGet, target, limitPeerA, nil)
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s request %d was rate-limited", target, i)
			}
			if got := rec.Header().Get(headerRateLimitLimit); got != "" {
				t.Fatalf("%s request %d carries %s = %q, so it passed through a limiter",
					target, i, headerRateLimitLimit, got)
			}
		}
	}
}

// TestHealthAndReadyDoNotConsumeAnyBucket is the sibling of the previous test.
// Exempt from REFUSAL and exempt from COUNTING are different properties, and
// asserting only the first lets health probes silently drain a real client's
// budget.
func TestHealthAndReadyDoNotConsumeAnyBucket(t *testing.T) {
	h := limitRouter(t, nil)

	for range 100 {
		do(t, h, http.MethodGet, pathHealth, limitPeerA, nil)
		do(t, h, http.MethodGet, pathReady, limitPeerA, nil)
	}

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Fatalf("general %s = %d after 200 probe requests, want %d: the probes are spending the "+
			"general bucket", headerRateLimitRemaining, got, generalCapacity-1)
	}
}

// TestTheCatchAllNotFoundIsNotRateLimited pins registration item 7: an
// unrouted path must not be able to exhaust a real client's bucket.
func TestTheCatchAllNotFoundIsNotRateLimited(t *testing.T) {
	h := limitRouter(t, nil)

	for range 100 {
		rec := do(t, h, http.MethodGet, "/api/nope", limitPeerA, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("got status %d, want 404", rec.Code)
		}
	}

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Fatalf("general %s = %d after 100 unrouted requests, want %d", headerRateLimitRemaining, got, generalCapacity-1)
	}
}

// TestTwoClientsFromDifferentAddressesDoNotShareABucket exists because a
// limiter keyed on a constant passes every single-client test above.
func TestTwoClientsFromDifferentAddressesDoNotShareABucket(t *testing.T) {
	h := limitRouter(t, nil)

	exhaust(t, h, http.MethodGet, pathWeather, limitPeerA, nil)

	rec := do(t, h, http.MethodGet, pathWeather, limitPeerB, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second client got status %d, want 200; the two peers share one bucket", rec.Code)
	}
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Errorf("second client's %s = %d, want a full allowance minus its own request (%d)",
			headerRateLimitRemaining, got, generalCapacity-1)
	}
}

// TestKeysAreResolvedThroughTheResolverNotRemoteAddr proves the wiring passes
// ratelimit.Resolver.Key's result and not r.RemoteAddr: two requests share one
// trusted peer address but carry different CF-Connecting-IP values, so they
// must land in different buckets.
func TestKeysAreResolvedThroughTheResolverNotRemoteAddr(t *testing.T) {
	h := limitRouter(t, trustTenSlashEight(t))

	firstClient := map[string]string{"CF-Connecting-IP": "198.51.100.1"}
	secondClient := map[string]string{"CF-Connecting-IP": "198.51.100.2"}

	exhaust(t, h, http.MethodGet, pathWeather, limitTrustedPeer, firstClient)

	rec := do(t, h, http.MethodGet, pathWeather, limitTrustedPeer, secondClient)
	if rec.Code != http.StatusOK {
		t.Fatalf("the second forwarded client got status %d, want 200; the limiter is keying on "+
			"r.RemoteAddr rather than the resolver's key", rec.Code)
	}
	if got := remainingOf(t, rec); got != generalCapacity-1 {
		t.Errorf("second forwarded client's %s = %d, want %d", headerRateLimitRemaining, got, generalCapacity-1)
	}
}

// TestSpoofedHeadersFromAnUntrustedPeerShareOneBucket is the end-to-end form
// of Task 15's headline test, through the real router: an UNTRUSTED peer gets
// exactly one bucket no matter what it puts in CF-Connecting-IP.
func TestSpoofedHeadersFromAnUntrustedPeerShareOneBucket(t *testing.T) {
	h := limitRouter(t, nil)

	admitted := 0
	for i := range exhaustCap {
		headers := map[string]string{"CF-Connecting-IP": "198.51.100." + strconv.Itoa(i%256)}
		rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, headers)
		if rec.Code == http.StatusTooManyRequests {
			admitted = i
			break
		}
	}
	if admitted != generalCapacity {
		t.Fatalf("a spoofing untrusted peer was admitted %d times before refusal, want exactly %d",
			admitted, generalCapacity)
	}
}

// TestTheFourScopesAreDistinctObjects guards the constructor itself: a
// newLimiters that returned the same *Limiter for two fields would make the
// disjointness tests above pass by accident in one direction.
func TestTheFourScopesAreDistinctObjects(t *testing.T) {
	l := newLimiters(60, func() time.Time { return routerFixedNow })

	named := map[string]*ratelimit.Limiter{
		"general": l.general,
		"verify":  l.verify,
		"sse":     l.sse,
		"proof":   l.proof,
	}
	for name, limiter := range named {
		if limiter == nil {
			t.Fatalf("scope %s is nil", name)
		}
	}
	for nameA, a := range named {
		for nameB, b := range named {
			if nameA >= nameB {
				continue
			}
			if a == b {
				t.Errorf("scopes %s and %s are the same *ratelimit.Limiter", nameA, nameB)
			}
		}
	}
}

// TestSSEConcurrencyConstantsMatchTheSpec pins the two numbers Task 18
// enforces, against literals. The caps themselves are Task 18's — a limiter
// counts requests in a window and cannot count live connections — but the
// numbers are part of spec §6.1's contract and live with the other scope
// numbers.
func TestSSEConcurrencyConstantsMatchTheSpec(t *testing.T) {
	if sseConcurrentPerIP != 12 {
		t.Errorf("sseConcurrentPerIP = %d, want 12", sseConcurrentPerIP)
	}
	if sseGlobalMax != 500 {
		t.Errorf("sseGlobalMax = %d, want 500", sseGlobalMax)
	}
}

// TestTheScopeNumbersMatchTheSpec pins the four request-rate numbers against
// literals, so a tuning edit to any of them is a red test rather than a silent
// contract change.
func TestTheScopeNumbersMatchTheSpec(t *testing.T) {
	if generalLimitPerMin != 600 {
		t.Errorf("generalLimitPerMin = %d, want 600", generalLimitPerMin)
	}
	if generalBurst != 120 {
		t.Errorf("generalBurst = %d, want 120", generalBurst)
	}
	if verifyLimitPerMin != 60 {
		t.Errorf("verifyLimitPerMin = %d, want 60", verifyLimitPerMin)
	}
	if sseStreamsPerMin != 30 {
		t.Errorf("sseStreamsPerMin = %d, want 30", sseStreamsPerMin)
	}
	if proofDefaultLimitPerMin != 60 {
		t.Errorf("proofDefaultLimitPerMin = %d, want 60", proofDefaultLimitPerMin)
	}
}

// advancingClock returns a clock that moves forward by step on every call. It
// is what makes a window's remainder FRACTIONAL at the moment of a refusal,
// which is how the Retry-After ceiling becomes observable end to end: with a
// frozen clock the remainder is a whole 60 s and a floor and a ceiling agree.
func advancingClock(start time.Time, step time.Duration) func() time.Time {
	calls := 0
	return func() time.Time {
		now := start.Add(time.Duration(calls) * step)
		calls++
		return now
	}
}

// TestRefusalRetryAfterIsRoundedUpEndToEnd pins the ceiling through the real
// router. The clock advances 1 ms per call, so by the time the verify bucket
// refuses ~60 ms of the 60 s window has elapsed and the true remainder is
// ~59.94 s: a ceiling reports 60 and a truncation reports 59. Both the
// expected value and the rounding direction are literals.
//
// The verify scope is used rather than the general one because 61 requests pin
// the same property as 721 and keep the package's wall clock where it is.
func TestRefusalRetryAfterIsRoundedUpEndToEnd(t *testing.T) {
	h := limitRouter(t, func(d *Deps) {
		d.Now = advancingClock(routerFixedNow, time.Millisecond)
	})

	exhaust(t, h, http.MethodPost, pathVerify, limitPeerA, nil)

	rec := do(t, h, http.MethodPost, pathVerify, limitPeerA, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("got status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get(headerRetryAfter); got != "60" {
		t.Errorf("%s = %q with a fractional remainder, want %q — a truncation reports 59",
			headerRetryAfter, got, "60")
	}
	if got := rec.Header().Get(headerRateLimitReset); got != "60" {
		t.Errorf("%s = %q with a fractional remainder, want %q", headerRateLimitReset, got, "60")
	}
}

// TestAWrongMethodOnALimitedPathStillCostsASlot pins the documented choice at
// withLimit's Allow call: the bucket is charged before the method check, so a
// free 405 cannot be used to probe a limited path unmetered.
func TestAWrongMethodOnALimitedPathStillCostsASlot(t *testing.T) {
	h := limitRouter(t, nil)

	rec405 := do(t, h, http.MethodPost, pathWeather, limitPeerA, nil)
	if rec405.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s: got status %d, want 405", pathWeather, rec405.Code)
	}
	// The 405 itself carries the headers, which is the first half of "it was
	// charged".
	if got := remainingOf(t, rec405); got != generalCapacity-1 {
		t.Errorf("405 response's %s = %d, want %d", headerRateLimitRemaining, got, generalCapacity-1)
	}

	// The second half, measured independently: the NEXT well-formed request sees
	// a bucket that is two down, not one.
	rec := do(t, h, http.MethodGet, pathWeather, limitPeerA, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	if got := remainingOf(t, rec); got != generalCapacity-2 {
		t.Errorf("%s = %d after one 405 and one 200, want %d: the 405 was served free",
			headerRateLimitRemaining, got, generalCapacity-2)
	}
}

// TestSecondsCeilRoundsUpSoRetryAfterIsNeverZero pins the ROUNDING directly.
// It cannot be pinned through a response with the suite's frozen clock, where
// a window's remainder is always a whole 60 seconds and a floor and a ceiling
// agree — which is exactly how a floor would survive every test above.
func TestSecondsCeilRoundsUpSoRetryAfterIsNeverZero(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{time.Millisecond, "1"},
		{500 * time.Millisecond, "1"},
		{time.Second, "1"},
		{1500 * time.Millisecond, "2"},
		{60 * time.Second, "60"},
		{0, "0"},
		{-time.Second, "0"},
	}
	for _, c := range cases {
		if got := secondsCeil(c.in); got != c.want {
			t.Errorf("secondsCeil(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestProofScopeFallsBackToTheDefaultWhenUnset pins the coercion: a zero or
// negative wiring must not become an unlimited scope. config.Validate already
// floors the env var at 1, so this is the second layer.
func TestProofScopeFallsBackToTheDefaultWhenUnset(t *testing.T) {
	const txid = "0000000000000000000000000000000000000000000000000000000000000002"

	for _, configured := range []int{0, -5} {
		h := limitRouter(t, func(d *Deps) { d.ProofRateLimitPerMin = configured })

		admitted := exhaust(t, h, http.MethodGet, "/api/proof/"+txid, limitPeerA, nil)
		if admitted != proofDefaultLimitPerMin {
			t.Errorf("ProofRateLimitPerMin=%d admitted %d requests, want the default %d",
				configured, admitted, proofDefaultLimitPerMin)
		}
	}
}
