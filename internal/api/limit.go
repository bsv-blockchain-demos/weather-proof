package api

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
	"github.com/bsv-blockchain-demos/weather-proof/internal/ratelimit"
)

// config.TrustedProxies satisfies ratelimit.TrustChecker structurally, so the
// wiring below compiles without this line — which is precisely why the line is
// wanted. internal/ratelimit is a leaf and must never import internal/config,
// so nothing else in the tree proves the two halves still fit; without the
// assertion, a signature change on either side turns a compile error into a
// silently nil Trusted, and a nil Trusted fails closed QUIETLY to one bucket
// per proxy pod.
var _ ratelimit.TrustChecker = config.TrustedProxies{}

// The limiter numbers are part of the contract, not a tuning detail (spec
// §6.1). They are named constants so a test can assert on them and a reviewer
// can find them in one place.
//
// generalLimitPerMin is 600 and not the TypeScript's 100 because one engaged
// /explorer visit costs ~103 requests, so a 100/min ceiling is tripped by a
// single legitimate user — and the SPA has no 429 handling, so a refusal there
// becomes a retry loop.
const (
	generalLimitPerMin = 600
	generalBurst       = 120
	verifyLimitPerMin  = 60
	sseStreamsPerMin   = 30
	sseConcurrentPerIP = 12
	sseGlobalMax       = 500
)

// proofDefaultLimitPerMin is PROOF_RATE_LIMIT_PER_MIN's default and the value
// newLimiters coerces a non-positive configuration to. config.Validate already
// floors the env var at 1; this is the second layer, because zero here would
// mean an UNLIMITED unauthenticated outbound proxy rather than a disabled one.
const proofDefaultLimitPerMin = 60

// limiterWindow is the fixed window every scope counts in. All six spec §6.1
// numbers are per minute.
const limiterWindow = time.Minute

// The four RFC-draft headers plus Retry-After, on every 429 (spec §6.1). The
// three RateLimit-* headers also go on every ALLOWED response; Retry-After
// does not, because it means "you were refused, wait this long".
const (
	headerRateLimitLimit     = "RateLimit-Limit"
	headerRateLimitRemaining = "RateLimit-Remaining"
	headerRateLimitReset     = "RateLimit-Reset"
	headerRetryAfter         = "Retry-After"
)

// msgTooManyRequests is the 429 body message, kept byte-identical to the
// TypeScript's so a client that string-matches it keeps working.
const msgTooManyRequests = "Too many requests, please try again later"

// msgNotImplemented is the placeholder body message for a route whose scope is
// wired but whose handler is not.
const msgNotImplemented = "not implemented"

// The route patterns, declared once. They are constants rather than literals
// at the registration site because the limiter tests drive the same paths a
// dozen times each and goconst fires at ten repeats of a string.
const (
	pathHealth = "/api/health"
	pathReady  = "/api/ready"
	pathEvents = "/api/events"
	pathVerify = "/api/verify"

	// pathProof carries a wildcard segment because the route it registers does:
	// it is a const like its siblings so B3, which is instructed to replace the
	// handler on exactly that registration line, edits a named pattern rather
	// than a bare literal that no test can reference.
	pathProof = "/api/proof/{txid}"

	pathWeather  = "/api/weather"
	pathStations = "/api/stations"
	pathOps      = "/api/ops"
)

// limiters holds one Limiter per SCOPE. The scopes are DISJOINT: a request
// consumes exactly one bucket. Mounting a general limiter on /api and a
// stricter one on /api/events — which is what the TypeScript does — makes one
// SSE request decrement BOTH, and because EventSource auto-reconnects the
// first 429 becomes a retry loop that cannot drain.
//
// Disjointness is structural, not conventional: each scope is a distinct
// *ratelimit.Limiter and withLimit is applied per ROUTE at registration, so
// there is no prefix middleware that could double-charge. Pinned in both
// directions by TestSSERequestDoesNotDecrementTheGeneralBucket and
// TestGeneralRequestDoesNotDecrementTheSSEBucket, and at the constructor by
// TestTheFourScopesAreDistinctObjects.
type limiters struct {
	general *ratelimit.Limiter
	verify  *ratelimit.Limiter
	sse     *ratelimit.Limiter
	proof   *ratelimit.Limiter
}

// newLimiters builds the four scopes. Each key map is bounded at
// ratelimit.DefaultMaxKeys, because the key source is influenced by an
// untrusted peer and an unbounded map is a memory DoS.
//
// now is injected so tests need no sleeps; ratelimit.New substitutes time.Now
// for a nil now, so a zero-valued Deps.Now cannot produce a dead clock.
func newLimiters(proofPerMin int, now func() time.Time) *limiters {
	if proofPerMin < 1 {
		proofPerMin = proofDefaultLimitPerMin
	}
	return &limiters{
		general: ratelimit.New(generalLimitPerMin, generalBurst, limiterWindow, ratelimit.DefaultMaxKeys, now),
		verify:  ratelimit.New(verifyLimitPerMin, 0, limiterWindow, ratelimit.DefaultMaxKeys, now),
		sse:     ratelimit.New(sseStreamsPerMin, 0, limiterWindow, ratelimit.DefaultMaxKeys, now),
		proof:   ratelimit.New(proofPerMin, 0, limiterWindow, ratelimit.DefaultMaxKeys, now),
	}
}

// withLimit applies ONE limiter to the handler it wraps. It is applied per
// ROUTE GROUP at registration rather than as a prefix middleware, which is what
// makes the scopes structurally disjoint instead of disjoint by convention.
//
// The key is resolved through res.Key — never r.RemoteAddr directly, which
// would collapse every client behind the ingress into one bucket (spec §6.1
// defect 1). The three RateLimit-* headers are written unconditionally, before
// anything else touches the ResponseWriter, since a header set after
// WriteHeader is silently dropped.
func withLimit(l *ratelimit.Limiter, res ratelimit.Resolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The bucket is charged HERE, before onlyMethod's check runs inside
			// next, so a WRONG-METHOD request to a limited path still costs a
			// slot. That is deliberate: the pattern is registered, the request
			// reached a real route, and a free 405 on every limited path would be
			// an unmetered way to probe the API. The cost is that a client with a
			// broken verb burns its own budget, which is the safe direction.
			// Pinned by TestAWrongMethodOnALimitedPathStillCostsASlot.
			d := l.Allow(res.Key(r))

			w.Header().Set(headerRateLimitLimit, strconv.Itoa(d.Limit))
			w.Header().Set(headerRateLimitRemaining, strconv.Itoa(d.Remaining))
			w.Header().Set(headerRateLimitReset, secondsCeil(d.ResetAfter))

			if !d.OK {
				w.Header().Set(headerRetryAfter, secondsCeil(d.ResetAfter))
				writeError(w, r, http.StatusTooManyRequests, msgTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// secondsCeil renders a duration as a whole number of seconds, rounded UP. The
// ceiling is load-bearing on Retry-After: a floor of a sub-second remainder
// yields "Retry-After: 0" and therefore an immediate retry, which is the retry
// loop the whole scope design exists to avoid. RateLimit-Reset uses the same
// rounding so the two never disagree and neither reads as 0 while a window is
// still open.
func secondsCeil(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return strconv.Itoa(int(math.Ceil(d.Seconds())))
}

// handleNotImplemented is the placeholder B3 replaces. It answers 501 with the
// standard errorDTO. It exists so the verify and proof SCOPES are wired and
// tested NOW: a scope added later is a scope that ships unlimited, and this is
// the one part of the limiter contract B2 could otherwise defer without
// noticing.
//
// B3 replaces the handler AT ITS REGISTRATION SITE in NewRouter — it must not
// add a second route for the same pattern, which would either panic on a
// ServeMux conflict or leave the limited copy shadowed.
func handleNotImplemented(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotImplemented, msgNotImplemented)
}
