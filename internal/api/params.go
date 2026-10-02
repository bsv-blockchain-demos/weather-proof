package api

import (
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/bsv-blockchain-demos/weather-proof/internal/store"
)

// The default and clamp limits for the two paginated list endpoints. maxPage
// is derived from maxOffset so page*limit never crosses the DoS ceiling — see
// parsePage.
const (
	maxOffset        = 100_000
	weatherLimitDef  = 20
	weatherLimitMax  = 100
	stationsLimitDef = 50
	stationsLimitMax = 200
)

// badRequest carries a 400 with a client-safe message. It is the ONLY error
// type a parse helper returns, so a handler's error branch is one errors.As.
type badRequestError struct{ msg string }

func (e badRequestError) Error() string { return e.msg }

// clampInt is the NaN-free clamp. Spec §6.4: the TypeScript used
// Math.max(1, parseInt(x)), and Math.max PROPAGATES NaN, so ?limit=abc
// yielded page=NaN, limit=NaN, skip=NaN and "page": null in the body. In Go
// the parse and the clamp are separate steps and the parse error is branched
// on, never propagated.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// parsePage reads ?page. Absent, empty or unparseable → 1. Parsed → clamped
// to [1, maxPage]. The upper bound exists because a huge OFFSET is a cheap
// CPU DoS (spec §6.4): maxPage is derived so page × limit stays under
// maxOffset (100 000).
func parsePage(q url.Values, limit int) int {
	raw := q.Get("page")
	if raw == "" {
		return 1
	}
	v, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		return 1
	}
	maxPage := maxOffset
	if limit > 0 {
		maxPage = maxOffset / limit
	}
	if maxPage < 1 {
		maxPage = 1
	}
	return clampInt(v, 1, maxPage)
}

// parseLimit reads ?limit. Absent, empty or unparseable → def. Parsed →
// clamped to [1, max].
func parseLimit(q url.Values, def, max int) int {
	raw := q.Get("limit")
	if raw == "" {
		return def
	}
	v, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		return def
	}
	return clampInt(v, 1, max)
}

// parseStatus reads ?status. Absent or empty → nil filter, no error. Present
// and in store.Status's allowlist → that status. Present and NOT in the
// allowlist → badRequest. NOTE the deliberate divergence from the
// TypeScript, which silently DROPPED an unknown status and answered 200 with
// unfiltered results: a wrong answer rather than an error (spec §6.4).
func parseStatus(q url.Values) (*store.Status, error) {
	raw := q.Get("status")
	if raw == "" {
		return nil, nil
	}
	s := store.Status(raw)
	if !s.Valid() {
		return nil, badRequestError{msg: "invalid status"}
	}
	return &s, nil
}

// parseStationID reads ?stationId. Absent or empty → nil filter. Unparseable
// → nil filter and NO error, which is the TS behavior spec §13.2 preserves
// for this parameter specifically. Out of int32 range → badRequest, because
// strconv accepts values the station_id column cannot hold and Postgres
// would answer 22003 → 500.
func parseStationID(q url.Values) (*int64, error) {
	raw := q.Get("stationId")
	if raw == "" {
		return nil, nil
	}
	v, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil {
		return nil, nil
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return nil, badRequestError{msg: "stationId out of range"}
	}
	return &v, nil
}

// parsePathStationID reads the {stationId} path segment. Unlike the query
// parameter, an unparseable value here is a badRequest — spec §13.4 pins 400
// for a parse failure and 404 for an unknown id. Out of int32 range → 400.
func parsePathStationID(raw string) (int64, error) {
	v, parseErr := strconv.ParseInt(raw, 10, 64)
	if parseErr != nil {
		return 0, badRequestError{msg: "invalid stationId"}
	}
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, badRequestError{msg: "stationId out of range"}
	}
	return v, nil
}

// parseSearch reads ?search, trims it, and rejects a value store.ValidText
// refuses. This is the API half of the two-layer NUL rule: the store treats
// a NUL search as matching nothing so it can never 500, and the API answers
// 400 so the caller is told the request was malformed rather than shown an
// empty page.
func parseSearch(q url.Values) (string, error) {
	s := strings.TrimSpace(q.Get("search"))
	if s == "" {
		return "", nil
	}
	if validErr := store.ValidText(s); validErr != nil {
		return "", badRequestError{msg: "invalid search"}
	}
	return s, nil
}

// parseRecordID validates the {id} path segment. Empty → badRequest. A value
// store.ValidText refuses → badRequest. Otherwise passed through VERBATIM:
// the id is opaque to the frontend (spec §13.5) and uuid.Parse is
// deliberately NOT applied — a strict UUID gate would 400 on any id a future
// writer chooses, and the store's Get already answers ErrNotFound for
// anything unknown.
func parseRecordID(raw string) (string, error) {
	if raw == "" {
		return "", badRequestError{msg: "invalid id"}
	}
	if validErr := store.ValidText(raw); validErr != nil {
		return "", badRequestError{msg: "invalid id"}
	}
	return raw, nil
}
