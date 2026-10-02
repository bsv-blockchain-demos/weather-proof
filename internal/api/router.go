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

// orDefaultLogger coerces a nil logger to slog.Default(), the same coercion
// NewHub does. It is one function rather than two inline nil checks so a third
// constructor cannot pick a different fallback.
func orDefaultLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.Default()
	}
	return log
}

// orFallbackHub returns d.Hub, or a fresh hub if it is nil — and SAYS SO when it
// falls back.
//
// The fallback keeps a caller that forgot the field from panicking on the first
// connection, but it also quietly breaks the single-hub guarantee Deps.Hub
// documents: NewRouter and NewOpsRouter each build their own, so /api/ops
// reports sseClients off a hub that no SSE stream ever registered in, and the
// gauge reads a permanent zero with nothing anywhere to explain it. A degraded
// gauge that announces itself is recoverable; a silent one is what makes an
// operator distrust the whole page.
//
// Warn rather than fail: refusing to build a router would turn a metrics defect
// into an outage, and it is one line in both constructors.
func orFallbackHub(h *Hub, log *slog.Logger, router string) *Hub {
	if h != nil {
		return h
	}
	log.Warn("no shared SSE hub was supplied; this router built its own",
		"router", router,
		"consequence", "/api/ops reports sseClients from a hub no stream is registered in")
	return NewHub(sseGlobalMax, sseConcurrentPerIP, log)
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
// The four read routes are registered as six patterns — each LIST path plus its
// "{$}" twin — because Go 1.22's ServeMux does not match a trailing slash
// against an exact pattern. The two DETAIL routes have no twin: "{id}" already
// requires a non-empty segment, so there is no trailing-slash spelling of them
// to catch.
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

	// A nil Logger is coerced, exactly as NewHub coerces one and for the same
	// reason: this package already documents that a partially-filled Deps is
	// supported (see the Hub field), and Logger was the one field that broke
	// that contract. withRecover dereferences the logger INSIDE the deferred
	// recovery, so a nil there turns the first panic in any handler into a
	// second panic during recovery — no 500, no log, connection aborted.
	d.Logger = orDefaultLogger(d.Logger)

	// Now is coerced next to Logger and for the same reason: Deps is documented
	// as supporting a partially-filled value, and d.Now is passed straight to
	// newLimiters and to the health and station handlers, where CALLING a nil
	// func panics. A nil Logger was the first field that broke that contract;
	// this was the second.
	if d.Now == nil {
		d.Now = time.Now
	}

	lim := newLimiters(d.ProofRateLimitPerMin, d.Now)
	res := ratelimit.Resolver{Trusted: d.Trusted}

	// ── 1-2. THE EXEMPTION SEAM. Everything registered in this block is
	// registered WITHOUT withLimit and must stay that way: a liveness probe
	// that can be rate-limited turns a traffic spike into a pod restart (spec
	// §6.0 control 3). /api/ops is not in this list at all: it lives on the ops
	// server (Task 20/21) and is therefore exempt by construction, which is
	// stronger than exempt by registration order.
	//
	// Pinned by TestHealthAndReadyAreNeverLimited and
	// TestHealthAndReadyDoNotConsumeAnyBucket.
	mux.HandleFunc(pathHealth, onlyGET(handleHealth(d.Now)))
	mux.HandleFunc(pathReady, onlyGET(handleReady(d.Store.Health)))

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
	hub := orFallbackHub(d.Hub, d.Logger, "api")
	mux.Handle(pathEvents, withLimit(lim.sse, res)(markSSEExempt(onlyGET(handleEvents(hub, d.Store.Stations, res, d.Logger)))))

	// ── 4-5. The two scopes whose HANDLERS are B3's. The scopes are wired and
	// tested now because a scope wired later is a scope that ships unlimited.
	// B3 replaces handleNotImplemented at these two lines.
	mux.Handle(pathVerify, withLimit(lim.verify, res)(onlyMethod(http.MethodPost, handleNotImplemented)))
	mux.Handle(pathProof, withLimit(lim.proof, res)(onlyGET(handleNotImplemented)))

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
