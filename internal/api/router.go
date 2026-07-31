package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/bsv-blockchain-demos/weather-proof/internal/config"
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
}

// onlyGET wraps a handler so that any method other than GET answers the JSON
// 405 shape with an Allow header, instead of falling through to the
// handler. This exists because net/http.ServeMux's own method-mismatch 405
// carries a text/plain body with no hook to override it: registering the
// pattern WITHOUT a method prefix (e.g. "/api/weather" rather than
// "GET /api/weather") and branching here is what produces a JSON body.
func onlyGET(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowedJSON(w, r)
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
func methodNotAllowedJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", http.MethodGet)
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

	weatherList := onlyGET(handleWeatherList(d.Store.Records))
	weatherDetail := onlyGET(handleWeatherDetail(d.Store.Records))
	stationList := onlyGET(handleStationList(d.Store.Stations, d.Now, d.PollRate))
	stationDetail := onlyGET(handleStationDetail(d.Store.Stations, d.Now, d.PollRate))

	mux.HandleFunc("/api/weather", weatherList)
	mux.HandleFunc("/api/weather/{$}", weatherList)
	mux.HandleFunc("/api/weather/{id}", weatherDetail)

	mux.HandleFunc("/api/stations", stationList)
	mux.HandleFunc("/api/stations/{$}", stationList)
	mux.HandleFunc("/api/stations/{stationId}", stationDetail)

	mux.HandleFunc("/", notFoundJSON)

	return withRequestID(withSecurityHeaders(withRecover(d.Logger)(mux)))
}
