package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
	"github.com/bsv-blockchain-demos/weather-proof/internal/ratelimit"
	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// Deps is everything the router needs. A struct rather than a parameter list
// because the middleware and limiter tasks each add a field, and a widening
// signature is how a call site silently keeps passing the old set.
type Deps struct {
	Store    store.Store
	Now      func() time.Time
	PollRate time.Duration
	Logger   *slog.Logger

	// Trusted is the parsed TRUSTED_PROXY_CIDRS list (Task 3). The zero value
	// means trust nothing, which is the correct fail-closed default.
	Trusted config.TrustedProxies

	// ProofRateLimitPerMin is config.Config.ProofRateLimitPerMin, the
	// GET /api/proof/{txid} scope's per-minute ceiling. A value below 1 is
	// coerced to proofDefaultLimitPerMin by newLimiters, because zero on an
	// unauthenticated outbound proxy would mean unlimited rather than disabled.
	//
	// The limiters themselves are NOT a Deps field: NewRouter constructs them,
	// so every router owns its own four buckets and no two routers — in
	// production or across tests — can share limiter state.
	ProofRateLimitPerMin int

	// Hub is Task 17's SSE fan-out. It is a Deps field rather than a parameter
	// because BOTH routers read it: NewRouter for GET /api/events, and Task
	// 20's NewOpsRouter for the sseClients gauge. Two hubs would make the
	// gauge report a set no stream is registered in.
	//
	// A nil Hub is replaced by a fresh one with the contract's caps, so a
	// caller that forgot to set it gets a working — if unobservable — stream
	// rather than a nil-pointer panic on the first connection.
	Hub *Hub
}

// onlyGET wraps a handler so that any method other than GET answers the JSON
// 405 shape with an Allow header, instead of falling through to the
// handler. This exists because net/http.ServeMux's own method-mismatch 405
// carries a text/plain body with no hook to override it: registering the
// pattern WITHOUT a method prefix (e.g. "/api/weather" rather than
// "GET /api/weather") and branching here is what produces a JSON body.
func onlyGET(h http.HandlerFunc) http.HandlerFunc {
	return onlyMethod(http.MethodGet, h)
}

// onlyMethod is onlyGET generalized to one other verb: POST /api/verify is the
// only non-GET route in the API, and it needs the same JSON 405 shape with its
// OWN Allow value rather than a hardcoded GET.
func onlyMethod(method string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			methodNotAllowedJSON(w, r, method)
			return
		}
		h(w, r)
	}
}

// notFoundJSON answers a routing miss with the same errorDTO shape as every
// other error, so a client never has to parse Go's default text/plain "404
// page not found" body.
func notFoundJSON(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "not found")
}

// methodNotAllowedJSON answers a method mismatch on a known path the same
// way. It sets the Allow header itself, since it never reaches
// net/http.ServeMux's own 405 handling (see onlyGET).
func methodNotAllowedJSON(w http.ResponseWriter, r *http.Request, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, r, http.StatusMethodNotAllowed, "method not allowed")
}

// NewRouter builds the API listener's handler. Route registration ORDER is
// part of the contract, not a style choice: /api/health, /api/ready and
// /api/ops (Tasks 19-20) are registered BEFORE the rate-limiter middleware
// (Task 16) wraps anything, so a liveness probe can never be rate-limited —
// see Task 16's assertions. Note that for net/http.ServeMux itself,
// registration ORDER does not affect which pattern matches a given request
// (only specificity does); the order constraint here is about middleware
// wrapping, not mux pattern precedence — do not "fix" the order thinking it
// changes matching.
//
// The four read routes are registered as eight patterns — each path plus its
// "{$}" twin — because Go 1.22's ServeMux does not match a trailing slash
// against an exact pattern:
//
//	GET /api/weather            GET /api/weather/{$}
//	GET /api/weather/{id}
//	GET /api/stations           GET /api/stations/{$}
//	GET /api/stations/{stationId}
//
// "/api/weather/{$}" and "/api/weather/{id}" do not conflict: "{$}" matches
// only the exact trailing slash, "{id}" requires a non-empty segment.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	lim := newLimiters(d.ProofRateLimitPerMin, d.Now)
	res := ratelimit.Resolver{Trusted: d.Trusted}

	// ── 1-2. THE EXEMPTION SEAM. Everything registered in this block is
	// registered WITHOUT withLimit and must stay that way: a liveness probe
	// that can be rate-limited turns a traffic spike into a pod restart (spec
	// §6.0 control 3). handleProbeStandIn is Task 19's replacement site — swap
	// the handler here, do not add a second registration and do not move these
	// two lines below the limited routes. /api/ops is not in this list at all:
	// it lives on the ops server (Task 20/21) and is therefore exempt by
	// construction, which is stronger than exempt by registration order.
	//
	// Pinned by TestHealthAndReadyAreNeverLimited and
	// TestHealthAndReadyDoNotConsumeAnyBucket.
	mux.HandleFunc(pathHealth, onlyGET(handleProbeStandIn))
	mux.HandleFunc(pathReady, onlyGET(handleProbeStandIn))

	// ── 3. SSE. Its own scope, with markSSEExempt INSIDE withLimit so the
	// LIMITER's 429 — written before the marker ever runs — still gets the
	// write deadline.
	//
	// Everything past the marker is exempt, and that deliberately includes the
	// HUB's own 429 and 503 refusal bodies: the marker is a context flag
	// flipped during dispatch, not a second router, so there is no seam that
	// exempts the stream while still deadlining a short body written by the
	// same handler. Harmless — both refusals are a few dozen bytes written
	// immediately, with no reader to stall on.
	//
	// markSSEExempt is NOT optional and nothing else enforces it: without the
	// marker the stream inherits withWriteDeadline's 30 s budget and every
	// connection dies at 30 seconds with no error anywhere. Pinned
	// behaviorally by TestEventsHasNoWriteDeadline.
	hub := d.Hub
	if hub == nil {
		hub = NewHub(sseGlobalMax, sseConcurrentPerIP, d.Logger)
	}
	mux.Handle(pathEvents, withLimit(lim.sse, res)(markSSEExempt(onlyGET(handleEvents(hub, d.Store.Stations, res)))))

	// ── 4-5. The two scopes whose HANDLERS are B3's. The scopes are wired and
	// tested now because a scope wired later is a scope that ships unlimited.
	// B3 replaces handleNotImplemented at these two lines.
	mux.Handle(pathVerify, withLimit(lim.verify, res)(onlyMethod(http.MethodPost, handleNotImplemented)))
	mux.Handle("/api/proof/{txid}", withLimit(lim.proof, res)(onlyGET(handleNotImplemented)))

	// ── 6. The four read routes and their {$} twins, all on the general scope.
	general := withLimit(lim.general, res)

	weatherList := general(onlyGET(handleWeatherList(d.Store.Records)))
	weatherDetail := general(onlyGET(handleWeatherDetail(d.Store.Records)))
	stationList := general(onlyGET(handleStationList(d.Store.Stations, d.Now, d.PollRate)))
	stationDetail := general(onlyGET(handleStationDetail(d.Store.Stations, d.Now, d.PollRate)))

	mux.Handle(pathWeather, weatherList)
	mux.Handle(pathWeather+"/{$}", weatherList)
	mux.Handle(pathWeather+"/{id}", weatherDetail)

	mux.Handle(pathStations, stationList)
	mux.Handle(pathStations+"/{$}", stationList)
	mux.Handle(pathStations+"/{stationId}", stationDetail)

	// ── 7. The catch-all 404, NOT limited: a limiter here would let an
	// unrouted path exhaust a real client's bucket.
	mux.HandleFunc("/", notFoundJSON)

	return withRequestID(withSecurityHeaders(withRecover(d.Logger)(withWriteDeadline(d.Logger)(mux))))
}
